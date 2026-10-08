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
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service/ai_router"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestStrictK3IngressAndChannelCompatibilityHTTP(t *testing.T) {
	require.NoError(t, i18n.Init())
	user, token := setupResponsesWSRequestTest(t)
	previousSettings, previousPolicies := ai_router_setting.GetSettings(), ai_router.GetTargetPolicySnapshot()
	previousRatio, err := common.Marshal(ratio_setting.GetModelRatioCopy())
	require.NoError(t, err)
	previousCount, previousLog := constant.CountToken, common.LogConsumeEnabled
	previousStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 60
	constant.CountToken, common.LogConsumeEnabled = false, false
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"kimi-k3":1}`))
	require.NoError(t, ai_router_setting.Configure(map[string]string{"enabled": "true", "target_models": `["kimi-k3"]`, "telemetry_enabled": "false", "tool_filter_enabled": "false", "unknown_model_400_groups": `["default"]`}))
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond)
		constant.CountToken, common.LogConsumeEnabled = previousCount, previousLog
		constant.StreamingTimeout = previousStreamingTimeout
		models, err := common.Marshal(previousSettings.TargetModels)
		require.NoError(t, err)
		groups, err := common.Marshal(previousSettings.UnknownModel400Groups)
		require.NoError(t, err)
		require.NoError(t, ai_router_setting.Configure(map[string]string{"enabled": strconv.FormatBool(previousSettings.Enabled), "target_models": string(models), "telemetry_enabled": strconv.FormatBool(previousSettings.TelemetryEnabled), "tool_filter_enabled": strconv.FormatBool(previousSettings.ToolFilterEnabled), "unknown_model_400_groups": string(groups)}))
		ai_router.PublishTargetPolicySnapshot(previousPolicies)
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(previousRatio)))
	})
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.Log{}))
	require.NoError(t, model.DB.Model(user).Updates(map[string]any{"quota": 100000, "setting": `{"billing_preference":"wallet_only"}`}).Error)
	require.NoError(t, model.DB.Model(token).Update("remain_quota", 3000).Error)
	var calls atomic.Int32
	var downstream atomic.Int32
	var failUpstream atomic.Bool
	var sent map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}
		if !assert.NoError(t, common.Unmarshal(body, &sent)) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if failUpstream.Load() {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"fixture rejection"}}`)
			return
		}
		if sent["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"FW-Kimi-K3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixture\",\"model\":\"FW-Kimi-K3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n")
		} else {
			_, _ = io.WriteString(w, `{"id":"fixture","model":"FW-Kimi-K3","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
		}
	}))
	t.Cleanup(upstream.Close)
	channel := &model.Channel{Type: constant.ChannelTypeOpenAI, Name: "strict-fixture", Key: "mock-only", Status: common.ChannelStatusEnabled, Group: "default", Models: "kimi-k3", BaseURL: &upstream.URL}
	channel.SetOtherSettings(dto.ChannelOtherSettings{Router: &dto.ChannelRoutingProfile{Enabled: true}, ResponseModelAlias: &dto.ChannelResponseModelAlias{Mode: "channel", Models: []string{"kimi-k3"}}})
	require.NoError(t, channel.Insert())
	// A known model unavailable in this group must not be reported nonexistent.
	require.NoError(t, model.DB.Create(&model.Channel{Type: constant.ChannelTypeOpenAI, Name: "disabled-fixture", Key: "mock-only", Status: common.ChannelStatusManuallyDisabled, Group: "vip", Models: "known-disabled"}).Error)
	common.MemoryCacheEnabled = true
	model.InitChannelCache()
	engine := gin.New()
	engine.Use(middleware.BodyStorageCleanup())
	engine.POST("/v1/chat/completions", middleware.TokenAuth(), middleware.AIRouterResponseBudget(), func(c *gin.Context) { downstream.Add(1); c.Next() }, middleware.Distribute(), func(c *gin.Context) { Relay(c, types.RelayFormatOpenAI) })
	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer sk-"+token.Key)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}
	publish := func(strict bool) {
		ai_router.PublishTargetPolicySnapshot(ai_router.BuildTargetPolicySnapshot([]ai_router.TargetPolicy{{Model: "kimi-k3", GroupId: "default", Enabled: true, MaxAttempts: 2, K3StrictValidation: strict, Candidates: []ai_router.TargetPolicyCandidate{{ChannelID: channel.Id, Enabled: true}}}}))
	}
	publish(true)
	// Authentication and model authorization keep precedence over validation.
	unauthenticated := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"kimi-k3","messages":[]}`))
	unauthorized := httptest.NewRecorder()
	engine.ServeHTTP(unauthorized, unauthenticated)
	assert.Equal(t, http.StatusUnauthorized, unauthorized.Code)
	require.NoError(t, model.DB.Model(token).Updates(map[string]any{"model_limits_enabled": true, "model_limits": "other"}).Error)
	denied := request(`{"model":"kimi-k3","messages":[]}`)
	assert.Equal(t, http.StatusForbidden, denied.Code, denied.Body.String())
	require.NoError(t, model.DB.Model(token).Update("model_limits_enabled", false).Error)
	downstream.Store(0)
	malformed := request(`{"model":"kimi-k3","messages":[{"role":"user","content":"Hi"}]} {}`)
	assert.Equal(t, http.StatusBadRequest, malformed.Code, malformed.Body.String())
	assert.Contains(t, malformed.Body.String(), `"code":"invalid_request"`)
	assert.Zero(t, calls.Load())
	assert.Zero(t, downstream.Load())
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct{ fields, messages, param string }{
			{messages: `[]`, param: "messages"},
			{messages: `[{"content":"Hi"}]`, param: "messages[0].role"},
			{messages: `[{"role":"badrole"}]`, param: "messages[0].role"},
			{fields: `,"thinking":{"type":"disabled"}`, param: "thinking.type"},
			{fields: `,"enable_thinking":false`, param: "enable_thinking"},
			{fields: `,"tools":[{"bad":"x"}]`, param: "tools[0].type"},
			{fields: `,"max_tokens":0`, param: "max_tokens"},
			{fields: `,"temperature":"1"`, param: "temperature"},
			{fields: `,"model":"other"`, param: "model"},
		} {
			messages := tc.messages
			if messages == "" {
				messages = `[{"role":"user","content":"Hi"}]`
			}
			w := request(fmt.Sprintf(`{"model":"kimi-k3","stream":%t,"messages":%s%s}`, stream, messages, tc.fields))
			require.Equal(t, 400, w.Code, w.Body.String())
			assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
			var response struct{ Error types.OpenAIError }
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &response))
			assert.Equal(t, tc.param, response.Error.Param)
			assert.Equal(t, "invalid_request", response.Error.Code)
			assert.Equal(t, "invalid_request_error", response.Error.Type)
			assert.NotEmpty(t, response.Error.Message)
			assert.Zero(t, calls.Load())
			assert.Zero(t, downstream.Load(), "invalid requests must not reach media admission or channel selection")
		}
	}
	var afterInvalid model.Token
	require.NoError(t, model.DB.First(&afterInvalid, token.Id).Error)
	assert.Equal(t, 3000, afterInvalid.RemainQuota)
	assert.Zero(t, afterInvalid.UsedQuota)
	for _, name := range []string{"nonexistent-fixture", "known-disabled"} {
		w := request(`{"model":"` + name + `","messages":[{"role":"user","content":"Hi"}]}`)
		if name == "known-disabled" {
			assert.Equal(t, 503, w.Code, w.Body.String())
		} else {
			assert.Equal(t, 400, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), `"code":"model_not_found"`)
		}
	}
	for _, compat := range []bool{false, true} {
		channel.SetSetting(dto.ChannelSettings{K3ParameterValidation: true, K3ReasoningCompatibility: compat, K3ToolChoiceCompatibility: compat, ChatCompatibilityModels: []string{"kimi-k3"}})
		require.NoError(t, model.DB.Model(channel).Update("setting", channel.Setting).Error)
		model.InitChannelCache()
		for _, stream := range []bool{false, true} {
			w := request(fmt.Sprintf(`{"model":"kimi-k3","stream":%t,"max_tokens":16,"reasoning_effort":"medium","tool_choice":"any","tools":[{"type":"function","function":{"name":"weather","parameters":{}}}],"messages":[{"role":"user","content":"Hi"}]}`, stream))
			if !compat {
				assert.Equal(t, 400, w.Code, w.Body.String())
				assert.Zero(t, calls.Load())
				continue
			}
			require.Equal(t, 200, w.Code, w.Body.String())
			assert.Equal(t, "high", sent["reasoning_effort"])
			assert.Equal(t, "required", sent["tool_choice"])
			assert.Contains(t, w.Body.String(), `"model":"kimi-k3"`)
			assert.Contains(t, w.Body.String(), `"total_tokens":3`)
			if stream {
				assert.Contains(t, w.Body.String(), "data: [DONE]")
			}
		}
	}
	configureTokenAutoGroupsTest(t, "5", `["default","vip"]`)
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP","auto":"Auto"}`))
	require.NoError(t, model.DB.Model(channel).Update("group", "default,vip").Error)
	channel.Group = "default,vip"
	require.NoError(t, channel.UpdateAbilities(nil))
	model.InitChannelCache()
	for _, tc := range []struct {
		group, autoGroups string
		status            int
	}{
		{group: "default", status: 400},
		{group: "vip", status: 200},
		{group: "auto", autoGroups: `["default","vip"]`, status: 400},
		{group: "auto", autoGroups: `["vip","default"]`, status: 200},
	} {
		if tc.autoGroups != "" {
			require.NoError(t, setting.UpdateAutoGroupsByJsonString(tc.autoGroups))
		}
		require.NoError(t, model.DB.Model(token).Update("group", tc.group).Error)
		w := request(`{"model":"kimi-k3","messages":[{"role":"user","content":"Hi"}],"thinking":{"type":"disabled"}}`)
		assert.Equal(t, tc.status, w.Code, w.Body.String())
	}
	require.NoError(t, model.DB.Model(token).Update("group", "").Error)
	// Disabling the new contract preserves the old permissive thinking path.
	publish(false)
	w := request(`{"model":"kimi-k3","messages":[{"role":"user","content":"Hi"}],"thinking":{"type":"disabled"}}`)
	assert.Equal(t, 200, w.Code, w.Body.String())
	// An upstream rejection before any SSE event is ordinary JSON, not SSE.
	failUpstream.Store(true)
	w = request(`{"model":"kimi-k3","stream":true,"messages":[{"role":"user","content":"Hi"}]}`)
	assert.Equal(t, 400, w.Code, w.Body.String())
	assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
	require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 5*time.Second, time.Millisecond)
}

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
