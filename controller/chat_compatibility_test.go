package controller

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/ai_router"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChatCompatibilityHTTPRejectionRefundAndFallback(t *testing.T) {
	for _, router := range []bool{false, true} {
		for _, passthrough := range []bool{false, true} {
			t.Run(fmt.Sprintf("router=%t/passthrough=%t", router, passthrough), func(t *testing.T) {
				user, token := setupResponsesWSRequestTest(t)
				previousBatch, previousLogs, previousCount := common.BatchUpdateEnabled, common.LogConsumeEnabled, constant.CountToken
				previousRetry, previousErrors := common.RetryTimes, constant.ErrorLogEnabled
				previousRouter := ai_router_setting.GetSettings()
				previousRatios, err := common.Marshal(ratio_setting.GetModelRatioCopy())
				require.NoError(t, err)
				require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-4o":1}`))
				previousPolicies := ai_router.GetTargetPolicySnapshot()
				common.BatchUpdateEnabled, common.LogConsumeEnabled, constant.CountToken = false, true, false
				common.RetryTimes, constant.ErrorLogEnabled = 3, false
				ai_router.PublishTargetPolicySnapshot(nil)
				require.NoError(t, ai_router_setting.Configure(map[string]string{
					"enabled": strconv.FormatBool(router), "target_models": `["gpt-4o"]`,
					"max_attempts": "3", "telemetry_enabled": "false", "tool_filter_enabled": "false",
				}))
				t.Cleanup(func() {
					// Refund and quota-cache work are asynchronous. Join their actual
					// workers before restoring globals or closing the fixture database.
					require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond)
					common.BatchUpdateEnabled, common.LogConsumeEnabled, constant.CountToken = previousBatch, previousLogs, previousCount
					common.RetryTimes, constant.ErrorLogEnabled = previousRetry, previousErrors
					models, err := common.Marshal(previousRouter.TargetModels)
					require.NoError(t, err)
					require.NoError(t, ai_router_setting.Configure(map[string]string{
						"enabled": strconv.FormatBool(previousRouter.Enabled), "target_models": string(models),
						"max_attempts": strconv.Itoa(previousRouter.MaxAttempts), "telemetry_enabled": strconv.FormatBool(previousRouter.TelemetryEnabled),
						"tool_filter_enabled": strconv.FormatBool(previousRouter.ToolFilterEnabled),
					}))
					ai_router.PublishTargetPolicySnapshot(previousPolicies)
					require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(previousRatios)))
				})
				require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.Log{}))
				require.NoError(t, model.DB.Model(user).Updates(map[string]any{"quota": 100000, "setting": `{"billing_preference":"wallet_only"}`}).Error)
				require.NoError(t, model.DB.Model(token).Update("remain_quota", 3000).Error)
				var tokenWrites atomic.Int32
				require.NoError(t, model.DB.Callback().Update().After("gorm:commit_or_rollback_transaction").Register("chat-reservation", func(tx *gorm.DB) {
					if tx.Error == nil && tx.Statement.Table == "tokens" {
						tokenWrites.Add(1)
					}
				}))
				var calls atomic.Int32
				var failPrimary atomic.Bool
				bodies := make(chan map[string]any, 8)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					body, err := io.ReadAll(r.Body)
					if !assert.NoError(t, err) {
						return
					}
					var fields map[string]any
					if !assert.NoError(t, common.Unmarshal(body, &fields)) {
						return
					}
					bodies <- fields
					w.Header().Set("Content-Type", "application/json")
					if failPrimary.Load() && r.Header.Get("Authorization") == "Bearer primary-fixture" {
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = io.WriteString(w, `{"error":{"type":"server_error","message":"fixture unavailable"}}`)
						return
					}
					_, _ = io.WriteString(w, `{"id":"fixture","object":"chat.completion","model":"FW-Kimi-K3","choices":[{"index":0,"message":{"role":"assistant","content":"before停止after","reasoning_content":"reason停止unchanged"},"finish_reason":"length"}],"usage":{"prompt_tokens":20,"completion_tokens":30,"total_tokens":50}}`)
				}))
				t.Cleanup(upstream.Close)
				settings := dto.ChannelSettings{StrictChatToolMessages: true, K3ParameterValidation: true, GatewayVisibleContentStop: true, ChatCompatibilityModels: []string{"kimi-k3"}, PassThroughBodyEnabled: passthrough}
				// A pass-through channel actually sends the client model unchanged.
				if passthrough {
					settings.ChatCompatibilityModels = []string{"gpt-4o"}
				}
				channels := make([]*model.Channel, 2)
				for i := range channels {
					mapping := `{"gpt-4o":"kimi-k3"}`
					channel := &model.Channel{Type: constant.ChannelTypeOpenAI, Name: fmt.Sprint("compat-", i), Key: "fallback-fixture", Status: common.ChannelStatusEnabled, Group: "default", Models: "gpt-4o", BaseURL: &upstream.URL, ModelMapping: &mapping, Priority: common.GetPointer(int64(2 - i))}
					if i == 0 {
						channel.Key = "primary-fixture"
					}
					channel.SetSetting(settings)
					channel.SetOtherSettings(dto.ChannelOtherSettings{Router: &dto.ChannelRoutingProfile{Enabled: true, MaxRetry: 1}})
					require.NoError(t, channel.Insert())
					channels[i] = channel
				}
				var used []string
				var routed *ai_router.RouteContext
				engine := gin.New()
				engine.POST("/v1/chat/completions", middleware.TokenAuth(), middleware.Distribute(), func(c *gin.Context) {
					Relay(c, types.RelayFormatOpenAI)
					used = c.GetStringSlice("use_channel")
					routed, _ = common.GetContextKeyType[*ai_router.RouteContext](c, constant.ContextKeyAIRouterContext)
					common.CleanupBodyStorage(c)
				})
				for _, tc := range []struct {
					name, extra, messages string
					typedError            bool
				}{
					{name: "missing result", messages: `[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"lookup","arguments":"{}"}}]}]`},
					{name: "wrong result", messages: `[{"role":"assistant","tool_calls":[{"id":"a"}]},{"role":"tool","tool_call_id":"b","content":"ok"}]`},
					{name: "invalid temperature", messages: `[{"role":"user","content":"Hi"}]`, extra: `,"temperature":0`},
					{name: "typed temperature", messages: `[{"role":"user","content":"Hi"}]`, extra: `,"temperature":"1"`, typedError: true},
				} {
					t.Run(tc.name, func(t *testing.T) {
						tokenWrites.Store(0)
						request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","max_tokens":16,"messages":`+tc.messages+tc.extra+`}`))
						request.Header.Set("Authorization", "Bearer sk-"+token.Key)
						request.Header.Set("Content-Type", "application/json")
						recorder := httptest.NewRecorder()
						engine.ServeHTTP(recorder, request)
						require.Equal(t, 400, recorder.Code, recorder.Body.String())
						var response struct {
							Error types.OpenAIError `json:"error"`
						}
						require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
						assert.Equal(t, "invalid_request_error", response.Error.Type)
						assert.Zero(t, calls.Load())
						if tc.typedError {
							assert.Empty(t, used)
						} else {
							assert.Equal(t, []string{strconv.Itoa(channels[0].Id)}, used)
							if router {
								require.NotNil(t, routed)
								assert.Len(t, routed.AttemptRecords, 1)
								assert.False(t, routed.AttemptRecords[0].Retryable)
							}
						}
						require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond)
						if tc.typedError {
							assert.Zero(t, tokenWrites.Load())
						} else {
							assert.EqualValues(t, 2, tokenWrites.Load(), "one real reservation followed by one refund")
						}
						require.NoError(t, model.DB.First(token, token.Id).Error)
						require.NoError(t, model.DB.First(user, user.Id).Error)
						assert.Equal(t, 3000, token.RemainQuota)
						assert.Zero(t, token.UsedQuota)
						assert.Equal(t, 100000, user.Quota)
						var consumed int64
						require.NoError(t, model.DB.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&consumed).Error)
						assert.Zero(t, consumed)
					})
				}
				// The legal loop preserves dynamic system.tools, strips upstream
				// stop only on the enabled attempt, and accounts for the full output.
				request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","max_tokens":16,"stop":["停止"],"messages":[{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"system","tools":[{"type":"function","function":{"name":"lookup"}}]},{"role":"tool","tool_call_id":"a","content":"ok"}]}`))
				request.Header.Set("Authorization", "Bearer sk-"+token.Key)
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, request)
				require.Equal(t, 200, recorder.Code, recorder.Body.String())
				assert.Contains(t, recorder.Body.String(), `"content":"before"`)
				assert.Contains(t, recorder.Body.String(), `"finish_reason":"stop"`)
				assert.Contains(t, recorder.Body.String(), `"completion_tokens":30`)
				body := <-bodies
				assert.NotContains(t, body, "stop")
				assert.Contains(t, body["messages"].([]any)[1].(map[string]any), "tools")
				assert.NotContains(t, body["messages"].([]any)[1].(map[string]any), "content")
				if router {
					// Same-channel retries and fallback each rebuild from the client
					// body. A fallback with the switch off must still receive stop.
					settings.GatewayVisibleContentStop = false
					channels[1].SetSetting(settings)
					require.NoError(t, model.DB.Model(channels[1]).Update("setting", channels[1].Setting).Error)
					failPrimary.Store(true)
					request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","max_tokens":16,"stop":["停止"],"messages":[{"role":"user","content":"Hi"}]}`))
					request.Header.Set("Authorization", "Bearer sk-"+token.Key)
					request.Header.Set("Content-Type", "application/json")
					recorder = httptest.NewRecorder()
					engine.ServeHTTP(recorder, request)
					require.Equal(t, 200, recorder.Code, recorder.Body.String())
					require.Len(t, used, 3)
					assert.NotContains(t, <-bodies, "stop")
					assert.NotContains(t, <-bodies, "stop")
					assert.Equal(t, []any{"停止"}, (<-bodies)["stop"])
					assert.Contains(t, recorder.Body.String(), `"content":"before停止after"`)
				}
			})
		}
	}
}
