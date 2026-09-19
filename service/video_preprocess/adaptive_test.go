package video_preprocess

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image/jpeg"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestProcessorSparseVFRFramesMatchTimestamps(t *testing.T) {
	for _, binary := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("required media binary unavailable: %s", binary)
		}
	}
	for _, tc := range []struct {
		name, sampler string
		startPTS      int
		colors        [][3]byte
	}{
		{"sparse", "adaptive-v1", 0, [][3]byte{{240, 30, 30}, {30, 240, 30}, {30, 30, 240}}},
		{"nonzero start", "adaptive-v1", 125, [][3]byte{{240, 30, 30}, {30, 240, 30}, {30, 30, 240}}},
		{"black fallback", "uniform-v1", 0, [][3]byte{{0, 0, 0}, {5, 5, 5}, {10, 10, 10}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const width, height = 96, 64
			colors := tc.colors
			var pixels []byte
			for _, color := range colors {
				pixels = append(pixels, bytes.Repeat(color[:], width*height)...)
			}
			path := filepath.Join(t.TempDir(), "sparse-vfr.mp4")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error",
				"-f", "rawvideo", "-pix_fmt", "rgb24", "-s", "96x64", "-r", "25", "-i", "pipe:0",
				"-vf", fmt.Sprintf("setpts='%d+if(eq(N,0),0,if(eq(N,1),80,210))'", tc.startPTS), "-fps_mode", "vfr",
				"-filter_threads", "1", "-threads", "1", "-c:v", "libx264", "-pix_fmt", "yuv420p", path)
			cmd.Stdin = bytes.NewReader(pixels)
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, string(output))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			cfg := testEngineConfig(t)
			cfg.Media.MinFramesPerVideo = 3
			cfg.DefaultMaxFrames, cfg.HighDetailMaxFrames = 3, 3
			p, err := NewProcessor(cfg)
			require.NoError(t, err)
			url := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(data)
			result, err := p.Process(ctx, ProcessInput{Model: "kimi-k3", Body: []byte(videoBody("kimi-k3", 0, url))})
			require.NoError(t, err)
			assert.Equal(t, tc.sampler, result.SamplerVersion)
			require.Equal(t, 3, result.ExtractedFrames)
			content := gjson.GetBytes(result.rewrittenBody, "messages.0.content").Array()
			wantTimes := []string{"00:00.000", "00:03.200", "00:08.400"}
			frameIndex := 0
			for i, part := range content {
				if part.Get("type").String() != "image_url" {
					continue
				}
				require.Less(t, frameIndex, len(colors))
				require.Greater(t, i, 0)
				assert.Contains(t, content[i-1].Get("text").String(), wantTimes[frameIndex])
				imageURL := part.Get("image_url.url").String()
				require.Contains(t, imageURL, "data:image/jpeg;base64,")
				jpegData, err := base64.StdEncoding.DecodeString(imageURL[len("data:image/jpeg;base64,"):])
				require.NoError(t, err)
				img, err := jpeg.Decode(bytes.NewReader(jpegData))
				require.NoError(t, err)
				r, g, b, _ := img.At(width/2, height/2).RGBA()
				for channel, value := range []uint32{r, g, b} {
					assert.InDelta(t, colors[frameIndex][channel], value>>8, 12, "frame %d channel %d", frameIndex, channel)
				}
				frameIndex++
			}
			assert.Equal(t, 3, frameIndex)
			assert.Zero(t, p.Snapshot().ActiveFFmpeg)
			assert.Zero(t, p.Snapshot().TempStorageBytes)
		})
	}
}

func TestAnalysisSourceTimingValidation(t *testing.T) {
	const prefix = "[showinfo@video_analysis @ 0x123] "
	for _, tc := range []struct {
		name, frames string
		invalid      bool
	}{
		{"padded and unpadded PTS", "n:   0 pts:      0 pts_time:0\nn:   1 pts:1114112 pts_time:68\n", false},
		{"missing PTS", "n:   0 pts:NOPTS pts_time:NOPTS\n", true},
		{"nonmonotonic PTS", "n:0 pts:10\nn:1 pts:9\n", true},
		{"candidate overflow", "n:0 pts:0\nn:1 pts:1\nn:2 pts:2\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			writer := &analysisTimingWriter{diagnostics: &limitedWriter{w: &diagnostics, limit: 64}, limit: 2}
			var lines strings.Builder
			lines.WriteString(prefix + "config in time_base: 1/16384, frame_rate: 1/1\n")
			for line := range strings.SplitSeq(strings.TrimSuffix(tc.frames, "\n"), "\n") {
				lines.WriteString(prefix + line + "\n")
			}
			input := lines.String()
			// Pipe writes need not align with log lines or numeric fields.
			for offset := 0; offset < len(input); offset += 7 {
				_, err := writer.Write([]byte(input[offset:min(offset+7, len(input))]))
				require.NoError(t, err)
			}
			assert.Equal(t, tc.invalid, writer.invalid)
			assert.LessOrEqual(t, len(writer.pts), 2)
			if !tc.invalid {
				assert.Equal(t, []int64{0, 1114112}, writer.pts)
				assert.Equal(t, 68.0, float64(writer.pts[1])*writer.timeBase)
			}
			_, err := writer.Write([]byte(strings.Repeat("diagnostic\n", 100)))
			require.NoError(t, err)
			assert.LessOrEqual(t, diagnostics.Len(), 64)
		})
	}
}

