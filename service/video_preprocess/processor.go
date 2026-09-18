package video_preprocess

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"time"
)

// Options carries the caller's configuration and limits for one ProcessVideo
// call. The zero value is invalid; use MediaLimits{} and supply at least one
// VideoInput.
type Options struct {
	// Limits is the full set of safety, format, and export constraints.
	Limits MediaLimits

	// AllowedMIMETypes filters the video/* MIME types accepted from Data URLs.
	// When empty, all video/* types are accepted, but the container and codec
	// must still pass the MediaLimits allowlists.
	AllowedMIMETypes []string
}

// ProcessVideoInput is the top-level input for ProcessVideo.
type ProcessVideoInput struct {
	// Video is the Base64 video Data URL to process.
	Video VideoInput

	// MaxFramesForThisVideo is the caller's remaining image budget for this
	// video only. When Options.Limits.MaxFramesPerRequest is zero, the budget
	// is allocated by duration alone.
	MaxFramesForThisVideo int
}

// ProcessVideoResult is the complete output of one ProcessVideo call.
type ProcessVideoResult struct {
	// VideoIndex echoes the caller's VideoInput.Index.
	VideoIndex int

	// Metadata is the validated video metadata from ffprobe.
	Metadata VideoMetadata

	// Frames are the exported JPEG frames, in ascending timestamp order.
	Frames []ExportedFrame

	// DurationMillis records the time spent in each processing stage.
	DurationMillis struct {
		Decode int64
		Probe  int64
		Export int64
		Total  int64
	}

	// workspace is retained so the caller can clean up the request-scoped
	// directory. It is not exported because the Cleanup method is the only
	// stable contract.
	workspace *workspace
}

// Cleanup removes all temporary files and directories created for this video.
// It is idempotent and safe to call concurrently.
func (r *ProcessVideoResult) Cleanup() error {
	if r == nil || r.workspace == nil {
		return nil
	}
	return r.workspace.Cleanup()
}

// ProcessVideo is the public entry point for the media engine. It decodes the
// Base64 Data URL, probes the video, generates uniform timestamps, exports JPEG
// frames, and returns ownership of the files to the caller.
//
// The caller must invoke result.Cleanup() when the frames are no longer needed,
// regardless of success, failure, cancellation or timeout.
func ProcessVideo(ctx context.Context, input ProcessVideoInput, opts Options) (*ProcessVideoResult, error) {
	start := time.Now()

	// Apply defaults and validate limits
	limits := opts.Limits.withDefaults()
	if err := limits.validate(); err != nil {
		return nil, err
	}

	// Create request-scoped workspace
	ws, err := newWorkspace(limits.TempRoot)
	if err != nil {
		return nil, err
	}

	result := &ProcessVideoResult{
		VideoIndex: input.Video.Index,
		workspace:  ws,
	}

	// Ensure cleanup on early return
	var cleanupOnError = true
	defer func() {
		if cleanupOnError {
			_ = ws.Cleanup()
		}
	}()

	// Stage 1: Parse and validate Data URL
	dataURL, decodedSize, err := parseVideoDataURL(input.Video.URL, opts.AllowedMIMETypes)
	if err != nil {
		return nil, err
	}
	if decodedSize > limits.MaxDecodedVideoBytes {
		return nil, newError(CodeTooLarge,
			fmt.Sprintf("decoded video size %d exceeds limit %d",
				decodedSize, limits.MaxDecodedVideoBytes))
	}

	// Stage 2: Decode Base64 to temporary file
	decodeStart := time.Now()
	videoFile, err := ws.create("video.bin")
	if err != nil {
		return nil, err
	}
	_, err = decodeVideoDataURL(dataURL, videoFile, limits.MaxDecodedVideoBytes)
	if closeErr := videoFile.Close(); closeErr != nil && err == nil {
		err = wrapError(CodeStorageFailed, "failed to close decoded video file", closeErr)
	}
	if err != nil {
		return nil, err
	}
	result.DurationMillis.Decode = time.Since(decodeStart).Milliseconds()

	videoPath := ws.path("video.bin")

	// Sanity check: sniff container magic bytes
	head, err := readHead(videoPath, 12)
	if err != nil {
		return nil, wrapError(CodeDecodeFailed, "failed to read decoded video", err)
	}
	sniffed := sniffContainer(head)
	if sniffed == "" {
		return nil, newError(CodeUnsupportedFormat, "decoded video has no recognizable container magic")
	}

	// Stage 3: Probe with ffprobe
	probeStart := time.Now()
	probeCtx, probeCancel := context.WithTimeout(ctx, limits.ProbeTimeout)
	defer probeCancel()

	meta, err := probeVideo(probeCtx, limits.FFprobePath, videoPath, limits)
	if err != nil {
		return nil, err
	}
	result.Metadata = *meta
	result.DurationMillis.Probe = time.Since(probeStart).Milliseconds()

	// Stage 4: Generate uniform timestamps
	maxFrames := input.MaxFramesForThisVideo
	if limits.MaxFramesPerRequest > 0 && (maxFrames <= 0 || maxFrames > limits.MaxFramesPerRequest) {
		maxFrames = limits.MaxFramesPerRequest
	}

	timestamps, err := generateUniformTimestamps(
		meta.DurationSeconds,
		maxFrames,
		limits.MinFramesPerVideo,
	)
	if err != nil {
		return nil, err
	}

	// Stage 5: Export JPEG frames with downgrade
	exportStart := time.Now()
	exportCtx, exportCancel := context.WithTimeout(ctx, limits.ExportTimeout)
	defer exportCancel()

	job := exportJob{
		videoPath:       videoPath,
		timestamps:      timestamps,
		outputDir:       ws.dir,
		quality:         limits.JPEGQuality,
		maxLongSide:     limits.MaxLongSide,
		rotationDegrees: meta.RotationDegrees,
		ffmpegThreads:   limits.FFmpegThreads,
	}

	frames, err := attemptExportWithDowngrade(exportCtx, limits.FFmpegPath, job, limits)
	if err != nil {
		return nil, err
	}
	result.Frames = frames
	result.DurationMillis.Export = time.Since(exportStart).Milliseconds()

	result.DurationMillis.Total = time.Since(start).Milliseconds()

	// Success: caller owns cleanup
	cleanupOnError = false
	return result, nil
}

// EncodeFrameToDataURL reads a JPEG frame from disk and returns it as a
// base64-encoded data URL suitable for inclusion in a rewritten request body.
func EncodeFrameToDataURL(framePath string) (string, error) {
	data, err := os.ReadFile(framePath)
	if err != nil {
		return "", wrapError(CodeStorageFailed, "failed to read exported frame", err)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return "data:image/jpeg;base64," + encoded, nil
}
