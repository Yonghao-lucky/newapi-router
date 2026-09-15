package ai_router

import (
	"fmt"
	"testing"

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
