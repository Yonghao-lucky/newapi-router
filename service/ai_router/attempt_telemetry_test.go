package ai_router

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"
)

// max_retry=2 on one channel, every call fails: one record per call with
// channel_retry_no 0,1,2 — the per-attempt ledger matches the retry loop.
func TestAttemptRecordsSameChannelRetries(t *testing.T) {
	chs := []RouterChannel{
		{ID: 1, Priority: 100, Weight: 1, Group: "default", RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsText: ptr(true), MaxRetry: 2}},
	}
	settings := ai_router_setting.DefaultSettings()
	settings.MaxAttempts = 1

	demand := RouteDemand{Model: "kimi-k3", Modality: ModalityText, DetectorFamily: "openai_chat"}
	rc := NewRouteContext(demand, settings.MaxAttempts)
	rc.RequestID = "req-retry"

	callRelay := func() *types.NewAPIError {
		rc.LastCall = &AttemptCall{ChannelName: "ch-1", DurationMs: 40}
		return types.NewErrorWithStatusCode(fmt.Errorf("rate limited"), types.ErrorCodeBadResponseStatusCode, 429)
	}

	err := RunFallbackLoop(rc, chs, "default", settings, callRelay)
	if err == nil {
		t.Fatal("expected final error")
	}
	if len(rc.AttemptRecords) != 3 {
		t.Fatalf("expected 3 attempt records, got %d", len(rc.AttemptRecords))
	}
	for i, record := range rc.AttemptRecords {
		if record.AttemptSeq != 1 {
			t.Errorf("record %d: attempt_seq = %d, want 1", i, record.AttemptSeq)
		}
		if record.ChannelRetryNo != i {
			t.Errorf("record %d: channel_retry_no = %d, want %d", i, record.ChannelRetryNo, i)
		}
		if record.Success {
			t.Errorf("record %d: success should be false", i)
		}
		if record.ErrorCategory != "429" {
			t.Errorf("record %d: category = %s, want 429", i, record.ErrorCategory)
		}
		if record.HttpStatusCode == nil || *record.HttpStatusCode != 429 {
			t.Errorf("record %d: status = %v, want 429", i, record.HttpStatusCode)
		}
		if !record.Retryable {
			t.Errorf("record %d: 429 must be retryable", i)
		}
		if record.ChannelName != "ch-1" {
			t.Errorf("record %d: channel_name = %s, want ch-1", i, record.ChannelName)
		}
	}
}

// A→B fallback: two records with different attempt_seq, both channels
// present, final selection is B.
func TestAttemptRecordsFallbackAcrossChannels(t *testing.T) {
	chs := []RouterChannel{
		{ID: 1, Priority: 100, Weight: 1, Group: "default", RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsText: ptr(true)}},
		{ID: 2, Priority: 50, Weight: 1, Group: "default", RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsText: ptr(true)}},
	}
	settings := ai_router_setting.DefaultSettings()
	settings.MaxAttempts = 3

	demand := RouteDemand{Model: "kimi-k3", Modality: ModalityText, DetectorFamily: "openai_chat"}
	rc := NewRouteContext(demand, settings.MaxAttempts)

	callRelay := func() *types.NewAPIError {
		if len(rc.AttemptRecords) == 0 {
			rc.LastCall = &AttemptCall{ChannelName: "ch-1", DurationMs: 20}
			return types.NewErrorWithStatusCode(fmt.Errorf("upstream exploded"), types.ErrorCodeBadResponseStatusCode, 500)
		}
		rc.LastCall = &AttemptCall{ChannelName: "ch-2", DurationMs: 80, TtftMs: int64Ptr(30)}
		return nil
	}

	if err := RunFallbackLoop(rc, chs, "default", settings, callRelay); err != nil {
		t.Fatalf("expected success on second channel, got: %v", err)
	}
	if len(rc.AttemptRecords) != 2 {
		t.Fatalf("expected 2 attempt records, got %d", len(rc.AttemptRecords))
	}
	first, second := rc.AttemptRecords[0], rc.AttemptRecords[1]
	if first.AttemptSeq != 1 || second.AttemptSeq != 2 {
		t.Errorf("attempt_seq = %d,%d; want 1,2", first.AttemptSeq, second.AttemptSeq)
	}
	if first.ChannelId != 1 || second.ChannelId != 2 {
		t.Errorf("channels = %d,%d; want 1,2", first.ChannelId, second.ChannelId)
	}
	if !second.Success || first.Success {
		t.Errorf("success flags = %v,%v; want false,true", first.Success, second.Success)
	}
	if second.TtftMs == nil || *second.TtftMs != 30 {
		t.Errorf("second ttft_ms = %v, want 30", second.TtftMs)
	}
	if first.ErrorCategory != "5xx" {
		t.Errorf("first category = %s, want 5xx", first.ErrorCategory)
	}
	// Final selection is the last decision (channel 2).
	if rc.Decision.ChannelID != 2 {
		t.Errorf("final channel = %d, want 2", rc.Decision.ChannelID)
	}
}

