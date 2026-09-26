package controller

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	ai_router "github.com/QuantumNous/new-api/service/ai_router"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"

	"github.com/gin-gonic/gin"
)

// GetAIRouterSettings returns the current global AI Router configuration.
func GetAIRouterSettings(c *gin.Context) {
	settings := ai_router_setting.GetSettings()
	common.ApiSuccess(c, settings)
}

// UpdateAIRouterSettings accepts partial config updates and persists
// each changed field through model.UpdateOption (which validates and
// publishes the snapshot atomically).
func UpdateAIRouterSettings(c *gin.Context) {
	var req map[string]any
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "无效的参数")
		return
	}

	// Convert each field to a string value for model.UpdateOption.
	for key, raw := range req {
		optionKey := "ai_router." + key
		var value string
		switch v := raw.(type) {
		case bool:
			if v {
				value = "true"
			} else {
				value = "false"
			}
		case float64:
			value = strconv.FormatFloat(v, 'f', -1, 64)
		case string:
			value = v
		case []any:
			// For target_models (JSON array of strings).
			jsonBytes, err := common.Marshal(raw)
			if err != nil {
				common.ApiErrorMsg(c, fmt.Sprintf("failed to serialize %s: %v", key, err))
				return
			}
			value = string(jsonBytes)
		default:
			common.ApiErrorMsg(c, fmt.Sprintf("unsupported type for key %s", key))
			return
		}

		if err := model.UpdateOption(optionKey, value); err != nil {
			common.ApiErrorMsg(c, err.Error())
			return
		}
	}

	common.ApiSuccess(c, ai_router_setting.GetSettings())
}

// GetAIRouterLogs returns paginated Router log entries.
func GetAIRouterLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	// The list view asks for a page of 20; the client-side export asks for one
	// 1000-row page, which is the documented export ceiling (V1.5 has no
	// server-side export endpoint).
	if pageSize > 1000 {
		pageSize = 1000
	}

	startTime, _ := strconv.ParseInt(c.Query("start_time"), 10, 64)
	endTime, _ := strconv.ParseInt(c.Query("end_time"), 10, 64)
	modelName := c.Query("model")
	userID, _ := strconv.Atoi(c.Query("user_id"))
	groupID := c.Query("group_id")
	successStr := c.Query("success")
	modality := c.Query("modality")
	fallback := c.Query("fallback")
	channelID, _ := strconv.Atoi(c.Query("channel_id"))
	statusCode, _ := strconv.Atoi(c.Query("http_status_code"))
	errorCategory := c.Query("error_category")
	streamStr := c.Query("stream_interrupted")

	var success *bool
	if successStr == "true" {
		v := true
		success = &v
	} else if successStr == "false" {
		v := false
		success = &v
	}

	// stream_interrupted: "true"/"false" filter, "" = no filter. It maps to
	// the stream_interrupted error category on attempt rows.
	var streamInterrupted *bool
	switch streamStr {
	case "true":
		v := true
		streamInterrupted = &v
	case "false":
		v := false
		streamInterrupted = &v
	}

	logs, total, err := model.GetAIRouterLogs(
		startTime, endTime, modelName, userID, groupID, success, modality,
		fallback, channelID, statusCode, errorCategory, streamInterrupted,
		page, pageSize,
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	common.ApiSuccess(c, gin.H{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"items":     logs,
	})
}

// GetAIRouterLogDetail returns one summary log with its full attempt list
// ordered by (attempt_seq, channel_retry_no). Logs recorded before Phase 3
// come back with an empty attempts list instead of an error.
func GetAIRouterLogDetail(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "无效的日志 ID")
		return
	}
	detail, err := model.GetAIRouterLogDetail(id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if detail == nil {
		common.ApiErrorMsg(c, "日志不存在")
		return
	}
	common.ApiSuccess(c, detail)
}

// GetAIRouterStats returns basic aggregate statistics for a time range.
func GetAIRouterStats(c *gin.Context) {
	startTime, err := strconv.ParseInt(c.Query("start_time"), 10, 64)
	if err != nil || startTime == 0 {
		common.ApiErrorMsg(c, "start_time is required")
		return
	}
	endTime, err := strconv.ParseInt(c.Query("end_time"), 10, 64)
	if err != nil || endTime == 0 {
		common.ApiErrorMsg(c, "end_time is required")
		return
	}
	modelName := c.Query("model")
	groupID := c.Query("group_id")
	requestCategory, ok := parseAIRouterRequestCategory(c)
	if !ok {
		return
	}

	stats, err := model.GetAIRouterStats(startTime, endTime, modelName, groupID, requestCategory)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	common.ApiSuccess(c, stats)
}

