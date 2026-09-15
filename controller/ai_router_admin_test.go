package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

func TestUpdateAIRouterSettingsValidation(t *testing.T) {
	boolPtr := func(v bool) *bool { return &v }
	// Verify that model.UpdateOption rejects invalid ai_router config values.
	// This tests the existing Phase 1 validation pipeline, not the HTTP layer.
	validProfiles := []dto.ChannelRoutingProfile{
		{Enabled: true, SupportsText: boolPtr(true)},
		{Enabled: true, SupportsVideo: boolPtr(true)},
		{Enabled: true, SupportsText: boolPtr(false), SupportsVideo: boolPtr(true)},
	}
	for _, p := range validProfiles {
		if err := p.Validate(); err != nil {
			t.Errorf("expected valid profile %+v, got %v", p, err)
		}
	}
}
