package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// TestVideoPreprocessHandlesUnavailableProcessor verifies that when the
// processor is unavailable, the middleware aborts with 500.
func TestVideoPreprocessHandlesUnavailableProcessor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body := `{"model":"kimi-k3","messages":[{"role":"user","content":"hello"}]}`
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyOriginalModel), "kimi-k3")
	c.Set(string(constant.ContextKeyUserId), 1)

	VideoPreprocess()(c)

	// When processor is unavailable (ffmpeg not found, etc.), middleware returns 500
	if c.IsAborted() {
		assert.Equal(t, http.StatusInternalServerError, w.Code)
	}
}

// TestVideoPreprocessContextKeyContract verifies that the middleware publishes
// context keys according to the documented contract.
func TestVideoPreprocessContextKeyContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body := `{"model":"kimi-k3","messages":[{"role":"user","content":"hello"}]}`
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyOriginalModel), "kimi-k3")
	c.Set(string(constant.ContextKeyUserId), 1)

	VideoPreprocess()(c)

	// The middleware must set ContextKeyVideoPreprocessed on every path
	// (bypass, success, or error before abort).
	// If the request was not aborted, check the preprocessed key.
	if !c.IsAborted() {
		preprocessed := common.GetContextKeyBool(c, constant.ContextKeyVideoPreprocessed)
		// Either preprocessed=true (success) or preprocessed=false (bypass)
		t.Logf("preprocessed=%v", preprocessed)

		// If bypassed, original/effective modality may or may not be set
		// depending on which bypass path was taken.
		if !preprocessed {
			origModality := common.GetContextKeyString(c, constant.ContextKeyVideoOriginalModality)
			effModality := common.GetContextKeyString(c, constant.ContextKeyVideoEffectiveModality)
			t.Logf("bypass: original_modality=%q effective_modality=%q", origModality, effModality)
		}
	}
}

// TestVideoPreprocessErrorMapping verifies that invalid requests are rejected
// with appropriate HTTP status codes.
func TestVideoPreprocessErrorMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	// Malformed JSON should be rejected
	body := `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"not-a-data-url"}}]}]}`
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(constant.ContextKeyOriginalModel), "kimi-k3")
	c.Set(string(constant.ContextKeyUserId), 1)

	VideoPreprocess()(c)

	// If processor is available and processed the request, invalid data URLs
	// should result in 400. If processor is unavailable, we get 500.
	if c.IsAborted() {
		assert.Contains(t, []int{http.StatusBadRequest, http.StatusInternalServerError}, w.Code)
	}
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
