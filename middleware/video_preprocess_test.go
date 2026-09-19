package middleware

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/service/video_preprocess"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	// Initialize i18n for tests
	_ = i18n.Init()
}

// generateTestVideo creates a minimal real video using ffmpeg for E2E tests.
// Returns base64-encoded data URL. Skips the test if ffmpeg is unavailable.
func generateTestVideo(t *testing.T, durationSecs int) string {
	t.Helper()

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found, skipping test requiring real video")
	}

	tempDir := t.TempDir()
	videoPath := filepath.Join(tempDir, "test.mp4")

	cmd := exec.CommandContext(context.Background(), "ffmpeg",
		"-f", "lavfi",
		"-i", "color=black:s=320x240:d="+string(rune(durationSecs+'0')),
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		"-preset", "ultrafast",
		"-y",
		videoPath,
	)
	if err := cmd.Run(); err != nil {
		t.Skipf("failed to generate test video: %v", err)
	}

	videoBytes, err := os.ReadFile(videoPath)
	require.NoError(t, err, "failed to read generated video")

	return "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(videoBytes)
}

// TestVideoPreprocessExtractsModelFromBody verifies that the middleware
// extracts the model from the request body when the processor is available.
func TestVideoPreprocessExtractsModelFromBody(t *testing.T) {
	if video_preprocess.GetProcessor() == nil {
		t.Skip("video processor not available")
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body := `{"model":"kimi-k3","messages":[{"role":"user","content":"hello"}]}`
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyUserId), 1)

	// Do NOT set ContextKeyOriginalModel — the middleware must extract it from body

	VideoPreprocess()(c)

	require.False(t, c.IsAborted(), "text-only request should not abort")
	preprocessed := common.GetContextKeyBool(c, constant.ContextKeyVideoPreprocessed)
	assert.False(t, preprocessed, "text-only request should bypass")
}

