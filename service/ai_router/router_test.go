package ai_router

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"
)

func TestFallbackLoopBasic(t *testing.T) {
	chs := fixtureChannels()
	settings := ai_router_setting.DefaultSettings()
	settings.Enabled = true
	settings.TargetModels = []string{"kimi-k3"}
	settings.MaxAttempts = 3

	demand := RouteDemand{Model: "kimi-k3", Modality: ModalityText, DetectorFamily: "openai_chat"}
	rc := NewRouteContext(demand, settings.MaxAttempts)

	attempts := []int{}
	callRelay := func() *types.NewAPIError {
		attempts = append(attempts, rc.Decision.ChannelID)
		if len(attempts) < 2 {
			// First attempt (highest priority text channel) fails with a retryable error.
			return types.NewError(
				fmt.Errorf("mock 429"),
				types.ErrorCodeModelNotFound,
			)
		}
		return nil // second attempt succeeds
	}

	err := RunFallbackLoop(rc, chs, "default", settings, callRelay)
	if err != nil {
		t.Fatalf("expected success after fallback, got: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(attempts))
	}
	// First attempt should be channel 1 or 2 (priority 100, text).
	if attempts[0] == 3 {
		t.Errorf("first attempt should be priority 100, got channel %d (priority 50)", attempts[0])
	}
	// Must not repeat the same channel.
	if attempts[0] == attempts[1] {
		t.Errorf("repeated channel %d", attempts[0])
	}
}

func TestFallbackLoopAllFail(t *testing.T) {
	chs := fixtureChannels()
	settings := ai_router_setting.DefaultSettings()
	settings.Enabled = true
	settings.MaxAttempts = 3

	demand := RouteDemand{Model: "kimi-k3", Modality: ModalityText, DetectorFamily: "openai_chat"}
	rc := NewRouteContext(demand, settings.MaxAttempts)

	attemptCount := 0
	callRelay := func() *types.NewAPIError {
		attemptCount++
		return types.NewError(
			fmt.Errorf("mock error"),
			types.ErrorCodeModelNotFound,
		)
	}

	err := RunFallbackLoop(rc, chs, "default", settings, callRelay)
	if err == nil {
		t.Fatal("expected error after all attempts fail")
	}
	if attemptCount != settings.MaxAttempts {
		t.Errorf("expected %d attempts, got %d", settings.MaxAttempts, attemptCount)
	}
}

func TestRouterDisabledNotSelected(t *testing.T) {
	chs := fixtureChannels()
	settings := ai_router_setting.DefaultSettings()
	settings.Enabled = false
	settings.MaxAttempts = 3

	demand := RouteDemand{Model: "kimi-k3", Modality: ModalityText, DetectorFamily: "openai_chat"}
	rc := NewRouteContext(demand, settings.MaxAttempts)

	callRelay := func() *types.NewAPIError { return nil }
	err := RunFallbackLoop(rc, chs, "default", settings, callRelay)
	// Even with router disabled in settings, FilterChannels still works.
	// The check "Router disabled → skip Router entirely" happens in ShouldUseRouter.
	// Here we just verify the fallback loop operates correctly with the fixture.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNoCompatibleChannel(t *testing.T) {
	chs := fixtureChannels()
	settings := ai_router_setting.DefaultSettings()
	settings.MaxAttempts = 3

	demand := RouteDemand{Model: "kimi-k3", Modality: ModalityVideo, DetectorFamily: "openai_chat"}
	rc := NewRouteContext(demand, settings.MaxAttempts)
	// Channel 3 has video but we'll exclude it by attempted set.
	rc.MarkAttempted(3)

	callRelay := func() *types.NewAPIError { return nil }
	err := RunFallbackLoop(rc, chs, "default", settings, callRelay)
	if err == nil {
		t.Fatal("expected error when no compatible channel remains")
	}
}

// max_retry=2 means the selected channel is called at most 3 times before it
// joins the attempted set and the loop moves to the next channel.
func TestChannelMaxRetryExhaustsBeforeSwitch(t *testing.T) {
	chs := []RouterChannel{
		{ID: 1, Priority: 100, Weight: 1, Group: "default", RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsText: ptr(true), MaxRetry: 2}},
		{ID: 2, Priority: 50, Weight: 1, Group: "default", RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsText: ptr(true)}},
	}
	settings := ai_router_setting.DefaultSettings()
	settings.MaxAttempts = 3

	demand := RouteDemand{Model: "kimi-k3", Modality: ModalityText, DetectorFamily: "openai_chat"}
	rc := NewRouteContext(demand, settings.MaxAttempts)

	type call struct {
		channelID int
		retryNo   int
	}
	var calls []call
	callRelay := func() *types.NewAPIError {
		calls = append(calls, call{rc.Decision.ChannelID, rc.ChannelRetryNo})
		return types.NewError(fmt.Errorf("mock 429"), types.ErrorCodeModelNotFound)
	}

	err := RunFallbackLoop(rc, chs, "default", settings, callRelay)
	if err == nil {
		t.Fatal("expected final error after all retries fail")
	}
	// Channel 1: retryNo 0,1,2 (1 call + max_retry 2), then channel 2 once.
	if len(calls) != 4 {
		t.Fatalf("expected 4 calls (3 on channel 1 + 1 on channel 2), got %d", len(calls))
	}
	for i, c := range calls[:3] {
		if c.channelID != 1 || c.retryNo != i {
			t.Errorf("call %d: channel %d retry %d, want channel 1 retry %d", i, c.channelID, c.retryNo, i)
		}
	}
	if calls[3].channelID != 2 || calls[3].retryNo != 0 {
		t.Errorf("call 3 should be channel 2 retry 0, got channel %d retry %d", calls[3].channelID, calls[3].retryNo)
	}
	if rc.ChannelRetryNo != 0 {
		t.Errorf("new channel must restart retry numbering, got %d", rc.ChannelRetryNo)
	}
}

