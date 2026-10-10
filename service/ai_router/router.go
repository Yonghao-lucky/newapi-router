package ai_router

import (
	"net/http"
	"slices"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
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
		if err := routerCancellation(rc); err != nil {
			return err
		}
		rc.CurrentAttempt = attempt
		candidates := FilterChannelsWithExclusions(
			allChannels, rc.Constraints, group, rc.AttemptedChannels,
		)
		if len(candidates) == 0 {
			if lastError != nil {
				return lastError
			}
			return types.WithOpenAIError(types.OpenAIError{Type: "server_unavailable", Code: "no_available_channel", Message: "No available channel for this request; please retry later."}, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
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
		profile := dto.DefaultChannelRoutingProfile()
		if selected := findChannel(allChannels, decision.ChannelID); selected != nil {
			profile = selected.RoutingProfile
		}
		maxRetry := profile.MaxRetry
		for retryNo := 0; retryNo <= maxRetry; retryNo++ {
			if err := routerCancellation(rc); err != nil {
				return err
			}
			rc.ChannelRetryNo = retryNo
			relayErr := callRelay()
			if canceled := routerCancellation(rc); canceled != nil {
				relayErr = canceled
			}
			call := rc.LastCall
			rc.LastCall = nil
			action := retryAction(rc, profile, call, relayErr)
			if action == "same_channel" && retryNo >= maxRetry {
				action = "next_channel"
			}
			if action == "next_channel" && (attempt+1 >= rc.MaxAttempts || len(FilterChannelsWithExclusions(allChannels, rc.Constraints, group, remainingExclusions(rc, decision.ChannelID))) == 0) {
				action = "stop"
			}
			settleAttemptRecord(rc, call, relayErr)
			record := rc.AttemptRecords[len(rc.AttemptRecords)-1]
			record.Retryable = action != "stop"
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
			if action == "stop" {
				rc.MarkAttempted(decision.ChannelID)
				return relayErr
			}
			if call != nil && call.RetryAfter > 0 {
				timer := time.NewTimer(call.RetryAfter)
				select {
				case <-timer.C:
				case <-rc.Context.Done():
					timer.Stop()
					record.Retryable = false
					rc.MarkAttempted(decision.ChannelID)
					return routerCancellation(rc)
				}
			}
			if action == "same_channel" {
				continue
			}
			break
		}
		rc.MarkAttempted(decision.ChannelID)
	}

	return lastError
}

func retryAction(rc *RouteContext, profile dto.ChannelRoutingProfile, call *AttemptCall, err *types.NewAPIError) string {
	if err == nil || types.IsSkipRetryError(err) || rc.ResponseStarted || routerCancellation(rc) != nil || call == nil || call.Responded {
		return "stop"
	}
	if slices.Contains(profile.EffectiveRetryStatusCodes(), call.UpstreamStatus) {
		return "same_channel"
	}
	if slices.Contains(profile.EffectiveSwitchStatusCodes(), call.UpstreamStatus) {
		return "next_channel"
	}
	return "stop"
}

func routerCancellation(rc *RouteContext) *types.NewAPIError {
	if rc.Context == nil || rc.Context.Err() == nil {
		return nil
	}
	if b := common.RouterBudget(rc.Context); b != nil {
		return b.APIError()
	}
	return types.NewErrorWithStatusCode(rc.Context.Err(), types.ErrorCodeDoRequestFailed, 499, types.ErrOptionWithSkipRetry())
}

func remainingExclusions(rc *RouteContext, current int) map[int]bool {
	result := make(map[int]bool, len(rc.AttemptedChannels)+1)
	for id, excluded := range rc.AttemptedChannels {
		result[id] = excluded
	}
	result[current] = true
	return result
}

// settleAttemptRecord turns one relay call's capture into a settled
// AttemptRecord appended to the route context. Classification reuses the
// router's own retry semantics: Retryable mirrors what the loop just did
// with the error — once a response started the loop returns immediately
// (no retry), otherwise the error's skip-retry flag decides. Success comes
// from the classifier so an interrupted stream (relay returned nil) still
// records as a failure.
func settleAttemptRecord(rc *RouteContext, call *AttemptCall, relayErr *types.NewAPIError) {
	if call == nil {
		// callRelay failed before its capture (e.g. cache miss): still
		// record the attempt so the chain has no gaps.
		call = &AttemptCall{Err: relayErr}
	}
	category, status, success := ClassifyAttemptError(relayErr, call.StreamInterrupted)
	if relayErr != nil && !call.StreamInterrupted {
		if relayErr.GetErrorCode() == "ai_router_idle_timeout" {
			category = "timeout"
		} else if call.UpstreamStatus >= 400 {
			switch {
			case call.UpstreamStatus == 429:
				category = "429"
			case call.UpstreamStatus >= 500:
				category = "5xx"
			default:
				category = "other"
			}
		}
	}
	if call.UpstreamStatus > 0 {
		status = &call.UpstreamStatus
	} else {
		status = nil
	}
	rc.AttemptRecords = append(rc.AttemptRecords, &AttemptRecord{
		AttemptSeq:     rc.CurrentAttempt + 1, // 1-based over distinct channels
		ChannelRetryNo: rc.ChannelRetryNo,
		ChannelId:      rc.Decision.ChannelID,
		ChannelName:    call.ChannelName,
		Modality:       string(rc.LogModality),
		HttpStatusCode: status,
		ErrorCategory:  category,
		Retryable:      false,
		DurationMs:     call.DurationMs,
		TtftMs:         call.TtftMs,
		Success:        success,
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