// GetAIRouterTrend returns time-bucketed success/failure counts.
func GetAIRouterTrend(c *gin.Context) {
	startTime, err := strconv.ParseInt(c.Query("start_time"), 10, 64)
	if err != nil || startTime == 0 {
		common.ApiErrorMsg(c, "start_time is required")
		return
	}
	endTime, err := strconv.ParseInt(c.Query("end_time"), 10, 64)
	if err != nil || endTime == 0 {
		common.ApiErrorMsg(c, "end_time is required")
		return
	}
	requestCategory, ok := parseAIRouterRequestCategory(c)
	if !ok {
		return
	}
	points, err := model.GetAIRouterTrend(startTime, endTime, c.Query("model"), c.Query("group_id"), requestCategory)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, points)
}

// GetAIRouterChannelStats aggregates per-channel attempts/finals for the
// monitoring dashboard.
func GetAIRouterChannelStats(c *gin.Context) {
	startTime, err := strconv.ParseInt(c.Query("start_time"), 10, 64)
	if err != nil || startTime == 0 {
		common.ApiErrorMsg(c, "start_time is required")
		return
	}
	endTime, err := strconv.ParseInt(c.Query("end_time"), 10, 64)
	if err != nil || endTime == 0 {
		common.ApiErrorMsg(c, "end_time is required")
		return
	}
	requestCategory, ok := parseAIRouterRequestCategory(c)
	if !ok {
		return
	}
	stats, err := model.GetAIRouterChannelStats(startTime, endTime, c.Query("model"), c.Query("group_id"), requestCategory)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	// Enrich with per-attempt error breakdown (Phase 3): counts of 429 /
	// 5xx / timeout / connection / stream_interrupted / other per channel.
	// Channels with no attempt rows (pre-Phase-3 traffic) keep zero error
	// counts and their summary-derived success rate. The same model/group
	// filter applies to both aggregates so the row stays internally
	// consistent.
	attemptStats, err := model.GetAIRouterAttemptChannelStats(startTime, endTime, c.Query("model"), c.Query("group_id"), requestCategory)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	view := make([]aiRouterChannelStatView, 0, len(stats))
	for _, stat := range stats {
		row := aiRouterChannelStatView{AIRouterChannelStat: stat}
		if attempt, ok := attemptStats[stat.ChannelId]; ok {
			row.ChannelName = attempt.ChannelName
			row.Count429 = attempt.Count429
			row.Count5xx = attempt.Count5xx
			row.CountTimeout = attempt.CountTimeout
			row.CountConnection = attempt.CountConnection
			row.CountInterrupted = attempt.CountInterrupted
			row.CountOther = attempt.CountOther
			if attempt.Attempts > 0 {
				// Attempt-derived rate and duration: the row answers "how did
				// this channel behave on every upstream call", not just on the
				// requests it happened to be the final channel of.
				row.SuccessRate = float64(attempt.SuccessCount) / float64(attempt.Attempts)
				row.AvgDurationMs = float64(attempt.TotalDurationMs) / float64(attempt.Attempts)
			}
		}
		view = append(view, row)
	}
	common.ApiSuccess(c, view)
}

// GetAIRouterErrorStats returns the failed-attempt distribution by error
// category and by upstream HTTP status for the error-analysis charts.
func GetAIRouterErrorStats(c *gin.Context) {
	startTime, err := strconv.ParseInt(c.Query("start_time"), 10, 64)
	if err != nil || startTime == 0 {
		common.ApiErrorMsg(c, "start_time is required")
		return
	}
	endTime, err := strconv.ParseInt(c.Query("end_time"), 10, 64)
	if err != nil || endTime == 0 {
		common.ApiErrorMsg(c, "end_time is required")
		return
	}
	requestCategory, ok := parseAIRouterRequestCategory(c)
	if !ok {
		return
	}
	dist, err := model.GetAIRouterErrorDistribution(startTime, endTime, c.Query("model"), c.Query("group_id"), requestCategory)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, dist)
}

func parseAIRouterRequestCategory(c *gin.Context) (model.AIRouterRequestCategory, bool) {
	value := c.Query("request_category")
	if value == "" {
		value = string(model.AIRouterRequestCategoryAll)
	}
	category := model.AIRouterRequestCategory(value)
	if !category.Valid() {
		common.ApiErrorMsg(c, "request_category must be one of: all, video, text_image")
		return "", false
	}
	return category, true
}

