package middleware

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/service/video_preprocess"
	"github.com/QuantumNous/new-api/setting/video_preprocess_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func videoMiddlewareConfig(t *testing.T, enabled bool) (*video_preprocess.Processor, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	p := video_preprocess.GetProcessor()
	require.NotNil(t, p)
	original := video_preprocess.BuildEngineConfig(video_preprocess_setting.GetSnapshot(), video_preprocess_setting.GetDeploymentConfig())
	t.Cleanup(func() { require.NoError(t, p.SetConfig(original)) })
	settings := video_preprocess_setting.DefaultSettings()
	settings.Enabled = enabled
	settings.TargetModels = []string{"kimi-k3"}
	root := t.TempDir()
	cfg := video_preprocess.BuildEngineConfig(video_preprocess_setting.Snapshot{Settings: settings}, video_preprocess_setting.DeploymentConfig{FFmpegPath: "ffmpeg", FFprobePath: "ffprobe", TempDir: root})
	require.NoError(t, p.SetConfig(cfg))
	return p, root
}

func TestVideoPreprocessBypassAndInputErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		enabled          bool
		status           int
	}{
		{"disabled", `{"model":"kimi-k3","messages":[{"content":[{"type":"video_url","video_url":{"url":"invalid"}}]}]}`, "", false, 204},
		{"disabled malformed JSON", `{"broken":`, "", false, 204},
		{"non-target", `{"model":"other","messages":[{"content":[{"type":"video_url","video_url":{"url":"invalid"}}]}]}`, "", true, 204},
		{"text", `{"model":"kimi-k3","messages":[{"role":"user","content":"hello"}]}`, "", true, 204},
		{"image", `{"model":"kimi-k3","messages":[{"content":[{"type":"image_url","image_url":{"url":"image"}}]}]}`, "", true, 204},
		{"invalid JSON", `{"model":"kimi-k3","messages":[{invalid}]}`, video_preprocess.CodeInvalidRequest, true, 400},
		{"duplicate model", `{"model":"other","model":"kimi-k3","messages":[]}`, video_preprocess.CodeInvalidRequest, true, 400},
		{"numeric model", `{"model":123,"messages":[]}`, video_preprocess.CodeInvalidRequest, true, 400},
		{"null model", `{"model":null,"messages":[]}`, video_preprocess.CodeInvalidRequest, true, 400},
		{"invalid video", `{"model":"kimi-k3","messages":[{"content":[{"type":"video_url","video_url":{"url":"https://secret/prompt"}}]}]}`, video_preprocess.CodeInvalidDataURL, true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, root := videoMiddlewareConfig(t, tc.enabled)
			var storage common.BodyStorage
			handlerCalled := false
			engine := gin.New()
			engine.Use(BodyStorageCleanup(), func(c *gin.Context) {
				var err error
				storage, err = common.GetBodyStorage(c)
				require.NoError(t, err)
				c.Next()
				current, err := common.GetBodyStorage(c)
				require.NoError(t, err)
				assert.Same(t, storage, current)
				data, err := current.Bytes()
				require.NoError(t, err)
				assert.Equal(t, tc.body, string(data), "original body survives both bypass and failure until request cleanup")
			})
			engine.POST("/v1/chat/completions", VideoPreprocess(), func(c *gin.Context) {
				handlerCalled = true
				assert.False(t, common.GetContextKeyBool(c, constant.ContextKeyVideoPreprocessed))
				c.Status(http.StatusNoContent)
			})
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tc.body))
			// JSON validation must also apply when Content-Type is omitted.
			engine.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			assert.Equal(t, tc.status == 204, handlerCalled)
			if tc.code != "" {
				var result struct {
					Error struct{ Code, Message string }
				}
				require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
				assert.Equal(t, tc.code, result.Error.Code)
				assert.NotContains(t, result.Error.Message, "secret")
			}
			_, err := storage.Bytes()
			assert.Error(t, err, "unified cleanup closes original storage")
			assert.Zero(t, p.Snapshot().RunningJobs)
			assert.Zero(t, p.Snapshot().WaitingJobs)
			files, err := os.ReadDir(root)
			require.NoError(t, err)
			assert.Empty(t, files)
		})
	}
}

