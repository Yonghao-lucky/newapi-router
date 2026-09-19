package video_preprocess

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// VideoMetadata holds the structural facts about a decoded video, obtained from
// ffprobe. Every field is validated against the caller's limits before the
// metadata is returned.
type VideoMetadata struct {
	Container        string
	VideoCodec       string
	DurationSeconds  float64
	Width            int
	Height           int
	Pixels           int64
	FPS              float64
	EstimatedFrames  int64
	RotationDegrees  int
	HasVideo         bool
	HasAudio         bool
	VideoStreamCount int
	AudioStreamCount int
}

// probeVideo spawns ffprobe against path, parses its JSON output and enforces
// limits. The subprocess is never passed through a shell, has closed stdin, runs
// under ctx and has its stderr capped at limits.MaxProbeStderrBytes.
//
// On cancellation, the entire process group is killed.
func probeVideo(ctx context.Context, ffprobePath, path string, limits MediaLimits) (*VideoMetadata, error) {
	if err := timeoutOrCancel(ctx); err != nil {
		return nil, err
	}

	args := []string{
		"-v", "error",
		"-protocol_whitelist", "file,pipe",
		"-threads", strconv.Itoa(limits.FFmpegThreads),
		"-max_pixels", strconv.FormatInt(limits.MaxPixels, 10), "-max_alloc", "268435456", "-max_streams", "16",
		"-show_entries", "format=format_name,duration:stream=codec_type,codec_name,width,height,r_frame_rate,avg_frame_rate,duration:stream_tags=rotate:stream_side_data=side_data_type,rotation",
		"-of", "json",
		path,
	}

	cmd := exec.CommandContext(ctx, ffprobePath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, limit: 1 << 20}
	cmd.Stderr = &limitedWriter{w: &stderr, limit: int64(limits.MaxProbeStderrBytes)}

	if err := cmd.Start(); err != nil {
		return nil, mediaCommandError(CodeProbeFailed, err, "")
	}
	if limits.probeChanged != nil {
		limits.probeChanged(1)
		defer limits.probeChanged(-1)
	}
	if err := cmd.Wait(); err != nil {
		if ctxErr := timeoutOrCancel(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, mediaCommandError(CodeProbeFailed, err, stderr.String())
	}

	meta, err := parseProbeOutput(stdout.Bytes())
	if err != nil {
		return nil, err
	}

	if err := validateMetadata(meta, limits); err != nil {
		return meta, err
	}

	return meta, nil
}

// ffprobeOutput is the shape ffprobe emits with -of json.
type ffprobeOutput struct {
	Format  ffprobeFormat   `json:"format"`
	Streams []ffprobeStream `json:"streams"`
}

type ffprobeFormat struct {
	FormatName string `json:"format_name"`
	Duration   string `json:"duration"`
}

type ffprobeStream struct {
	CodecType    string            `json:"codec_type"`
	CodecName    string            `json:"codec_name"`
	Width        int               `json:"width"`
	Height       int               `json:"height"`
	RFrameRate   string            `json:"r_frame_rate"`
	AvgFrameRate string            `json:"avg_frame_rate"`
	Duration     string            `json:"duration"`
	Tags         map[string]string `json:"tags"`
	SideDataList []sideData        `json:"side_data_list,omitempty"`
}

type sideData struct {
	SideDataType string `json:"side_data_type"`
	Rotation     int    `json:"rotation"`
}

func parseProbeOutput(data []byte) (*VideoMetadata, error) {
	var out ffprobeOutput
	if err := common.Unmarshal(data, &out); err != nil {
		return nil, wrapError(CodeProbeFailed, "failed to parse ffprobe output", err)
	}

	meta := &VideoMetadata{}

	// Container format
	if out.Format.FormatName != "" {
		formats := strings.Split(out.Format.FormatName, ",")
		if len(formats) > 0 {
			meta.Container = strings.TrimSpace(formats[0])
		}
	}

	// Duration from format
	if out.Format.Duration != "" {
		if d, err := strconv.ParseFloat(out.Format.Duration, 64); err == nil {
			meta.DurationSeconds = d
		}
	}

	// Parse streams
	for _, stream := range out.Streams {
		switch stream.CodecType {
		case "video":
			meta.VideoStreamCount++
			meta.HasVideo = true
			if meta.VideoCodec == "" {
				meta.VideoCodec = stream.CodecName
			}
			if meta.Width == 0 && stream.Width > 0 {
				meta.Width = stream.Width
			}
			if meta.Height == 0 && stream.Height > 0 {
				meta.Height = stream.Height
			}
			if meta.FPS == 0 {
				meta.FPS = parseFPS(stream.RFrameRate, stream.AvgFrameRate)
			}
			if meta.VideoStreamCount == 1 && stream.Duration != "" {
				if d, err := strconv.ParseFloat(stream.Duration, 64); err == nil {
					meta.DurationSeconds = d
				}
			}
			for _, sd := range stream.SideDataList {
				if sd.SideDataType == "Display Matrix" && sd.Rotation != 0 {
					meta.RotationDegrees = sd.Rotation
				}
			}
			if meta.RotationDegrees == 0 {
				if rot, ok := stream.Tags["rotate"]; ok {
					if r, err := strconv.Atoi(rot); err == nil {
						meta.RotationDegrees = r
					}
				}
			}
		case "audio":
			meta.AudioStreamCount++
			meta.HasAudio = true
		}
	}

	if !meta.HasVideo {
		return nil, newError(CodeUnsupportedFormat, "media has no video stream")
	}
	if meta.Width <= 0 || meta.Height <= 0 {
		return nil, newError(CodeUnsupportedFormat, "video dimensions could not be determined")
	}
	if !isFinitePositive(meta.DurationSeconds) {
		return nil, newError(CodeUnsupportedFormat, "video duration could not be determined")
	}

	if !isFinitePositive(meta.FPS) || meta.FPS*meta.DurationSeconds >= float64(math.MaxInt64) {
		return nil, newError(CodeUnsupportedFormat, "video frame rate could not be determined safely")
	}

	if int64(meta.Width) > math.MaxInt64/int64(meta.Height) {
		return nil, newError(CodeResolutionExceeded, "video pixel count exceeds integer bounds")
	}
	meta.Pixels = int64(meta.Width) * int64(meta.Height)

	if meta.FPS > 0 && meta.DurationSeconds > 0 {
		meta.EstimatedFrames = int64(meta.FPS * meta.DurationSeconds)
	}

	return meta, nil
}

// parseFPS extracts a numeric frame rate from ffprobe's rational strings.
func parseFPS(rFrameRate, avgFrameRate string) float64 {
	if fps := parseRational(rFrameRate); fps > 0 {
		return fps
	}
	return parseRational(avgFrameRate)
}

// parseRational converts "num/den" into a float64, returning 0 on error.
func parseRational(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "0/0" {
		return 0
	}
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
		return 0
	}
	num, err1 := strconv.ParseFloat(parts[0], 64)
	den, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil || den == 0 {
		return 0
	}
	return num / den
}