// aiRouterChannelStatView extends the V1.5 channel stat with the Phase 3
// per-attempt error counts.
type aiRouterChannelStatView struct {
	model.AIRouterChannelStat
	// ChannelName comes from the attempt rows, which snapshot the name at
	// write time. Channels without attempt telemetry keep the empty string
	// and the table falls back to "#id".
	ChannelName      string `json:"channel_name"`
	Count429         int64  `json:"count_429"`
	Count5xx         int64  `json:"count_5xx"`
	CountTimeout     int64  `json:"count_timeout"`
	CountConnection  int64  `json:"count_connection"`
	CountInterrupted int64  `json:"count_stream_interrupted"`
	CountOther       int64  `json:"count_other"`
}

// aiRouterCandidateView is one candidate row in the policy list response.
type aiRouterCandidateView struct {
	Id        int                            `json:"id"`
	PolicyId  int                            `json:"policy_id"`
	ChannelId int                            `json:"channel_id"`
	Enabled   bool                           `json:"enabled"`
	Channel   *model.TargetPolicyChannelInfo `json:"channel"`
}

// aiRouterTargetPolicyView is the policy row for the Web list: candidates are
// enriched with the read-only channel view (scheduling/capabilities sourced
// from the Channel itself).
type aiRouterTargetPolicyView struct {
	Id          int                     `json:"id"`
	Model       string                  `json:"model"`
	GroupId     string                  `json:"group_id"`
	Enabled     bool                    `json:"enabled"`
	MaxAttempts int                     `json:"max_attempts"`
	CreatedAt   int64                   `json:"created_at"`
	UpdatedAt   int64                   `json:"updated_at"`
	Candidates  []aiRouterCandidateView `json:"candidates"`
}

// enrichTargetPolicies joins candidate channels with their live channel data.
func enrichTargetPolicies(policies []*model.AIRouterTargetPolicy) []aiRouterTargetPolicyView {
	ids := make([]int, 0, len(policies)*4)
	for _, policy := range policies {
		for _, candidate := range policy.Candidates {
			ids = append(ids, candidate.ChannelId)
		}
	}
	infos, err := model.BuildTargetPolicyChannelInfos(ids)
	if err != nil {
		common.SysError(fmt.Sprintf("ai router: build policy channel infos: %v", err))
		infos = map[int]*model.TargetPolicyChannelInfo{}
	}
	views := make([]aiRouterTargetPolicyView, 0, len(policies))
	for _, policy := range policies {
		view := aiRouterTargetPolicyView{
			Id:          policy.Id,
			Model:       policy.Model,
			GroupId:     policy.GroupId,
			Enabled:     policy.Enabled,
			MaxAttempts: policy.MaxAttempts,
			CreatedAt:   policy.CreatedAt,
			UpdatedAt:   policy.UpdatedAt,
			Candidates:  make([]aiRouterCandidateView, 0, len(policy.Candidates)),
		}
		for _, candidate := range policy.Candidates {
			view.Candidates = append(view.Candidates, aiRouterCandidateView{
				Id:        candidate.Id,
				PolicyId:  candidate.PolicyId,
				ChannelId: candidate.ChannelId,
				Enabled:   candidate.Enabled,
				Channel:   infos[candidate.ChannelId],
			})
		}
		views = append(views, view)
	}
	return views
}

// GetAIRouterTargetPolicies returns paginated target policies with enriched
// candidate channel info. search matches model/group substring; group_id is
// an exact filter.
func GetAIRouterTargetPolicies(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	policies, total, err := model.GetAIRouterTargetPolicyPage(
		c.Query("search"), c.Query("group_id"), page, pageSize,
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"items":     enrichTargetPolicies(policies),
	})
}

// parseTargetPolicyInput decodes and lightly validates the shared payload.
func parseTargetPolicyInput(c *gin.Context) (ai_router.TargetPolicyInput, bool) {
	var input ai_router.TargetPolicyInput
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		common.ApiErrorMsg(c, "无效的参数")
		return input, false
	}
	return input, true
}

// validateIssues returns true when the list contains a blocking error.
func validateIssues(c *gin.Context, issues []ai_router.ValidationIssue) bool {
	for _, issue := range issues {
		if issue.Level == "error" {
			return true
		}
	}
	return false
}

