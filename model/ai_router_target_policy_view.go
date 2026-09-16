package model

import (
	"strings"
)

// TargetPolicyChannelInfo is the read-only channel data joined onto target
// policy candidates for the Web UI. It mirrors the fields the candidate table
// displays — scheduling and capabilities stay sourced from the Channel itself.
type TargetPolicyChannelInfo struct {
	Id              int    `json:"id"`
	Name            string `json:"name"`
	Status          int    `json:"status"`
	Group           string `json:"group"`
	Models          string `json:"models"`
	Priority        int64  `json:"priority"`
	Weight          int    `json:"weight"`
	RouterEnabled   bool   `json:"router_enabled"`
	RouterSet       bool   `json:"router_set"`
	MaxRetry        int    `json:"max_retry"`
	SupportsText    *bool  `json:"supports_text"`
	SupportsImage   *bool  `json:"supports_image"`
	SupportsVideo   *bool  `json:"supports_video"`
	UnknownFallback bool   `json:"unknown_fallback"`
	ToolSupport     string `json:"tool_support"`
}

// BuildTargetPolicyChannelInfos joins candidate channel rows with their live
// channel data. Deleted channels are omitted — the caller renders those as
// "已删除渠道 (#id)".
func BuildTargetPolicyChannelInfos(channelIDs []int) (map[int]*TargetPolicyChannelInfo, error) {
	if len(channelIDs) == 0 {
		return map[int]*TargetPolicyChannelInfo{}, nil
	}
	channels, err := GetChannelsByIds(channelIDs)
	if err != nil {
		return nil, err
	}
	infos := make(map[int]*TargetPolicyChannelInfo, len(channels))
	for _, ch := range channels {
		otherSettings := ch.GetOtherSettings()
		profile := otherSettings.RoutingProfile()
		infos[ch.Id] = &TargetPolicyChannelInfo{
			Id:              ch.Id,
			Name:            ch.Name,
			Status:          ch.Status,
			Group:           ch.Group,
			Models:          ch.Models,
			Priority:        ch.GetPriority(),
			Weight:          ch.GetWeight(),
			RouterEnabled:   profile.Enabled,
			RouterSet:       otherSettings.Router != nil,
			MaxRetry:        profile.MaxRetry,
			SupportsText:    profile.SupportsText,
			SupportsImage:   profile.SupportsImage,
			SupportsVideo:   profile.SupportsVideo,
			UnknownFallback: profile.UnknownFallback,
			ToolSupport:     profile.ToolSupport,
		}
	}
	return infos, nil
}

// GetAIRouterTargetPolicyPage returns one page of target policies with their
// candidates. search matches model or group substring, case-insensitive.
func GetAIRouterTargetPolicyPage(search string, groupID string, page, pageSize int) ([]*AIRouterTargetPolicy, int64, error) {
	query := DB.Model(&AIRouterTargetPolicy{})
	if search != "" {
		like := "%" + strings.ToLower(search) + "%"
		query = query.Where("LOWER(model) LIKE ? OR LOWER(group_id) LIKE ?", like, like)
	}
	if groupID != "" {
		query = query.Where("group_id = ?", groupID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var policies []*AIRouterTargetPolicy
	err := query.Preload("Candidates").
		Order("id DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&policies).Error
	if err != nil {
		return nil, 0, err
	}
	return policies, total, nil
}

// CountPoliciesReferencingChannel counts enabled policies whose candidates
// include the channel — the Web channel drawer shows this as 引用策略数.
func CountPoliciesReferencingChannel(channelID int) (int64, error) {
	var count int64
	err := DB.Model(&AIRouterTargetCandidate{}).
		Joins("JOIN ai_router_target_policies ON ai_router_target_policies.id = ai_router_target_candidates.policy_id").
		Where("ai_router_target_candidates.channel_id = ? AND ai_router_target_policies.enabled = ?", channelID, true).
		Count(&count).Error
	return count, err
}

// ListPoliciesReferencingChannel returns the distinct policies referencing a
// channel via their candidates, for the channel drawer's read-only reference list.
func ListPoliciesReferencingChannel(channelID int) ([]AIRouterTargetPolicy, error) {
	var policies []AIRouterTargetPolicy
	err := DB.Model(&AIRouterTargetPolicy{}).
		Joins("JOIN ai_router_target_candidates ON ai_router_target_candidates.policy_id = ai_router_target_policies.id").
		Where("ai_router_target_candidates.channel_id = ?", channelID).
		Distinct("ai_router_target_policies.*").
		Find(&policies).Error
	return policies, err
}
