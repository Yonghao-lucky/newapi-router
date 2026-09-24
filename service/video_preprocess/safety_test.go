package video_preprocess

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBase64ExactBoundsAndCancellation(t *testing.T) {
	for _, payload := range []string{"YQ==", "YWI=", "YWJj", "YQ", "YWI"} {
		t.Run(payload, func(t *testing.T) {
			u, size, err := parseVideoDataURL("data:video/mp4;base64,"+payload, nil)
			require.NoError(t, err)
			expected := "abc"[:size]
			var out bytes.Buffer
			written, err := decodeVideoDataURL(context.Background(), u, &out, size)
			require.NoError(t, err)
			assert.Equal(t, size, written)
			assert.Equal(t, expected, out.String())
			_, err = decodeVideoDataURL(context.Background(), u, io.Discard, size-1)
			assert.Equal(t, CodeTooLarge, CodeOf(err))
		})
	}
	for _, payload := range []string{"YR==", "YQ=", "YQ==YQ==", "YW\nJj", "!!!!"} {
		u, _, err := parseVideoDataURL("data:video/mp4;base64,"+payload, nil)
		if err == nil {
			_, err = decodeVideoDataURL(context.Background(), u, io.Discard, 100)
		}
		require.Error(t, err, payload)
		assert.Equal(t, CodeInvalidBase64, CodeOf(err))
	}
	ctx, cancel := context.WithCancel(context.Background())
	writer := &cancelAfterWrite{cancel: cancel}
	payload := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 512<<10))
	u, _, err := parseVideoDataURL("data:video/mp4;base64,"+payload, nil)
	require.NoError(t, err)
	_, err = decodeVideoDataURL(ctx, u, writer, 1<<20)
	assert.Equal(t, CodeProcessingCanceled, CodeOf(err))
	assert.Equal(t, 1, writer.calls, "cancellation stops before writing another block")
	for _, url := range []string{"SECRET://example", "data:video/SECRET;base64,AAAA", "data:video/mp4;SECRET;base64,AAAA"} {
		_, _, err := parseVideoDataURL(url, []string{"video/mp4"})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "SECRET")
	}
}

func TestVideoPublicErrorsKeepCodesAndDoNotExposeDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		err     error
		status  int
		message string
	}{
		{newError(CodeUnsupportedFormat, "video codec is not enabled"), 400, "video codec is not enabled"},
		{newError(CodeUnsupportedFormat, "video MIME type contradicts its container"), 400, "video MIME type contradicts its container"},
		{newError(CodeInvalidDataURL, "remote video URLs are disabled"), 400, "remote video URLs are disabled"},
		{newError(CodeInvalidDataURL, "remote video size is unknown and unknown sizes are disabled"), 400, "remote video size is unknown and unknown sizes are disabled"},
		{remoteVideoStatusError("Range", 403), 503, "remote video source rejected Range request (HTTP 403)"},
		{newError(CodeOutputTooLarge, "SECRET"), 413, "video frames or rewritten request exceed the configured output byte limit"},
		{wrapError(CodeProbeFailed, "stderr /tmp/SECRET", fmt.Errorf("https://private.test/?signature=SECRET")), 503, "video preprocessing is temporarily unavailable"},
	} {
		wrapped := &StageError{Err: tc.err}
		assert.Equal(t, tc.status, HTTPStatusForError(wrapped))
		assert.Equal(t, tc.message, PublicMessageForError(wrapped))
		assert.NotContains(t, PublicMessageForError(wrapped), "SECRET")
	}
}

type cancelAfterWrite struct {
	cancel context.CancelFunc
	calls  int
}

func (w *cancelAfterWrite) Write(p []byte) (int, error) { w.calls++; w.cancel(); return len(p), nil }