// fixtureVideo creates deterministic clips with a known source-frame timeline.
// No random media, network assets, or timing-sensitive event generation is used.
func fixtureVideo(t *testing.T, kind string) string {
	t.Helper()
	for _, binary := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("required media binary unavailable: %s", binary)
		}
	}
	const width, height, count = 160, 96, 100
	pixels := make([]byte, width*height*3*count)
	for frame := range count {
		scene := 0
		if kind == "slides" {
			if frame >= 36 && frame < 40 {
				scene = 1
			}
			if frame >= 80 {
				scene = 2
			}
		}
		if kind == "scenes" {
			scene = frame / 25 % 3
		}
		for y := range height {
			for x := range width {
				value := byte(200)
				if scene == 0 && x < width/3 || scene == 1 && y < height/3 || scene == 2 && x > y && x < y+width/3 {
					value = 45
				}
				if kind == "motion" {
					value = byte(30 + (x*13+y*7+frame*31)%210)
				}
				if kind == "black" && frame >= 30 && frame < 65 || kind == "all-black" {
					value = 0
				}
				offset := (frame*width*height + y*width + x) * 3
				pixels[offset], pixels[offset+1], pixels[offset+2] = value, value, value
			}
		}
	}
	path := filepath.Join(t.TempDir(), kind+".mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-f", "rawvideo", "-pix_fmt", "rgb24", "-s", "160x96", "-r", "8", "-i", "pipe:0", "-filter_threads", "1", "-threads", "1", "-c:v", "libx264", "-pix_fmt", "yuv420p", path)
	cmd.Stdin = bytes.NewReader(pixels)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(data)
}

func TestAdaptiveSamplingContentAndCoverage(t *testing.T) {
	for _, kind := range []string{"static", "slides", "scenes", "motion", "black", "all-black"} {
		t.Run(kind, func(t *testing.T) {
			url := fixtureVideo(t, kind)
			cfg := testEngineConfig(t)
			cfg.Media.MinFramesPerVideo = 2
			cfg.Analysis.MinGapSeconds = 0.25
			kept, err := NewMediaEngine().ProbeAndKeep(context.Background(), url, cfg.Media, cfg.AllowedMIMETypes, 1)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, kept.Cleanup()) })
			analysis, err := analyzeVideo(context.Background(), kept, cfg.Analysis)
			require.NoError(t, err)
			assert.Len(t, analysis.Candidates, 50)
			selected, err := selectAdaptiveFrames(context.Background(), analysis, 12.5, 8, 2, cfg.Analysis)
			require.NoError(t, err)
			if kind == "all-black" {
				assert.Empty(t, selected, "successful but visually empty analysis explicitly requests uniform fallback")
				processor, err := NewProcessor(cfg)
				require.NoError(t, err)
				result, err := processor.Process(context.Background(), ProcessInput{Model: "kimi-k3", Body: []byte(videoBody("kimi-k3", 0, url))})
				require.NoError(t, err)
				assert.Equal(t, "uniform-v1", result.SamplerVersion)
				return
			}
			require.NotEmpty(t, selected)
			assert.LessOrEqual(t, len(selected), 8)
			assert.Less(t, selected[0].Timestamp, 4.0)
			assert.Greater(t, selected[len(selected)-1].Timestamp, 9.0)
			for i, candidate := range selected {
				assert.Less(t, candidate.BlackRatio, cfg.Analysis.BlackThreshold)
				if i > 0 {
					assert.GreaterOrEqual(t, candidate.Timestamp-selected[i-1].Timestamp, cfg.Analysis.MinGapSeconds)
				}
			}
			switch kind {
			case "static":
				assert.Less(t, len(selected), 8, "deduplication leaves temporal coverage without filling the uniform budget")
				assert.InDelta(t, 1, analysis.Complexity, 0.02)
			case "slides":
				adaptiveScenes := map[int]bool{}
				uniformScenes := map[int]bool{}
				for _, c := range selected {
					adaptiveScenes[fixtureScene(c.Timestamp)] = true
				}
				uniform, err := generateUniformTimestamps(12.5, 8, 1)
				require.NoError(t, err)
				for _, timestamp := range uniform {
					uniformScenes[fixtureScene(timestamp)] = true
				}
				assert.Len(t, adaptiveScenes, 3, "brief slide must survive adaptive selection")
				assert.Greater(t, len(adaptiveScenes), len(uniformScenes))
			case "motion":
				assert.Greater(t, analysis.Complexity, 1.1)
			}
			kept.selected = selected
			timestamps := make([]float64, len(selected))
			for i, c := range selected {
				timestamps[i] = c.Timestamp
			}
			require.NoError(t, exportJobWithTimestamps(context.Background(), kept.limits, kept.workspace.path("video.bin"), timestamps, kept.workspace.dir, kept.Metadata.RotationDegrees, kept))
			require.Len(t, kept.Frames, len(selected))
			for _, frame := range kept.Frames {
				data, err := os.ReadFile(frame.Path)
				require.NoError(t, err)
				decoded, err := jpeg.Decode(bytes.NewReader(data))
				require.NoError(t, err)
				assert.Equal(t, 160, decoded.Bounds().Dx())
				info, err := os.Stat(frame.Path)
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
			}
		})
	}
}

