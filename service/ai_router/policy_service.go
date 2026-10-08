package ai_router

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"

	"gorm.io/gorm"
)

const (
	MinTargetMaxAttempts = ai_router_setting.MinMaxAttempts
	MaxTargetMaxAttempts = ai_router_setting.MaxMaxAttempts
)

// ValidationIssue is a single validate finding. Errors block saving;
// warnings only inform the admin (Phase 2 Web consumes this shape).
type ValidationIssue struct {
	Level   string `json:"level"` // "error" | "warning"
	Code    string `json:"code"`
	Message string `json:"message"`
}

// TargetPolicyInput is the payload for create/update. Candidates reference
// channels by ID; all scheduling data stays on the Channel itself.
type TargetPolicyInput struct {
	Model              string                       `json:"model"`
	GroupId            string                       `json:"group_id"`
	Enabled            bool                         `json:"enabled"`
	MaxAttempts        int                          `json:"max_attempts"`
	K3StrictValidation *bool                        `json:"k3_strict_validation,omitempty"`
	Candidates         []TargetPolicyCandidateInput `json:"candidates"`
}

type TargetPolicyCandidateInput struct {
	ChannelID int  `json:"channel_id"`
	Enabled   bool `json:"enabled"`
}

// ValidateTargetPolicyInput checks a policy payload against the database and
// returns errors/warnings. It never writes. DB lookups are acceptable here:
// validation runs on the admin save path, not the request hot path.
func ValidateTargetPolicyInput(input TargetPolicyInput) ([]ValidationIssue, error) {
	var issues []ValidationIssue

	if strings.TrimSpace(input.Model) == "" {
		issues = append(issues, ValidationIssue{Level: "error", Code: "model_required", Message: "model is required"})
	}
	if strings.TrimSpace(input.GroupId) == "" {
		issues = append(issues, ValidationIssue{Level: "error", Code: "group_required", Message: "group_id is required"})
	}
	if input.MaxAttempts < MinTargetMaxAttempts || input.MaxAttempts > MaxTargetMaxAttempts {
		issues = append(issues, ValidationIssue{
			Level: "error", Code: "max_attempts_range",
			Message: fmt.Sprintf("max_attempts must be between %d and %d", MinTargetMaxAttempts, MaxTargetMaxAttempts),
		})
	}

	seen := make(map[int]struct{}, len(input.Candidates))
	channelIDs := make([]int, 0, len(input.Candidates))
	for _, candidate := range input.Candidates {
		if _, dup := seen[candidate.ChannelID]; dup {
			issues = append(issues, ValidationIssue{
				Level: "error", Code: "candidate_duplicate",
				Message: fmt.Sprintf("channel %d appears more than once", candidate.ChannelID),
			})
			continue
		}
		seen[candidate.ChannelID] = struct{}{}
		channelIDs = append(channelIDs, candidate.ChannelID)
	}

	// Early out for structural errors before touching the database.
	for _, issue := range issues {
		if issue.Level == "error" {
			return issues, nil
		}
	}
	if len(channelIDs) == 0 {
		issues = append(issues, ValidationIssue{Level: "warning", Code: "no_candidates", Message: "policy has no candidate channels"})
		return issues, nil
	}

	channels, err := model.GetChannelsByIds(channelIDs)
	if err != nil {
		return nil, err
	}
	byID := make(map[int]*model.Channel, len(channels))
	for _, ch := range channels {
		byID[ch.Id] = ch
	}

	enabledCandidates := 0
	for _, candidate := range input.Candidates {
		ch, ok := byID[candidate.ChannelID]
		if !ok || ch.Status != common.ChannelStatusEnabled {
			issues = append(issues, ValidationIssue{
				Level: "error", Code: "candidate_channel_unavailable",
				Message: fmt.Sprintf("channel %d does not exist or is disabled", candidate.ChannelID),
			})
			continue
		}
		if !slices.Contains(ch.GetGroups(), input.GroupId) {
			issues = append(issues, ValidationIssue{
				Level: "error", Code: "candidate_channel_group",
				Message: fmt.Sprintf("channel %d does not serve group %s", candidate.ChannelID, input.GroupId),
			})
			continue
		}
		if !slices.Contains(ch.GetModels(), input.Model) {
			issues = append(issues, ValidationIssue{
				Level: "warning", Code: "candidate_channel_model",
				Message: fmt.Sprintf("channel %d does not list model %s (model mapping may still route it)", candidate.ChannelID, input.Model),
			})
		}
		otherSettings := ch.GetOtherSettings()
		profile := otherSettings.RoutingProfile()
		if !profile.Enabled {
			issues = append(issues, ValidationIssue{
				Level: "warning", Code: "candidate_router_disabled",
				Message: fmt.Sprintf("channel %d has the Router disabled", candidate.ChannelID),
			})
		} else if candidate.Enabled {
			enabledCandidates++
		}
	}

	if input.Enabled && input.MaxAttempts > enabledCandidates {
		issues = append(issues, ValidationIssue{
			Level: "warning", Code: "max_attempts_exceeds_candidates",
			Message: fmt.Sprintf("max_attempts %d exceeds the %d enabled candidates", input.MaxAttempts, enabledCandidates),
		})
	}
	if input.Enabled {
		hasUnknownFallback := false
		hasVideo := false
		for _, candidate := range input.Candidates {
			ch, ok := byID[candidate.ChannelID]
			if !ok || !candidate.Enabled {
				continue
			}
			otherSettings := ch.GetOtherSettings()
			profile := otherSettings.RoutingProfile()
			if profile.Enabled && profile.UnknownFallback {
				hasUnknownFallback = true
			}
			if profile.Enabled && profile.EffectiveVideo() {
				hasVideo = true
			}
		}
		if !hasVideo {
			issues = append(issues, ValidationIssue{Level: "warning", Code: "no_video_channel", Message: "no enabled candidate supports video"})
		}
		if !hasUnknownFallback {
			issues = append(issues, ValidationIssue{Level: "warning", Code: "no_unknown_fallback", Message: "no enabled candidate accepts unknown-modality requests"})
		}
	}

	return issues, nil
}