func TestQueuedVideoSpillsWithCacheDisabledAndPreservesReplay(t *testing.T) {
	// The ordinary gateway cache is disabled: video still has to leave the heap.
	cacheConfig := common.GetDiskCacheConfig()
	previous := cacheConfig
	cacheConfig.Enabled = false
	common.SetDiskCacheConfig(cacheConfig)
	t.Cleanup(func() { common.SetDiskCacheConfig(previous) })
	cfg := testEngineConfig(t)
	cfg.Admission.MaxRunningJobs = 1
	p, err := NewProcessor(cfg)
	require.NoError(t, err)
	held, err := p.queue.Acquire(context.Background(), AcquireRequest{UserID: 1}, nil)
	require.NoError(t, err)
	defer p.queue.Release(held, nil)
	body := videoBody("kimi-k3", 0, "data:video/mp4;base64,"+strings.Repeat("A", 8<<20))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
	storage, err := common.GetBodyStorage(c)
	require.NoError(t, err)
	require.False(t, storage.IsDisk())
	done := make(chan error, 1)
	go func() { _, err := p.Process(ctx, ProcessInput{UserID: 2, Model: "kimi-k3", Ctx: c}); done <- err }()
	require.Eventually(t, func() bool { return p.Snapshot().WaitingJobs == 1 }, 5*time.Second, time.Millisecond)
	assert.True(t, storage.IsDisk())
	assert.Equal(t, int64(len(body)), p.Snapshot().WaitingBytes)
	assert.GreaterOrEqual(t, p.Snapshot().TempStorageBytes, int64(len(body)))
	// Even a backdated active spool survives the janitor.
	entries, err := os.ReadDir(cfg.Media.TempRoot)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(cfg.Media.TempRoot, entries[0].Name()), old, old))
	require.NoError(t, CleanupStale(cfg.Media.TempRoot, time.Hour))
	cancel()
	require.Equal(t, CodeProcessingCanceled, CodeOf(<-done))
	current, err := common.GetBodyStorage(c)
	require.NoError(t, err)
	assert.Same(t, storage, current)
	reader, err := current.NewReader()
	require.NoError(t, err)
	replay, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	assert.Equal(t, body, string(replay))
	common.CleanupBodyStorage(c)
	assert.Zero(t, p.Snapshot().TempStorageBytes)
	entries, err = os.ReadDir(cfg.Media.TempRoot)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestStorageReservationsCoverOutstandingWrites(t *testing.T) {
	cfg := testEngineConfig(t)
	cfg.Admission.TempStorageCapacity = 100
	p, err := NewProcessor(cfg)
	require.NoError(t, err)
	release, err := p.reserveStorage(cfg, 60)
	require.NoError(t, err)
	_, err = p.reserveStorage(cfg, 41)
	assert.Equal(t, CodeStorageExhausted, CodeOf(err))
	release()
	release()
	next, err := p.reserveStorage(cfg, 100)
	require.NoError(t, err)
	next()
	cfg.Admission.TempStorageCapacity = 0
	_, err = p.reserveStorage(cfg, 1<<60)
	assert.Equal(t, CodeDiskStorageExhausted, CodeOf(err))
}

func TestDownloadSlotsBoundWorkAndReleaseAfterCancellation(t *testing.T) {
	p, err := NewProcessor(testEngineConfig(t))
	require.NoError(t, err)
	first, err := p.acquireDownload(context.Background(), 1)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		release, waitErr := p.acquireDownload(ctx, 1)
		if release != nil {
			release()
		}
		done <- waitErr
	}()
	cancel()
	select {
	case err := <-done:
		assert.Equal(t, CodeProcessingCanceled, CodeOf(err))
	case <-time.After(5 * time.Second):
		t.Fatal("download slot wait did not stop on cancellation")
	}
	first()
	first() // A cleanup repeated by error handling must not create extra capacity.
	second, err := p.acquireDownload(context.Background(), 1)
	require.NoError(t, err)
	defer second()
	expired, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	_, err = p.acquireDownload(expired, 1)
	assert.Equal(t, CodeProcessingTimeout, CodeOf(err))
}

func TestMediaProbeRejectsExternalReferencePlaylists(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	limits := engineLimits(t).withDefaults()
	for _, playlist := range []string{
		"#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\nhttps://127.0.0.1/SECRET\n#EXT-X-ENDLIST\n",
		"ffconcat version 1.0\nfile '/etc/SECRET'\n",
	} {
		path := filepath.Join(t.TempDir(), "video.bin")
		require.NoError(t, os.WriteFile(path, []byte(playlist), 0600))
		_, err := probeVideo(context.Background(), "ffprobe", path, limits)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "SECRET")
		_, err = ProcessVideo(context.Background(), ProcessVideoInput{Video: VideoInput{URL: "data:video/mp4;base64," + base64.StdEncoding.EncodeToString([]byte(playlist))}}, Options{Limits: limits})
		assert.Equal(t, CodeUnsupportedFormat, CodeOf(err))
	}
}

