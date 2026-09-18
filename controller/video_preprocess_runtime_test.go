package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetVideoPreprocessRuntimeShape locks the wire contract of the runtime
// endpoint: live counters under "queue", the config generation and the binary
// health flags — and no filesystem path of any kind.
func TestGetVideoPreprocessRuntimeShape(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/ai-router/video/runtime", nil)

	GetVideoPreprocessRuntime(c)

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	for _, key := range []string{
		`"success":true`,
		`"available"`,
		`"queue"`,
		`"running_jobs"`,
		`"waiting_jobs"`,
		`"active_ffmpeg"`,
		`"temp_storage_bytes"`,
		`"oldest_wait_ms"`,
		`"config_version"`,
		`"ffmpeg_available"`,
		`"ffprobe_available"`,
	} {
		assert.True(t, strings.Contains(body, key), "runtime payload must contain %s, got: %s", key, body)
	}
	assert.NotContains(t, body, string("/"), "no filesystem path may leak into the runtime payload")
}