// SaveTargetPolicy normalizes, validates, commits policy+candidates in one
// transaction, then publishes a fresh immutable snapshot built from the
// committed data. On any failure the database and the previous snapshot are
// left untouched.
func SaveTargetPolicy(input TargetPolicyInput, policyID int) (*model.AIRouterTargetPolicy, []ValidationIssue, error) {
	input.Model = strings.TrimSpace(input.Model)
	input.GroupId = strings.TrimSpace(input.GroupId)

	issues, err := ValidateTargetPolicyInput(input)
	if err != nil {
		return nil, nil, err
	}
	for _, issue := range issues {
		if issue.Level == "error" {
			return nil, issues, fmt.Errorf("validation failed: %s", issue.Message)
		}
	}

	// Uniqueness of (model, group) is enforced by the database; check here to
	// give a friendly message before entering the transaction.
	var conflicts int64
	conflictQuery := model.DB.Model(&model.AIRouterTargetPolicy{}).
		Where("model = ? AND group_id = ?", input.Model, input.GroupId)
	if policyID != 0 {
		conflictQuery = conflictQuery.Where("id != ?", policyID)
	}
	if err := conflictQuery.Count(&conflicts).Error; err != nil {
		return nil, nil, err
	}
	if conflicts > 0 {
		return nil, issues, fmt.Errorf("a policy for model %s in group %s already exists", input.Model, input.GroupId)
	}

	now := common.GetTimestamp()
	policy := &model.AIRouterTargetPolicy{
		Id:          policyID,
		Model:       input.Model,
		GroupId:     input.GroupId,
		Enabled:     input.Enabled,
		MaxAttempts: input.MaxAttempts,
		UpdatedAt:   now,
	}
	if input.K3StrictValidation != nil {
		policy.K3StrictValidation = *input.K3StrictValidation
	}

	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if policyID == 0 {
			policy.CreatedAt = now
			if err := tx.Create(policy).Error; err != nil {
				return err
			}
		} else {
			var existing model.AIRouterTargetPolicy
			if err := tx.First(&existing, policyID).Error; err != nil {
				return err
			}
			policy.CreatedAt = existing.CreatedAt
			if input.K3StrictValidation == nil {
				policy.K3StrictValidation = existing.K3StrictValidation
			}
			if err := tx.Model(&model.AIRouterTargetPolicy{}).
				Where("id = ?", policyID).
				Updates(map[string]any{
					"model":                policy.Model,
					"group_id":             policy.GroupId,
					"enabled":              policy.Enabled,
					"max_attempts":         policy.MaxAttempts,
					"k3_strict_validation": policy.K3StrictValidation,
					"updated_at":           policy.UpdatedAt,
				}).Error; err != nil {
				return err
			}
			// Replace the candidate set wholesale.
			if err := tx.Where("policy_id = ?", policyID).
				Delete(&model.AIRouterTargetCandidate{}).Error; err != nil {
				return err
			}
		}
		if len(input.Candidates) > 0 {
			rows := make([]model.AIRouterTargetCandidate, 0, len(input.Candidates))
			for _, candidate := range input.Candidates {
				rows = append(rows, model.AIRouterTargetCandidate{
					PolicyId:  policy.Id,
					ChannelId: candidate.ChannelID,
					Enabled:   candidate.Enabled,
					CreatedAt: now,
					UpdatedAt: now,
				})
			}
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// Transaction failed: previous snapshot stays as-is.
		return nil, nil, err
	}

	if err := ReloadTargetPolicySnapshot(); err != nil {
		return nil, nil, err
	}
	return policy, issues, nil
}