func TestMediaCodecAndConstraintMatrix(t *testing.T) {
	for _, binary := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("missing required binary %s", binary)
		}
	}
	for _, tc := range []struct {
		name, codec, mime, source, extension string
		encoder                              []string
	}{
		{"h264_short", "h264", "video/mp4", "testsrc2=s=160x120:r=25:d=0.04", "mp4", []string{"-c:v", "libx264"}},
		{"hevc", "hevc", "video/mp4", "testsrc2=s=160x120:r=4:d=1", "mp4", []string{"-c:v", "libx265", "-x265-params", "pools=1:frame-threads=1:log-level=error"}},
		{"vp9", "vp9", "video/webm", "testsrc2=s=160x120:r=4:d=1", "webm", []string{"-c:v", "libvpx-vp9", "-deadline", "realtime"}},
		{"av1", "av1", "video/webm", "testsrc2=s=160x120:r=4:d=1", "webm", []string{"-c:v", "libaom-av1", "-cpu-used", "8"}},
		{"mpeg4_mp4", "mpeg4", "video/mp4", "testsrc2=s=160x120:r=4:d=1", "mp4", []string{"-c:v", "mpeg4"}},
		{"existing_custom_mime", "h264", "video/custom", "testsrc2=s=160x120:r=4:d=1", "mp4", []string{"-c:v", "libx264"}},
		{"mpeg4_avi", "mpeg4", "video/x-msvideo", "testsrc2=s=160x120:r=4:d=1", "avi", []string{"-c:v", "mpeg4"}},
		{"avi_mime_alias", "mpeg4", "video/avi", "testsrc2=s=160x120:r=4:d=1", "avi", []string{"-c:v", "mpeg4"}},
		{"vp9_mkv", "vp9", "video/x-matroska", "testsrc2=s=160x120:r=4:d=1", "mkv", []string{"-c:v", "libvpx-vp9", "-deadline", "realtime"}},
		{"vp8", "vp8", "video/webm", "testsrc2=s=160x120:r=4:d=1", "webm", []string{"-c:v", "libvpx", "-deadline", "realtime"}},
		{"mjpeg", "mjpeg", "video/x-msvideo", "testsrc2=s=160x120:r=4:d=1", "avi", []string{"-c:v", "mjpeg"}},
		{"mpeg2video", "mpeg2video", "video/x-matroska", "testsrc2=s=160x120:r=25:d=0.2", "mkv", []string{"-c:v", "mpeg2video"}},
		{"h263", "h263", "video/x-msvideo", "testsrc2=s=176x144:r=25:d=0.2", "avi", []string{"-c:v", "h263"}},
		{"flv1", "flv1", "video/x-flv", "testsrc2=s=160x120:r=4:d=1", "flv", []string{"-c:v", "flv"}},
		{"prores", "prores", "video/quicktime", "testsrc2=s=160x120:r=4:d=1", "mov", []string{"-c:v", "prores_ks", "-profile:v", "0"}},
		{"ffv1", "ffv1", "video/x-matroska", "testsrc2=s=160x120:r=4:d=1", "mkv", []string{"-c:v", "ffv1"}},
		{"4k", "h264", "video/mp4", "color=gray:s=3840x2160:r=1:d=1", "mp4", []string{"-c:v", "libx264", "-preset", "ultrafast"}},
		{"multiple_video_tracks", "h264", "video/mp4", "testsrc2=s=160x120:r=4:d=1", "mp4", []string{"-map", "0:v", "-map", "0:v", "-c:v", "libx264"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fixture."+tc.extension)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			args := []string{"-nostdin", "-v", "error", "-f", "lavfi", "-i", tc.source, "-threads", "1"}
			args = append(args, tc.encoder...)
			args = append(args, path)
			output, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput()
			require.NoError(t, err, string(output))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			url := "data:" + tc.mime + ";base64," + base64.StdEncoding.EncodeToString(data)
			cfg := testEngineConfig(t)
			// New choices are opt-in. Defaults must still reject disabled codecs.
			if !containsFold(cfg.Media.AllowedVideoCodecs, tc.codec) || !containsFold(cfg.AllowedMIMETypes, tc.mime) {
				rejected, createErr := NewProcessor(cfg)
				require.NoError(t, createErr)
				_, rejectErr := rejected.Process(ctx, ProcessInput{Model: "kimi-k3", Body: []byte(videoBody("kimi-k3", 0, url))})
				assert.Equal(t, CodeUnsupportedFormat, CodeOf(rejectErr))
			}
			cfg.Media.AllowedVideoCodecs = append(cfg.Media.AllowedVideoCodecs, tc.codec)
			cfg.AllowedMIMETypes = append(cfg.AllowedMIMETypes, tc.mime)
			p, err := NewProcessor(cfg)
			require.NoError(t, err)
			result, err := p.Process(ctx, ProcessInput{Model: "kimi-k3", Body: []byte(videoBody("kimi-k3", 0, url))})
			if tc.name == "multiple_video_tracks" {
				require.Error(t, err)
				assert.Equal(t, CodeUnsupportedFormat, CodeOf(err))
				assert.Zero(t, p.Snapshot().TempStorageBytes)
				return
			}
			require.NoError(t, err)
			assert.Greater(t, result.ExtractedFrames, 0)
			require.Len(t, result.Videos, 1)
			assert.Equal(t, tc.codec, result.Videos[0].Codec)
			assert.Zero(t, p.Snapshot().ActiveMediaProcesses)
			assert.Zero(t, p.Snapshot().TempStorageBytes)
			if tc.mime == "video/webm" {
				_, err = p.Process(ctx, ProcessInput{Model: "kimi-k3", Body: []byte(videoBody("kimi-k3", 0, strings.Replace(url, "video/webm", "video/mp4", 1)))})
				assert.Equal(t, CodeUnsupportedFormat, CodeOf(err))
			}
			if tc.mime == "video/x-matroska" {
				_, err = p.Process(ctx, ProcessInput{Model: "kimi-k3", Body: []byte(videoBody("kimi-k3", 0, strings.Replace(url, tc.mime, "video/webm", 1)))})
				assert.Equal(t, CodeUnsupportedFormat, CodeOf(err), "MKV must not impersonate WebM")
			}
			// The same configured format must work through the protected URL pipeline.
			downloader := newRemoteVideoTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				if r.Method == http.MethodGet {
					_, _ = w.Write(data)
				}
			}))
			cfg.RemoteURL = RemoteURLConfig{Enabled: true, AllowUnknownSize: true, DownloadTimeout: 5 * time.Second, MaxDownloads: 1, downloader: &downloader}
			if tc.name == "vp9_mkv" {
				disabled := testEngineConfig(t)
				disabled.RemoteURL = cfg.RemoteURL
				denied, createErr := NewProcessor(disabled)
				require.NoError(t, createErr)
				_, denyErr := denied.Process(ctx, ProcessInput{Model: "kimi-k3", Body: []byte(videoBody("kimi-k3", 0, "https://video.example.test/fixture"))})
				assert.Equal(t, CodeUnsupportedFormat, CodeOf(denyErr))
				assert.Equal(t, "downloaded video container is not enabled", PublicMessageForError(denyErr))
				assert.Zero(t, denied.Snapshot().TempStorageBytes)
			}
			remoteProcessor, err := NewProcessor(cfg)
			require.NoError(t, err)
			remoteResult, err := remoteProcessor.Process(ctx, ProcessInput{Model: "kimi-k3", Body: []byte(videoBody("kimi-k3", 0, "https://video.example.test/fixture?signature=SECRET"))})
			require.NoError(t, err)
			assert.Greater(t, remoteResult.ExtractedFrames, 0)
			assert.Equal(t, tc.codec, remoteResult.Videos[0].Codec)
			assert.Zero(t, remoteProcessor.Snapshot().TempStorageBytes)
		})
	}
	// Structural metadata validation covers values a hostile file can advertise.
	base := `{"format":{"format_name":"mov","duration":"999"},"streams":[{"codec_type":"video","codec_name":"h264","width":160,"height":120,"r_frame_rate":"8/1","duration":"2"}]}`
	meta, err := parseProbeOutput([]byte(base))
	require.NoError(t, err)
	assert.Equal(t, 2.0, meta.DurationSeconds, "trailing audio must not inflate video duration")
	for _, tc := range []struct {
		name   string
		limits MediaLimits
		code   string
	}{
		{"duration", MediaLimits{MaxDurationSeconds: 1}, CodeTooLong},
		{"width", MediaLimits{MaxWidth: 100}, CodeResolutionExceeded},
		{"pixels", MediaLimits{MaxPixels: 100}, CodeResolutionExceeded},
		{"fps", MediaLimits{MaxFPS: 4}, CodeUnsupportedFormat},
		{"frames", MediaLimits{MaxAnalyzedFrameCount: 10}, CodeTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.code, CodeOf(validateMetadata(meta, tc.limits.withDefaults()))) })
	}
	for _, rate := range []string{"NaN", "+Inf", "1e300"} {
		_, err := parseProbeOutput([]byte(strings.Replace(base, "8/1", rate, 1)))
		require.Error(t, err, rate)
		assert.Equal(t, CodeUnsupportedFormat, CodeOf(err))
	}
	multi := fmt.Sprintf(`{"format":{"format_name":"mov","duration":"1"},"streams":[%s,%s]}`, `{"codec_type":"video","codec_name":"h264","width":160,"height":120,"r_frame_rate":"8/1"}`, `{"codec_type":"video","codec_name":"h264","width":160,"height":120,"r_frame_rate":"8/1"}`)
	meta, err = parseProbeOutput([]byte(multi))
	require.NoError(t, err)
	assert.Equal(t, CodeUnsupportedFormat, CodeOf(validateMetadata(meta, MediaLimits{}.withDefaults())))
}