func fixtureScene(timestamp float64) int {
	if timestamp >= 4.5 && timestamp < 5 {
		return 1
	}
	if timestamp >= 10 {
		return 2
	}
	return 0
}

func TestAdaptiveScanDurationAndCandidateBounds(t *testing.T) {
	for _, duration := range []int{1, 5, 180, 900} {
		t.Run(fmt.Sprint(duration), func(t *testing.T) {
			for _, binary := range []string{"ffmpeg", "ffprobe"} {
				if _, err := exec.LookPath(binary); err != nil {
					t.Skipf("required media binary unavailable: %s", binary)
				}
			}
			cfg := testEngineConfig(t)
			cfg.Analysis.CandidateLimit = 16
			path := filepath.Join(t.TempDir(), "duration.mp4")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", fmt.Sprintf("color=gray:s=96x64:r=1:d=%d", duration), "-threads", "1", "-c:v", "libx264", path).CombinedOutput()
			require.NoError(t, err, string(out))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			kept, err := NewMediaEngine().ProbeAndKeep(ctx, "data:video/mp4;base64,"+base64.StdEncoding.EncodeToString(data), cfg.Media, cfg.AllowedMIMETypes, 1)
			require.NoError(t, err)
			defer kept.Cleanup()
			analysis, err := analyzeVideo(ctx, kept, cfg.Analysis)
			require.NoError(t, err)
			require.NotEmpty(t, analysis.Candidates)
			if duration == 1 {
				require.Len(t, analysis.Candidates, 1, "analysis must not synthesize duplicate source frames")
				assert.Zero(t, analysis.Candidates[0].Timestamp)
				require.NoError(t, NewMediaEngine().ExportUniform(ctx, kept, 8))
				assert.Len(t, kept.Frames, 1, "a single source frame remains valid below the configured coverage minimum")
			} else {
				assert.GreaterOrEqual(t, analysis.Candidates[len(analysis.Candidates)-1].Timestamp, float64(duration)*0.75)
			}
			assert.LessOrEqual(t, len(analysis.Candidates), 16)
		})
	}
	cfg := testEngineConfig(t).Analysis
	assert.Equal(t, 4.0, cfg.scanFPS(10))
	assert.Equal(t, 2.0, cfg.scanFPS(300))
	assert.Equal(t, 0.5, cfg.scanFPS(900))
}

