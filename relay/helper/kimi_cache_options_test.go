package helper

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Kimi's official API rejects invalid cache-write options with a 400 naming
// the field; some compatible upstreams silently fall back to 5m instead.
func TestKimiCacheWriteOptionsValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newCtx := func(body string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/relay", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		return c
	}
	chat := func(model, opts string) func() error {
		return func() error {
			_, err := GetAndValidateTextRequest(newCtx(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]`+opts+`}`), relayconstant.RelayModeChatCompletions)
			return err
		}
	}
	responses := func(opts string) func() error {
		return func() error {
			_, err := GetAndValidateResponsesRequest(newCtx(`{"model":"kimi-k3.1-pro","input":"hi"` + opts + `}`))
			return err
		}
	}
	messages := func(opts string) func() error {
		return func() error {
			_, err := GetAndValidateClaudeRequest(newCtx(`{"model":"kimi-k3.1-pro","max_tokens":8,"messages":[{"role":"user","content":"hi"}]` + opts + `}`))
			return err
		}
	}

	tests := []struct {
		name      string
		run       func() error
		wantParam string
	}{
		{name: "chat official 1h", run: chat("kimi-k3.1-pro", `,"prompt_cache_options":{"mode":"implicit","ttl":"1h"}`)},
		{name: "chat explicit 5m", run: chat("kimi-k3.1-pro", `,"prompt_cache_options":{"mode":"explicit","ttl":"5m"}`)},
		{name: "chat ttl only", run: chat("kimi-k3.1-pro", `,"prompt_cache_options":{"ttl":"1h"}`)},
		{name: "chat without options", run: chat("kimi-k3.1-pro", ``)},
		{name: "chat invalid mode", run: chat("kimi-k3.1-pro", `,"prompt_cache_options":{"mode":"ephemeral","ttl":"1h"}`), wantParam: "prompt_cache_options.mode"},
		{name: "chat invalid ttl", run: chat("kimi-k3.1-pro", `,"prompt_cache_options":{"mode":"implicit","ttl":"30M"}`), wantParam: "prompt_cache_options.ttl"},
		{name: "chat non-string ttl", run: chat("kimi-k3.1-pro", `,"prompt_cache_options":{"ttl":3600}`), wantParam: "prompt_cache_options.ttl"},
		{name: "chat non-object options", run: chat("kimi-k3.1-pro", `,"prompt_cache_options":"1h"`), wantParam: "prompt_cache_options"},
		{name: "non-kimi model is not validated", run: chat("gpt-4o", `,"prompt_cache_options":{"mode":"ephemeral","ttl":"30M"}`)},
		{name: "responses official 1h", run: responses(`,"prompt_cache_options":{"mode":"implicit","ttl":"1h"}`)},
		{name: "responses invalid ttl", run: responses(`,"prompt_cache_options":{"mode":"implicit","ttl":"30M"}`), wantParam: "prompt_cache_options.ttl"},
		{name: "messages official 1h", run: messages(`,"cache_control":{"type":"ephemeral","ttl":"1h"}`)},
		{name: "messages invalid ttl", run: messages(`,"cache_control":{"type":"ephemeral","ttl":"30M"}`), wantParam: "cache_control.ttl"},
		{name: "messages invalid type", run: messages(`,"cache_control":{"type":"implicit","ttl":"1h"}`), wantParam: "cache_control.type"},
		{name: "messages prompt_cache_options implicit 1h", run: messages(`,"prompt_cache_options":{"mode":"implicit","ttl":"1h"}`)},
		{name: "messages prompt_cache_options ephemeral 5m", run: messages(`,"prompt_cache_options":{"mode":"ephemeral","ttl":"5m"}`)},
		{name: "messages prompt_cache_options invalid mode", run: messages(`,"prompt_cache_options":{"mode":"readonly","ttl":"1h"}`), wantParam: "prompt_cache_options.mode"},
		{name: "messages prompt_cache_options invalid ttl", run: messages(`,"prompt_cache_options":{"mode":"implicit","ttl":"30M"}`), wantParam: "prompt_cache_options.ttl"},
		{name: "chat still rejects ephemeral mode", run: chat("kimi-k3.1-pro", `,"prompt_cache_options":{"mode":"ephemeral","ttl":"5m"}`), wantParam: "prompt_cache_options.mode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if tt.wantParam == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			assert.Equal(t, tt.wantParam, apiErr.ToOpenAIError().Param)
			assert.Contains(t, err.Error(), tt.wantParam)
		})
	}
}

// Messages upstreams only honor the top-level cache_control, so a valid
// prompt_cache_options must be mapped onto it for the requested TTL to apply.
func TestKimiMessagesPromptCacheOptionsMapsToCacheControl(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name             string
		model            string
		extra            string
		wantCacheControl string
	}{
		{name: "1h ttl maps to cache_control", model: "kimi-k3.1-pro", extra: `,"prompt_cache_options":{"mode":"implicit","ttl":"1h"}`, wantCacheControl: `{"ttl":"1h","type":"ephemeral"}`},
		{name: "5m ttl maps to cache_control", model: "kimi-k3.1-pro", extra: `,"prompt_cache_options":{"mode":"ephemeral","ttl":"5m"}`, wantCacheControl: `{"ttl":"5m","type":"ephemeral"}`},
		{name: "no ttl keeps upstream default", model: "kimi-k3.1-pro", extra: `,"prompt_cache_options":{"mode":"implicit"}`},
		{name: "explicit cache_control wins", model: "kimi-k3.1-pro", extra: `,"cache_control":{"type":"ephemeral","ttl":"5m"},"prompt_cache_options":{"mode":"implicit","ttl":"1h"}`, wantCacheControl: `{"type":"ephemeral","ttl":"5m"}`},
		{name: "non-kimi model is untouched", model: "claude-sonnet-4", extra: `,"prompt_cache_options":{"mode":"implicit","ttl":"1h"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			body := `{"model":"` + tt.model + `","max_tokens":8,"messages":[{"role":"user","content":"hi"}]` + tt.extra + `}`
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(body))
			c.Request.Header.Set("Content-Type", "application/json")

			req, err := GetAndValidateClaudeRequest(c)

			require.NoError(t, err)
			if tt.wantCacheControl == "" {
				assert.Empty(t, req.CacheControl)
				return
			}
			assert.JSONEq(t, tt.wantCacheControl, string(req.CacheControl))
		})
	}
}
