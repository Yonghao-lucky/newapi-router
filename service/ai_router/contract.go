package ai_router

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
)

type Modality string

const (
	ModalityText    Modality = "text"
	ModalityImage   Modality = "image"
	ModalityVideo   Modality = "video"
	ModalityUnknown Modality = "unknown"
)

type RouteDemand struct {
	Model               string   `json:"model"`
	Modality            Modality `json:"modality"`
	HasTools            bool     `json:"has_tools"`
	UnknownContentTypes []string `json:"unknown_content_types,omitempty"`
	DetectorFamily      string   `json:"detector_family"`
}

type ChannelRouteConstraints struct {
	RequiredModality    Modality `json:"required_modality"`
	RequireTools        bool     `json:"require_tools"`
	UnknownFallbackOnly bool     `json:"unknown_fallback_only"`
	ExcludedChannelIDs  []int    `json:"excluded_channel_ids,omitempty"`
}

type RouteDecision struct {
	ChannelID       int      `json:"channel_id"`
	Priority        int64    `json:"priority"`
	Weight          int      `json:"weight"`
	Modality        Modality `json:"modality"`
	SelectionReason string   `json:"selection_reason"`
	AttemptIndex    int      `json:"attempt_index"`
}

// AttemptCall captures the observable outcome of one relay call — the raw
// material for an attempt-log row. The relay closure fills it; the router
// loop turns it into an AttemptRecord together with the classification.
type AttemptCall struct {
	UpstreamStatus int
	RetryAfter     time.Duration
	// ChannelName is snapshotted at call time from the channel cache so
	// attempt rows survive channel deletion.
	ChannelName string
	// Responded is true when THIS call got a (possibly partial) response
	// from the upstream, i.e. streaming had begun.
	Responded bool
	// StreamInterrupted is true when the stream started but ended
	// abnormally (panic, scanner error, timeout, ...) — first event
	// arrived, then the flow died.
	StreamInterrupted bool
	// TtftMs is the time to first event of this call; nil when it never
	// produced one.
	TtftMs *int64
	// DurationMs covers the whole callRelay invocation.
	DurationMs int64
	// Err is the relay error of this call; nil on success.
	Err *types.NewAPIError
}

// AttemptRecord is the settled outcome of one upstream call, ready for
// telemetry persistence.
type AttemptRecord struct {
	AttemptSeq     int
	ChannelRetryNo int
	ChannelId      int
	ChannelName    string
	Modality       string
	HttpStatusCode *int
	ErrorCategory  string
	Retryable      bool
	DurationMs     int64
	TtftMs         *int64
	Success        bool
}

// RouteContext carries per-request routing state.
// It must never be shared across requests.
type RouteContext struct {
	Context   context.Context
	RequestID string
	Demand    RouteDemand
	// LogModality is the client-visible modality persisted by telemetry.
	// Routing continues to use Demand.Modality independently.
	LogModality       Modality
	Constraints       ChannelRouteConstraints
	MaxAttempts       int
	AttemptedChannels map[int]bool
	AttemptOrder      []int // attempt order for telemetry; maps give no ordering
	CurrentAttempt    int
	ChannelRetryNo    int // 0 = first call to the current channel
	ResponseStarted   bool
	// StreamInterrupted marks the whole request: the stream started but
	// ended abnormally, so the summary is not a success even when the relay
	// returned no error.
	StreamInterrupted bool
	StartedAt         int64 // unix nano
	TTFT              int64 // milliseconds; 0 for non-streaming
	Decision          RouteDecision
	// AttemptRecords holds one settled record per real upstream call in
	// call order; filled by the router loop from AttemptCall captures.
	AttemptRecords []*AttemptRecord
	// LastCall is the capture of the most recent relay call, reset before
	// each callRelay and settled into AttemptRecords by the loop.
	LastCall *AttemptCall
}

func NewRouteContext(demand RouteDemand, maxAttempts int) *RouteContext {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	return &RouteContext{
		Context:           context.Background(),
		Demand:            demand,
		LogModality:       demand.Modality,
		MaxAttempts:       maxAttempts,
		AttemptedChannels: make(map[int]bool),
		StartedAt:         timeNowUnixNano(),
	}
}

func (rc *RouteContext) MarkAttempted(channelID int) {
	rc.AttemptedChannels[channelID] = true
	rc.AttemptOrder = append(rc.AttemptOrder, channelID)
	rc.CurrentAttempt++
}

func (rc *RouteContext) HasAttempted(channelID int) bool {
	return rc.AttemptedChannels[channelID]
}

func (rc *RouteContext) ElapsedMS() int64 {
	return time.Since(time.Unix(0, rc.StartedAt)).Milliseconds()
}
