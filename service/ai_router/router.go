package ai_router

import (
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"

	"github.com/gin-gonic/gin"
)

func timeNowUnixNano() int64 { return time.Now().UnixNano() }

// ShouldUseRouter returns true when the Router should intercept this request:
// globally enabled, target model exact match, and a supported path.
func ShouldUseRouter(c *gin.Context, modelName, requestPath string) bool {
	settings := ai_router_setting.GetSettings()
	if !settings.Enabled {
		return false
	}
	for _, m := range settings.TargetModels {
		if m == modelName {
			return requestPath == chatCompletionsPath
		}
	}
	return false
}

// FilterChannelsWithExclusions applies capability filtering plus per-request
// exclusion of already-attempted channels.
func FilterChannelsWithExclusions(channels []RouterChannel, constraints ChannelRouteConstraints, group string, attempted map[int]bool) []RouterChannel {
	filtered := FilterChannels(channels, constraints, group)
	if len(attempted) == 0 {
		return filtered
	}
	var result []RouterChannel
	for _, c := range filtered {
		if !attempted[c.ID] {
			result = append(result, c)
		}
	}
	return result
}

// RunFallbackLoop implements the request-level fallback strategy:
//  1. Filter by capability (excluding already-attempted channels)
//  2. Select highest priority, weight-random within tier
//  3. Call relay
//  4. On retryable error and response not started, exclude channel and retry
//  5. On success or non-retryable error or response started, return
//
// callRelay is the callback that invokes the actual relay handler.
func RunFallbackLoop(
	rc *RouteContext,
	allChannels []RouterChannel,
	group string,
	settings ai_router_setting.Settings,
	callRelay func() *types.NewAPIError,
) *types.NewAPIError {
	rc.Constraints = BuildConstraints(rc.Demand, settings)

	var lastError *types.NewAPIError

	for attempt := 0; attempt < rc.MaxAttempts; attempt++ {
		rc.CurrentAttempt = attempt
		candidates := FilterChannelsWithExclusions(
			allChannels, rc.Constraints, group, rc.AttemptedChannels,
		)
		if len(candidates) == 0 {
			if lastError != nil {
				return lastError
			}
			return types.NewError(
				fmt.Errorf("no compatible channel for modality %s", rc.Demand.Modality),
				types.ErrorCodeGetChannelFailed,
				types.ErrOptionWithSkipRetry(),
			)
		}

		decision, err := SelectChannel(candidates, rc.Constraints, attempt)
		if err != nil {
			if lastError != nil {
				return lastError
			}
			return types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		rc.Decision = *decision
		rc.MarkAttempted(decision.ChannelID)

		relayErr := callRelay()
		if relayErr == nil {
			return nil
		}
		lastError = relayErr

		// If response already started, cannot fallback to another channel.
		if rc.ResponseStarted {
			return relayErr
		}

		// If non-retryable, return immediately.
		if !isRetryable(relayErr) {
			return relayErr
		}
	}

	return lastError
}

// isRetryable uses New API's existing retry classification.
// Router does not maintain its own error taxonomy.
func isRetryable(err *types.NewAPIError) bool {
	if err == nil {
		return false
	}
	if types.IsSkipRetryError(err) {
		return false
	}
	return true
}
