package video_preprocess

import (
	"fmt"
	"math"
)

// VideoFramePlan is the frame budget allocated to one video of a request.
// Allocation happens before any media work, so an over-budget request is
// rejected without spending decode/probe/export resources on it.
type VideoFramePlan struct {
	// VideoIndex is the 1-based video number of the request.
	VideoIndex int
	// Frames is the exact number of images this video may produce.
	Frames int
}

// FrameBudget is the request-level image allocation produced by
// allocateFrameBudget.
type FrameBudget struct {
	// Videos carries one plan per request video, in request order.
	Videos []VideoFramePlan
	// Available is the image capacity left for videos after existing images
	// and reserved slots were subtracted from the request hard limit.
	Available int
	// TotalFrames is the sum of the per-video budgets.
	TotalFrames int
}

// FrameBudgetError reports a request whose image budget cannot cover its
// videos. It carries the admission-style code so the HTTP mapping stays on the
// stable classification path.
type FrameBudgetError struct {
	Code    string
	Message string
}

func (e *FrameBudgetError) Error() string { return e.Code + ": " + e.Message }

// VideoPreprocessCode plugs the budget rejection into the package's unified
// error classification (CodeOf, StatusForError, HTTPStatusForError).
func (e *FrameBudgetError) VideoPreprocessCode() string { return e.Code }

// VideoPreprocessMessage is the operator-safe counterpart of
// VideoPreprocessCode.
func (e *FrameBudgetError) VideoPreprocessMessage() string { return e.Message }

// VideoBudgetInput describes one request video for budgeting purposes. The
// probe metadata fills it; ordering follows the request's VideoPart order.
type VideoBudgetInput struct {
	VideoIndex      int
	DurationSeconds float64
}

// allocateFrameBudget distributes the request's remaining image capacity over
// its videos:
//
//	available = max_images_per_request - existing_images - reserve_image_slots
//
// Every video first claims min_frames_per_video; the remaining capacity is
// split proportionally to sqrt(duration), so a longer video contributes more
// detail without letting one long video swallow the whole budget. The result
// never exceeds the available capacity, the per-video default target
// (default_max_frames) or the request hard limit — and when even the minimum
// cannot be honored, the request is rejected instead of silently dropping a
// video the model was asked about.
func allocateFrameBudget(
	available int,
	minFramesPerVideo int,
	defaultMaxFrames int,
	videos []VideoBudgetInput,
) (*FrameBudget, error) {
	if available < 0 {
		available = 0
	}
	if len(videos) == 0 {
		return &FrameBudget{Videos: nil, Available: available}, nil
	}
	if minFramesPerVideo < 1 {
		minFramesPerVideo = 1
	}

	needed := len(videos) * minFramesPerVideo
	if needed > available {
		return nil, &FrameBudgetError{
			Code: CodeImageLimitExceeded,
			Message: fmt.Sprintf(
				"request has %d video(s) needing at least %d frames but only %d image slots remain",
				len(videos), needed, available),
		}
	}

	budget := &FrameBudget{
		Videos:    make([]VideoFramePlan, len(videos)),
		Available: available,
	}

	// Phase 1: every video claims its minimum.
	remaining := available
	for i := range videos {
		frames := min(minFramesPerVideo, remaining)
		budget.Videos[i] = VideoFramePlan{VideoIndex: videos[i].VideoIndex, Frames: frames}
		budget.TotalFrames += frames
		remaining -= frames
	}
	if remaining == 0 {
		return budget, nil
	}

	// Phase 2: split the rest by sqrt(duration) weight, capped by the
	// default per-video target. A video whose duration is unknown (probe
	// failed earlier in a different request shape) gets the minimum weight, so
	// it never starves its siblings.
	weights := make([]float64, len(videos))
	weightSum := 0.0
	for i := range videos {
		duration := max(videos[i].DurationSeconds, 0)
		weight := math.Sqrt(duration)
		if !isFinitePositive(weight) {
			weight = 1
		}
		weights[i] = weight
		weightSum += weight
	}
	if weightSum <= 0 {
		for i := range weights {
			weights[i] = 1
			weightSum++
		}
	}

	// Integer largest-remainder apportionment: the proportional share is
	// floored, then the still-unassigned frames go to the videos with the
	// largest fractional leftovers. Deterministic and sums exactly to
	// `remaining`.
	extra := make([]int, len(videos))
	leftovers := make([]float64, len(videos))
	assigned := 0
	for i := range videos {
		share := float64(remaining) * weights[i] / weightSum
		extra[i] = int(math.Floor(share))
		leftovers[i] = share - float64(extra[i])
		assigned += extra[i]
	}
	order := make([]int, len(videos))
	for i := range order {
		order[i] = i
	}
	// Sort by fractional leftover descending, index ascending as the tiebreak.
	for a := 1; a < len(order); a++ {
		for b := a; b > 0; b-- {
			x, y := order[b-1], order[b]
			if leftovers[x] > leftovers[y] || (leftovers[x] == leftovers[y] && x < y) {
				order[b-1], order[b] = y, x
				continue
			}
			break
		}
	}

	left := remaining - assigned
	for _, i := range order {
		if left == 0 {
			break
		}
		// Cap the combined budget at the default per-video target so one long
		// video cannot take every remaining slot; the hard request limit is
		// already enforced through `available`.
		cap := max(defaultMaxFrames-minFramesPerVideo, 0)
		take := min(left, cap)
		if take > 0 {
			extra[i] += take
			left -= take
		}
	}

	for i := range budget.Videos {
		budget.Videos[i].Frames += extra[i]
		budget.TotalFrames += extra[i]
	}
	return budget, nil
}

// isFinitePositive reports whether v is a usable positive finite number.
func isFinitePositive(v float64) bool {
	return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v)
}
