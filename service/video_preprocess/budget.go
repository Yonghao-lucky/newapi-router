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
	Complexity      float64
	MaxFrames       int
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

	// Allocate by sqrt(duration) * complexity. Per-video caps participate in
	// apportionment, so a capped short clip returns unused capacity to siblings.
	weights := make([]float64, len(videos))
	ceilings := make([]int, len(videos))
	for i, video := range videos {
		complexity := video.Complexity
		if !isFinitePositive(complexity) {
			complexity = 1
		}
		weights[i] = math.Sqrt(max(video.DurationSeconds, 1)) * complexity
		ceiling := defaultMaxFrames
		if video.MaxFrames > 0 {
			ceiling = video.MaxFrames
		}
		ceilings[i] = max(minFramesPerVideo, min(ceiling, frameBudgetForDuration(video.DurationSeconds)*int(math.Ceil(complexity))))
	}
	for remaining > 0 {
		best := -1
		bestShare := -1.0
		for i := range videos {
			if budget.Videos[i].Frames >= ceilings[i] {
				continue
			}
			share := weights[i] / float64(budget.Videos[i].Frames-minFramesPerVideo+1)
			if share > bestShare {
				best, bestShare = i, share
			}
		}
		if best < 0 {
			break
		}
		budget.Videos[best].Frames++
		budget.TotalFrames++
		remaining--
	}

	return budget, nil
}

// isFinitePositive reports whether v is a usable positive finite number.
func isFinitePositive(v float64) bool {
	return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v)
}
