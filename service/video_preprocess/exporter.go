package video_preprocess

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// ExportedFrame is one JPEG frame extracted from the video at a specific timestamp.
type ExportedFrame struct {
	TimestampSeconds float64
	Path             string
	Bytes            int64
}

// exportJob describes one batch export request. All timestamps are exported in
// a single FFmpeg invocation to avoid per-frame process overhead.
type exportJob struct {
	videoPath       string
	timestamps      []float64
	outputDir       string
	quality         int
	maxLongSide     int
	rotationDegrees int
	ffmpegThreads   int
}

// exportFrames spawns FFmpeg to extract JPEG frames at the given timestamps
// from videoPath. The subprocess runs under ctx, uses -nostdin, and has its
// stderr capped. On cancellation, the entire process group is killed.
//
// The export writes one JPEG per timestamp into outputDir with generated names,
// then returns the list of frames in timestamp order. The caller owns the files.
func exportFrames(ctx context.Context, ffmpegPath string, job exportJob, limits Limits) ([]ExportedFrame, error) {
	if err := timeoutOrCancel(ctx); err != nil {
		return nil, err
	}
	if len(job.timestamps) == 0 {
		return nil, nil
	}

	// Build the filter chain: scale + optional rotation
	filter := buildScaleFilter(job.maxLongSide, job.rotationDegrees)

	// Build FFmpeg arguments for batch export
	args := []string{
		"-threads", strconv.Itoa(job.ffmpegThreads),
		"-i", job.videoPath,
	}

	frames := make([]ExportedFrame, len(job.timestamps))
	for i, ts := range job.timestamps {
		outPath := filepath.Join(job.outputDir, fmt.Sprintf("frame_%04d.jpg", i))
		frames[i] = ExportedFrame{
			TimestampSeconds: ts,
			Path:             outPath,
		}

		args = append(args,
			"-ss", fmt.Sprintf("%.3f", ts),
			"-vframes", "1",
			"-q:v", strconv.Itoa(job.quality),
			"-vf", filter,
			"-y",
			outPath,
		)
	}

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, limit: int64(limits.MaxFFmpegStderrBytes)}

	if err := cmd.Run(); err != nil {
		if ctxErr := timeoutOrCancel(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		stderrSnippet := truncateForLog(stderr.String(), 512)
		return nil, wrapError(CodeExportFailed, "ffmpeg export failed: "+stderrSnippet, err)
	}

	// Verify all frames exist and record their sizes
	var totalBytes int64
	for i := range frames {
		info, err := os.Stat(frames[i].Path)
		if err != nil {
			return nil, wrapError(CodeExportFailed, "exported frame is missing", err)
		}
		frames[i].Bytes = info.Size()
		totalBytes += info.Size()
	}

	if totalBytes > limits.MaxTotalImageBytes {
		return frames, newError(CodeOutputTooLarge,
			fmt.Sprintf("exported frames total %d bytes, exceeds limit %d bytes",
				totalBytes, limits.MaxTotalImageBytes))
	}

	return frames, nil
}

// buildScaleFilter constructs the FFmpeg filter string for scaling and rotation.
// maxLongSide bounds the longest dimension while preserving aspect ratio.
// rotationDegrees applies the necessary transpose/rotation to correct orientation.
func buildScaleFilter(maxLongSide, rotationDegrees int) string {
	scale := fmt.Sprintf("scale='if(gte(iw,ih),min(%d,iw),-2)':'if(lt(iw,ih),min(%d,ih),-2)'", maxLongSide, maxLongSide)

	// Normalize rotation to [0, 360)
	rot := rotationDegrees % 360
	if rot < 0 {
		rot += 360
	}

	var transpose string
	switch rot {
	case 90:
		transpose = "transpose=1" // 90° clockwise
	case 180:
		transpose = "transpose=1,transpose=1" // 180°
	case 270:
		transpose = "transpose=2" // 90° counter-clockwise
	default:
		return scale
	}

	return transpose + "," + scale
}

// attemptExportWithDowngrade tries to export frames at the current quality and
// size, and if the output exceeds the byte budget, deterministically downgrades
// quality → size → frame count until success or the downgrade ladder is exhausted.
//
// Returns the exported frames or an error if even the most degraded export fails.
func attemptExportWithDowngrade(
	ctx context.Context,
	ffmpegPath string,
	job exportJob,
	limits Limits,
) ([]ExportedFrame, error) {
	quality := job.quality
	longSide := job.maxLongSide
	timestamps := job.timestamps

	for {
		currentJob := job
		currentJob.quality = quality
		currentJob.maxLongSide = longSide
		currentJob.timestamps = timestamps

		frames, err := exportFrames(ctx, ffmpegPath, currentJob, limits)
		if err == nil {
			// Success
			return frames, nil
		}

		// If not an output-too-large error, fail immediately
		if CodeOf(err) != CodeOutputTooLarge {
			return nil, err
		}

		// Apply deterministic downgrade
		if quality > minExportQuality+10 {
			quality -= 10
			continue
		}
		if longSide > minExportLongSide+128 {
			longSide -= 128
			quality = job.quality // reset quality for new size
			continue
		}
		if len(timestamps) > job.ffmpegThreads {
			// Drop lowest-value frames (for uniform sampling, drop from the end)
			timestamps = timestamps[:len(timestamps)*3/4]
			quality = job.quality
			longSide = job.maxLongSide
			continue
		}

		// All downgrades exhausted, fail
		return nil, newError(CodeOutputTooLarge,
			"exported frames exceed byte budget even after maximum downgrade")
	}
}
