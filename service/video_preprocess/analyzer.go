package video_preprocess

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/bits"
	"os/exec"
	"slices"
	"strconv"
	"syscall"
	"time"
)

// AnalysisConfig controls the bounded, sequential low resolution pass. It is
// captured with the rest of a request's settings, never reread mid-job.
type AnalysisConfig struct {
	Width, CandidateLimit                                      int
	ShortFPS, MediumFPS, LongFPS                               float64
	MinGapSeconds                                              float64
	SceneWeight, MotionWeight, SharpnessWeight, TemporalWeight float64
	DuplicateThreshold, BlackThreshold                         float64
}

type FrameCandidate struct {
	Timestamp, Scene, Motion, Sharpness, BlackRatio, Luma float64
	Hash                                                  uint64
	Value                                                 float64
}

type VideoAnalysis struct {
	Candidates []FrameCandidate
	Complexity float64
}

func (a AnalysisConfig) scanFPS(duration float64) float64 {
	fps := a.ShortFPS
	if duration > 120 {
		fps = a.MediumFPS
	}
	if duration > 600 {
		fps = a.LongFPS
	}
	// Sampling covers the whole duration even when the candidate ceiling binds.
	return min(fps, float64(a.CandidateLimit)/duration)
}

func analyzeVideo(ctx context.Context, kept *ProcessVideoResult, cfg AnalysisConfig) (*VideoAnalysis, error) {
	if err := timeoutOrCancel(ctx); err != nil {
		return nil, err
	}
	if cfg.Width < 1 || cfg.CandidateLimit < 1 {
		return nil, invalidConfig("invalid analysis bounds")
	}
	meta, limits := kept.Metadata, kept.limits
	fps := cfg.scanFPS(meta.DurationSeconds)
	if !isFinitePositive(fps) {
		return nil, invalidConfig("invalid analysis scan rate")
	}
	width, height := meta.Width, meta.Height
	if meta.RotationDegrees%180 != 0 {
		width, height = height, width
	}
	// AnalysisWidth is also a long-side bound for extremely tall input, keeping
	// the two live grayscale buffers below 2 * analysis_width squared bytes.
	scale := min(1.0, float64(cfg.Width)/float64(max(width, height)))
	width, height = max(1, int(float64(width)*scale)), max(1, int(float64(height)*scale))
	filter := fmt.Sprintf("fps=%.9f:start_time=0:round=up,scale=%d:%d,format=gray", fps, width, height)
	cmd := exec.CommandContext(ctx, limits.FFmpegPath,
		"-nostdin", "-hide_banner", "-v", "error", "-protocol_whitelist", "file,pipe",
		"-threads", strconv.Itoa(limits.FFmpegThreads), "-filter_threads", "1",
		"-max_pixels", strconv.FormatInt(limits.MaxPixels, 10), "-max_alloc", "268435456", "-err_detect", "explode",
		"-i", kept.workspace.path("video.bin"), "-map", "0:v:0", "-an", "-sn", "-dn",
		"-vf", filter, "-frames:v", strconv.Itoa(cfg.CandidateLimit), "-threads", "1",
		"-f", "rawvideo", "-pix_fmt", "gray", "pipe:1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, limit: int64(limits.MaxFFmpegStderrBytes)}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, wrapError(CodeDecodeFailed, "failed to open video analysis stream", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, mediaCommandError(CodeDecodeFailed, err, "")
	}
	if limits.processChanged != nil {
		limits.processChanged(1)
		defer limits.processChanged(-1)
	}
	result := &VideoAnalysis{Complexity: 1}
	current, previous := make([]byte, width*height), make([]byte, width*height)
	for i := range cfg.CandidateLimit {
		_, err = io.ReadFull(stdout, current)
		if err == io.EOF {
			err = nil
			break
		}
		if err != nil {
			break
		}
		candidate := scoreFrame(current, previous, width, height)
		candidate.Timestamp = float64(i) / fps
		if i == 0 {
			candidate.Scene, candidate.Motion = 0, 0
		}
		result.Candidates = append(result.Candidates, candidate)
		current, previous = previous, current
	}
	if err != nil {
		_ = cmd.Cancel()
	}
	waitErr := cmd.Wait()
	if ctxErr := timeoutOrCancel(ctx); ctxErr != nil {
		return nil, ctxErr
	}
	if waitErr != nil {
		return nil, mediaCommandError(CodeDecodeFailed, waitErr, stderr.String())
	}
	if err != nil || len(result.Candidates) == 0 {
		return nil, newError(CodeUnsupportedFormat, "video analysis produced no complete frames")
	}
	var changes, sharpness float64
	for _, candidate := range result.Candidates {
		changes += min(1, candidate.Scene*3+candidate.Motion*2)
		sharpness = max(sharpness, candidate.Sharpness)
	}
	result.Complexity += 2 * changes / float64(len(result.Candidates))
	for i := range result.Candidates {
		c := &result.Candidates[i]
		c.Value = cfg.SceneWeight*c.Scene + cfg.MotionWeight*c.Motion
		if sharpness > 0 {
			c.Value += cfg.SharpnessWeight * c.Sharpness / sharpness
		}
	}
	return result, nil
}