func TestMediaSubprocessCancellationReapsProcessAndWorkspace(t *testing.T) {
	url := fixtureVideo(t, "static")
	for _, stage := range []string{"probe", "analysis", "export"} {
		t.Run(stage, func(t *testing.T) {
			cfg := testEngineConfig(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pidFile := filepath.Join(t.TempDir(), "pid")
			t.Setenv("VIDEO_SAFETY_PID", pidFile)
			binary := filepath.Join(t.TempDir(), "blocked-media")
			// The test executable remains blocked until cancellation; no timer creates
			// the synchronization. Its PID must disappear, including zombie state.
			require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s' \"$$\" > \"$VIDEO_SAFETY_PID\"\nexec cat /dev/zero\n"), 0700))
			// cat writes indefinitely, exercising the bounded stdout/stderr consumers.
			// Probe consumes bounded JSON output; analysis/export read bounded frames.
			done := make(chan error, 1)
			var kept *ProcessVideoResult
			var err error
			if stage != "probe" {
				kept, err = NewMediaEngine().ProbeAndKeep(ctx, url, cfg.Media, cfg.AllowedMIMETypes, 1)
				require.NoError(t, err)
				defer kept.Cleanup()
				kept.limits.FFmpegPath = binary
			}
			go func() {
				var err error
				switch stage {
				case "probe":
					cfg.Media.FFprobePath = binary
					_, err = ProcessVideo(ctx, ProcessVideoInput{Video: VideoInput{URL: url}}, Options{Limits: cfg.Media})
				case "analysis":
					_, err = analyzeVideo(ctx, kept, cfg.Analysis)
				case "export":
					err = NewMediaEngine().ExportUniform(ctx, kept, 2)
				}
				done <- err
			}()
			var pid int
			require.Eventually(t, func() bool {
				data, err := os.ReadFile(pidFile)
				if err != nil {
					return false
				}
				pid, err = strconv.Atoi(string(data))
				return err == nil
			}, 5*time.Second, time.Millisecond)
			group, err := syscall.Getpgid(pid)
			require.NoError(t, err)
			assert.Equal(t, pid, group, "media has its own process group")
			cancel()
			select {
			case err := <-done:
				assert.Equal(t, CodeProcessingCanceled, CodeOf(err))
			case <-time.After(5 * time.Second):
				t.Fatal("media did not stop after cancellation")
			}
			assert.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "Wait must reap the media process")
			if kept != nil {
				require.NoError(t, kept.Cleanup())
			}
			entries, err := os.ReadDir(cfg.Media.TempRoot)
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

func TestMediaToolCrashMissingAndBoundedDiagnostics(t *testing.T) {
	url := fixtureVideo(t, "static")
	for _, tc := range []struct {
		name   string
		probe  bool
		script string
		code   string
	}{
		{"probe missing", true, "", CodeProbeFailed},
		{"ffmpeg missing", false, "", CodeDecodeFailed},
		{"probe crash", true, "#!/bin/sh\nprintf 'SECRET' >&2\nkill -SEGV $$\n", CodeProbeFailed},
		{"ffmpeg crash", false, "#!/bin/sh\nprintf 'SECRET' >&2\nkill -SEGV $$\n", CodeDecodeFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testEngineConfig(t)
			binary := filepath.Join(t.TempDir(), "tool")
			if tc.script != "" {
				require.NoError(t, os.WriteFile(binary, []byte(tc.script), 0700))
			}
			if tc.probe {
				cfg.Media.FFprobePath = binary
			} else {
				cfg.Media.FFmpegPath = binary
			}
			p, err := NewProcessor(cfg)
			require.NoError(t, err)
			_, err = p.Process(context.Background(), ProcessInput{Model: "kimi-k3", Body: []byte(videoBody("kimi-k3", 0, url))})
			require.Error(t, err)
			assert.Equal(t, tc.code, CodeOf(err))
			assert.NotContains(t, err.Error(), "SECRET")
			assert.Zero(t, p.Snapshot().ActiveMediaProcesses)
			assert.Zero(t, p.Snapshot().TempStorageBytes)
		})
	}
	var output bytes.Buffer
	bounded := limitedWriter{w: &output, limit: 32}
	n, err := bounded.Write(bytes.Repeat([]byte("SECRET"), 10000))
	require.NoError(t, err)
	assert.Equal(t, 60000, n)
	assert.Len(t, output.Bytes(), 32)
	_, err = bounded.Write([]byte("ignored"))
	require.NoError(t, err)
	assert.Len(t, output.Bytes(), 32)
}

func TestVideoHandoffLogsNeverEchoMediaOrPrompt(t *testing.T) {
	var output bytes.Buffer
	common.LogWriterMu.Lock()
	previousOut, previousErr := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = &output, &output
	common.LogWriterMu.Unlock()
	previousDebug := common.DebugEnabled
	common.DebugEnabled = true
	t.Cleanup(func() {
		common.DebugEnabled = previousDebug
		common.LogWriterMu.Lock()
		gin.DefaultWriter, gin.DefaultErrorWriter = previousOut, previousErr
		common.LogWriterMu.Unlock()
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(c, constant.ContextKeyVideoPreprocessed, true)
	c.Set(common.RequestIdKey, "video-private-test")
	logger.LogDebug(c, "data:image/jpeg;base64,SECRET")
	logger.LogWarn(c, "sensitive words in prompt: SECRET")
	logger.LogError(c, "upstream echoed SECRET")
	assert.NotContains(t, output.String(), "SECRET")
	assert.Contains(t, output.String(), "video-private-test")
	assert.Contains(t, output.String(), "[WARN]")
	assert.Contains(t, output.String(), "[ERR]")
}

func TestDecodedVideoWritePropagatesDiskFull(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/dev/full ENOSPC verification requires Linux")
	}
	file, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	require.NoError(t, err, "Linux ENOSPC test device is required")
	defer file.Close()
	input, _, err := parseVideoDataURL("data:video/mp4;base64,YWJj", nil)
	require.NoError(t, err)
	_, err = decodeVideoDataURL(context.Background(), input, file, 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, syscall.ENOSPC)
	assert.Equal(t, CodeStorageFailed, CodeOf(err))
	assert.Equal(t, 503, HTTPStatusForError(err))
}
