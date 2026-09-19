package router

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/ai_router"
	"github.com/QuantumNous/new-api/service/video_preprocess"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/video_preprocess_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModelsSupportsOpenAIAndGeminiAuthentication(t *testing.T) {
	setupRelayRouterTestDB(t)

	user := model.User{
		Username: "models-user",
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    100,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         user.Id,
		Key:            "modelstestkey",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	tests := []struct {
		name           string
		path           string
		headerName     string
		expectedObject string
		expectedField  string
	}{
		{
			name:           "OpenAI bearer token",
			path:           "/v1/models",
			headerName:     "Authorization",
			expectedObject: "list",
			expectedField:  "data",
		},
		{
			name:          "Gemini API key header",
			path:          "/v1/models",
			headerName:    "x-goog-api-key",
			expectedField: "models",
		},
		{
			name:          "Gemini API key query",
			path:          "/v1/models?key=modelstestkey",
			expectedField: "models",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.headerName != "" {
				value := "modelstestkey"
				if test.headerName == "Authorization" {
					value = "Bearer " + value
				}
				request.Header.Set(test.headerName, value)
			}

			engine.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code)
			var payload map[string]any
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.Contains(t, payload, test.expectedField)
			assert.NotContains(t, payload, "error")
			if test.expectedObject != "" {
				assert.Equal(t, test.expectedObject, payload["object"])
			}
		})
	}
}

func setupRelayRouterTestDB(t *testing.T) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	originalIsMasterNode := common.IsMasterNode
	originalRedisEnabled := common.RedisEnabled
	originalSQLitePath := common.SQLitePath
	originalMainDatabaseType := common.MainDatabaseType()
	originalLogDatabaseType := common.LogDatabaseType()
	originalSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")

	common.IsMasterNode = false
	common.RedisEnabled = false
	common.SQLitePath = fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	require.NoError(t, os.Setenv("SQL_DSN", "local"))
	require.NoError(t, model.InitDB())
	model.LOG_DB = model.DB
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Token{}, &model.Ability{}))

	t.Cleanup(func() {
		if sqlDB, err := model.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		common.IsMasterNode = originalIsMasterNode
		common.RedisEnabled = originalRedisEnabled
		common.SQLitePath = originalSQLitePath
		common.SetDatabaseTypes(originalMainDatabaseType, originalLogDatabaseType)
		if hadSQLDSN {
			require.NoError(t, os.Setenv("SQL_DSN", originalSQLDSN))
		} else {
			require.NoError(t, os.Unsetenv("SQL_DSN"))
		}
	})
}