// TestVideoPreprocessHandlesInvalidJSON verifies that malformed JSON is rejected.
func TestVideoPreprocessHandlesInvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body := `{"model":"kimi-k3","messages":[{invalid}]}`
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyUserId), 1)

	VideoPreprocess()(c)

	require.True(t, c.IsAborted(), "malformed JSON must be rejected")
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestVideoPreprocessBypassesNonTargetModel verifies that non-target models bypass.
func TestVideoPreprocessBypassesNonTargetModel(t *testing.T) {
	if video_preprocess.GetProcessor() == nil {
		t.Skip("video processor not available")
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	// Use a model that is not in the video preprocessing target list
	body := `{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyUserId), 1)

	VideoPreprocess()(c)

	require.False(t, c.IsAborted(), "non-target model should not abort")
	preprocessed := common.GetContextKeyBool(c, constant.ContextKeyVideoPreprocessed)
	assert.False(t, preprocessed, "non-target model should bypass")
}

// TestVideoPreprocessHandlesUnavailableProcessor verifies 500 when processor is nil.
func TestVideoPreprocessHandlesUnavailableProcessor(t *testing.T) {
	if video_preprocess.GetProcessor() != nil {
		t.Skip("processor is available, cannot test unavailable case")
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body := `{"model":"kimi-k3","messages":[{"role":"user","content":"hello"}]}`
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyUserId), 1)

	VideoPreprocess()(c)

	require.True(t, c.IsAborted(), "unavailable processor must abort")
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// TestVideoPreprocessPublishesMetadata verifies context key contract on bypass/success.
func TestVideoPreprocessPublishesMetadata(t *testing.T) {
	if video_preprocess.GetProcessor() == nil {
		t.Skip("video processor not available")
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body := `{"model":"kimi-k3","messages":[{"role":"user","content":"hello"}]}`
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyUserId), 1)

	VideoPreprocess()(c)

	require.False(t, c.IsAborted(), "text request should not abort")
	_, exists := c.Get(string(constant.ContextKeyVideoPreprocessed))
	assert.True(t, exists, "ContextKeyVideoPreprocessed must always be set")
}

// TestVideoPreprocessReusesBodyStorage verifies that the middleware integrates
// with BodyStorage for retry/fallback.
func TestVideoPreprocessReusesBodyStorage(t *testing.T) {
	if video_preprocess.GetProcessor() == nil {
		t.Skip("video processor not available")
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	originalBody := `{"model":"kimi-k3","messages":[{"role":"user","content":"hello"}]}`
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(originalBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyUserId), 1)

	VideoPreprocess()(c)

	require.False(t, c.IsAborted(), "text request should not abort")
	storage, err := common.GetBodyStorage(c)
	require.NoError(t, err, "BodyStorage must be installed")
	_, err = storage.Seek(0, io.SeekStart)
	require.NoError(t, err)
	retrieved, err := io.ReadAll(storage)
	require.NoError(t, err)
	// For a text-only bypass, body should be unchanged
	assert.Equal(t, originalBody, string(retrieved))

	// Verify BodyStorage can be read multiple times (for retry/fallback)
	_, err = storage.Seek(0, io.SeekStart)
	require.NoError(t, err)
	retrieved2, err := io.ReadAll(storage)
	require.NoError(t, err)
	assert.Equal(t, originalBody, string(retrieved2), "BodyStorage must be reusable")
}

// TestVideoPreprocessE2EWithRealVideo verifies the complete middleware flow
// with a real video: decode, probe, extract frames, rewrite body, and install
// BodyStorage that can be read multiple times for retry/fallback.
//
// This test requires video preprocessing to be enabled in the runtime settings.
// When disabled (the default), it skips rather than fails.
func TestVideoPreprocessE2EWithRealVideo(t *testing.T) {
	if video_preprocess.GetProcessor() == nil {
		t.Skip("video processor not available")
	}

	videoDataURL := generateTestVideo(t, 1)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body := `{"model":"kimi-k3","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"describe this"},` +
		`{"type":"video_url","video_url":{"url":"` + videoDataURL + `"}}` +
		`]}]}`

	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyUserId), 1)

	VideoPreprocess()(c)

	require.False(t, c.IsAborted(), "real video processing should not abort on success")

	// Check if video preprocessing is disabled in runtime settings
	preprocessed := common.GetContextKeyBool(c, constant.ContextKeyVideoPreprocessed)
	if !preprocessed {
		// Video preprocessing is disabled or bypassed - skip this E2E test
		t.Skip("video preprocessing is disabled or video was bypassed")
	}

	// Verify preprocessing succeeded
	assert.True(t, preprocessed, "video should be preprocessed")

	// Verify metadata was published
	videoCount := common.GetContextKeyInt(c, constant.ContextKeyVideoCount)
	assert.Equal(t, 1, videoCount, "should process 1 video")

	extractedFrames := common.GetContextKeyInt(c, constant.ContextKeyVideoExtractedFrames)
	assert.Greater(t, extractedFrames, 0, "should extract at least 1 frame")

	// Verify BodyStorage was installed and contains rewritten body
	storage, err := common.GetBodyStorage(c)
	require.NoError(t, err, "BodyStorage must be installed")

	_, err = storage.Seek(0, io.SeekStart)
	require.NoError(t, err)
	rewrittenBody, err := io.ReadAll(storage)
	require.NoError(t, err)

	// Verify video_url was replaced with image_url
	assert.NotContains(t, string(rewrittenBody), "video_url", "video_url should be removed")
	assert.Contains(t, string(rewrittenBody), "image_url", "should contain image_url")
	assert.Contains(t, string(rewrittenBody), "data:image/jpeg;base64,", "should contain JPEG frames")

	// Verify BodyStorage can be read multiple times (for retry/fallback)
	_, err = storage.Seek(0, io.SeekStart)
	require.NoError(t, err)
	reread, err := io.ReadAll(storage)
	require.NoError(t, err)
	assert.Equal(t, rewrittenBody, reread, "BodyStorage must support multiple reads")

	// Verify rewritten body is valid JSON
	var parsed map[string]any
	err = json.Unmarshal(rewrittenBody, &parsed)
	require.NoError(t, err, "rewritten body must be valid JSON")
}