// CreateAIRouterTargetPolicy creates a policy and publishes a new snapshot.
func CreateAIRouterTargetPolicy(c *gin.Context) {
	input, ok := parseTargetPolicyInput(c)
	if !ok {
		return
	}
	policy, issues, err := ai_router.SaveTargetPolicy(input, 0)
	if err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}
	common.ApiSuccess(c, gin.H{
		"policy":   policy,
		"issues":   issues,
		"blocking": validateIssues(c, issues),
	})
}

// UpdateAIRouterTargetPolicy replaces a policy wholesale and republishes.
func UpdateAIRouterTargetPolicy(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "invalid policy id")
		return
	}
	input, ok := parseTargetPolicyInput(c)
	if !ok {
		return
	}
	policy, issues, err := ai_router.SaveTargetPolicy(input, id)
	if err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}
	common.ApiSuccess(c, gin.H{
		"policy":   policy,
		"issues":   issues,
		"blocking": validateIssues(c, issues),
	})
}

// GetAIRouterTargetPolicy returns one policy with enriched candidates.
func GetAIRouterTargetPolicy(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "invalid policy id")
		return
	}
	policy, err := ai_router.GetTargetPolicyWithCandidates(id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	views := enrichTargetPolicies([]*model.AIRouterTargetPolicy{policy})
	common.ApiSuccess(c, views[0])
}

// DeleteAIRouterTargetPolicy removes the policy and its candidates.
func DeleteAIRouterTargetPolicy(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "invalid policy id")
		return
	}
	if err := ai_router.DeleteTargetPolicy(id); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

// ValidateAIRouterTargetPolicy pre-checks a policy payload without writing.
func ValidateAIRouterTargetPolicy(c *gin.Context) {
	input, ok := parseTargetPolicyInput(c)
	if !ok {
		return
	}
	issues, err := ai_router.ValidateTargetPolicyInput(input)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"issues":   issues,
		"blocking": validateIssues(c, issues),
	})
}

// aiRouterChannelView is the list-row shape for the Router channel tab.
type aiRouterChannelView struct {
	Id                 int                            `json:"id"`
	Name               string                         `json:"name"`
	Status             int                            `json:"status"`
	Group              string                         `json:"group"`
	Models             string                         `json:"models"`
	Priority           int64                          `json:"priority"`
	Weight             int                            `json:"weight"`
	RoutingProfile     aiRouterChannelRouterView      `json:"router"`
	ResponseModelAlias *dto.ChannelResponseModelAlias `json:"response_model_alias"`
	ChatCompatibility  aiRouterChatCompatibility      `json:"chat_compatibility"`
	ReferencedBy       []aiRouterPolicyRef            `json:"referenced_by"`
}

type aiRouterChatCompatibility struct {
	K3ReasoningCompatibility  bool     `json:"k3_reasoning_compatibility"`
	K3ToolChoiceCompatibility *bool    `json:"k3_tool_choice_compatibility,omitempty"`
	Models                    []string `json:"chat_compatibility_models"`
}

type aiRouterChannelRouterView struct {
	VideoPriority     *int64 `json:"video_priority"`
	Enabled           bool   `json:"enabled"`
	RouterSet         bool   `json:"router_set"`
	MaxRetry          int    `json:"max_retry"`
	RetryStatusCodes  []int  `json:"retry_status_codes"`
	SwitchStatusCodes []int  `json:"switch_status_codes"`
	SupportsText      *bool  `json:"supports_text,omitempty"`
	SupportsImage     *bool  `json:"supports_image,omitempty"`
	SupportsVideo     *bool  `json:"supports_video,omitempty"`
	UnknownFallback   bool   `json:"unknown_fallback"`
	ToolSupport       string `json:"tool_support,omitempty"`
}

type aiRouterPolicyRef struct {
	PolicyId int    `json:"policy_id"`
	Model    string `json:"model"`
	GroupId  string `json:"group_id"`
	Enabled  bool   `json:"enabled"`
}

// orEmptyRefs normalizes an absent reference list to an empty one so the
// JSON shape of referenced_by is always an array.
func orEmptyRefs(refs []aiRouterPolicyRef) []aiRouterPolicyRef {
	if refs == nil {
		return []aiRouterPolicyRef{}
	}
	return refs
}