// Uses the real router, authentication, distributor, Relay retry loops and
// OpenAI adapter against two local HTTP upstreams. No relay hooks are mocked.
func TestVideoRelayRetryAndAIRouterFallback(t *testing.T) {
	for _, aiRouter := range []bool{false, true} {
		t.Run(fmt.Sprintf("ai_router=%t", aiRouter), func(t *testing.T) {
			for _, binary := range []string{"ffmpeg", "ffprobe"} {
				if _, err := exec.LookPath(binary); err != nil {
					t.Skipf("required media binary unavailable: %s", binary)
				}
			}
			setupRelayRouterTestDB(t)
			require.NoError(t, i18n.Init())
			require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Log{}, &model.UserSubscription{}))
			previousCache, previousRetry := common.MemoryCacheEnabled, common.RetryTimes
			previousPrice := ratio_setting.ModelPrice2JSONString()
			previousPolicy := ai_router.GetTargetPolicySnapshot()
			previousRouter := ai_router_setting.GetSettings()
			previousLog := common.LogConsumeEnabled
			common.LogConsumeEnabled = false
			common.MemoryCacheEnabled, common.RetryTimes = true, 1
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"kimi-k3":0}`))
			require.NoError(t, ai_router_setting.Configure(map[string]string{"enabled": fmt.Sprint(aiRouter), "telemetry_enabled": "false", "max_attempts": "2"}))
			service.InitHttpClient()
			t.Cleanup(func() {
				common.MemoryCacheEnabled, common.RetryTimes, common.LogConsumeEnabled = previousCache, previousRetry, previousLog
				require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrice))
				ai_router.PublishTargetPolicySnapshot(previousPolicy)
				require.NoError(t, ai_router_setting.Configure(map[string]string{"enabled": fmt.Sprint(previousRouter.Enabled), "telemetry_enabled": fmt.Sprint(previousRouter.TelemetryEnabled), "max_attempts": fmt.Sprint(previousRouter.MaxAttempts)}))
			})
			p := video_preprocess.GetProcessor()
			require.NotNil(t, p)
			originalConfig := video_preprocess.BuildEngineConfig(video_preprocess_setting.GetSnapshot(), video_preprocess_setting.GetDeploymentConfig())
			t.Cleanup(func() { require.NoError(t, p.SetConfig(originalConfig)) })
			root := t.TempDir()
			settings := video_preprocess_setting.DefaultSettings()
			settings.Enabled = true
			settings.MinFramesPerVideo, settings.DefaultMaxFrames = 2, 2
			ffmpeg, err := exec.LookPath("ffmpeg")
			require.NoError(t, err)
			countFile := filepath.Join(root, "invocations")
			t.Setenv("VIDEO_TEST_FFMPEG", ffmpeg)
			t.Setenv("VIDEO_TEST_INVOCATIONS", countFile)
			wrapper := filepath.Join(root, "ffmpeg")
			require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf 'pass\\n' >> \"$VIDEO_TEST_INVOCATIONS\"\nexec \"$VIDEO_TEST_FFMPEG\" \"$@\"\n"), 0700))
			cfg := video_preprocess.BuildEngineConfig(video_preprocess_setting.Snapshot{Settings: settings}, video_preprocess_setting.DeploymentConfig{FFmpegPath: wrapper, FFprobePath: "ffprobe", TempDir: filepath.Join(root, "media")})
			require.NoError(t, p.SetConfig(cfg))
			videoPath := filepath.Join(root, "fixture.mp4")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=160x120:r=8:d=2", "-threads", "1", "-c:v", "libx264", videoPath).CombinedOutput()
			require.NoError(t, err, string(out))
			data, err := os.ReadFile(videoPath)
			require.NoError(t, err)
			body := `{"model":"kimi-k3","max_tokens":10,"messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"data:video/mp4;base64,` + base64.StdEncoding.EncodeToString(data) + `"}}]}]}`
			user := model.User{Username: "video-relay", Status: common.UserStatusEnabled, Group: "default", Quota: 1000000}
			require.NoError(t, model.DB.Create(&user).Error)
			require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "videorelaykey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
			var mu sync.Mutex
			var bodies [][]byte
			var channels []int
			var candidates []ai_router.TargetPolicyCandidate
			for index := range 2 {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					received, err := io.ReadAll(r.Body)
					assert.NoError(t, err)
					mu.Lock()
					bodies = append(bodies, received)
					channels = append(channels, index)
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					if index == 0 {
						w.WriteHeader(http.StatusTooManyRequests)
						_, _ = io.WriteString(w, `{"error":{"message":"retry another channel","type":"rate_limit_error","code":"rate_limit"}}`)
						return
					}
					_, _ = io.WriteString(w, `{"id":"chatcmpl-video","object":"chat.completion","model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","content":"frames received"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`)
				}))
				t.Cleanup(server.Close)
				channel := model.Channel{Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: fmt.Sprintf("video-%d", index), Key: "test", BaseURL: &server.URL, Models: "kimi-k3", Group: "default", Priority: common.GetPointer(int64(100 - index)), AutoBan: common.GetPointer(0)}
				channel.SetSetting(dto.ChannelSettings{PassThroughBodyEnabled: true})
				profile := dto.DefaultChannelRoutingProfile()
				profile.Enabled = true
				profile.SupportsImage = common.GetPointer(true)
				profile.SupportsVideo = common.GetPointer(false)
				channel.SetOtherSettings(dto.ChannelOtherSettings{Router: &profile})
				require.NoError(t, channel.Insert())
				candidates = append(candidates, ai_router.TargetPolicyCandidate{ChannelID: channel.Id, Enabled: true})
			}
			model.InitChannelCache()
			ai_router.PublishTargetPolicySnapshot(ai_router.BuildTargetPolicySnapshot([]ai_router.TargetPolicy{{Model: "kimi-k3", GroupId: "default", Enabled: true, MaxAttempts: 2, Candidates: candidates}}))
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				c.Next()
				assert.True(t, common.GetContextKeyBool(c, constant.ContextKeyVideoPreprocessed))
				assert.Equal(t, "video", common.GetContextKeyString(c, constant.ContextKeyVideoOriginalModality))
				assert.Equal(t, "image", common.GetContextKeyString(c, constant.ContextKeyVideoEffectiveModality))
				if aiRouter {
					route, ok := common.GetContextKey(c, constant.ContextKeyAIRouterContext)
					require.True(t, ok)
					assert.Equal(t, ai_router.ModalityImage, route.(*ai_router.RouteContext).Demand.Modality)
				}
			})
			SetRelayRouter(engine)
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer videorelaykey")
			req.Header.Set("Content-Type", "application/json")
			engine.ServeHTTP(w, req)
			require.Equal(t, 200, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), "frames received")
			assert.Contains(t, w.Body.String(), `"total_tokens":15`)
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, []int{0, 1}, channels)
			require.Len(t, bodies, 2)
			assert.Equal(t, bodies[0], bodies[1])
			assert.NotContains(t, string(bodies[0]), "video_url")
			assert.Contains(t, string(bodies[0]), "data:image/jpeg;base64,")
			invocations, err := os.ReadFile(countFile)
			require.NoError(t, err)
			assert.Equal(t, "pass\npass\n", string(invocations), "one analysis and one export across both relay attempts")
			assert.Zero(t, p.Snapshot().RunningJobs)
			assert.Zero(t, p.Snapshot().WaitingJobs)
			entries, err := os.ReadDir(cfg.Media.TempRoot)
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