// DeleteTargetPolicy removes a policy and its candidates in one transaction,
// then republishes the snapshot. Orphan candidates are impossible because the
// delete covers both rows.
func DeleteTargetPolicy(policyID int) error {
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		// Candidates first: the AutoMigrate-generated foreign key from
		// candidates back to policies would otherwise block the delete.
		if err := tx.Where("policy_id = ?", policyID).
			Delete(&model.AIRouterTargetCandidate{}).Error; err != nil {
			return err
		}
		result := tx.Delete(&model.AIRouterTargetPolicy{}, policyID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	return ReloadTargetPolicySnapshot()
}

// GetTargetPolicyWithCandidates loads one policy with its candidates.
func GetTargetPolicyWithCandidates(policyID int) (*model.AIRouterTargetPolicy, error) {
	var policy model.AIRouterTargetPolicy
	if err := model.DB.Preload("Candidates").First(&policy, policyID).Error; err != nil {
		return nil, err
	}
	return &policy, nil
}

// ReloadTargetPolicySnapshot rebuilds and atomically publishes the snapshot
// from the committed database state. Used at startup and after every
// successful policy mutation.
func ReloadTargetPolicySnapshot() error {
	var rows []model.AIRouterTargetPolicy
	if err := model.DB.Preload("Candidates").Find(&rows).Error; err != nil {
		return err
	}
	policies := make([]TargetPolicy, 0, len(rows))
	for _, row := range rows {
		policy := TargetPolicy{
			Id:                 row.Id,
			Model:              row.Model,
			GroupId:            row.GroupId,
			Enabled:            row.Enabled,
			MaxAttempts:        row.MaxAttempts,
			K3StrictValidation: row.K3StrictValidation,
			Candidates:         make([]TargetPolicyCandidate, 0, len(row.Candidates)),
		}
		for _, candidate := range row.Candidates {
			policy.Candidates = append(policy.Candidates, TargetPolicyCandidate{
				ChannelID: candidate.ChannelId,
				Enabled:   candidate.Enabled,
			})
		}
		policies = append(policies, policy)
	}
	PublishTargetPolicySnapshot(BuildTargetPolicySnapshot(policies))
	return nil
}

// MigrateV1TargetPolicies converts the V1 global target_models option into
// per-(model, default) policies. It is idempotent: models that already have a
// policy are skipped, so repeated runs and manual policies survive. V1's
// target_models option stays in place as a compatibility mirror.
func MigrateV1TargetPolicies() error {
	settings := ai_router_setting.GetSettings()
	if len(settings.TargetModels) == 0 {
		return nil
	}

	var existing []model.AIRouterTargetPolicy
	if err := model.DB.Find(&existing).Error; err != nil {
		return err
	}
	hasPolicies := map[string]struct{}{}
	for _, policy := range existing {
		hasPolicies[policy.Model+"\x00"+policy.GroupId] = struct{}{}
	}

	// Candidates come from the current Router-enabled channels that serve the
	// model, belong to the default group, and are enabled — the same set V1
	// would route to today.
	created := 0
	for _, modelName := range settings.TargetModels {
		key := modelName + "\x00default"
		if _, ok := hasPolicies[key]; ok {
			continue
		}
		channelIDs, err := v1CandidateChannelIDs(modelName)
		if err != nil {
			return err
		}
		input := TargetPolicyInput{
			Model:       modelName,
			GroupId:     "default",
			Enabled:     true,
			MaxAttempts: settings.MaxAttempts,
		}
		for _, channelID := range channelIDs {
			input.Candidates = append(input.Candidates, TargetPolicyCandidateInput{ChannelID: channelID, Enabled: true})
		}
		if _, _, err := SaveTargetPolicy(input, 0); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			// A failure here must not corrupt V1 config or the running
			// snapshot; report and stop the migration loop.
			return fmt.Errorf("migrating target model %s: %w", modelName, err)
		}
		created++
	}
	if created > 0 {
		common.SysLog(fmt.Sprintf("ai router: migrated %d V1 target models into policies", created))
	}
	return nil
}

// v1CandidateChannelIDs enumerates the channels that V1's routing would have
// selected for the model in the default group.
func v1CandidateChannelIDs(modelName string) ([]int, error) {
	channels := model.GetEnabledChannelsForGroupModel("default", modelName, nil)
	ids := make([]int, 0, len(channels))
	for _, ch := range channels {
		otherSettings := ch.GetOtherSettings()
		if !otherSettings.RoutingProfile().Enabled {
			continue
		}
		ids = append(ids, ch.Id)
	}
	return ids, nil
}