func TestAdaptiveRequestAndExportBudgets(t *testing.T) {
	url := fixtureVideo(t, "motion")
	cfg := testEngineConfig(t)
	cfg.Media.MinFramesPerVideo = 2
	cfg.DefaultMaxFrames = 8
	cfg.HighDetailMaxFrames = 12
	cfg.Rewrite.MaxImagesPerRequest = 10
	cfg.ReserveImageSlots = 2
	cfg.Rewrite.MaxOutputImageBytes = 20000
	cfg.Rewrite.MaxRewrittenBodyBytes = 32000
	processor, err := NewProcessor(cfg)
	require.NoError(t, err)
	result, err := processor.Process(context.Background(), ProcessInput{Model: "kimi-k3", Body: []byte(videoBody("kimi-k3", 2, url, url))})
	require.NoError(t, err)
	assert.Equal(t, "adaptive-v1", result.SamplerVersion)
	assert.LessOrEqual(t, result.ExtractedFrames, 6)
	assert.GreaterOrEqual(t, result.ExtractedFrames, 4)
	assert.LessOrEqual(t, result.RewrittenBodyBytes, int64(32000))
	assert.Zero(t, processor.Snapshot().TempStorageBytes)
	cfg.Rewrite.MaxOutputImageBytes = 1
	require.NoError(t, processor.SetConfig(cfg))
	_, err = processor.Process(context.Background(), ProcessInput{Model: "kimi-k3", Body: []byte(videoBody("kimi-k3", 0, url))})
	require.Error(t, err)
	assert.Equal(t, CodeOutputTooLarge, CodeOf(err))
	assert.Zero(t, processor.Snapshot().RunningJobs)
	entries, err := os.ReadDir(cfg.Media.TempRoot)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestAdaptiveAnalysisFailureNeverFallsBack(t *testing.T) {
	url := fixtureVideo(t, "static")
	for _, tc := range []struct {
		name    string
		cancel  bool
		timeout bool
		missing bool
		code    string
	}{
		{name: "canceled", cancel: true, code: CodeProcessingCanceled},
		{name: "timeout", timeout: true, code: CodeProcessingTimeout},
		{name: "missing binary", missing: true, code: CodeDecodeFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testEngineConfig(t)
			if tc.missing {
				cfg.Media.FFmpegPath = filepath.Join(t.TempDir(), "missing")
			}
			processor, err := NewProcessor(cfg)
			require.NoError(t, err)
			ctx := context.Background()
			if tc.cancel {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			if tc.timeout {
				c, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				ctx = c
			}
			result, err := processor.Process(ctx, ProcessInput{Model: "kimi-k3", Body: []byte(videoBody("kimi-k3", 0, url))})
			require.Error(t, err)
			assert.Nil(t, result)
			assert.Equal(t, tc.code, CodeOf(err))
			assert.Zero(t, processor.Snapshot().ActiveFFmpeg)
			assert.Zero(t, processor.Snapshot().RunningJobs)
		})
	}
}

func TestAdaptiveBudgetWeightsAndQualityFilters(t *testing.T) {
	_, shortfall := generateUniformTimestamps(10, 2, 4)
	require.Equal(t, CodeImageLimitExceeded, CodeOf(shortfall))

	budget, err := allocateFrameBudget(20, 2, 16, []VideoBudgetInput{{VideoIndex: 1, DurationSeconds: 100, Complexity: 1}, {VideoIndex: 2, DurationSeconds: 100, Complexity: 2}})
	require.NoError(t, err)
	assert.Equal(t, 20, budget.TotalFrames)
	assert.Greater(t, budget.Videos[1].Frames, budget.Videos[0].Frames)
	capped, err := allocateFrameBudget(60, 2, 8, []VideoBudgetInput{{VideoIndex: 1, DurationSeconds: 1800, Complexity: 3}})
	require.NoError(t, err)
	assert.Equal(t, 8, capped.TotalFrames, "a per-video ceiling cannot be bypassed by weighted shares")
	cfg := testEngineConfig(t).Analysis
	candidates := []FrameCandidate{
		{Timestamp: 0, Sharpness: 100, Luma: 100, Value: 1},
		{Timestamp: 0.1, Sharpness: 100, Luma: 120, Hash: math.MaxUint64, Value: 1},
		{Timestamp: 1, Sharpness: 0.01, Luma: 150, Hash: math.MaxUint64, Value: 1},
		{Timestamp: 2, Sharpness: 100, BlackRatio: 1, Value: 1},
		{Timestamp: 3, Sharpness: 100, Luma: 170, Hash: math.MaxUint64, Value: 1},
	}
	selected, err := selectAdaptiveFrames(context.Background(), &VideoAnalysis{Candidates: candidates}, 4, 5, 1, cfg)
	require.NoError(t, err)
	for _, candidate := range selected {
		assert.NotEqual(t, 1.0, candidate.Timestamp)
		assert.NotEqual(t, 2.0, candidate.Timestamp)
	}
	for i := 1; i < len(selected); i++ {
		assert.GreaterOrEqual(t, selected[i].Timestamp-selected[i-1].Timestamp, cfg.MinGapSeconds)
	}
}