func TestVideoPreprocessRouteIsolation(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, i18n.Init())
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}))
	previousCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousCache })
	previousRouter := ai_router_setting.GetSettings()
	require.NoError(t, ai_router_setting.Configure(map[string]string{"enabled": "false"}))
	t.Cleanup(func() {
		require.NoError(t, ai_router_setting.Configure(map[string]string{"enabled": fmt.Sprint(previousRouter.Enabled)}))
	})
	settings := video_preprocess_setting.DefaultSettings()
	settings.Enabled = true
	p := video_preprocess.GetProcessor()
	require.NotNil(t, p)
	original := video_preprocess.BuildEngineConfig(video_preprocess_setting.GetSnapshot(), video_preprocess_setting.GetDeploymentConfig())
	t.Cleanup(func() { require.NoError(t, p.SetConfig(original)) })
	require.NoError(t, p.SetConfig(video_preprocess.BuildEngineConfig(video_preprocess_setting.Snapshot{Settings: settings}, video_preprocess_setting.DeploymentConfig{TempDir: t.TempDir(), FFmpegPath: "/missing/ffmpeg", FFprobePath: "/missing/ffprobe"})))
	user := model.User{Username: "routeisolation", Status: common.UserStatusEnabled, Group: "default", Quota: 1000000}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{UserId: user.Id, Key: "isolationkey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}).Error)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/v1/chat/completions", 503},
		{"POST", "/v1/completions", 503},
		{"POST", "/v1/messages", 503},
		{"POST", "/v1/responses", 503},
		{"POST", "/v1/responses/compact", 503},
		{"POST", "/v1/images/generations", 503},
		{"POST", "/v1/images/edits", 503},
		{"POST", "/v1/audio/transcriptions", 503},
		{"POST", "/v1/audio/translations", 503},
		{"POST", "/v1/audio/speech", 503},
		{"POST", "/v1/embeddings", 503},
		{"GET", "/v1/realtime?model=kimi-k3", 503},
		{"GET", "/v1/responses", 400},
		{"POST", "/pg/chat/completions", 401},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			engine := gin.New()
			var handlers []string
			var visited bool
			engine.Use(func(c *gin.Context) {
				handlers = c.HandlerNames()
				c.Next()
				_, visited = c.Get(string(constant.ContextKeyVideoPreprocessed))
			})
			SetRelayRouter(engine)
			SetTaskPluginProtocolRouter(engine)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"model":"kimi-k3","messages":[{"role":"user","content":"hello"}]}`))
			req.Header.Set("Authorization", "Bearer isolationkey")
			req.Header.Set("Content-Type", "application/json")
			engine.ServeHTTP(w, req)
			assert.Equal(t, tc.status, w.Code, w.Body.String())
			isChat := tc.path == "/v1/chat/completions"
			assert.Equal(t, isChat, visited)
			videoIndex := -1
			for i, name := range handlers {
				if strings.Contains(name, ".VideoPreprocess.") {
					videoIndex = i
				}
			}
			if isChat {
				require.Positive(t, videoIndex)
				assert.Contains(t, handlers[videoIndex-1], ".ModelRequestRateLimit.")
				assert.Contains(t, handlers[videoIndex-2], ".TokenAuth.")
				assert.Contains(t, handlers[videoIndex-3], ".SystemPerformanceCheck.")
				assert.Contains(t, handlers[videoIndex+1], ".Distribute.")
			} else {
				assert.Equal(t, -1, videoIndex)
			}
			assert.Zero(t, p.Snapshot().RunningJobs)
			assert.Zero(t, p.Snapshot().WaitingJobs)
		})
	}
}