func TestVideoPreprocessErrorResponses(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
	}{
		{video_preprocess.CodeInvalidRequest, 400}, {video_preprocess.CodeInvalidDataURL, 400},
		{video_preprocess.CodeInvalidBase64, 400}, {video_preprocess.CodeUnsupportedFormat, 400},
		{video_preprocess.CodeTooLong, 400}, {video_preprocess.CodeResolutionExceeded, 400},
		{video_preprocess.CodeImageLimitExceeded, 400}, {video_preprocess.CodeTooLarge, 413},
		{video_preprocess.CodeOutputTooLarge, 413}, {video_preprocess.CodeQueueFull, 429},
		{video_preprocess.CodeQueueBytes, 429}, {video_preprocess.CodeUserLimit, 429},
		{video_preprocess.CodeEstimatedWait, 429}, {video_preprocess.CodeStorageExhausted, 503},
		{video_preprocess.CodeDiskStorageExhausted, 503}, {video_preprocess.CodeStorageFailed, 503},
		{video_preprocess.CodeProbeFailed, 503}, {video_preprocess.CodeDecodeFailed, 503},
		{video_preprocess.CodeExportFailed, 503}, {video_preprocess.CodeInvalidConfig, 503},
		{video_preprocess.CodeProcessingTimeout, 408}, {video_preprocess.CodeQueueTimeout, 408},
		{video_preprocess.CodeProcessingCanceled, 499}, {video_preprocess.CodeInternal, 500},
		{video_preprocess.CodeProcessingFailed, 500}, {video_preprocess.CodeRewriteFailed, 500},
	} {
		t.Run(tc.code, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			handleVideoPreprocessError(c, &video_preprocess.Error{Code: tc.code, Message: "ffmpeg stderr /tmp/private data:video/mp4;base64,SECRET prompt"})
			require.True(t, c.IsAborted())
			assert.Equal(t, tc.status, w.Code)
			var result struct {
				Error struct{ Code, Message string }
			}
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
			assert.Equal(t, tc.code, result.Error.Code)
			assert.NotContains(t, w.Body.String(), "SECRET")
			assert.NotContains(t, w.Body.String(), "/tmp/")
			assert.NotContains(t, w.Body.String(), "stderr")
			if tc.status == 429 {
				assert.Equal(t, "1", w.Header().Get("Retry-After"))
			} else {
				assert.Empty(t, w.Header().Get("Retry-After"))
			}
		})
	}
	for _, err := range []error{errors.New("secret path"), &video_preprocess.AdmissionError{Code: video_preprocess.CodeQueueFull, RetryAfterSecs: 7, HTTPStatus: 500}} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
		handleVideoPreprocessError(c, err)
		if video_preprocess.CodeOf(err) == "" {
			assert.Equal(t, 500, w.Code)
			assert.Contains(t, w.Body.String(), video_preprocess.CodeInternal)
		} else {
			assert.Equal(t, 429, w.Code, "stable code is the authority even if producer supplied a conflicting status")
			assert.Equal(t, "7", w.Header().Get("Retry-After"))
		}
	}
}

func TestVideoPreprocessRealVideoAndStorageOwnership(t *testing.T) {
	for _, binary := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("required media binary unavailable: %s", binary)
		}
	}
	p, root := videoMiddlewareConfig(t, true)
	path := filepath.Join(t.TempDir(), "fixture.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=160x120:r=8:d=2", "-threads", "1", "-c:v", "libx264", "-y", path).CombinedOutput()
	require.NoError(t, err, string(output))
	video, err := os.ReadFile(path)
	require.NoError(t, err)
	dataURL := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(video)
	body := `{"model":"kimi-k3","custom":{"keep":true},"messages":[{"role":"user","content":[{"type":"text","text":"before"},{"type":"video_url","video_url":{"url":"` + dataURL + `"}},{"type":"text","text":"after"}]}]}`
	var old, installed common.BodyStorage
	called := false
	engine := gin.New()
	engine.Use(BodyStorageCleanup(), func(c *gin.Context) {
		var err error
		old, err = common.GetBodyStorage(c)
		require.NoError(t, err)
	})
	engine.POST("/v1/chat/completions", VideoPreprocess(), func(c *gin.Context) {
		called = true
		var err error
		installed, err = common.GetBodyStorage(c)
		require.NoError(t, err)
		assert.NotSame(t, old, installed)
		_, err = old.Bytes()
		assert.Error(t, err, "successful installation closes old storage")
		rewritten, err := installed.Bytes()
		require.NoError(t, err)
		assert.Equal(t, int64(len(rewritten)), c.Request.ContentLength)
		replay, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		assert.Equal(t, rewritten, replay)
		assert.NotContains(t, string(rewritten), "video_url")
		assert.NotContains(t, string(rewritten), dataURL)
		assert.Contains(t, string(rewritten), "data:image/jpeg;base64,")
		assert.Contains(t, string(rewritten), `"custom":{"keep":true}`)
		assert.Contains(t, string(rewritten), "before")
		assert.Contains(t, string(rewritten), "after")
		assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyVideoPreprocessed))
		assert.Equal(t, "video", common.GetContextKeyString(c, constant.ContextKeyVideoOriginalModality))
		assert.Equal(t, "image", common.GetContextKeyString(c, constant.ContextKeyVideoEffectiveModality))
		assert.Equal(t, 1, common.GetContextKeyInt(c, constant.ContextKeyVideoCount))
		assert.Positive(t, common.GetContextKeyInt(c, constant.ContextKeyVideoExtractedFrames))
		c.Status(204)
	})
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	require.Equal(t, 204, w.Code, w.Body.String())
	require.True(t, called)
	_, err = installed.Bytes()
	assert.Error(t, err, "request cleanup closes installed storage")
	assert.Zero(t, p.Snapshot().RunningJobs)
	assert.Zero(t, p.Snapshot().ActiveFFmpeg)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
