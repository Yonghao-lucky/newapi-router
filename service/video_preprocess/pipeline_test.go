package video_preprocess

import (
	"context"
	"encoding/base64"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	ai_router "github.com/QuantumNous/new-api/service/ai_router"
	"github.com/QuantumNous/new-api/setting/video_preprocess_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// videoBody builds a Chat Completions request with existing images, one
// video_url part per given URL and leading/trailing text parts, so ordering
// can be asserted after the rewrite.
func videoBody(model string, existingImages int, urls ...string) string {
	var b strings.Builder
	b.WriteString(`{"model":"` + model + `","messages":[{"role":"user","content":[`)
	b.WriteString(`{"type":"text","text":"before"}`)
	for range existingImages {
		b.WriteString(`,{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR"}}`)
	}
	for _, url := range urls {
		b.WriteString(`,{"type":"video_url","video_url":{"url":"` + url + `"}}`)
	}
	b.WriteString(`,{"type":"text","text":"after"}]}]}`)
	return b.String()
}

// engineLimits points the media engine at t.TempDir and the smallest sane
// budgets so the tests stay fast.
func engineLimits(t *testing.T) MediaLimits {
	t.Helper()
	return MediaLimits{
		TempRoot:            t.TempDir(),
		MaxFramesPerRequest: 60,
		MinFramesPerVideo:   1,
	}
}

func TestAllocateFrameBudget(t *testing.T) {
	t.Run("minimum honored for many videos", func(t *testing.T) {
		budget, err := allocateFrameBudget(6, 2, 32, []VideoBudgetInput{
			{VideoIndex: 1, DurationSeconds: 5},
			{VideoIndex: 2, DurationSeconds: 5},
			{VideoIndex: 3, DurationSeconds: 5},
		})
		require.NoError(t, err)
		assert.Equal(t, 6, budget.TotalFrames)
		for _, plan := range budget.Videos {
			assert.Equal(t, 2, plan.Frames)
		}
	})

	t.Run("longer video gets larger share", func(t *testing.T) {
		budget, err := allocateFrameBudget(30, 4, 32, []VideoBudgetInput{
			{VideoIndex: 1, DurationSeconds: 10},
			{VideoIndex: 2, DurationSeconds: 600},
		})
		require.NoError(t, err)
		assert.Equal(t, 30, budget.TotalFrames)
		short := budget.Videos[0].Frames
		long := budget.Videos[1].Frames
		assert.Greater(t, long, short, "sqrt(duration) weight: %v", budget.Videos)
		// The per-video default target caps the long video's share.
		assert.LessOrEqual(t, long, 32)
	})

	t.Run("insufficient capacity is rejected", func(t *testing.T) {
		_, err := allocateFrameBudget(3, 2, 32, []VideoBudgetInput{
			{VideoIndex: 1}, {VideoIndex: 2},
		})
		require.Error(t, err)
		assert.Equal(t, CodeImageLimitExceeded, CodeOf(err))
	})

	t.Run("no videos needs no capacity", func(t *testing.T) {
		budget, err := allocateFrameBudget(0, 2, 32, nil)
		require.NoError(t, err)
		assert.Equal(t, 0, budget.TotalFrames)
	})

	t.Run("total never exceeds available", func(t *testing.T) {
		for available := range 40 {
			budget, err := allocateFrameBudget(available, 1, 60, []VideoBudgetInput{
				{VideoIndex: 1, DurationSeconds: 3},
				{VideoIndex: 2, DurationSeconds: 900},
			})
			if available < 2 {
				require.Error(t, err)
				continue
			}
			require.NoError(t, err)
			assert.LessOrEqual(t, budget.TotalFrames, available)
		}
	})
}

func TestBuildEngineConfigMapping(t *testing.T) {
	settings := video_preprocess_setting.DefaultSettings()
	settings.MaxDecodedVideoMB = 75
	settings.MaxTotalImageMB = 20
	settings.MaxRewrittenBodyMB = 32
	settings.MaxWaitingBytesMB = 100
	settings.MaxTempStorageGB = 5
	settings.MaxRunningJobs = 16
	settings.FFmpegThreadsPerJob = 3
	settings.MaxJobSeconds = 180
	settings.StaleFileTTLMinutes = 30

	cfg := BuildEngineConfig(
		video_preprocess_setting.Snapshot{Version: 7, Settings: settings},
		video_preprocess_setting.DeploymentConfig{
			FFmpegPath:  "/usr/bin/ffmpeg",
			FFprobePath: "/usr/bin/ffprobe",
			TempDir:     t.TempDir(),
		},
	)

	assert.Equal(t, int64(7), cfg.Version)
	assert.Equal(t, "/usr/bin/ffmpeg", cfg.Media.FFmpegPath)
	assert.Equal(t, "/usr/bin/ffprobe", cfg.Media.FFprobePath)
	assert.Equal(t, int64(75)<<20, cfg.Media.MaxDecodedVideoBytes)
	assert.Equal(t, int64(20)<<20, cfg.Media.MaxTotalImageBytes)
	assert.Equal(t, 82, cfg.Media.JPEGQuality)
	assert.Equal(t, 3, cfg.Media.FFmpegThreads)
	assert.Equal(t, float64(1800), cfg.Media.MaxDurationSeconds)

	assert.Equal(t, 60, cfg.Rewrite.MaxImagesPerRequest)
	assert.Equal(t, int64(20)<<20, cfg.Rewrite.MaxOutputImageBytes)
	assert.Equal(t, int64(32)<<20, cfg.Rewrite.MaxRewrittenBodyBytes)

	assert.Equal(t, 16, cfg.Admission.MaxRunningJobs)
	assert.Equal(t, int64(100)<<20, cfg.Admission.MaxWaitingBytes)
	assert.Equal(t, 180*time.Second, cfg.Admission.JobTimeout)
	assert.Equal(t, int64(5)<<30, cfg.Admission.TempStorageCapacity)
	assert.Equal(t, 30*time.Minute, cfg.StaleTTL)

	require.NoError(t, cfg.Admission.Validate())
	require.NoError(t, cfg.Media.withDefaults().validate())
}

// TestProcessorBypassesDisabledSwitch: a video request under a disabled
// config bypasses with the disabled reason. The queue counters prove the
// bypass never admits.
func TestProcessorBypassesDisabledSwitch(t *testing.T) {
	cfg := testEngineConfig(t)
	cfg.Enabled = false
	p, err := NewProcessor(cfg)
	require.NoError(t, err)

	body := videoBody("kimi-k3", 0, "data:video/mp4;base64,QUFB")
	result, err := p.Process(context.Background(), ProcessInput{UserID: 1, Model: "kimi-k3", Body: []byte(body)})
	require.NoError(t, err)
	assert.True(t, result.Bypassed)
	assert.Equal(t, BypassReasonDisabled, result.BypassReason)
	assert.Empty(t, result.OriginalModality, "a disabled bypass never reads the body")
	assert.False(t, result.Installed)
	assert.Equal(t, 0, p.Snapshot().RunningJobs)
	assert.Equal(t, 0, p.Snapshot().WaitingJobs)
}

// TestProcessorBypassesNonTargetModel: a video request whose model is not in
// target_models bypasses with the model reason, without entering the queue.
func TestProcessorBypassesNonTargetModel(t *testing.T) {
	cfg := testEngineConfig(t)
	p, err := NewProcessor(cfg)
	require.NoError(t, err)

	body := videoBody("gpt-4o", 0, "data:video/mp4;base64,QUFB")
	result, err := p.Process(context.Background(), ProcessInput{UserID: 1, Model: "gpt-4o", Body: []byte(body)})
	require.NoError(t, err)
	assert.True(t, result.Bypassed)
	assert.Equal(t, BypassReasonModelNotTarget, result.BypassReason)
	assert.Empty(t, result.OriginalModality)
	assert.Equal(t, 0, p.Snapshot().RunningJobs)
	assert.Equal(t, 0, p.Snapshot().WaitingJobs)
}

// TestProcessorBypassesTextRequest: a text-only request bypasses with the
// no-video reason, reports the observed modality and leaves an installed
// BodyStorage byte-identical and replayable.
func TestProcessorBypassesTextRequest(t *testing.T) {
	p, err := NewProcessor(testEngineConfig(t))
	require.NoError(t, err)

	body := `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(w)
	ginCtx.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	storage, err := common.CreateBodyStorage([]byte(body))
	require.NoError(t, err)
	ginCtx.Set(common.KeyBodyStorage, storage)

	result, err := p.Process(context.Background(), ProcessInput{UserID: 1, Model: "kimi-k3", Ctx: ginCtx})
	require.NoError(t, err)
	assert.True(t, result.Bypassed)
	assert.Equal(t, BypassReasonNoVideo, result.BypassReason)
	assert.Equal(t, "text", result.OriginalModality)
	assert.Equal(t, "text", result.EffectiveModality)
	assert.False(t, result.Installed)

	// The storage must still replay the original body, byte for byte.
	replayed, err := storage.Bytes()
	require.NoError(t, err)
	assert.Equal(t, body, string(replayed))
	storage.Close()
}

// TestProcessorBypassesImageRequest: an image-only request bypasses the same
// way as text, keeping its BodyStorage unchanged and replayable.
func TestProcessorBypassesImageRequest(t *testing.T) {
	p, err := NewProcessor(testEngineConfig(t))
	require.NoError(t, err)

	body := `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR"}}]}]}`

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(w)
	ginCtx.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	storage, err := common.CreateBodyStorage([]byte(body))
	require.NoError(t, err)
	ginCtx.Set(common.KeyBodyStorage, storage)

	result, err := p.Process(context.Background(), ProcessInput{UserID: 1, Model: "kimi-k3", Ctx: ginCtx})
	require.NoError(t, err)
	assert.True(t, result.Bypassed)
	assert.Equal(t, BypassReasonNoVideo, result.BypassReason)
	assert.Equal(t, "image", result.OriginalModality)
	assert.Equal(t, "image", result.EffectiveModality)
	assert.False(t, result.Installed)

	replayed, err := storage.Bytes()
	require.NoError(t, err)
	assert.Equal(t, body, string(replayed))
	storage.Close()
}

// TestProcessorStillErrorsOnInvalidJSON: a structurally broken request is a
// real error, never a bypass.
func TestProcessorStillErrorsOnInvalidJSON(t *testing.T) {
	p, err := NewProcessor(testEngineConfig(t))
	require.NoError(t, err)

	_, err = p.Process(context.Background(), ProcessInput{
		UserID: 1, Model: "kimi-k3", Body: []byte(`{"model":"kimi-k3","messages":[`),
	})
	require.Error(t, err)
	assert.NotEqual(t, "", CodeOf(err))
	assert.False(t, strings.Contains(err.Error(), "bypass"))
}

// TestProcessorPreservesStorageReadError: a BodyStorage that fails to read
// surfaces the real cause (a closed storage reports ErrStorageClosed), not a
// fabricated "request body is empty".
func TestProcessorPreservesStorageReadError(t *testing.T) {
	p, err := NewProcessor(testEngineConfig(t))
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(w)
	ginCtx.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"kimi-k3"}`))
	storage, err := common.CreateBodyStorage([]byte(`{"model":"kimi-k3"}`))
	require.NoError(t, err)
	require.NoError(t, storage.Close())
	ginCtx.Set(common.KeyBodyStorage, storage)

	_, err = p.Process(context.Background(), ProcessInput{UserID: 1, Model: "kimi-k3", Ctx: ginCtx})
	require.Error(t, err)
	assert.ErrorIs(t, err, common.ErrStorageClosed)
	assert.Equal(t, CodeInvalidRequest, CodeOf(err))
	assert.Equal(t, 0, p.Snapshot().RunningJobs)
}

// TestProcessorEmptyBodyStillErrors keeps the empty-body contract on the new
// resolveBody path: an explicit zero-length body is an error, not a bypass.
func TestProcessorEmptyBodyStillErrors(t *testing.T) {
	p, err := NewProcessor(testEngineConfig(t))
	require.NoError(t, err)

	_, err = p.Process(context.Background(), ProcessInput{UserID: 1, Model: "kimi-k3", Body: nil})
	require.Error(t, err)
	assert.Equal(t, CodeInvalidRequest, CodeOf(err))
}

// TestProcessorBypassConfigFromSettings locks the config adapter's gate
// mapping: Enabled and TargetModels flow from the settings document, and the
// slice is a private copy — mutating the config never reaches the settings.
func TestProcessorBypassConfigFromSettings(t *testing.T) {
	settings := video_preprocess_setting.DefaultSettings()
	settings.Enabled = true
	settings.TargetModels = []string{"kimi-k3", "video-pro"}
	cfg := BuildEngineConfig(
		video_preprocess_setting.Snapshot{Version: 3, Settings: settings},
		video_preprocess_setting.DeploymentConfig{TempDir: t.TempDir()},
	)

	assert.True(t, cfg.Enabled)
	assert.True(t, cfg.IsTargetModel("kimi-k3"))
	assert.True(t, cfg.IsTargetModel("video-pro"))
	assert.False(t, cfg.IsTargetModel("gpt-4o"))
	assert.False(t, cfg.IsTargetModel(""))

	// The slice is a copy: mutating it must not touch the settings document.
	cfg.TargetModels[0] = "mutated"
	assert.Equal(t, "kimi-k3", settings.TargetModels[0])

	// A disabled snapshot maps to a disabled config.
	settings.Enabled = false
	disabled := BuildEngineConfig(
		video_preprocess_setting.Snapshot{Version: 4, Settings: settings},
		video_preprocess_setting.DeploymentConfig{TempDir: t.TempDir()},
	)
	assert.False(t, disabled.Enabled)
}

func TestProcessorEndToEndRealVideo(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not found")
	}

	tempDir := t.TempDir()
	videoPath := filepath.Join(tempDir, "test.mp4")
	cmd := exec.Command("ffmpeg",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-y", videoPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("failed to generate test video: %v: %s", err, out)
	}
	videoBytes, err := os.ReadFile(videoPath)
	require.NoError(t, err)
	dataURL := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(videoBytes)

	cfg := testEngineConfig(t)
	cfg.Media.TempRoot = t.TempDir()
	cfg.Media.MinFramesPerVideo = 2
	cfg.Media.MaxFramesPerRequest = 8
	cfg.Rewrite.MaxImagesPerRequest = 8
	cfg.DefaultMaxFrames = 4
	cfg.Rewrite.MaxRequestBytes = 10 << 20

	p, err := NewProcessor(cfg)
	require.NoError(t, err)

	body := videoBody("kimi-k3", 2, dataURL, dataURL)
	result, err := p.Process(context.Background(), ProcessInput{UserID: 1, Model: "kimi-k3", Body: []byte(body)})
	require.NoError(t, err)
	defer result.Cleanup()

	// The rewrite must turn the video request into an image request the AI
	// Router detector recognizes, with the existing images preserved.
	assert.Equal(t, "video", result.OriginalModality)
	assert.Equal(t, "image", result.EffectiveModality)
	assert.Equal(t, 2, result.VideoCount)
	assert.Equal(t, 2, result.ExistingImages)
	assert.Greater(t, result.ExtractedFrames, 0)
	assert.False(t, strings.Contains(string(result.RewrittenBody()), "video_url"), "original video data must be gone")
	assert.True(t, result.ExtractedFrames+result.ExistingImages <= cfg.Rewrite.MaxImagesPerRequest)

	detected, err := ai_router.DetectRequest("/v1/chat/completions", strings.NewReader(string(result.RewrittenBody())))
	require.NoError(t, err)
	assert.Equal(t, ai_router.ModalityImage, detected.Modality)

	original, err := ai_router.DetectRequest("/v1/chat/completions", strings.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, ai_router.ModalityVideo, original.Modality)

	// Cleanup must remove the workspaces; the result reports nothing about
	// their paths.
	result.Cleanup()
	assert.Empty(t, result.workspaces)

	// Queue counters must be back to zero after the request ended.
	snapshot := p.Snapshot()
	assert.Equal(t, 0, snapshot.RunningJobs)
	assert.Equal(t, 0, snapshot.WaitingJobs)
	assert.Equal(t, int64(0), snapshot.TempStorageBytes, "no workspace may outlive the request")
	assert.Equal(t, 0, snapshot.ActiveFFmpeg)
}

func TestProcessorRejectsBudgetShortfall(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not found")
	}

	tempDir := t.TempDir()
	videoPath := filepath.Join(tempDir, "test.mp4")
	cmd := exec.Command("ffmpeg", "-f", "lavfi", "-i", "testsrc=duration=1:size=320x240:rate=10",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-y", videoPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("failed to generate test video: %v: %s", err, out)
	}
	videoBytes, err := os.ReadFile(videoPath)
	require.NoError(t, err)
	dataURL := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(videoBytes)

	cfg := testEngineConfig(t)
	cfg.Media.MinFramesPerVideo = 2
	// Two videos need 2*2 = 4 images; only 3 slots remain after the 2 existing
	// images occupy the 5-image request budget.
	cfg.Rewrite.MaxImagesPerRequest = 5
	cfg.Media.MaxFramesPerRequest = 5

	p, err := NewProcessor(cfg)
	require.NoError(t, err)

	body := videoBody("kimi-k3", 2, dataURL, dataURL)
	_, err = p.Process(context.Background(), ProcessInput{UserID: 1, Model: "kimi-k3", Body: []byte(body)})
	require.Error(t, err)
	assert.Equal(t, CodeImageLimitExceeded, CodeOf(err))

	// Nothing leaked: all slots are free again.
	snapshot := p.Snapshot()
	assert.Equal(t, 0, snapshot.RunningJobs)
	assert.Equal(t, 0, snapshot.WaitingJobs)
}

func TestProcessorQueueRejectionCountsAsAdmission(t *testing.T) {
	cfg := testEngineConfig(t)
	// A queue that accepts no waiting requests: the second concurrent request
	// is rejected with the admission code the middleware maps to 429.
	cfg.Admission.MaxRunningJobs = 1
	cfg.Admission.MaxWaitingJobs = 0
	cfg.Admission.MaxWaitingPerUser = 0

	p, err := NewProcessor(cfg)
	require.NoError(t, err)

	url := "data:video/mp4;base64,QUFBQUlHWjBhWEJwY201bGNpNTb7"

	// Occupy the only slot without ffmpeg present, so the job stalls until the
	// test releases it.
	parked := make(chan struct{})
	release := make(chan struct{})
	go func() {
		lease, acquireErr := p.queue.Acquire(context.Background(), AcquireRequest{RequestID: "occupy", UserID: 9}, nil)
		require.NoError(t, acquireErr)
		close(parked)
		<-release
		p.queue.Release(lease, nil)
	}()
	<-parked

	_, err = p.Process(context.Background(), ProcessInput{UserID: 1, Model: "kimi-k3", Body: []byte(videoBody("kimi-k3", 0, url))})
	require.Error(t, err)
	assert.Equal(t, CodeQueueFull, CodeOf(err))
	assert.Equal(t, 429, HTTPStatusForError(err))

	close(release)
}

// testEngineConfig returns a valid EngineConfig whose paths point at temp
// directories. Media work fails fast without ffmpeg, which the tests either
// skip or rely on.
func testEngineConfig(t *testing.T) EngineConfig {
	t.Helper()
	settings := video_preprocess_setting.DefaultSettings()
	settings.Enabled = true
	settings.MaxVideosPerRequest = 4
	cfg := BuildEngineConfig(
		video_preprocess_setting.Snapshot{Version: 1, Settings: settings},
		video_preprocess_setting.DeploymentConfig{TempDir: t.TempDir()},
	)
	cfg.Media.TempRoot = t.TempDir()
	cfg.Media.MinFramesPerVideo = 1
	cfg.Media.MaxFramesPerRequest = 60
	cfg.Rewrite.MaxImagesPerRequest = 60
	cfg.Rewrite.MaxRequestBytes = 32 << 20
	return cfg
}

// TestProcessorInstallsBodyStorageOnGinContext runs the whole pipeline through
// a gin request: the rewritten body must be installed as a replayable
// BodyStorage, the detector must see an image request through the installed
// storage, and the original body must be gone (no video_url anywhere).
func TestProcessorInstallsBodyStorageOnGinContext(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not found")
	}

	tempDir := t.TempDir()
	videoPath := filepath.Join(tempDir, "test.mp4")
	cmd := exec.Command("ffmpeg", "-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-y", videoPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("failed to generate test video: %v: %s", err, out)
	}
	videoBytes, err := os.ReadFile(videoPath)
	require.NoError(t, err)
	dataURL := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(videoBytes)

	cfg := testEngineConfig(t)
	cfg.Media.MinFramesPerVideo = 1
	cfg.Media.MaxFramesPerRequest = 4
	cfg.Rewrite.MaxImagesPerRequest = 4
	p, err := NewProcessor(cfg)
	require.NoError(t, err)

	body := videoBody("kimi-k3", 0, dataURL)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(w)
	ginCtx.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	ginCtx.Request.Header.Set("Content-Type", "application/json")
	// Materialize the original storage the middleware would have installed.
	original, err := common.CreateBodyStorage([]byte(body))
	require.NoError(t, err)
	ginCtx.Set(common.KeyBodyStorage, original)

	result, err := p.Process(context.Background(), ProcessInput{
		UserID: 7, Model: "kimi-k3", Ctx: ginCtx,
	})
	require.NoError(t, err)
	defer result.Cleanup()

	assert.True(t, result.Installed, "the rewritten body must be installed on the gin context")

	// The installed storage replays as the rewritten image request.
	installed, err := common.GetBodyStorage(ginCtx)
	require.NoError(t, err)
	installedBody, err := installed.Bytes()
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(installedBody), "video_url"))
	detected, err := ai_router.DetectRequest("/v1/chat/completions", strings.NewReader(string(installedBody)))
	require.NoError(t, err)
	assert.Equal(t, ai_router.ModalityImage, detected.Modality)
	assert.Equal(t, int64(len(installedBody)), installed.Size())

	installed.Close()
	common.CleanupBodyStorage(ginCtx)
}

// TestProcessorRejectsCorruptVideo feeds a data URL whose payload sniffs as a
// container but probes as garbage: the whole request fails with a media code,
// and neither the queue nor the temp storage leaks anything.
func TestProcessorRejectsCorruptVideo(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not found")
	}
	cfg := testEngineConfig(t)
	p, err := NewProcessor(cfg)
	require.NoError(t, err)

	// "ftyp" header keeps the sniff happy; the rest is not a real movie.
	body := videoBody("kimi-k3", 0, "data:video/mp4;base64,"+base64.StdEncoding.EncodeToString([]byte("ftyp garbage not a video at all")))

	_, err = p.Process(context.Background(), ProcessInput{UserID: 3, Model: "kimi-k3", Body: []byte(body)})
	require.Error(t, err)
	code := CodeOf(err)
	assert.NotEmpty(t, code, "a corrupt video must classify into a stable code")
	assert.Equal(t, 0, p.Snapshot().RunningJobs)
	assert.Equal(t, int64(0), p.Snapshot().TempStorageBytes)
}

// TestProcessorCancelDuringQueueWait cancels the request context while the
// request is parked in the queue: the failure must classify as canceled and
// leave no counters behind.
func TestProcessorCancelDuringQueueWait(t *testing.T) {
	cfg := testEngineConfig(t)
	cfg.Admission.MaxRunningJobs = 1
	cfg.Admission.MaxWaitingJobs = 8
	cfg.Admission.MaxWaitingPerUser = 8
	p, err := NewProcessor(cfg)
	require.NoError(t, err)

	// Occupy the only running slot directly so the processor's request queues.
	occupier := acquireOK(t, context.Background(), p.queue, AcquireRequest{RequestID: "occupy", UserID: 42})
	defer func() { p.queue.Release(occupier, nil) }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		body := videoBody("kimi-k3", 0, "data:video/mp4;base64,QUFB")
		_, err := p.Process(ctx, ProcessInput{UserID: 1, Model: "kimi-k3", Body: []byte(body)})
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	err = <-done
	require.Error(t, err)
	assert.Equal(t, CodeProcessingCanceled, CodeOf(err))
	assert.Equal(t, 0, p.Snapshot().WaitingJobs)
	assert.Equal(t, 1, p.Snapshot().RunningJobs, "only the test's occupier holds a slot")
	p.queue.Release(occupier, nil)
	assert.Equal(t, 0, p.Snapshot().RunningJobs)
}
