package controller

import (
	"context"
	"fmt"
	"github.com/QuantumNous/new-api/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestVideoPreprocessLogAPIValidationAndPagination(t *testing.T) {
	db := setupVideoPreprocessControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.VideoPreprocessLog{}))
	now := time.Now().Unix()
	row := model.VideoPreprocessLog{RequestId: "video-api-test", Model: "kimi-k3", Status: "rejected", ErrorCode: "video_queue_full", CreatedAt: now - 1}
	require.NoError(t, model.InsertVideoPreprocessLog(context.Background(), &row))
	router := gin.New()
	router.GET("/logs", GetVideoPreprocessLogs)
	router.GET("/logs/:id", GetVideoPreprocessLogDetail)
	router.GET("/stats", GetVideoPreprocessStats)
	router.GET("/stats/trend", GetVideoPreprocessStats)
	router.GET("/stats/durations", GetVideoPreprocessStats)
	router.GET("/stats/errors", GetVideoPreprocessStats)
	for _, tc := range []struct {
		path   string
		status int
		field  string
	}{
		{"/logs?page_size=10000", 200, `"page_size":100`},
		{"/logs?status=rejected", 200, `"total":1`},
		{"/logs?status=succeeded", 200, `"total":0`},
		{"/logs?codec=h264", 200, `"total":0`},
		{"/logs?page=0", 400, ""},
		{"/logs?page_size=invalid", 400, ""},
		{"/logs?start_time=100&end_time=10", 400, ""},
		{"/logs?start_time=1&end_time=100000000", 400, ""},
		{"/logs?user_id=-1", 400, ""},
		{"/logs?status=unknown", 400, ""},
		{"/logs?codec=%25", 400, ""},
		{"/logs/not-a-number", 400, ""},
		{"/logs/999999", 404, ""},
		{fmt.Sprintf("/logs/%d", row.Id), 200, `"request_id":"video-api-test"`},
		{"/stats", 200, `"total_requests":1`},
		{"/stats/trend", 200, `"rejected":1`},
		{"/stats/durations", 200, `"queue"`},
		{"/stats/errors", 200, `"video_queue_full":1`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.field != "" {
				assert.Contains(t, w.Body.String(), tc.field)
			}
		})
	}
}
