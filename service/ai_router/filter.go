package ai_router

import (
	"fmt"
	"math/rand"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"
)

// RouterChannel is a decoupled view of a channel for routing purposes.
type RouterChannel struct {
	AllowsThinkingDisabled bool
	AllowsReasoningNone    bool
	ID                     int
	Priority               int64
	Weight                 int
	Group                  string
	RoutingProfile         dto.ChannelRoutingProfile
}

// BuildConstraints converts a RouteDemand into filtering constraints.
func BuildConstraints(demand RouteDemand, settings ai_router_setting.Settings) ChannelRouteConstraints {
	return ChannelRouteConstraints{
		VideoRequest:            demand.Modality == ModalityVideo || demand.OriginalModality == ModalityVideo,
		RequiredModality:        demand.Modality,
		RequireTools:            settings.ToolFilterEnabled && demand.HasTools,
		UnknownFallbackOnly:     demand.Modality == ModalityUnknown || len(demand.UnknownContentTypes) > 0,
		RequireThinkingDisabled: demand.RequireThinkingDisabled,
		RequireReasoningNone:    demand.RequireReasoningNone,
	}
}

// FilterChannels applies strict capability filtering to candidates.
// Does not exclude failed channels, does not implement fallback.
func FilterChannels(channels []RouterChannel, constraints ChannelRouteConstraints, group string) []RouterChannel {
	var result []RouterChannel
	for _, c := range channels {
		if c.Group != group {
			continue
		}
		if !matchesCapability(c, constraints) {
			continue
		}
		result = append(result, c)
	}
	return result
}

func matchesCapability(c RouterChannel, constraints ChannelRouteConstraints) bool {
	p := c.RoutingProfile
	if !p.Enabled {
		return false
	}
	if constraints.RequireThinkingDisabled && !c.AllowsThinkingDisabled {
		return false
	}
	if constraints.RequireReasoningNone && !c.AllowsReasoningNone {
		return false
	}
	if constraints.UnknownFallbackOnly && !p.UnknownFallback {
		return false
	}
	switch constraints.RequiredModality {
	case ModalityUnknown:
		return p.UnknownFallback
	case ModalityVideo:
		return p.EffectiveVideo()
	case ModalityImage:
		return p.EffectiveImage()
	case ModalityText:
		return p.EffectiveText()
	default:
		return false
	}
}

// SelectChannel picks the highest-priority channel, weight-random within the tier.
func SelectChannel(candidates []RouterChannel, constraints ChannelRouteConstraints, attemptIndex int) (*RouteDecision, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no compatible channels for modality %s", constraints.RequiredModality)
	}

	// Build the preferred pool only after capability/policy/exclusion filtering.
	// Once exhausted, ordinary channels retain their native priority ordering.
	videoTier := false
	if constraints.VideoRequest {
		var preferred []RouterChannel
		for _, c := range candidates {
			if c.RoutingProfile.VideoPriority != nil {
				preferred = append(preferred, c)
			}
		}
		if len(preferred) > 0 {
			candidates = preferred
			videoTier = true
		}
	}
	maxPriority := candidates[0].Priority
	if videoTier {
		maxPriority = *candidates[0].RoutingProfile.VideoPriority
	}
	for _, c := range candidates[1:] {
		priority := c.Priority
		if videoTier {
			priority = *c.RoutingProfile.VideoPriority
		}
		maxPriority = max(maxPriority, priority)
	}

	var sameTier []RouterChannel
	totalWeight := 0
	for _, c := range candidates {
		priority := c.Priority
		if videoTier {
			priority = *c.RoutingProfile.VideoPriority
		}
		if priority == maxPriority {
			sameTier = append(sameTier, c)
			totalWeight += max(c.Weight, 1)
		}
	}

	roll := rand.Intn(totalWeight)
	selected := sameTier[len(sameTier)-1]
	for _, c := range sameTier {
		roll -= max(c.Weight, 1)
		if roll < 0 {
			selected = c
			break
		}
	}

	selectionReason := "capability_match"
	if videoTier {
		selectionReason = "video_priority"
	}
	return &RouteDecision{
		ChannelID:       selected.ID,
		Priority:        selected.Priority,
		Weight:          selected.Weight,
		Modality:        constraints.RequiredModality,
		SelectionReason: selectionReason,
		AttemptIndex:    attemptIndex,
	}, nil
}

// RouteCandidates is the one-step pipeline: demand → constraints → filter → select.
func RouteCandidates(demand RouteDemand, channels []RouterChannel, group string, settings ai_router_setting.Settings) (*RouteDecision, []RouterChannel, error) {
	constraints := BuildConstraints(demand, settings)
	filtered := FilterChannels(channels, constraints, group)
	decision, err := SelectChannel(filtered, constraints, 0)
	if err != nil {
		return nil, filtered, err
	}
	return decision, filtered, nil
}