// First event arrived, then the stream died: the failed call is classified
// as stream_interrupted, not connection — even though the transport error
// looks like a reset.
func TestAttemptRecordsStreamInterrupted(t *testing.T) {
	chs := []RouterChannel{
		{ID: 1, Priority: 100, Weight: 1, Group: "default", RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsText: ptr(true)}},
	}
	settings := ai_router_setting.DefaultSettings()
	settings.MaxAttempts = 1

	demand := RouteDemand{Model: "kimi-k3", Modality: ModalityText, DetectorFamily: "openai_chat"}
	rc := NewRouteContext(demand, settings.MaxAttempts)

	var captured []*AttemptRecord
	callRelay := func() *types.NewAPIError {
		// Mirrors the relay closure: first event arrived (Responded, TTFT),
		// then the upstream connection died mid-stream.
		rc.LastCall = &AttemptCall{ChannelName: "ch-1", Responded: true, TtftMs: int64Ptr(120), DurationMs: 900}
		return types.NewErrorWithStatusCode(fmt.Errorf("read tcp: connection reset"), types.ErrorCodeDoRequestFailed, 500)
	}

	if err := RunFallbackLoop(rc, chs, "default", settings, callRelay); err == nil {
		t.Fatal("expected stream error to surface")
	}
	captured = rc.AttemptRecords
	if len(captured) != 1 {
		t.Fatalf("expected 1 attempt record, got %d", len(captured))
	}
	record := captured[0]
	if record.ErrorCategory != "stream_interrupted" {
		t.Errorf("category = %s, want stream_interrupted", record.ErrorCategory)
	}
	if record.TtftMs == nil || *record.TtftMs != 120 {
		t.Errorf("ttft_ms = %v, want 120 (first event did arrive)", record.TtftMs)
	}
	if record.Retryable {
		t.Error("post-start failure must not be retried")
	}
}

// The telemetry entry builder maps settled records onto DB rows with the
// summary's id and a shared timestamp.
func TestBuildAttemptLogEntries(t *testing.T) {
	rc := NewRouteContext(RouteDemand{Model: "m", Modality: ModalityText}, 2)
	rc.RequestID = "req-build"
	rc.Decision = RouteDecision{ChannelID: 7}
	rc.AttemptRecords = []*AttemptRecord{
		{AttemptSeq: 1, ChannelRetryNo: 0, ChannelId: 4, ChannelName: "snap-4", Modality: "text", ErrorCategory: "429", HttpStatusCode: intPtr(429), Retryable: true, DurationMs: 60},
		{AttemptSeq: 2, ChannelRetryNo: 0, ChannelId: 7, ChannelName: "snap-7", Modality: "text", ErrorCategory: "success", DurationMs: 150, TtftMs: int64Ptr(45), Success: true},
	}

	rows := buildAttemptLogEntries(rc, 123)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	for _, row := range rows {
		if row.RequestLogId != 123 {
			t.Errorf("request_log_id = %d, want 123", row.RequestLogId)
		}
		if row.RequestId != "req-build" {
			t.Errorf("request_id = %s, want req-build", row.RequestId)
		}
		if row.CreatedAt == 0 {
			t.Error("created_at must be set")
		}
	}
	if rows[0].ChannelName != "snap-4" || rows[1].ChannelName != "snap-7" {
		t.Errorf("channel name snapshots wrong: %s,%s", rows[0].ChannelName, rows[1].ChannelName)
	}
	if rows[0].Success || !rows[1].Success {
		t.Error("success flags must mirror the records")
	}
}

func TestBuildAttemptLogEntriesEmpty(t *testing.T) {
	rc := NewRouteContext(RouteDemand{Model: "m", Modality: ModalityText}, 2)
	if rows := buildAttemptLogEntries(rc, 1); rows != nil {
		t.Errorf("expected nil rows for record-less context, got %d", len(rows))
	}
}

func intPtr(v int) *int {
	return &v
}

func int64Ptr(v int64) *int64 {
	return &v
}
