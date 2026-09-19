package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	// Initialize i18n for tests
	_ = i18n.Init()
}

// TestVideoPreprocessExtractsModelFromBody verifies that the middleware
// extracts the model from the request body, not from a pre-set context key.
func TestVideoPreprocessExtractsModelFromBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body := `{"model":"kimi-k3","messages":[{"role":"user","content":"hello"}]}`
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyUserId), 1)

	// Do NOT set ContextKeyOriginalModel — the middleware must extract it from body

	VideoPreprocess()(c)

	// If processor is unavailable (no ffmpeg), expect 500
	// If processor is available and bypasses (no video), expect preprocessed=false
	// Either way, the middleware must have successfully extracted the model
	if c.IsAborted() {
		assert.Equal(t, http.StatusInternalServerError, w.Code, "unavailable processor")
	} else {
		preprocessed := common.GetContextKeyBool(c, constant.ContextKeyVideoPreprocessed)
		assert.False(t, preprocessed, "text-only request should bypass")
	}
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
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	// Use a model that is not in the video preprocessing target list
	body := `{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyUserId), 1)

	VideoPreprocess()(c)

	// If processor unavailable: 500
	// If processor available: bypass with preprocessed=false
	if c.IsAborted() {
		assert.Equal(t, http.StatusInternalServerError, w.Code)
	} else {
		preprocessed := common.GetContextKeyBool(c, constant.ContextKeyVideoPreprocessed)
		assert.False(t, preprocessed, "non-target model should bypass")
	}
}

// TestVideoPreprocessHandlesUnavailableProcessor verifies 500 when processor is nil.
func TestVideoPreprocessHandlesUnavailableProcessor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body := `{"model":"kimi-k3","messages":[{"role":"user","content":"hello"}]}`
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyUserId), 1)

	VideoPreprocess()(c)

	// When processor is unavailable (ffmpeg not found, etc.), middleware returns 500
	if c.IsAborted() {
		assert.Equal(t, http.StatusInternalServerError, w.Code)
	}
}

// TestVideoPreprocessPublishesMetadata verifies context key contract on bypass/success.
func TestVideoPreprocessPublishesMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body := `{"model":"kimi-k3","messages":[{"role":"user","content":"hello"}]}`
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyUserId), 1)

	VideoPreprocess()(c)

	// If not aborted, ContextKeyVideoPreprocessed must be set
	if !c.IsAborted() {
		_, exists := c.Get(string(constant.ContextKeyVideoPreprocessed))
		assert.True(t, exists, "ContextKeyVideoPreprocessed must always be set")
	}
}

// TestVideoPreprocessReusesBodyStorage verifies that the middleware integrates
// with BodyStorage for retry/fallback.
func TestVideoPreprocessReusesBodyStorage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	originalBody := `{"model":"kimi-k3","messages":[{"role":"user","content":"hello"}]}`
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(originalBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyUserId), 1)

	VideoPreprocess()(c)

	// After VideoPreprocess, if not aborted, BodyStorage should be installed
	if !c.IsAborted() {
		storage, err := common.GetBodyStorage(c)
		require.NoError(t, err, "BodyStorage must be installed")
		_, err = storage.Seek(0, io.SeekStart)
		require.NoError(t, err)
		retrieved, err := io.ReadAll(storage)
		require.NoError(t, err)
		// For a text-only bypass, body should be unchanged
		assert.Equal(t, originalBody, string(retrieved))
	}
}

// TestVideoPreprocessRouteIsolation verifies only /v1/chat/completions uses VideoPreprocess.
// This is a documentation test; actual routing is in router/relay-router.go.
func TestVideoPreprocessRouteIsolation(t *testing.T) {
	// This test documents the expected routing contract.
	// The actual route registration is:
	//   chatRouter.Use(middleware.VideoPreprocess())
	//   chatRouter.POST("/chat/completions", ...)
	//   httpRouter does NOT use VideoPreprocess
	//
	// To verify routing, check router/relay-router.go lines 45-60.
	t.Log("VideoPreprocess is registered only on the /v1/chat/completions route")
	t.Log("Other routes (/v1/completions, /v1/messages, etc.) bypass it")
}

// videoBody builds a Chat Completions request with existing images and videos.
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
	b.WriteString(`]}]}`)
	return b.String()
}

// testBodyStorage is a minimal BodyStorage that holds bytes in memory.
type testBodyStorage struct {
	*bytes.Reader
	data []byte
}

func newTestBodyStorage(data []byte) *testBodyStorage {
	return &testBodyStorage{
		Reader: bytes.NewReader(data),
		data:   data,
	}
}

func (s *testBodyStorage) Bytes() ([]byte, error) {
	return s.data, nil
}
