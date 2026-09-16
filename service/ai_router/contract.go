package ai_router

import "time"

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

// RouteContext carries per-request routing state.
// It must never be shared across requests.
type RouteContext struct {
	RequestID         string
	Demand            RouteDemand
	Constraints       ChannelRouteConstraints
	MaxAttempts       int
	AttemptedChannels map[int]bool
	AttemptOrder      []int // attempt order for telemetry; maps give no ordering
	CurrentAttempt    int
	ChannelRetryNo    int // 0 = first call to the current channel
	ResponseStarted   bool
	StartedAt         int64 // unix nano
	TTFT              int64 // milliseconds; 0 for non-streaming
	Decision          RouteDecision
}

func NewRouteContext(demand RouteDemand, maxAttempts int) *RouteContext {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	return &RouteContext{
		Demand:            demand,
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
