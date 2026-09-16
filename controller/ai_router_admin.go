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

	var success *bool
	if successStr == "true" {
		v := true
		success = &v
	} else if successStr == "false" {
		v := false
		success = &v
	}

	logs, total, err := model.GetAIRouterLogs(
		startTime, endTime, modelName, userID, groupID, success, modality,
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

// GetAIRouterTargetPolicies returns paginated target policies.
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
	modelFilter := c.Query("model")
	groupFilter := c.Query("group_id")

	query := model.DB.Model(&model.AIRouterTargetPolicy{})
	if modelFilter != "" {
		query = query.Where("model = ?", modelFilter)
	}
	if groupFilter != "" {
		query = query.Where("group_id = ?", groupFilter)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	var policies []*model.AIRouterTargetPolicy
	if err := query.Preload("Candidates").
		Order("id DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&policies).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"items":     policies,
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

// GetAIRouterTargetPolicy returns one policy with its candidates.
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
	common.ApiSuccess(c, policy)
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
// other setting key is preserved as-is. Scheduling fields live on the
// Channel itself and are updated via the scheduling endpoint.
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
	if req.Router == nil {
		common.ApiErrorMsg(c, "router is required")
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
