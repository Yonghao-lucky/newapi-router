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
//  3. Call relay; on a retryable error and response not started, retry the
//     same channel up to its max_retry before excluding it
//  4. On success or non-retryable error or response started, return
//
// max_attempts counts distinct channels; max_retry only adds calls to the
// current channel. callRelay is the callback that invokes the actual relay
// handler.
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
		rc.ChannelRetryNo = 0

		// Channel-level retries: max_retry extra calls to the SAME channel
		// after retryable failures, before the channel joins the attempted
		// set and we move on. Non-retryable errors and a started response
		// return immediately; the attempted-set exclusion below must not run
		// until this channel's own retries are exhausted.
		maxRetry := 0
		if selected := findChannel(allChannels, decision.ChannelID); selected != nil {
			maxRetry = selected.RoutingProfile.MaxRetry
		}
		for retryNo := 0; retryNo <= maxRetry; retryNo++ {
			rc.ChannelRetryNo = retryNo
			relayErr := callRelay()
			call := rc.LastCall
			rc.LastCall = nil
			settleAttemptRecord(rc, call, relayErr)
			if relayErr == nil {
				// The successful channel must join the attempt chain before
				// returning, or a first-channel success records attempts=0
				// and an empty chain (breaking channel filters and stats).
				rc.MarkAttempted(decision.ChannelID)
				return nil
			}
			lastError = relayErr

			// If response already started, cannot retry or switch channels.
			// Same for a non-retryable error: this channel was the request's
			// last, so it joins the chain on every exit path.
			if rc.ResponseStarted {
				rc.MarkAttempted(decision.ChannelID)
				return relayErr
			}
			// If non-retryable, return immediately.
			if !isRetryable(relayErr) {
				rc.MarkAttempted(decision.ChannelID)
				return relayErr
			}
			if retryNo < maxRetry {
				continue
			}
			break
		}
		rc.MarkAttempted(decision.ChannelID)
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

// settleAttemptRecord turns one relay call's capture into a settled
// AttemptRecord appended to the route context. Classification reuses the
// router's own retry semantics: Retryable mirrors what the loop just did
// with the error — once a response started the loop returns immediately
// (no retry), otherwise the error's skip-retry flag decides.
func settleAttemptRecord(rc *RouteContext, call *AttemptCall, relayErr *types.NewAPIError) {
	if call == nil {
		// callRelay failed before its capture (e.g. cache miss): still
		// record the attempt so the chain has no gaps.
		call = &AttemptCall{Err: relayErr}
	}
	category, status := ClassifyAttemptError(relayErr, call.Responded)
	retryable := false
	if relayErr != nil && !call.Responded && !rc.ResponseStarted {
		retryable = isRetryable(relayErr)
	}
	rc.AttemptRecords = append(rc.AttemptRecords, &AttemptRecord{
		AttemptSeq:     rc.CurrentAttempt + 1, // 1-based over distinct channels
		ChannelRetryNo: rc.ChannelRetryNo,
		ChannelId:      rc.Decision.ChannelID,
		ChannelName:    call.ChannelName,
		Modality:       string(rc.Demand.Modality),
		HttpStatusCode: status,
		ErrorCategory:  category,
		Retryable:      retryable,
		DurationMs:     call.DurationMs,
		TtftMs:         call.TtftMs,
		Success:        relayErr == nil,
	})
}

func findChannel(channels []RouterChannel, id int) *RouterChannel {
	for i := range channels {
		if channels[i].ID == id {
			return &channels[i]
		}
	}
	return nil
}