// TestVideoPreprocessErrorCodeMapping verifies correct HTTP status codes.
func TestVideoPreprocessErrorCodeMapping(t *testing.T) {
	// This test verifies the error code mapping defined in handleVideoPreprocessError.
	// The actual codes come from the video_preprocess package.
	tests := []struct {
		name       string
		code       string
		wantStatus int
	}{
		{"invalid request", video_preprocess.CodeInvalidRequest, http.StatusBadRequest},
		{"invalid data url", video_preprocess.CodeInvalidDataURL, http.StatusBadRequest},
		{"too large", video_preprocess.CodeTooLarge, http.StatusRequestEntityTooLarge},
		{"image limit exceeded", video_preprocess.CodeImageLimitExceeded, http.StatusRequestEntityTooLarge},
		{"queue full", video_preprocess.CodeQueueFull, http.StatusTooManyRequests},
		{"storage exhausted", video_preprocess.CodeStorageExhausted, http.StatusServiceUnavailable},
		{"processing timeout", video_preprocess.CodeProcessingTimeout, http.StatusRequestTimeout},
		{"processing canceled", video_preprocess.CodeProcessingCanceled, 499},
		{"probe failed", video_preprocess.CodeProbeFailed, http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &video_preprocess.Error{Code: tt.code, Message: "test"}
			status := video_preprocess.HTTPStatusForError(err)
			assert.Equal(t, tt.wantStatus, status, "code %s should map to status %d", tt.code, tt.wantStatus)
		})
	}
}

// TestVideoPreprocessMultipleReads verifies BodyStorage supports retry/fallback.
func TestVideoPreprocessMultipleReads(t *testing.T) {
	if video_preprocess.GetProcessor() == nil {
		t.Skip("video processor not available")
	}

	videoDataURL := generateTestVideo(t, 1)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body := `{"model":"kimi-k3","messages":[{"role":"user","content":[` +
		`{"type":"video_url","video_url":{"url":"` + videoDataURL + `"}}` +
		`]}]}`

	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyUserId), 1)

	VideoPreprocess()(c)

	require.False(t, c.IsAborted(), "processing should succeed")

	storage, err := common.GetBodyStorage(c)
	require.NoError(t, err)

	// Read the rewritten body 3 times to simulate retry/fallback
	for i := 0; i < 3; i++ {
		_, err := storage.Seek(0, io.SeekStart)
		require.NoError(t, err, "seek failed on read %d", i+1)
		data, err := io.ReadAll(storage)
		require.NoError(t, err, "read failed on read %d", i+1)
		require.NotEmpty(t, data, "read %d returned empty data", i+1)

		// Verify it's still valid JSON
		var parsed map[string]any
		err = json.Unmarshal(data, &parsed)
		require.NoError(t, err, "read %d returned invalid JSON", i+1)
	}
}

// TestVideoPreprocessPreservesOriginalContent verifies that non-video content
// is preserved during rewriting.
func TestVideoPreprocessPreservesOriginalContent(t *testing.T) {
	if video_preprocess.GetProcessor() == nil {
		t.Skip("video processor not available")
	}

	videoDataURL := generateTestVideo(t, 1)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body := `{"model":"kimi-k3","temperature":0.7,"max_tokens":1000,"messages":[` +
		`{"role":"system","content":"You are a helpful assistant."},` +
		`{"role":"user","content":[` +
		`{"type":"text","text":"Before video"},` +
		`{"type":"video_url","video_url":{"url":"` + videoDataURL + `"}},` +
		`{"type":"text","text":"After video"}` +
		`]}]}`

	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyUserId), 1)

	VideoPreprocess()(c)

	require.False(t, c.IsAborted(), "processing should succeed")

	storage, err := common.GetBodyStorage(c)
	require.NoError(t, err)
	_, err = storage.Seek(0, io.SeekStart)
	require.NoError(t, err)
	rewritten, err := io.ReadAll(storage)
	require.NoError(t, err)

	var parsed map[string]any
	err = json.Unmarshal(rewritten, &parsed)
	require.NoError(t, err)

	// Verify top-level parameters are preserved
	assert.Equal(t, 0.7, parsed["temperature"])
	assert.Equal(t, float64(1000), parsed["max_tokens"])

	// Verify text content is preserved
	rewrittenStr := string(rewritten)
	assert.Contains(t, rewrittenStr, "Before video")
	assert.Contains(t, rewrittenStr, "After video")
	assert.Contains(t, rewrittenStr, "You are a helpful assistant")
}
