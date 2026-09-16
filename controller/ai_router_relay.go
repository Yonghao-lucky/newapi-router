package controller

import (
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	ai_router "github.com/QuantumNous/new-api/service/ai_router"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"

	"github.com/gin-gonic/gin"
)

// runAIRouterRelay implements the AI Router fallback path. It replaces
// the standard retry loop when the Router is active for this request.
func runAIRouterRelay(
	c *gin.Context,
	relayInfo *relaycommon.RelayInfo,
	relayFormat types.RelayFormat,
	newAPIError **types.NewAPIError,
) {
	settings := ai_router_setting.GetSettings()

	// Build the RouteDemand by detecting request capabilities.
	bodyStorage, bodyErr := common.GetBodyStorage(c)
	if bodyErr != nil {
		*newAPIError = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		return
	}

	reader, readerErr := bodyStorage.NewReader()
	if readerErr != nil {
		*newAPIError = types.NewErrorWithStatusCode(readerErr, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		return
	}
	defer reader.Close()
	demand, detectErr := ai_router.DetectRequest(c.Request.URL.Path, reader)
	if detectErr != nil {
		*newAPIError = types.NewError(fmt.Errorf("ai router detection failed: %w", detectErr), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
		return
	}
	demand.Model = relayInfo.OriginModelName

	// Collect all enabled channels for this group and model from cache.
	filters := service.GetChannelConstraints(c).Filters
	group := resolveRouterGroup(c, relayInfo, filters)
	allChannels := collectChannelsForGroup(group, relayInfo.OriginModelName, filters)

	routeCtx := ai_router.NewRouteContext(demand, settings.MaxAttempts)
	routeCtx.RequestID = c.GetString(common.RequestIdKey)
	common.SetContextKey(c, constant.ContextKeyAIRouterContext, routeCtx)

	// Call relay for a specific channel. SetupContextForSelectedChannel
	// updates the gin context so InitChannelMeta picks up the new channel.
	callRelay := func() *types.NewAPIError {
		channel, err := model.CacheGetChannel(routeCtx.Decision.ChannelID)
		if err != nil {
			return types.NewError(fmt.Errorf("ai router: channel %d not found in cache: %w", routeCtx.Decision.ChannelID, err), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		if setupErr := middleware.SetupContextForSelectedChannel(c, channel, relayInfo.OriginModelName); setupErr != nil {
			return setupErr
		}
		addUsedChannel(c, channel.Id)

		if billingErr := service.PrepareTieredBillingForSelectedGroup(c, relayInfo); billingErr != nil {
			return billingErr
		}

		// Reset body for this attempt.
		c.Request.Body = io.NopCloser(bodyStorage)

		var relayErr *types.NewAPIError
		switch relayFormat {
		case types.RelayFormatOpenAIRealtime:
			relayErr = relay.WssHelper(c, relayInfo)
		case types.RelayFormatClaude:
			relayErr = relay.ClaudeHelper(c, relayInfo)
		case types.RelayFormatGemini:
			relayErr = geminiRelayHandler(c, relayInfo)
		default:
			relayErr = relayHandler(c, relayInfo)
		}

		// Capture response-start state on both success and error paths:
		// TTFT must be recorded for successful streams too.
		if relayInfo.HasSendResponse() {
			routeCtx.ResponseStarted = true
			if routeCtx.TTFT == 0 {
				routeCtx.TTFT = relayInfo.FirstResponseTime.Sub(relayInfo.StartTime).Milliseconds()
			}
		}

		if relayErr == nil {
			relayInfo.LastError = nil
			return nil
		}
		relayErr = service.NormalizeViolationFeeError(relayErr)
		relayInfo.LastError = relayErr

		processChannelError(c, *types.NewChannelError(
			channel.Id, channel.Type, channel.Name,
			channel.ChannelInfo.IsMultiKey,
			common.GetContextKeyString(c, constant.ContextKeyChannelKey),
			channel.GetAutoBan(),
		), relayErr, relayInfo)

		return relayErr
	}

	finalErr := ai_router.RunFallbackLoop(routeCtx, allChannels, group, settings, callRelay)
	*newAPIError = finalErr

	// Record Telemetry asynchronously (never blocks the request).
	success := finalErr == nil
	errMsg := ""
	if finalErr != nil {
		errMsg = finalErr.Error()
		logger.LogError(c, fmt.Sprintf("ai_router relay error: %s", common.LocalLogPreview(finalErr.Error())))
	}
	ai_router.RecordTelemetry(routeCtx, relayInfo.UserId, group, success, errMsg)
}

// resolveRouterGroup picks the group the Router enumerates channels from,
// mirroring New API's own selection: an empty token group falls back to
// "default", and "auto" expands to the first auto group that can serve the
// model. The resolved auto group is published so billing uses its ratio.
func resolveRouterGroup(c *gin.Context, relayInfo *relaycommon.RelayInfo, filters []dto.ChannelFilter) string {
	group := relayInfo.TokenGroup
	if group == "" {
		group = "default"
	}
	if group != "auto" {
		return group
	}
	userGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
	for _, autoGroup := range service.GetRequestAutoGroups(c, userGroup) {
		if len(model.GetEnabledChannelsForGroupModel(autoGroup, relayInfo.OriginModelName, filters)) > 0 {
			common.SetContextKey(c, constant.ContextKeyAutoGroup, autoGroup)
			return autoGroup
		}
	}
	return group
}

// collectChannelsForGroup converts the model-eligible channels of a group into
// RouterChannel views for the Router pipeline.
func collectChannelsForGroup(group, modelName string, filters []dto.ChannelFilter) []ai_router.RouterChannel {
	channels := model.GetEnabledChannelsForGroupModel(group, modelName, filters)
	result := make([]ai_router.RouterChannel, 0, len(channels))
	for _, ch := range channels {
		otherSettings := ch.GetOtherSettings()
		profile := otherSettings.RoutingProfile()
		result = append(result, ai_router.RouterChannel{
			ID:             ch.Id,
			Priority:       ch.GetPriority(),
			Weight:         ch.GetWeight(),
			Group:          group,
			RoutingProfile: profile,
		})
	}
	return result
}
