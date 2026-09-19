package video_preprocess

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type ExportedFrame struct {
	TimestampSeconds float64
	Path             string
	Bytes            int64
}

type exportJob struct {
	videoPath                                            string
	timestamps                                           []float64
	values                                               []FrameCandidate
	outputDir                                            string
	quality, maxLongSide, rotationDegrees, ffmpegThreads int
	width, height, minimumFrames                         int
}

// exportFrames decodes once in timestamp order. FFmpeg only emits bounded raw
// RGB frames; Go writes JPEG quality 0–100 directly to private, byte-limited
// files, so neither qscale semantics nor encoder-created file modes can differ
// from the administrator's contract.
func exportFrames(ctx context.Context, ffmpegPath string, job exportJob, limits MediaLimits) ([]ExportedFrame, error) {
	if err := timeoutOrCancel(ctx); err != nil {
		return nil, err
	}
	if len(job.timestamps) == 0 {
		return nil, newError(CodeImageLimitExceeded, "video has no selected frames")
	}
	width, height := job.width, job.height
	if job.rotationDegrees%180 != 0 {
		width, height = height, width
	}
	if width < 1 || height < 1 {
		return nil, invalidConfig("export requires probed video dimensions")
	}
	scale := min(1.0, float64(job.maxLongSide)/float64(max(width, height)))
	width, height = max(1, int(float64(width)*scale)), max(1, int(float64(height)*scale))
	var selection strings.Builder
	for i, ts := range job.timestamps {
		if i > 0 {
			selection.WriteByte('+')
		}
		fmt.Fprintf(&selection, "eq(selected_n\\,%d)*gte(t\\,%.9f)", i, ts)
	}
	filter := fmt.Sprintf("select=%s,scale=%d:%d,format=rgb24", selection.String(), width, height)
	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-nostdin", "-hide_banner", "-v", "error", "-protocol_whitelist", "file,pipe",
		"-threads", strconv.Itoa(job.ffmpegThreads), "-filter_threads", "1",
		"-i", job.videoPath, "-map", "0:v:0", "-an", "-sn", "-dn",
		"-vf", filter, "-fps_mode", "vfr", "-frames:v", strconv.Itoa(len(job.timestamps)),
		"-threads", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, limit: int64(limits.MaxFFmpegStderrBytes)}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, wrapError(CodeExportFailed, "failed to open export stream", err)
	}
	if err = cmd.Start(); err != nil {
		return nil, mediaCommandError(CodeExportFailed, err, "")
	}
	if limits.processChanged != nil {
		limits.processChanged(1)
		defer limits.processChanged(-1)
	}
	rgb := make([]byte, width*height*3)
	frame := image.NewRGBA(image.Rect(0, 0, width, height))
	frames := make([]ExportedFrame, 0, len(job.timestamps))
	var total int64
	for i, ts := range job.timestamps {
		_, err = io.ReadFull(stdout, rgb)
		if err != nil {
			break
		}
		for p := range width * height {
			copy(frame.Pix[p*4:p*4+3], rgb[p*3:p*3+3])
			frame.Pix[p*4+3] = 255
		}
		path := filepath.Join(job.outputDir, fmt.Sprintf("frame_%04d.jpg", i))
		var file *os.File
		file, err = os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			err = wrapError(CodeStorageFailed, "failed to create exported frame", err)
			break
		}
		writer := &imageBudgetWriter{ctx: ctx, writer: file, remaining: limits.MaxTotalImageBytes - total}
		err = jpeg.Encode(writer, frame, &jpeg.Options{Quality: job.quality})
		closeErr := file.Close()
		if err != nil {
			if CodeOf(err) == "" {
				err = wrapError(CodeStorageFailed, "failed to write exported frame", err)
			}
			break
		}
		if closeErr != nil {
			err = wrapError(CodeStorageFailed, "failed to close exported frame", closeErr)
			break
		}
		total += writer.written
		frames = append(frames, ExportedFrame{TimestampSeconds: ts, Path: path, Bytes: writer.written})
	}
	if err != nil {
		_ = cmd.Cancel()
	}
	waitErr := cmd.Wait()
	if ctxErr := timeoutOrCancel(ctx); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil && CodeOf(err) != "" {
		return nil, err
	}
	if waitErr != nil {
		return nil, mediaCommandError(CodeExportFailed, waitErr, stderr.String())
	}
	if err != nil || len(frames) != len(job.timestamps) {
		return nil, newError(CodeUnsupportedFormat, "video did not contain all selected frames")
	}
	return frames, nil
}

// imageBudgetWriter refuses before writing beyond the output allowance.
type imageBudgetWriter struct {
	ctx                context.Context
	writer             io.Writer
	remaining, written int64
}

func (w *imageBudgetWriter) Write(data []byte) (int, error) {
	if err := timeoutOrCancel(w.ctx); err != nil {
		return 0, err
	}
	if int64(len(data)) > w.remaining {
		return 0, newError(CodeOutputTooLarge, "exported frames exceed the output byte budget")
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	w.written += int64(n)
	return n, err
}

func attemptExportWithDowngrade(ctx context.Context, ffmpegPath string, job exportJob, limits MediaLimits) ([]ExportedFrame, error) {
	original := job
	for {
		frames, err := exportFrames(ctx, ffmpegPath, job, limits)
		if err == nil {
			return frames, nil
		}
		// Remove partial output before retrying, so degradation never accumulates
		// abandoned files beyond the request's disk reservation.
		for i := range len(original.timestamps) {
			_ = os.Remove(filepath.Join(job.outputDir, fmt.Sprintf("frame_%04d.jpg", i)))
		}
		if CodeOf(err) != CodeOutputTooLarge {
			return nil, err
		}
		if job.quality > 40 {
			job.quality = max(40, job.quality-15)
			continue
		}
		if job.maxLongSide > 256 {
			job.maxLongSide = max(256, job.maxLongSide*3/4)
			continue
		}
		if len(job.timestamps) <= max(1, job.minimumFrames) {
			return nil, err
		}
		// Adaptive frames carry their importance, including temporal anchors.
		// Uniform fallback preserves its coverage by removing an interior frame.
		drop := len(job.timestamps) / 2
		if len(job.values) == len(job.timestamps) {
			for i := range job.values {
				if job.values[i].Value < job.values[drop].Value {
					drop = i
				}
			}
			job.values = append(job.values[:drop:drop], job.values[drop+1:]...)
		}
		job.timestamps = append(job.timestamps[:drop:drop], job.timestamps[drop+1:]...)
	}
}
