package video_preprocess

import (
	"context"
	"math"
	"slices"
)

// selectAdaptiveFrames reserves time coverage, then scene/motion events, then
// their neighbours. All phases share quality, time-gap and duplicate checks.
// Insufficient visual evidence (all black/blurred candidates) is the only
// condition that permits the caller to fall back to uniform-v1.
func selectAdaptiveFrames(ctx context.Context, analysis *VideoAnalysis, duration float64, budget, minimum int, cfg AnalysisConfig) ([]FrameCandidate, error) {
	if err := timeoutOrCancel(ctx); err != nil {
		return nil, err
	}
	candidates := analysis.Candidates
	var maxSharpness float64
	for _, c := range candidates {
		maxSharpness = max(maxSharpness, c.Sharpness)
	}
	eligible := make([]FrameCandidate, 0, len(candidates))
	for _, c := range candidates {
		if c.BlackRatio >= cfg.BlackThreshold {
			continue
		}
		if maxSharpness > 1 && c.Sharpness < maxSharpness*0.08 {
			continue
		}
		eligible = append(eligible, c)
	}
	if len(eligible) == 0 {
		return nil, nil
	}
	budget = min(budget, len(eligible))
	minimum = min(minimum, budget)
	selected := make([]FrameCandidate, 0, budget)
	// Temporal anchors guarantee representation from the whole clip rather than
	// allowing one burst of motion to consume the request's entire frame budget.
	anchors := min(budget, max(minimum, int(math.Ceil(float64(budget)*0.4))))
	for i := range anchors {
		if err := timeoutOrCancel(ctx); err != nil {
			return nil, err
		}
		target := (float64(i) + 0.5) * duration / float64(anchors)
		if i < minimum {
			target = (float64(i) + 0.5) * duration / float64(minimum)
		}
		best := -1
		bestValue := math.Inf(-1)
		for j, c := range eligible {
			if !candidateFits(c, selected, cfg, i >= minimum) {
				continue
			}
			value := c.Value - cfg.TemporalWeight*math.Abs(c.Timestamp-target)/max(duration/float64(anchors), 0.001)
			if value > bestValue {
				best, bestValue = j, value
			}
		}
		if best >= 0 {
			anchor := eligible[best]
			anchor.Value += cfg.TemporalWeight
			selected = append(selected, anchor)
		}
	}
	ranked := slices.Clone(eligible)
	slices.SortStableFunc(ranked, func(a, b FrameCandidate) int {
		if a.Value > b.Value {
			return -1
		}
		if a.Value < b.Value {
			return 1
		}
		return 0
	})
	changes := max(1, int(math.Ceil(float64(budget)*0.4)))
	for _, c := range ranked {
		if len(selected) >= budget || changes == 0 {
			break
		}
		if !candidateFits(c, selected, cfg, true) {
			continue
		}
		selected = append(selected, c)
		changes--
	}
	// Preserve context on both sides of the strongest event. A neighbour must
	// still be distinct, clear, non-black and separated by the configured gap.
	for _, event := range ranked[:min(len(ranked), budget)] {
		if err := timeoutOrCancel(ctx); err != nil {
			return nil, err
		}
		if event.Scene+event.Motion < 0.05 {
			continue
		}
		for _, direction := range []float64{-1, 1} {
			best := -1
			distance := math.Inf(1)
			for i, c := range eligible {
				delta := (c.Timestamp - event.Timestamp) * direction
				if delta <= 0 || delta >= distance || !candidateFits(c, selected, cfg, true) {
					continue
				}
				best, distance = i, delta
			}
			if best >= 0 && len(selected) < budget {
				selected = append(selected, eligible[best])
			}
		}
		if len(selected) >= budget {
			break
		}
	}
	// Fill unused capacity only with distinct content. A static clip keeps its
	// minimum temporal anchors instead of manufacturing many duplicate frames.
	for _, c := range ranked {
		if len(selected) >= budget {
			break
		}
		if candidateFits(c, selected, cfg, true) {
			selected = append(selected, c)
		}
	}
	slices.SortFunc(selected, func(a, b FrameCandidate) int {
		if a.Timestamp < b.Timestamp {
			return -1
		}
		if a.Timestamp > b.Timestamp {
			return 1
		}
		return 0
	})
	if err := timeoutOrCancel(ctx); err != nil {
		return nil, err
	}
	return selected, nil
}

func candidateFits(candidate FrameCandidate, selected []FrameCandidate, cfg AnalysisConfig, deduplicate bool) bool {
	for _, other := range selected {
		if math.Abs(candidate.Timestamp-other.Timestamp) < max(cfg.MinGapSeconds, 0.000001) {
			return false
		}
		if deduplicate && duplicateCandidate(candidate, other, cfg.DuplicateThreshold) {
			return false
		}
	}
	return true
}
