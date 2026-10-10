package controller

import (
	"fmt"
	"io"
	"net/http"
	"time"

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
	globalResponseAlias := settings.ResponseModelAlias(c.Request.URL.Path, relayInfo.OriginModelName)
	isChatCompletions := c.Request.URL.Path == "/v1/chat/completions"

	// Build the RouteDemand by detecting request capabilities.
	bodyStorage, bodyErr := common.GetBodyStorage(c)
	if bodyErr != nil {
		*newAPIError = types.NewErrorWithStatusCode(bodyErr, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		return
	}

	demand, cached := ai_router.CachedStoredChatDemand(c, bodyStorage)
	if !cached {
		reader, readerErr := bodyStorage.NewReader()
		if readerErr != nil {
			*newAPIError = types.NewErrorWithStatusCode(readerErr, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
			return
		}
		var detectErr error
		demand, detectErr = ai_router.DetectRequest(c.Request.URL.Path, reader)
		_ = reader.Close()
		if detectErr != nil {
			*newAPIError = types.NewError(fmt.Errorf("ai router detection failed: %w", detectErr), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
			return
		}
	}
	demand.Model = relayInfo.OriginModelName
	demand.RequireThinkingDisabled = c.GetBool("ai_router_require_thinking_disabled")
	if c.GetBool("ai_router_has_unknown_input") && len(demand.UnknownContentTypes) == 0 {
		demand.UnknownContentTypes = []string{"unrecognized_input"}
	}
	if common.GetContextKeyBool(c, constant.ContextKeyVideoPreprocessed) &&
		common.GetContextKeyString(c, constant.ContextKeyVideoOriginalModality) == string(ai_router.ModalityVideo) {
		demand.OriginalModality = ai_router.ModalityVideo
	}

	// Resolve the group first (mirrors New API's own selection), then build
	// the candidate set from the target policy snapshot intersected with the
	// model/state-eligible channels of that group. All reads come from memory
	// snapshots and the channel cache; the policy tables are never queried on
	// this path.
	filters := service.GetChannelConstraints(c).Filters
	group := resolveRouterGroup(c, relayInfo, filters)
	policy := ai_router.RequestPolicySnapshot(c).GetPolicy(relayInfo.OriginModelName, group)
	allChannels := collectPolicyChannelsWithPolicy(group, relayInfo.OriginModelName, filters, policy)
	maxAttempts := settings.MaxAttempts
	if policy != nil && policy.Enabled && policy.MaxAttempts >= 1 {
		maxAttempts = policy.MaxAttempts
	}
	routeCtx := ai_router.NewRouteContext(demand, maxAttempts)
	routeCtx.Context = c.Request.Context()
	if demand.OriginalModality == ai_router.ModalityVideo {
		routeCtx.LogModality = ai_router.ModalityVideo
	}
	routeCtx.RequestID = c.GetString(common.RequestIdKey)
	common.SetContextKey(c, constant.ContextKeyAIRouterContext, routeCtx)

	// Call relay for a specific channel. SetupContextForSelectedChannel
	// updates the gin context so InitChannelMeta picks up the new channel.
	// Each call captures its outcome into routeCtx.LastCall for the
	// per-attempt telemetry (attempt logs), including the channel name
	// snapshotted from the cache.
	callRelay := func() *types.NewAPIError {
		c.Set(common.UpstreamRequestIdKey, "")
		c.Set(common.RouterUpstreamStatusKey, 0)
		c.Set(common.RouterRetryAfterKey, time.Duration(0))
		callStart := time.Now()
		call := &ai_router.AttemptCall{}
		defer func() {
			call.UpstreamStatus = c.GetInt(common.RouterUpstreamStatusKey)
			if value, ok := c.Get(common.RouterRetryAfterKey); ok {
				call.RetryAfter, _ = value.(time.Duration)
			}
			call.DurationMs = time.Since(callStart).Milliseconds()
			routeCtx.LastCall = call
		}()

		channel, err := model.CacheGetChannel(routeCtx.Decision.ChannelID)
		if err != nil {
			call.Err = types.NewError(fmt.Errorf("ai router: channel %d not found in cache: %w", routeCtx.Decision.ChannelID, err), types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
			return call.Err
		}
		call.ChannelName = channel.Name
		if setupErr := middleware.SetupContextForSelectedChannel(c, channel, relayInfo.OriginModelName); setupErr != nil {
			call.Err = setupErr
			return setupErr
		}
		// Resolve once per selected channel; never let a previous attempt's
		// alias leak into a channel that opts out. Stream events use this snapshot.
		if isChatCompletions {
			alias := channel.GetOtherSettings().ResponseModelAlias.Resolve(globalResponseAlias, relayInfo.OriginModelName)
			common.SetChatResponseModelAlias(c, alias)
		}
		addUsedChannel(c, channel.Id)

		if billingErr := service.PrepareTieredBillingForSelectedGroup(c, relayInfo); billingErr != nil {
			call.Err = billingErr
			return billingErr
		}

		// Reset body for this attempt.
		c.Request.Body = io.NopCloser(bodyStorage)

		// FirstResponseTime is request-sticky; compare before/after to know
		// whether THIS call got a response.
		respondedBefore := relayInfo.HasSendResponse()

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
		if budget := common.RouterBudget(c.Request.Context()); budget != nil {
			if budgetErr := budget.APIError(); budgetErr != nil {
				relayErr = budgetErr
			}
		}

		// Capture response-start state on both success and error paths:
		// TTFT must be recorded for successful streams too. The loop never
		// re-enters after a response started, so the first responding call
		// is also the only one.
		if relayInfo.HasSendResponse() || c.Writer.Written() {
			routeCtx.ResponseStarted = true
			call.Responded = !respondedBefore
			if routeCtx.TTFT == 0 && relayInfo.HasSendResponse() {
				ttft := relayInfo.FirstResponseTime.Sub(relayInfo.StartTime).Milliseconds()
				routeCtx.TTFT = ttft
				call.TtftMs = &ttft
			}
			// The relay returned without error but the stream ended
			// abnormally (panic, scanner error, client-visible timeout):
			// 断流, not success.
			call.StreamInterrupted = !relayInfo.StreamStatus.IsNormalEnd()
			if call.StreamInterrupted {
				routeCtx.StreamInterrupted = true
			}
		}

		if relayErr == nil {
			relayInfo.LastError = nil
			return nil
		}
		relayErr = service.NormalizeViolationFeeError(relayErr)
		relayInfo.LastError = relayErr
		call.Err = relayErr

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

	// Record Telemetry asynchronously (never blocks the request). A stream
	// that started but ended abnormally is 断流: not a success for the
	// summary either, even when the relay returned no error.
	success := finalErr == nil && !routeCtx.StreamInterrupted
	errMsg := ""
	if finalErr != nil {
		errMsg = finalErr.Error()
		logger.LogError(c, fmt.Sprintf("ai_router relay error: %s", common.LocalLogPreview(finalErr.Error())))
	} else if routeCtx.StreamInterrupted {
		errMsg = "stream interrupted"
	}
	if common.GetContextKeyBool(c, constant.ContextKeyVideoPreprocessed) {
		if finalErr != nil {
			errMsg = fmt.Sprintf("video relay failed with HTTP status %d", finalErr.StatusCode)
		}
		routeCtx.Demand.UnknownContentTypes = nil
	}
	ai_router.RecordTelemetry(routeCtx, relayInfo.UserId, group, success, errMsg)
}

// resolveRouterGroup picks the group the Router enumerates channels from,
// mirroring New API's own selection: an empty token group falls back to
// "default", and "auto" expands to the first auto group that can serve the
// model. The resolved auto group is published so billing uses its ratio.
func resolveRouterGroup(c *gin.Context, relayInfo *relaycommon.RelayInfo, filters []dto.ChannelFilter) string {
	return service.ResolveAIRouterGroup(c, relayInfo.OriginModelName, relayInfo.TokenGroup, filters)
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
			AllowsThinkingDisabled: ai_router.ChannelAllowsThinkingDisabled(ch, modelName),
			ID:                     ch.Id,
			Priority:               ch.GetPriority(),
			Weight:                 ch.GetWeight(),
			Group:                  group,
			RoutingProfile:         profile,
		})
	}
	return result
}

// collectPolicyChannels builds the Router's candidate set for a request: the
// enabled candidates of the (model, group) target policy, intersected with the
// model-eligible enabled channels of the group. Channels outside the policy
// are never considered, for the first selection or for fallback. When no
// enabled policy covers (model, group), it falls back to the V1-compatible
// behavior of considering every model-eligible channel in the group.
func collectPolicyChannels(group, modelName string, filters []dto.ChannelFilter, defaultMaxAttempts int) []ai_router.RouterChannel {
	policy := ai_router.GetTargetPolicySnapshot().GetPolicy(modelName, group)
	return collectPolicyChannelsWithPolicy(group, modelName, filters, policy)
}

func collectPolicyChannelsWithPolicy(group, modelName string, filters []dto.ChannelFilter, policy *ai_router.TargetPolicy) []ai_router.RouterChannel {
	groupChannels := collectChannelsForGroup(group, modelName, filters)
	if policy == nil || !policy.Enabled || len(policy.Candidates) == 0 {
		return groupChannels
	}
	candidateEnabled := make(map[int]bool, len(policy.Candidates))
	for _, candidate := range policy.Candidates {
		if candidate.Enabled {
			candidateEnabled[candidate.ChannelID] = true
		}
	}
	result := make([]ai_router.RouterChannel, 0, len(candidateEnabled))
	for _, ch := range groupChannels {
		if candidateEnabled[ch.ID] {
			result = append(result, ch)
		}
	}
	return result
}

// policyMaxAttempts returns the policy's own max_attempts when an enabled
// policy covers (model, group); otherwise the global default applies.
func policyMaxAttempts(group, modelName string, defaultMaxAttempts int) int {
	policy := ai_router.GetTargetPolicySnapshot().GetPolicy(modelName, group)
	if policy != nil && policy.Enabled && policy.MaxAttempts >= 1 {
		return policy.MaxAttempts
	}
	return defaultMaxAttempts
}
