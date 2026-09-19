package ai_router

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/ai_router_setting"
)

func TestTelemetryDisabledSkips(t *testing.T) {
	settings := ai_router_setting.DefaultSettings()
	settings.TelemetryEnabled = false
	// Should not panic and should return immediately.
	rc := NewRouteContext(RouteDemand{Model: "m", Modality: ModalityText}, 3)
	RecordTelemetry(rc, 1, "default", true, "")
}

func TestTelemetryEnabledBuilds(t *testing.T) {
	settings := ai_router_setting.DefaultSettings()
	settings.TelemetryEnabled = true

	rc := NewRouteContext(RouteDemand{Model: "kimi-k3", Modality: ModalityText, HasTools: false}, 3)
	rc.RequestID = "test-req-123"
	rc.MarkAttempted(1)
	rc.MarkAttempted(2)
	rc.Decision = RouteDecision{ChannelID: 2, Priority: 100, Modality: ModalityText}
	rc.TTFT = 250

	log := buildLogEntry(rc, 42, "default", true, "")
	if log.RequestId != "test-req-123" {
		t.Errorf("request_id = %s, want test-req-123", log.RequestId)
	}
	if log.UserId != 42 {
		t.Errorf("user_id = %d, want 42", log.UserId)
	}
	if log.Modality != "text" {
		t.Errorf("modality = %s, want text", log.Modality)
	}
	if log.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", log.Attempts)
	}
	if log.FinalChannelId != 2 {
		t.Errorf("final_channel_id = %d, want 2", log.FinalChannelId)
	}
	if log.TTFTMs != 250 {
		t.Errorf("ttft_ms = %d, want 250", log.TTFTMs)
	}
	if !log.Success {
		t.Error("success should be true")
	}
	if log.SelectedChannels == "" {
		t.Error("selected_channels should not be empty")
	}
}

func TestTelemetryErrorLog(t *testing.T) {
	rc := NewRouteContext(RouteDemand{Model: "m", Modality: ModalityVideo}, 3)
	rc.MarkAttempted(5)

	log := buildLogEntry(rc, 1, "default", false, "mock error")
	if log.Success {
		t.Error("success should be false")
	}
	if log.ErrorMessage != "mock error" {
		t.Errorf("error_message = %s, want mock error", log.ErrorMessage)
	}
	if log.Modality != "video" {
		t.Errorf("modality = %s, want video", log.Modality)
	}
	if log.TTFTMs != 0 {
		t.Errorf("ttft_ms = %d, want 0 for non-streaming", log.TTFTMs)
	}
}

func TestTelemetryUsesIndependentLogModality(t *testing.T) {
	rc := NewRouteContext(RouteDemand{Model: "m", Modality: ModalityImage}, 1)
	rc.LogModality = ModalityVideo

	log := buildLogEntry(rc, 1, "default", true, "")

	if rc.Demand.Modality != ModalityImage {
		t.Errorf("routing modality = %s, want image", rc.Demand.Modality)
	}
	if log.Modality != "video" {
		t.Errorf("log modality = %s, want video", log.Modality)
	}
}
