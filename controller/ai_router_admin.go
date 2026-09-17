package controller

import (
	"encoding/json"
	"fmt"
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
			jsonBytes, err := json.Marshal(raw)
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
	if pageSize > 100 {
		pageSize = 100
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

	stats, err := model.GetAIRouterStats(startTime, endTime, modelName, groupID)
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
	points, err := model.GetAIRouterTrend(startTime, endTime, c.Query("model"), c.Query("group_id"))
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
	stats, err := model.GetAIRouterChannelStats(startTime, endTime, c.Query("model"), c.Query("group_id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}

	// Enrich with per-attempt error breakdown (Phase 3): counts of 429 /
	// 5xx / timeout / connection / stream_interrupted / other per channel.
	// Channels with no attempt rows (pre-Phase-3 traffic) keep zero error
	// counts and their summary-derived success rate.
	attemptStats, err := model.GetAIRouterAttemptChannelStats(startTime, endTime)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	view := make([]aiRouterChannelStatView, 0, len(stats))
	for _, stat := range stats {
		row := aiRouterChannelStatView{AIRouterChannelStat: stat}
		if attempt, ok := attemptStats[stat.ChannelId]; ok {
			row.Count429 = attempt.Count429
			row.Count5xx = attempt.Count5xx
			row.CountTimeout = attempt.CountTimeout
			row.CountConnection = attempt.CountConnection
			row.CountInterrupted = attempt.CountInterrupted
			row.CountOther = attempt.CountOther
			if attempt.Attempts > 0 {
				row.SuccessRate = float64(attempt.SuccessCount) / float64(attempt.Attempts)
			}
		}
		view = append(view, row)
	}
	common.ApiSuccess(c, view)
}

// aiRouterChannelStatView extends the V1.5 channel stat with the Phase 3
// per-attempt error counts.
type aiRouterChannelStatView struct {
	model.AIRouterChannelStat
	Count429         int64 `json:"count_429"`
	Count5xx         int64 `json:"count_5xx"`
	CountTimeout     int64 `json:"count_timeout"`
	CountConnection  int64 `json:"count_connection"`
	CountInterrupted int64 `json:"count_stream_interrupted"`
	CountOther       int64 `json:"count_other"`
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
	Id             int                       `json:"id"`
	Name           string                    `json:"name"`
	Status         int                       `json:"status"`
	Group          string                    `json:"group"`
	Models         string                    `json:"models"`
	Priority       int64                     `json:"priority"`
	Weight         int                       `json:"weight"`
	RoutingProfile aiRouterChannelRouterView `json:"router"`
	ReferencedBy   []aiRouterPolicyRef       `json:"referenced_by"`
}

type aiRouterChannelRouterView struct {
	Enabled         bool   `json:"enabled"`
	RouterSet       bool   `json:"router_set"`
	MaxRetry        int    `json:"max_retry"`
	SupportsText    *bool  `json:"supports_text,omitempty"`
	SupportsImage   *bool  `json:"supports_image,omitempty"`
	SupportsVideo   *bool  `json:"supports_video,omitempty"`
	UnknownFallback bool   `json:"unknown_fallback"`
	ToolSupport     string `json:"tool_support,omitempty"`
}

type aiRouterPolicyRef struct {
	PolicyId int    `json:"policy_id"`
	Model    string `json:"model"`
	GroupId  string `json:"group_id"`
	Enabled  bool   `json:"enabled"`
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

	var total int64
	query := model.DB.Model(&model.Channel{})
	if nameFilter := c.Query("name"); nameFilter != "" {
		query = query.Where("name LIKE ?", "%"+nameFilter+"%")
	}
	if groupFilter := c.Query("group_id"); groupFilter != "" {
		query = query.Where("`group` = ?", groupFilter)
	}
	if statusFilter := c.Query("status"); statusFilter != "" {
		if status, err := strconv.Atoi(statusFilter); err == nil {
			query = query.Where("status = ?", status)
		}
	}
	if err := query.Count(&total).Error; err != nil {
		common.ApiError(c, err)
		return
	}

	var channels []*model.Channel
	if err := query.Order("id ASC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&channels).Error; err != nil {
		common.ApiError(c, err)
		return
	}

	// Which policies reference these channels (from the runtime snapshot —
	// no extra query of the candidate table).
	ids := make([]int, 0, len(channels))
	for _, ch := range channels {
		ids = append(ids, ch.Id)
	}
	refs := make(map[int][]aiRouterPolicyRef, len(ids))
	for _, policy := range ai_router.GetTargetPolicySnapshot().Policies() {
		for _, candidate := range policy.Candidates {
			found := false
			for _, id := range ids {
				if id == candidate.ChannelID {
					found = true
					break
				}
			}
			if found {
				refs[candidate.ChannelID] = append(refs[candidate.ChannelID], aiRouterPolicyRef{
					PolicyId: policy.Id,
					Model:    policy.Model,
					GroupId:  policy.GroupId,
					Enabled:  policy.Enabled,
				})
			}
		}
	}

	items := make([]aiRouterChannelView, 0, len(channels))
	for _, ch := range channels {
		otherSettings := ch.GetOtherSettings()
		profile := otherSettings.RoutingProfile()
		items = append(items, aiRouterChannelView{
			Id:       ch.Id,
			Name:     ch.Name,
			Status:   ch.Status,
			Group:    ch.Group,
			Models:   ch.Models,
			Priority: ch.GetPriority(),
			Weight:   ch.GetWeight(),
			RoutingProfile: aiRouterChannelRouterView{
				Enabled:         profile.Enabled,
				RouterSet:       otherSettings.Router != nil,
				MaxRetry:        profile.MaxRetry,
				SupportsText:    profile.SupportsText,
				SupportsImage:   profile.SupportsImage,
				SupportsVideo:   profile.SupportsVideo,
				UnknownFallback: profile.UnknownFallback,
				ToolSupport:     profile.ToolSupport,
			},
			ReferencedBy: refs[ch.Id],
		})
	}
	common.ApiSuccess(c, gin.H{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"items":     items,
	})
}

// UpdateAIRouterChannelRouter merges Router capability fields into the
// channel's existing settings JSON. Only the "router" key changes; every
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
		Router *dto.ChannelRoutingProfile `json:"router"`
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
	if err := otherSettings.ValidateRouter(); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}
	channel.SetOtherSettings(otherSettings)
	if err := channel.ValidateSettings(); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}

	if err := model.DB.Model(channel).Update("settings", channel.OtherSettings).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	model.InitChannelCache()
	common.ApiSuccess(c, gin.H{"id": channel.Id, "router": req.Router})
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