func validateMetadata(meta *VideoMetadata, limits MediaLimits) error {
	if meta.RotationDegrees%90 != 0 {
		return newError(CodeUnsupportedFormat, "video rotation must be a right angle")
	}
	if meta.VideoStreamCount != 1 || meta.AudioStreamCount > 1 {
		return newError(CodeUnsupportedFormat, "only one video and at most one audio stream are supported")
	}
	if len(limits.AllowedContainerFormats) > 0 && !containsFold(limits.AllowedContainerFormats, meta.Container) {
		return newError(CodeUnsupportedFormat, "video container format is not enabled")
	}
	if len(limits.AllowedVideoCodecs) > 0 && !containsFold(limits.AllowedVideoCodecs, meta.VideoCodec) {
		return newError(CodeUnsupportedFormat, "video codec is not enabled")
	}
	if meta.DurationSeconds > limits.MaxDurationSeconds {
		return newError(CodeTooLong, fmt.Sprintf("video duration %.1fs exceeds limit %.1fs",
			meta.DurationSeconds, limits.MaxDurationSeconds))
	}
	if meta.Width > limits.MaxWidth || meta.Height > limits.MaxHeight {
		return newError(CodeResolutionExceeded, fmt.Sprintf("video resolution %dx%d exceeds limits %dx%d",
			meta.Width, meta.Height, limits.MaxWidth, limits.MaxHeight))
	}
	if meta.Pixels > limits.MaxPixels {
		return newError(CodeResolutionExceeded, fmt.Sprintf("video pixel count %d exceeds limit %d",
			meta.Pixels, limits.MaxPixels))
	}
	if meta.FPS > limits.MaxFPS {
		return newError(CodeUnsupportedFormat, fmt.Sprintf("video FPS %.1f exceeds limit %.1f",
			meta.FPS, limits.MaxFPS))
	}
	if meta.EstimatedFrames > limits.MaxAnalyzedFrameCount {
		return newError(CodeTooLarge, fmt.Sprintf("estimated frame count %d exceeds analysis limit %d",
			meta.EstimatedFrames, limits.MaxAnalyzedFrameCount))
	}
	return nil
}

// limitedWriter wraps an io.Writer and stops accepting data once limit is reached.
type limitedWriter struct {
	w       *bytes.Buffer
	written int64
	limit   int64
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	if lw.written >= lw.limit {
		return len(p), nil
	}
	take := int64(len(p))
	if lw.written+take > lw.limit {
		take = lw.limit - lw.written
	}
	n, err := lw.w.Write(p[:take])
	lw.written += int64(n)
	return len(p), err
}