// GetAIRouterChannels returns channel routing info plus the policies that
// reference each channel.
func GetAIRouterChannels(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	query := model.DB.Model(&model.Channel{})
	if nameFilter := c.Query("name"); nameFilter != "" {
		query = query.Where("name LIKE ?", "%"+nameFilter+"%")
	}
	if statusFilter := c.Query("status"); statusFilter != "" {
		if status, err := strconv.Atoi(statusFilter); err == nil {
			query = query.Where("status = ?", status)
		}
	}
	var channels []*model.Channel
	// Settings are portable JSON text. Read only the administration view's
	// columns (never credentials), then filter and order before pagination.
	// This avoids database-specific JSON expressions and group LIKE matching.
	if err := query.Select("id", "name", "status", "group", "models", "priority", "weight", "settings", "setting").
		Find(&channels).Error; err != nil {
		common.ApiError(c, err)
		return
	}

	items := make([]aiRouterChannelView, 0, len(channels))
	groupFilter := c.Query("group_id")
	for _, ch := range channels {
		if groupFilter != "" && !slices.Contains(ch.GetGroups(), groupFilter) {
			continue
		}
		otherSettings := ch.GetOtherSettings()
		settings := ch.GetSetting()
		profile := otherSettings.RoutingProfile()
		items = append(items, aiRouterChannelView{
			Id:                 ch.Id,
			Name:               ch.Name,
			Status:             ch.Status,
			Group:              ch.Group,
			Models:             ch.Models,
			Priority:           ch.GetPriority(),
			Weight:             ch.GetWeight(),
			ResponseModelAlias: otherSettings.ResponseModelAlias,
			ChatCompatibility:  aiRouterChatCompatibility{K3ReasoningCompatibility: settings.K3ReasoningCompatibility, K3ToolChoiceCompatibility: common.GetPointer(settings.K3ToolChoiceCompatibility), Models: settings.ChatCompatibilityModels},
			RoutingProfile: aiRouterChannelRouterView{
				VideoPriority:     profile.VideoPriority,
				Enabled:           profile.Enabled,
				RouterSet:         otherSettings.Router != nil,
				MaxRetry:          profile.MaxRetry,
				RetryStatusCodes:  profile.EffectiveRetryStatusCodes(),
				SwitchStatusCodes: profile.EffectiveSwitchStatusCodes(),
				SupportsText:      profile.SupportsText,
				SupportsImage:     profile.SupportsImage,
				SupportsVideo:     profile.SupportsVideo,
				UnknownFallback:   profile.UnknownFallback,
				ToolSupport:       profile.ToolSupport,
			},
		})
	}
	videoOrder := c.Query("order") == "video_priority"
	slices.SortFunc(items, func(a, b aiRouterChannelView) int {
		if videoOrder {
			av, bv := a.RoutingProfile.VideoPriority, b.RoutingProfile.VideoPriority
			switch {
			case av != nil && bv == nil:
				return -1
			case av == nil && bv != nil:
				return 1
			case av != nil && bv != nil:
				if order := cmp.Compare(*bv, *av); order != 0 {
					return order
				}
				return cmp.Compare(a.Id, b.Id)
			}
		}
		if order := cmp.Compare(b.Priority, a.Priority); order != 0 {
			return order
		}
		// Weight controls selection within a tier, not its display priority.
		return cmp.Compare(a.Id, b.Id)
	})
	total := len(items)
	start := total
	if page-1 <= total/pageSize {
		start = min((page-1)*pageSize, total)
	}
	items = items[start:min(start+pageSize, total)]

	// Enrich only the current page from the runtime snapshot, without
	// querying candidate tables or scanning all channels for every reference.
	refs := make(map[int][]aiRouterPolicyRef, len(items))
	for _, item := range items {
		refs[item.Id] = nil
	}
	for _, policy := range ai_router.GetTargetPolicySnapshot().Policies() {
		for _, candidate := range policy.Candidates {
			if _, found := refs[candidate.ChannelID]; found {
				refs[candidate.ChannelID] = append(refs[candidate.ChannelID], aiRouterPolicyRef{
					PolicyId: policy.Id,
					Model:    policy.Model,
					GroupId:  policy.GroupId,
					Enabled:  policy.Enabled,
				})
			}
		}
	}
	for i := range items {
		// Unreferenced channels still expose an array rather than null.
		items[i].ReferencedBy = orEmptyRefs(refs[items[i].Id])
	}
	common.ApiSuccess(c, gin.H{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"items":     items,
	})
}

