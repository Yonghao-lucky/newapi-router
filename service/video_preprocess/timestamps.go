package video_preprocess

import (
	"fmt"
	"math"
)

// generateUniformTimestamps returns an ascending list of timestamps uniformly
// distributed across [0, durationSeconds). The count is bounded by
// maxFramesPerRequest (caller's image budget) and minFramesPerVideo (minimum
// viable coverage).
//
// When maxFramesPerRequest is zero, the caller has no image budget and the
// frames are allocated solely by the video duration heuristic.
func generateUniformTimestamps(durationSeconds float64, maxFramesPerRequest, minFramesPerVideo int) ([]float64, error) {
	if durationSeconds <= 0 {
		return nil, invalidConfig("video duration must be positive")
	}
	if minFramesPerVideo <= 0 {
		return nil, invalidConfig("min frames per video must be positive")
	}

	// Determine target frame count based on duration
	targetFrames := frameBudgetForDuration(durationSeconds)

	// Respect the caller's hard budget
	if maxFramesPerRequest > 0 && targetFrames > maxFramesPerRequest {
		targetFrames = maxFramesPerRequest
	}

	// Ensure minimum viable coverage
	if targetFrames < minFramesPerVideo {
		targetFrames = minFramesPerVideo
	}

	// Generate uniform timestamps
	if targetFrames == 1 {
		return []float64{durationSeconds / 2.0}, nil
	}

	timestamps := make([]float64, targetFrames)
	step := durationSeconds / float64(targetFrames)

	for i := range targetFrames {
		// Offset by half a step so the first frame is not at exactly 0.0 (which
		// may be a black leader frame) and the last is not at exactly duration
		// (which FFmpeg may reject as out of bounds).
		timestamps[i] = (float64(i) + 0.5) * step
	}

	return timestamps, nil
}

// frameBudgetForDuration returns the suggested number of frames for a video of
// the given duration, following the development plan's heuristic table:
//
//	<= 10s     →  6-8
//	10-30s     →  8-16
//	30-120s    → 16-24
//	2-5 min    → 24-32
//	5-10 min   → 32-40
//	10-30 min  → 40-48
//	> 30 min   → 48-60
//
// The exact curve balances temporal coverage with the caller's image budget.
func frameBudgetForDuration(seconds float64) int {
	switch {
	case seconds <= 10:
		return 8
	case seconds <= 30:
		return 12
	case seconds <= 120:
		return 20
	case seconds <= 300:
		return 28
	case seconds <= 600:
		return 36
	case seconds <= 1800:
		return 44
	default:
		return 56
	}
}

// formatTimestamp converts a floating-point timestamp in seconds into the
// MM:SS.mmm format expected by the request rewriter. Negative timestamps are
// clamped to zero.
func formatTimestamp(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	minutes := int(seconds) / 60
	secs := math.Mod(seconds, 60)
	return fmt.Sprintf("%02d:%06.3f", minutes, secs)
}
