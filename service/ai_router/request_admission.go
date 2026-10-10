package ai_router

import (
	"net/http"
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// ChannelAllowsThinkingDisabled uses the same exact outgoing-model scope as
// relay compatibility. The capability never changes thinking or usage values.
func ChannelAllowsThinkingDisabled(channel *model.Channel, clientModel string) bool {
	settings := channel.GetSetting()
	if !settings.K3AllowThinkingDisabled {
		return false
	}
	modelName := clientModel
	if channel.ModelMapping != nil && *channel.ModelMapping != "" {
		var mapping map[string]string
		if common.UnmarshalJsonStr(*channel.ModelMapping, &mapping) != nil {
			return false
		}
		if mapped := mapping[clientModel]; mapped != "" {
			modelName = mapped
		}
	}
	return slices.Contains(settings.ChatCompatibilityModels, modelName)
}

// AdmitRouteInput runs on cached configuration, before media processing. It
// treats absent capability differently from temporarily unavailable capacity.
// No changes to candidate sets or credentials are made here.
func AdmitRouteInput(demand *RouteDemand, group string, policy *TargetPolicy) *types.NewAPIError {
	if demand.ContentError != nil {
		return demand.ContentError.APIError()
	}
	unknown := len(demand.UnknownContentTypes) > 0
	if !unknown && !demand.ThinkingDisabled {
		return nil
	}
	configured, ready := model.CachedConfiguredChannelsForGroupModel(group, demand.Model)
	if !ready {
		return types.WithOpenAIError(types.OpenAIError{Type: "server_unavailable", Code: "route_capability_unavailable", Message: "route capabilities are temporarily unavailable"}, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
	}
	allowed := map[int]bool{}
	restricted := policy != nil && policy.Enabled && len(policy.Candidates) > 0
	if restricted {
		for _, c := range policy.Candidates {
			allowed[c.ChannelID] = true
		}
	}
	known, unknownSupported, thinkingSupported := false, false, false
	for _, channel := range configured {
		if restricted && !allowed[channel.Id] {
			continue
		}
		known = true
		otherSettings := channel.GetOtherSettings()
		profile := otherSettings.RoutingProfile()
		unknownSupported = unknownSupported || profile.UnknownFallback
		thinkingSupported = thinkingSupported || ChannelAllowsThinkingDisabled(channel, demand.Model)
	}
	if !known {
		return types.WithOpenAIError(types.OpenAIError{Type: "server_unavailable", Code: "no_available_channel", Message: "no configured channel is available for this route"}, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
	}
	if unknown && !unknownSupported {
		// Do not echo arbitrary discriminator strings: they can contain secrets.
		message := "This content type is not supported by the current route. Use a supported text/image/video content block or an endpoint supporting this input."
		for _, kind := range demand.UnknownContentTypes {
			switch kind {
			case "document", "file", "input_audio":
				message = "Content type '" + kind + "' is not supported by the current route. Use an endpoint supporting this input."
			case "video":
				message = "Content type 'video' is not supported; use a video_url block with video_url.url. Renaming type alone does not convert the content."
			}
			break
		}
		return (&ChatValidationError{Param: demand.UnknownParam, Message: message, Code: "unsupported_content_type"}).APIError()
	}
	strict := policy != nil && policy.Enabled && policy.K3StrictValidation
	if demand.ThinkingDisabled && strict && !thinkingSupported {
		return (&ChatValidationError{Param: "thinking.type", Message: "This route does not support thinking.type 'disabled'; use a route configured to allow disabling thinking.", Code: "unsupported_thinking_mode"}).APIError()
	}
	// Legacy non-strict routes stay unchanged unless explicitly opted in.
	demand.RequireThinkingDisabled = demand.ThinkingDisabled && thinkingSupported
	return nil
}