// Non-retryable errors must not consume channel-level retries.
func TestNonRetryableErrorSkipsMaxRetry(t *testing.T) {
	chs := []RouterChannel{
		{ID: 1, Priority: 100, Weight: 1, Group: "default", RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsText: ptr(true), MaxRetry: 5}},
	}
	settings := ai_router_setting.DefaultSettings()
	settings.MaxAttempts = 3

	demand := RouteDemand{Model: "kimi-k3", Modality: ModalityText, DetectorFamily: "openai_chat"}
	rc := NewRouteContext(demand, settings.MaxAttempts)

	callCount := 0
	callRelay := func() *types.NewAPIError {
		callCount++
		return types.NewError(
			fmt.Errorf("invalid request"),
			types.ErrorCodeInvalidRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}

	err := RunFallbackLoop(rc, chs, "default", settings, callRelay)
	if err == nil {
		t.Fatal("expected the non-retryable error")
	}
	if callCount != 1 {
		t.Fatalf("non-retryable error must stop after 1 call, got %d", callCount)
	}
}

// Once the response has started, neither same-channel retries nor channel
// switches are allowed.
func TestResponseStartedStopsRetriesAndSwitch(t *testing.T) {
	chs := []RouterChannel{
		{ID: 1, Priority: 100, Weight: 1, Group: "default", RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsText: ptr(true), MaxRetry: 5}},
		{ID: 2, Priority: 50, Weight: 1, Group: "default", RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsText: ptr(true)}},
	}
	settings := ai_router_setting.DefaultSettings()
	settings.MaxAttempts = 3

	demand := RouteDemand{Model: "kimi-k3", Modality: ModalityText, DetectorFamily: "openai_chat"}
	rc := NewRouteContext(demand, settings.MaxAttempts)

	callCount := 0
	callRelay := func() *types.NewAPIError {
		callCount++
		rc.ResponseStarted = true
		return types.NewError(fmt.Errorf("stream interrupted"), types.ErrorCodeModelNotFound)
	}

	err := RunFallbackLoop(rc, chs, "default", settings, callRelay)
	if err == nil {
		t.Fatal("expected the stream error")
	}
	if callCount != 1 {
		t.Fatalf("response started must stop after 1 call, got %d", callCount)
	}
}