// UpdateAIRouterChannelRouter merges Router capability fields into the
// channel's existing settings JSON. Explicit compatibility and response model
// settings can also be updated; omitted settings are preserved. Every
// other setting key is preserved as-is. Sending "router": null removes the
// Router profile entirely (falls back to the default profile). Scheduling
// fields live on the Channel itself and are updated via the scheduling
// endpoint.
func UpdateAIRouterChannelRouter(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "invalid channel id")
		return
	}
	var req struct {
		Router             *dto.ChannelRoutingProfile     `json:"router"`
		ResponseModelAlias *dto.ChannelResponseModelAlias `json:"response_model_alias"`
		ChatCompatibility  *aiRouterChatCompatibility     `json:"chat_compatibility"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "无效的参数")
		return
	}

	channel, err := model.GetChannelById(id, true)
	if err != nil {
		common.ApiErrorMsg(c, "channel not found")
		return
	}

	otherSettings := channel.GetOtherSettings()
	otherSettings.Router = req.Router
	// Legacy clients that only edit routing must preserve this independent
	// setting. Explicit channel mode with an empty list disables restoration.
	if req.ResponseModelAlias != nil {
		otherSettings.ResponseModelAlias = req.ResponseModelAlias
	}
	if err := otherSettings.ValidateRouter(); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}
	channel.SetOtherSettings(otherSettings)
	updates := map[string]any{"settings": channel.OtherSettings}
	if req.ChatCompatibility != nil {
		// Merge only these fields, preserving unknown and unrelated settings.
		settings := map[string]json.RawMessage{}
		if channel.Setting != nil && *channel.Setting != "" {
			if err := common.UnmarshalJsonStr(*channel.Setting, &settings); err != nil {
				common.ApiErrorMsg(c, "invalid existing channel setting")
				return
			}
		}
		if settings == nil {
			settings = map[string]json.RawMessage{}
		}
		models := make([]string, 0, len(req.ChatCompatibility.Models))
		for _, name := range req.ChatCompatibility.Models {
			name = strings.TrimSpace(name)
			if name != "" && !slices.Contains(models, name) {
				models = append(models, name)
			}
		}
		settings["k3_reasoning_compatibility"] = json.RawMessage(strconv.FormatBool(req.ChatCompatibility.K3ReasoningCompatibility))
		if req.ChatCompatibility.K3ToolChoiceCompatibility != nil {
			settings["k3_tool_choice_compatibility"] = json.RawMessage(strconv.FormatBool(*req.ChatCompatibility.K3ToolChoiceCompatibility))
		}
		modelJSON, err := common.Marshal(models)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		settings["chat_compatibility_models"] = modelJSON
		encoded, err := common.Marshal(settings)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		channel.Setting = common.GetPointer(string(encoded))
		updates["setting"] = *channel.Setting
	}
	if err := channel.ValidateSettings(); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}

	if err := model.DB.Model(channel).Updates(updates).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	model.InitChannelCache()
	common.ApiSuccess(c, gin.H{"id": channel.Id, "router": req.Router, "response_model_alias": otherSettings.ResponseModelAlias})
}

// UpdateAIRouterChannelScheduling updates the channel's existing group,
// priority and weight fields. It reuses the channel save path (validation,
// abilities refresh, cache rebuild) and nothing else changes.
func UpdateAIRouterChannelScheduling(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorMsg(c, "invalid channel id")
		return
	}
	var req struct {
		Group    *string `json:"group"`
		Priority *int64  `json:"priority"`
		Weight   *uint   `json:"weight"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "无效的参数")
		return
	}
	if req.Group == nil && req.Priority == nil && req.Weight == nil {
		common.ApiErrorMsg(c, "nothing to update")
		return
	}

	channel, err := model.GetChannelById(id, true)
	if err != nil {
		common.ApiErrorMsg(c, "channel not found")
		return
	}

	if req.Group != nil {
		group := strings.TrimSpace(*req.Group)
		if group == "" {
			common.ApiErrorMsg(c, "group cannot be empty")
			return
		}
		channel.Group = group
	}
	if req.Priority != nil {
		if *req.Priority < 0 {
			common.ApiErrorMsg(c, "priority must not be negative")
			return
		}
		channel.Priority = req.Priority
	}
	if req.Weight != nil {
		channel.Weight = req.Weight
	}
	if err := validateChannel(channel, false); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}

	if err := channel.Update(); err != nil {
		common.ApiError(c, err)
		return
	}
	model.InitChannelCache()
	common.ApiSuccess(c, gin.H{
		"id":       channel.Id,
		"group":    channel.Group,
		"priority": channel.GetPriority(),
		"weight":   channel.GetWeight(),
	})
}
