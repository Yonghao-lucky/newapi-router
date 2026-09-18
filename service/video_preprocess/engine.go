package video_preprocess

import (
	"context"
	"os"
)

// MediaEngine is the facade over the media pipeline. It owns no configuration
// — callers pass a MediaLimits value per request — and exposes the two-step
// flow the request Processor needs: decode+probe every video first (so the
// multi-video frame budget is allocated from real metadata), then export each
// video's frames under its allocated budget.
//
// The engine is stateless and safe for concurrent use; each video runs in its
// own request-scoped workspace.
type MediaEngine struct{}

// NewMediaEngine builds the engine facade.
func NewMediaEngine() *MediaEngine { return &MediaEngine{} }

// ProbeAndKeep decodes one video Data URL into a fresh workspace and probes
// it. videoIndex is the request's 1-based video number echoed into the result
// so frames map back to the content part they came from. The decoded file
// stays on disk in the retained result so the later export never re-decodes.
// The caller owns the result and must Cleanup on every path, success or
// failure.
func (e *MediaEngine) ProbeAndKeep(ctx context.Context, dataURL string, limits MediaLimits, allowedMIMETypes []string, videoIndex int) (*ProcessVideoResult, error) {
	return processVideoStage(ctx, ProcessVideoInput{Video: VideoInput{URL: dataURL, Index: videoIndex}}, Options{
		Limits:           limits,
		AllowedMIMETypes: allowedMIMETypes,
	}, false)
}

// ExportUniform exports the allocated number of uniform frames from a
// previously probed video. The timestamps are generated over the probed
// duration; the export downgrade ladder and the byte budget apply as in the
// one-shot flow.
func (e *MediaEngine) ExportUniform(ctx context.Context, kept *ProcessVideoResult, frames int) error {
	return exportKept(ctx, kept, frames)
}

// readFrameJPEG loads one exported frame's bytes. The frame was written by the
// engine itself, so the path is engine-owned, never client-controlled.
func readFrameJPEG(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, wrapError(CodeStorageFailed, "failed to read an exported frame", err)
	}
	return data, nil
}

// exportKept runs the timestamp generation and FFmpeg export for a video that
// ProbeAndKeep already decoded and probed. It shares the one-shot flow's
// downgrade and verification code.
func exportKept(ctx context.Context, kept *ProcessVideoResult, frames int) error {
	if kept == nil || kept.workspace == nil {
		return newError(CodeInvalidConfig, "no retained video to export")
	}
	if frames < 1 {
		return newError(CodeImageLimitExceeded, "video was allocated no frames")
	}
	limits := kept.limits.withDefaults()

	videoPath := kept.workspace.path("video.bin")
	timestamps, err := generateUniformTimestamps(kept.Metadata.DurationSeconds, frames, 1)
	if err != nil {
		return err
	}
	if err := exportJobWithTimestamps(ctx, limits, videoPath, timestamps, kept.workspace.dir, kept.Metadata.RotationDegrees, kept); err != nil {
		return err
	}
	return nil
}