// scoreFrame measures histogram scene change, pixel motion, Laplacian variance,
// black pixels and a 63-bit DCT pHash using only the current and previous frame.
func scoreFrame(current, previous []byte, width, height int) FrameCandidate {
	var histogram, oldHistogram [32]int
	var c FrameCandidate
	var lapSum, lapSquare float64
	var lapCount int
	for i, pixel := range current {
		value := float64(pixel)
		c.Luma += value
		c.Motion += math.Abs(value - float64(previous[i]))
		if pixel < 20 {
			c.BlackRatio++
		}
		histogram[int(pixel)/8]++
		oldHistogram[int(previous[i])/8]++
		x, y := i%width, i/width
		if x > 0 && x < width-1 && y > 0 && y < height-1 {
			lap := 4*value - float64(current[i-1]) - float64(current[i+1]) - float64(current[i-width]) - float64(current[i+width])
			lapSum += lap
			lapSquare += lap * lap
			lapCount++
		}
	}
	count := float64(len(current))
	c.Luma /= count
	c.Motion /= count * 255
	c.BlackRatio /= count
	for i := range histogram {
		c.Scene += math.Abs(float64(histogram[i]-oldHistogram[i])) / (2 * count)
	}
	if lapCount > 0 {
		c.Sharpness = max(0, lapSquare/float64(lapCount)-math.Pow(lapSum/float64(lapCount), 2))
	}
	c.Hash = perceptualHash(current, width, height)
	return c
}

var hashCosines = func() [8][32]float64 {
	var table [8][32]float64
	for u := range 8 {
		for x := range 32 {
			table[u][x] = math.Cos(float64(2*x+1) * float64(u) * math.Pi / 64)
		}
	}
	return table
}()

func perceptualHash(gray []byte, width, height int) uint64 {
	var rows [32][8]float64
	for y := range 32 {
		sourceY := min(height-1, (2*y+1)*height/64)
		for x := range 32 {
			pixel := float64(gray[sourceY*width+min(width-1, (2*x+1)*width/64)])
			for u := range 8 {
				rows[y][u] += pixel * hashCosines[u][x]
			}
		}
	}
	var coefficients [64]float64
	for v := range 8 {
		for u := range 8 {
			for y := range 32 {
				coefficients[v*8+u] += rows[y][u] * hashCosines[v][y]
			}
		}
	}
	ordered := slices.Clone(coefficients[1:])
	slices.Sort(ordered)
	median := ordered[len(ordered)/2]
	var hash uint64
	for i, c := range coefficients[1:] {
		if c > median+1e-6 {
			hash |= uint64(1) << i
		}
	}
	return hash
}

func duplicateCandidate(a, b FrameCandidate, threshold float64) bool {
	// Hashes deliberately ignore DC; brightness guards distinguish otherwise
	// identical blank slides and fades of different luminance.
	return math.Abs(a.Luma-b.Luma) < 12 && float64(bits.OnesCount64(a.Hash^b.Hash))/63 <= threshold
}
