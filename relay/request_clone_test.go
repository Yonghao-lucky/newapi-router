package relay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/jinzhu/copier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChatCompatibilityToolTurns(t *testing.T) {
	for _, tc := range []struct {
		name, messages string
		valid          bool
	}{
		{"text only", `[{"role":"user","content":"hello"}]`, true},
		{"missing result", `[{"role":"assistant","tool_calls":[{"id":"a"}]}]`, false},
		{"unknown result", `[{"role":"assistant","tool_calls":[{"id":"a"}]},{"role":"tool","tool_call_id":"b"}]`, false},
		{"orphan result", `[{"role":"tool","tool_call_id":"a"}]`, false},
		{"duplicate result", `[{"role":"assistant","tool_calls":[{"id":"a"}]},{"role":"tool","tool_call_id":"a"},{"role":"tool","tool_call_id":"a"}]`, false},
		{"parallel incomplete", `[{"role":"assistant","tool_calls":[{"id":"a"},{"id":"b"}]},{"role":"tool","tool_call_id":"a"}]`, false},
		{"parallel unordered", `[{"role":"assistant","tool_calls":[{"id":"a"},{"id":"b"}]},{"role":"tool","tool_call_id":"b"},{"role":"tool","tool_call_id":"a"}]`, true},
		{"dynamic tools between results", `[{"role":"assistant","tool_calls":[{"id":"a"},{"id":"b"}]},{"role":"tool","tool_call_id":"b"},{"role":"system","tools":[{"type":"function","function":{"name":"next"}}]},{"role":"tool","tool_call_id":"a"}]`, true},
		{"multi round", `[{"role":"assistant","tool_calls":[{"id":"a"}]},{"role":"tool","tool_call_id":"a"},{"role":"assistant","content":"done"},{"role":"user","content":"again"},{"role":"assistant","tool_calls":[{"id":"a"}]},{"role":"tool","tool_call_id":"a"}]`, true},
		{"result across intervening turn", `[{"role":"assistant","tool_calls":[{"id":"a"}]},{"role":"user","content":"again"},{"role":"tool","tool_call_id":"a"}]`, false},
		{"duplicate call IDs", `[{"role":"assistant","tool_calls":[{"id":"a"},{"id":"a"}]},{"role":"tool","tool_call_id":"a"}]`, false},
		{"continuation without pending tools", `[{"role":"user","content":"continue"},{"role":"assistant","content":"The answer is","prefix":true}]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateChatToolMessages(json.RawMessage(tc.messages))
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestChatCompatibilityK3Parameters(t *testing.T) {
	for _, tc := range []struct {
		body, invalid string
	}{
		{`{}`, ""},
		{`{"temperature":1,"top_p":0.95,"n":1,"presence_penalty":0,"frequency_penalty":0,"reasoning_effort":"max"}`, ""},
		{`{"temperature":1.0,"reasoning_effort":"low"}`, ""},
		{`{"temperature":10e-1,"top_p":950e-3,"presence_penalty":-0.0}`, ""},
		{`{"reasoning_effort":"high"}`, ""},
		{`{"temperature":null,"top_p":null,"n":null,"presence_penalty":null,"frequency_penalty":null,"reasoning_effort":null}`, ""},
		{`{"temperature":0}`, "temperature"},
		{`{"temperature":1.0000000000000001}`, "temperature"},
		{`{"temperature":"1"}`, "temperature"},
		{`{"temperature":true}`, "temperature"},
		{`{"temperature":{}}`, "temperature"},
		{`{"temperature":[]}`, "temperature"},
		{`{"top_p":0}`, "top_p"},
		{`{"top_p":1}`, "top_p"},
		{`{"top_p":0.95000000000000001}`, "top_p"},
		{`{"n":0}`, "n"},
		{`{"n":2}`, "n"},
		{`{"n":1.0}`, "n"},
		{`{"presence_penalty":1}`, "presence_penalty"},
		{`{"presence_penalty":1e-2147483648}`, "presence_penalty"},
		{`{"frequency_penalty":-1}`, "frequency_penalty"},
		{`{"reasoning_effort":"medium"}`, "reasoning_effort"},
		{`{"reasoning_effort":""}`, "reasoning_effort"},
		{`{"reasoning_effort":1}`, "reasoning_effort"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			var fields map[string]json.RawMessage
			require.NoError(t, common.Unmarshal([]byte(tc.body), &fields))
			param, err := validateK3Parameters(fields, false)
			if tc.invalid == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Equal(t, tc.invalid, param)
			}
		})
	}
}

func TestK3ToolChoiceCompatibility(t *testing.T) {
	const tools = `[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{}}}}]`
	for _, tc := range []struct {
		name, selector, want, override                            string
		noTools, dynamic, disabled, outside, responses, anthropic bool
		status                                                    int
	}{
		{name: "omitted"},
		{name: "null", selector: `null`},
		{name: "empty", selector: `" "`},
		{name: "auto", selector: `"auto"`, want: `"auto"`},
		{name: "none", selector: `"none"`, want: `"none"`},
		{name: "required", selector: `"required"`, want: `"required"`},
		{name: "trim and case", selector: `" AUTO "`, want: `"auto"`},
		{name: "unsupported string", selector: `"bogus"`, want: `"auto"`},
		{name: "any retains requirement", selector: `"any"`, want: `"required"`},
		{name: "object any", selector: `{"type":"any"}`, want: `"required"`},
		{name: "object auto", selector: `{"type":"auto"}`, want: `"auto"`},
		{name: "object none", selector: `{"type":"none"}`, want: `"none"`},
		{name: "named", selector: `{"type":"function","function":{"name":"lookup"}}`, want: `{"type":"function","function":{"name":"lookup"}}`},
		{name: "flat function", selector: `{"type":"function","name":"lookup"}`, want: `{"type":"function","function":{"name":"lookup"}}`},
		{name: "tool alias", selector: `{"type":"tool","name":"lookup"}`, want: `{"type":"function","function":{"name":"lookup"}}`},
		{name: "auto without tools", selector: `"auto"`, noTools: true},
		{name: "none without tools", selector: `"none"`, noTools: true},
		{name: "invalid without tools", selector: `"bogus"`, noTools: true},
		{name: "required without tools", selector: `"required"`, noTools: true, status: 400},
		{name: "any without tools", selector: `"any"`, noTools: true, status: 400},
		{name: "named without tools", selector: `{"type":"tool","name":"lookup"}`, noTools: true, status: 400},
		{name: "unknown function", selector: `{"type":"function","function":{"name":"absent"}}`, status: 400},
		{name: "numeric", selector: `0`, status: 400},
		{name: "boolean", selector: `false`, status: 400},
		{name: "array", selector: `[]`, status: 400},
		{name: "empty object", selector: `{}`, status: 400},
		{name: "unknown object", selector: `{"type":"bogus"}`, status: 400},
		{name: "extra object options not lost", selector: `{"type":"any","disable_parallel_tool_use":true}`, status: 400},
		{name: "empty function name", selector: `{"type":"tool","name":""}`, status: 400},
		{name: "dynamic system tool", selector: `{"type":"tool","name":"lookup"}`, want: `{"type":"function","function":{"name":"lookup"}}`, dynamic: true},
		{name: "off", selector: `"bogus"`, want: `"bogus"`, disabled: true},
		{name: "unmatched model", selector: `"bogus"`, want: `"bogus"`, outside: true},
		{name: "Responses boundary", selector: `"bogus"`, responses: true, status: 500},
		{name: "Anthropic conversion not rewritten as OpenAI", selector: `"auto"`, anthropic: true, status: 500},
		{name: "valid override wins", selector: `"bogus"`, override: `"none"`, want: `"none"`},
		{name: "bad override type", selector: `"auto"`, override: `123`, status: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]json.RawMessage{"model": json.RawMessage(`"kimi-k3"`), "messages": json.RawMessage(`[{"role":"user","content":"Hi"}]`), "parallel_tool_calls": json.RawMessage(`false`), "custom": json.RawMessage(`9007199254740993`)}
			if !tc.noTools {
				fields["tools"] = json.RawMessage(tools)
			}
			if tc.dynamic {
				delete(fields, "tools")
				fields["messages"] = json.RawMessage(`[{"role":"system","tools":` + tools + `},{"role":"user","content":"Hi"}]`)
			}
			if tc.selector != "" {
				fields["tool_choice"] = json.RawMessage(tc.selector)
			}
			body, err := common.Marshal(fields)
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			t.Cleanup(func() { common.CleanupBodyStorage(c) })
			info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: types.RelayFormatOpenAI, ChannelMeta: &relaycommon.ChannelMeta{ChannelSetting: dto.ChannelSettings{K3ToolChoiceCompatibility: !tc.disabled, ChatCompatibilityModels: []string{"kimi-k3"}}}}
			if tc.outside {
				info.ChannelSetting.ChatCompatibilityModels = []string{"different"}
			}
			if tc.anthropic {
				info.ApiType = constant.APITypeAnthropic
			}
			outgoing := body
			if tc.override != "" {
				fields["tool_choice"] = json.RawMessage(tc.override)
				outgoing, err = common.Marshal(fields)
				require.NoError(t, err)
			}
			result, apiErr := applyChatCompatibility(c, info, outgoing, tc.responses)
			if tc.status != 0 {
				require.NotNil(t, apiErr)
				assert.Equal(t, tc.status, apiErr.StatusCode)
				assert.Equal(t, tc.status == 400, types.IsSkipRetryError(apiErr))
				return
			}
			require.Nil(t, apiErr)
			var actual map[string]json.RawMessage
			require.NoError(t, common.Unmarshal(result, &actual))
			if tc.want == "" {
				assert.NotContains(t, actual, "tool_choice")
			} else {
				assert.JSONEq(t, tc.want, string(actual["tool_choice"]))
			}
			delete(actual, "tool_choice")
			delete(fields, "tool_choice")
			assert.Equal(t, fields, actual, "only tool_choice may change")
			storage, err := common.GetBodyStorage(c)
			require.NoError(t, err)
			stored, err := storage.Bytes()
			require.NoError(t, err)
			assert.Equal(t, body, stored)
			info.ChannelSetting.K3ToolChoiceCompatibility = false
			replayed, apiErr := applyChatCompatibility(c, info, body, false)
			require.Nil(t, apiErr)
			assert.Equal(t, body, replayed, "next channel with switch off sees original bytes")
		})
	}
}

// Opt-in, bounded supplier verification. Credentials arrive on stdin and are
// never written to fixtures, test output, customer databases or channel settings.
func TestK3ToolChoiceLive(t *testing.T) {
	if os.Getenv("K3_TOOL_CHOICE_LIVE") != "1" {
		t.Skip("explicit supplier verification not enabled")
	}
	var config struct {
		URL string `json:"url"`
		Key string `json:"key"`
	}
	require.NoError(t, common.DecodeJson(io.LimitReader(os.Stdin, 65536), &config))
	require.NotEmpty(t, config.URL)
	require.NotEmpty(t, config.Key)
	for _, tc := range []struct {
		name, selector         string
		enabled, stream, named bool
		status                 int
	}{
		{"off-invalid", `"bogus"`, false, false, false, 400},
		{"on-invalid-nonstream", `"bogus"`, true, false, false, 200},
		{"on-invalid-stream", `"bogus"`, true, true, false, 200},
		{"on-named-nonstream", `{"type":"tool","name":"ping"}`, true, false, true, 200},
		{"on-named-stream", `{"type":"tool","name":"ping"}`, true, true, true, 200},
		{"on-required", `{"type":"any"}`, true, false, true, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]any{"model": "kimi-k3", "messages": []any{map[string]any{"role": "user", "content": "Call ping with value OK. Do not answer without calling the tool."}}, "max_tokens": 512, "reasoning_effort": "low", "stream": tc.stream,
				"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "ping", "description": "Echo a value", "parameters": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []string{"value"}}}}}, "tool_choice": json.RawMessage(tc.selector)}
			if tc.stream {
				fields["stream_options"] = map[string]bool{"include_usage": true}
			}
			body, err := common.Marshal(fields)
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			t.Cleanup(func() { common.CleanupBodyStorage(c) })
			info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: types.RelayFormatOpenAI, ChannelMeta: &relaycommon.ChannelMeta{ChannelSetting: dto.ChannelSettings{K3ToolChoiceCompatibility: tc.enabled, ChatCompatibilityModels: []string{"kimi-k3"}}}}
			outgoing, apiErr := applyChatCompatibility(c, info, body, false)
			require.Nil(t, apiErr)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(config.URL, "/")+"/v1/chat/completions", bytes.NewReader(outgoing))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+config.Key)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			payload, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			require.NoError(t, err)
			require.Equal(t, tc.status, resp.StatusCode, "unexpected supplier status (response omitted)")
			if tc.status != 200 {
				assert.Contains(t, string(payload), "tool_choice")
				return
			}
			var events [][]byte
			if tc.stream {
				scanner := bufio.NewScanner(bytes.NewReader(payload))
				scanner.Buffer(make([]byte, 4096), 2<<20)
				done := false
				for scanner.Scan() {
					line := scanner.Text()
					if line == "data: [DONE]" {
						done = true
						continue
					}
					if data, ok := strings.CutPrefix(line, "data: "); ok {
						events = append(events, []byte(data))
					}
				}
				require.NoError(t, scanner.Err())
				require.True(t, done, "supplier stream must complete")
			} else {
				events = append(events, payload)
			}
			var content, names, arguments strings.Builder
			usage := false
			for _, event := range events {
				var data struct {
					Error   any            `json:"error"`
					Usage   map[string]any `json:"usage"`
					Choices []struct {
						FinishReason string `json:"finish_reason"`
						Message      struct {
							Content   string `json:"content"`
							ToolCalls []struct {
								Function struct {
									Name      string `json:"name"`
									Arguments string `json:"arguments"`
								} `json:"function"`
							} `json:"tool_calls"`
						} `json:"message"`
						Delta struct {
							Content   string `json:"content"`
							ToolCalls []struct {
								Function struct {
									Name      string `json:"name"`
									Arguments string `json:"arguments"`
								} `json:"function"`
							} `json:"tool_calls"`
						} `json:"delta"`
					} `json:"choices"`
				}
				require.NoError(t, common.Unmarshal(event, &data))
				require.Nil(t, data.Error)
				usage = usage || len(data.Usage) > 0
				for _, choice := range data.Choices {
					require.NotEqual(t, "error", choice.FinishReason)
					content.WriteString(choice.Message.Content)
					content.WriteString(choice.Delta.Content)
					for _, call := range choice.Message.ToolCalls {
						names.WriteString(call.Function.Name)
						arguments.WriteString(call.Function.Arguments)
					}
					for _, call := range choice.Delta.ToolCalls {
						names.WriteString(call.Function.Name)
						arguments.WriteString(call.Function.Arguments)
					}
				}
			}
			require.True(t, content.Len() > 0 || names.Len() > 0, "must deliver business output, not only reasoning")
			if tc.named {
				require.Equal(t, "ping", names.String())
				var args map[string]any
				require.NoError(t, common.UnmarshalJsonStr(arguments.String(), &args))
				assert.Equal(t, "OK", args["value"])
			}
			t.Logf("HTTP=%d stream=%t meaningful_output=true tool_name=%s usage_present=%t", resp.StatusCode, tc.stream, names.String(), usage)
		})
	}
}

func TestK3ReasoningCompatibilityDispatch(t *testing.T) {
	service.InitHttpClient()
	for _, stream := range []bool{false, true} {
		for _, passthrough := range []bool{false, true} {
			for _, tc := range []struct {
				name, fields, want string
				status             int
			}{
				{"medium", `,"reasoning_effort":"medium"`, "high", 502},
				{"ultra", `,"reasoning_effort":"ultra"`, "high", 502},
				{"empty", `,"reasoning_effort":""`, "high", 502},
				{"null", `,"reasoning_effort":null`, "high", 502},
				{"omitted", ``, "", 502},
				{"tool choice invalid", `,"tool_choice":"bogus"`, "", 502},
				{"tool choice required", `,"tool_choice":"required"`, "", 400},
				{"other K3 validation", `,"reasoning_effort":"medium","temperature":0`, "", 400},
				{"type validation", `,"reasoning_effort":12`, "", 400},
			} {
				t.Run(fmt.Sprintf("stream=%t/passthrough=%t/%s", stream, passthrough, tc.name), func(t *testing.T) {
					var captured []byte
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						captured, _ = io.ReadAll(r.Body)
						// A sentinel failure avoids invoking settlement; this test
						// verifies the actual outbound HTTP request for both modes.
						w.WriteHeader(http.StatusBadGateway)
						_, _ = io.WriteString(w, `{"error":{"message":"fixture upstream reached"}}`)
					}))
					t.Cleanup(upstream.Close)
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					body := fmt.Sprintf(`{"model":"kimi-k3","stream":%t,"messages":[{"role":"user","content":"Hi"}]%s}`, stream, tc.fields)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
					c.Request.Header.Set("Content-Type", "application/json")
					t.Cleanup(func() { common.CleanupBodyStorage(c) })
					common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
					common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
					common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{
						K3ParameterValidation: true, K3ReasoningCompatibility: true, K3ToolChoiceCompatibility: true, StrictChatToolMessages: true,
						ChatCompatibilityModels: []string{"kimi-k3"}, PassThroughBodyEnabled: passthrough,
					})
					request, err := helper.GetAndValidateTextRequest(c, relayconstant.RelayModeChatCompletions)
					if tc.name == "type validation" {
						require.Error(t, err)
						assert.Empty(t, captured)
						return
					}
					require.NoError(t, err)
					info := &relaycommon.RelayInfo{Request: request, OriginModelName: "kimi-k3", RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: types.RelayFormatOpenAI, RequestURLPath: c.Request.URL.Path, IsStream: stream}
					apiErr := TextHelper(c, info)
					require.NotNil(t, apiErr)
					assert.Equal(t, tc.status, apiErr.StatusCode, apiErr.Error())
					if tc.status == 400 {
						assert.Empty(t, captured)
						return
					}
					require.NotEmpty(t, captured)
					var fields map[string]any
					require.NoError(t, common.Unmarshal(captured, &fields))
					if tc.want == "" {
						assert.NotContains(t, fields, "reasoning_effort")
					} else {
						assert.Equal(t, tc.want, fields["reasoning_effort"])
					}
					assert.Equal(t, stream, fields["stream"])
					if tc.name == "tool choice invalid" {
						assert.NotContains(t, fields, "tool_choice")
					}
					assert.Equal(t, "Hi", fields["messages"].([]any)[0].(map[string]any)["content"])
				})
			}
		}
	}
}

func TestK3ReasoningCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, original, outgoing, want                                string
		disabled, noValidation, outsideScope, viaResponses, allowNone bool
		status                                                        int
	}{
		{name: "medium", original: `"medium"`, outgoing: `"medium"`, want: `"high"`},
		{name: "ultra", original: `"ultra"`, outgoing: `"ultra"`, want: `"high"`},
		{name: "none", original: `"none"`, outgoing: `"none"`, want: `"high"`},
		{name: "none opt-in", original: `"none"`, outgoing: `"none"`, want: `"none"`, allowNone: true},
		{name: "none restored when omitted", original: `"none"`, want: `"none"`, allowNone: true},
		{name: "none without reasoning compatibility", original: `"none"`, outgoing: `"none"`, want: `"none"`, allowNone: true, disabled: true},
		{name: "none conflicting override", original: `"none"`, outgoing: `"high"`, status: 500, allowNone: true},
		{name: "none Responses conversion blocked", original: `"none"`, outgoing: `"none"`, status: 500, allowNone: true, viaResponses: true},
		{name: "null omitted by DTO", original: `null`, want: `"high"`},
		{name: "empty omitted by DTO", original: `""`, want: `"high"`},
		{name: "whitespace", original: `"  "`, outgoing: `"  "`, want: `"high"`},
		{name: "low", original: `"low"`, outgoing: `"low"`, want: `"low"`},
		{name: "high", original: `"high"`, outgoing: `"high"`, want: `"high"`},
		{name: "max", original: `"max"`, outgoing: `"max"`, want: `"max"`},
		{name: "absent stays absent"},
		{name: "legal channel override wins", original: `"medium"`, outgoing: `"max"`, want: `"max"`},
		{name: "outgoing override normalized", original: `"low"`, outgoing: `"ultra"`, want: `"high"`},
		{name: "independent from validation", original: `"medium"`, outgoing: `"medium"`, want: `"high"`, noValidation: true},
		{name: "disabled retains rejection", original: `"medium"`, outgoing: `"medium"`, disabled: true, status: 400},
		{name: "different model unchanged", original: `"medium"`, outgoing: `"medium"`, want: `"medium"`, outsideScope: true},
		{name: "numeric client rejected", original: `1`, outgoing: `"high"`, status: 400},
		{name: "boolean client rejected", original: `false`, outgoing: `"high"`, noValidation: true, status: 400},
		{name: "array client rejected", original: `[]`, outgoing: `"high"`, status: 400},
		{name: "object client rejected", original: `{}`, outgoing: `"high"`, status: 400},
		{name: "invalid override type rejected", original: `"medium"`, outgoing: `1`, status: 500},
		{name: "Responses conversion not bypassed", original: `"medium"`, outgoing: `"medium"`, viaResponses: true, noValidation: true, status: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := map[string]json.RawMessage{"model": json.RawMessage(`"kimi-k3"`), "messages": json.RawMessage(`[{"role":"user","content":"Hi"}]`), "temperature": json.RawMessage(`1`)}
			outgoing := maps.Clone(original)
			if tc.original != "" {
				original["reasoning_effort"] = json.RawMessage(tc.original)
			}
			if tc.outgoing != "" {
				outgoing["reasoning_effort"] = json.RawMessage(tc.outgoing)
			}
			originalJSON, err := common.Marshal(original)
			require.NoError(t, err)
			outgoingJSON, err := common.Marshal(outgoing)
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(originalJSON))
			t.Cleanup(func() { common.CleanupBodyStorage(c) })
			info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: types.RelayFormatOpenAI,
				ChannelMeta: &relaycommon.ChannelMeta{ChannelSetting: dto.ChannelSettings{
					K3ParameterValidation: !tc.noValidation, K3ReasoningCompatibility: !tc.disabled, K3AllowReasoningEffortNone: tc.allowNone, ChatCompatibilityModels: []string{"kimi-k3"},
				}},
			}
			if tc.outsideScope {
				info.ChannelSetting.ChatCompatibilityModels = []string{"other"}
			}
			result, apiErr := applyChatCompatibility(c, info, outgoingJSON, tc.viaResponses)
			if tc.status != 0 {
				require.NotNil(t, apiErr)
				assert.Equal(t, tc.status, apiErr.StatusCode)
				assert.Equal(t, tc.status == 400, types.IsSkipRetryError(apiErr))
				return
			}
			require.Nil(t, apiErr)
			var fields map[string]json.RawMessage
			require.NoError(t, common.Unmarshal(result, &fields))
			assert.Equal(t, tc.want, string(fields["reasoning_effort"]))
			delete(fields, "reasoning_effort")
			delete(outgoing, "reasoning_effort")
			assert.Equal(t, outgoing, fields, "only reasoning_effort may change")
			storage, err := common.GetBodyStorage(c)
			require.NoError(t, err)
			stored, err := storage.Bytes()
			require.NoError(t, err)
			assert.Equal(t, originalJSON, stored, "retry input must remain byte-identical")
			if tc.name == "medium" {
				assert.Equal(t, "high", info.ReasoningEffort)
				assert.NotEmpty(t, info.ParamOverrideAudit)
				info.ChannelSetting.K3ReasoningCompatibility = false
				_, fallbackErr := applyChatCompatibility(c, info, outgoingJSON, false)
				require.NotNil(t, fallbackErr)
				assert.Equal(t, 400, fallbackErr.StatusCode, "a fallback with compatibility off sees the original medium")
			}
		})
	}
}

func TestK3IndependentSamplingCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
		settings        dto.ChannelSettings
		status          int
	}{
		{"temperature", `"temperature":0.2,"top_p":0.3`, `"temperature":1,"top_p":0.3`, dto.ChannelSettings{K3TemperatureCompatibility: true}, 0},
		{"equivalent number unchanged", `"temperature":1.0`, `"temperature":1.0`, dto.ChannelSettings{K3TemperatureCompatibility: true}, 0},
		{"top p", `"top_p":0.3,"temperature":0.2`, `"top_p":0.95,"temperature":0.2`, dto.ChannelSettings{K3TopPCompatibility: true}, 0},
		{"presence", `"presence_penalty":2,"frequency_penalty":1`, `"presence_penalty":0,"frequency_penalty":1`, dto.ChannelSettings{K3PresencePenaltyCompatibility: true}, 0},
		{"frequency", `"frequency_penalty":2,"presence_penalty":1`, `"frequency_penalty":0,"presence_penalty":1`, dto.ChannelSettings{K3FrequencyPenaltyCompatibility: true}, 0},
		{"null", `"temperature":null`, `"temperature":null`, dto.ChannelSettings{K3TemperatureCompatibility: true}, 0},
		{"string rejected", `"temperature":"1"`, ``, dto.ChannelSettings{K3TemperatureCompatibility: true}, 400},
		{"object rejected", `"top_p":{}`, ``, dto.ChannelSettings{K3TopPCompatibility: true}, 400},
		{"off rejects", `"temperature":0.2`, ``, dto.ChannelSettings{K3ParameterValidation: true}, 400},
		{"before fixed check", `"temperature":0.2`, `"temperature":1`, dto.ChannelSettings{K3TemperatureCompatibility: true, K3ParameterValidation: true}, 0},
		{"thinking stays intact", `"thinking":{"type":"disabled"},"reasoning_effort":"high"`, `"thinking":{"type":"disabled"},"reasoning_effort":"high"`, dto.ChannelSettings{K3AllowThinkingDisabled: true, K3ReasoningCompatibility: true, K3ParameterValidation: true}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"x"}],` + tc.raw + `}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			t.Cleanup(func() { common.CleanupBodyStorage(c) })
			tc.settings.ChatCompatibilityModels = []string{"kimi-k3"}
			info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: types.RelayFormatOpenAI, ChannelMeta: &relaycommon.ChannelMeta{ChannelSetting: tc.settings}}
			result, apiErr := applyChatCompatibility(c, info, body, false)
			if tc.status != 0 {
				require.NotNil(t, apiErr)
				assert.Equal(t, tc.status, apiErr.StatusCode)
				return
			}
			require.Nil(t, apiErr)
			assert.JSONEq(t, `{"model":"kimi-k3","messages":[{"role":"user","content":"x"}],`+tc.want+`}`, string(result))
			storage, err := common.GetBodyStorage(c)
			require.NoError(t, err)
			stored, err := storage.Bytes()
			require.NoError(t, err)
			assert.Equal(t, body, stored)
			info.ChannelSetting = dto.ChannelSettings{}
			retry, apiErr := applyChatCompatibility(c, info, stored, false)
			require.Nil(t, apiErr)
			assert.Equal(t, body, retry)
		})
	}
}

func TestChatCompatibilityAttemptIsolation(t *testing.T) {
	original := `{"model":"customer-alias","messages":[{"role":"user","content":"hello"}],"stop":["END","结束"],"temperature":null,"stream":false,"reasoning_effort":"medium"}`
	outgoing := strings.Replace(original, "customer-alias", "upstream-model", 1)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(original))
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{ApiType: constant.APITypeOpenAI, ChannelSetting: dto.ChannelSettings{
			StrictChatToolMessages: true, K3ParameterValidation: true, K3ReasoningCompatibility: true, GatewayVisibleContentStop: true,
			ChatCompatibilityModels: []string{"upstream-model"},
		}},
	}
	body, apiErr := applyChatCompatibility(c, info, []byte(outgoing), false)
	require.Nil(t, apiErr)
	assert.NotContains(t, string(body), `"stop"`)
	assert.Contains(t, string(body), `"temperature":null`)
	assert.Contains(t, string(body), `"reasoning_effort":"high"`)
	assert.Equal(t, []string{"END", "结束"}, info.VisibleStop)
	storage, err := common.GetBodyStorage(c)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })
	stored, err := storage.Bytes()
	require.NoError(t, err)
	assert.Equal(t, original, string(stored))

	// A subsequent channel receives the unchanged original request, including
	// stop, and cannot inherit the first channel's compatibility state.
	info.ChannelSetting = dto.ChannelSettings{}
	body, apiErr = applyChatCompatibility(c, info, []byte(original), false)
	require.Nil(t, apiErr)
	assert.Equal(t, original, string(body))
	assert.Nil(t, info.VisibleStop)
}

func TestChatCompatibilityScopeAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name, original, outgoing string
		viaResponses             bool
		format                   types.RelayFormat
		status                   int
	}{
		{name: "client invalid even when override fixes", original: `{"temperature":0}`, outgoing: `{"temperature":1}`, status: 400},
		{name: "configuration invalid", original: `{"temperature":1}`, outgoing: `{"temperature":0}`, status: 500},
		{name: "omitted original", original: `{}`, outgoing: `{}`},
		{name: "explicit null", original: `{"temperature":null}`, outgoing: `{}`},
		{name: "client model alias", original: `{"model":"alias","temperature":0}`, outgoing: `{"temperature":1}`, status: 400},
		{name: "final override selects other model", original: `{"temperature":0}`, outgoing: `{"model":"other","temperature":0}`},
		{name: "Responses conversion rejected", original: `{}`, outgoing: `{}`, viaResponses: true, status: 500},
		{name: "Responses API unaffected", original: `{"temperature":0}`, outgoing: `{"temperature":0}`, format: types.RelayFormatOpenAIResponses},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var original, outgoing map[string]json.RawMessage
			require.NoError(t, common.Unmarshal([]byte(tc.original), &original))
			require.NoError(t, common.Unmarshal([]byte(tc.outgoing), &outgoing))
			for _, body := range []map[string]json.RawMessage{original, outgoing} {
				if body["model"] == nil {
					body["model"] = json.RawMessage(`"target"`)
				}
				body["messages"] = json.RawMessage(`[{"role":"user","content":"Hi"}]`)
			}
			originalJSON, err := common.Marshal(original)
			require.NoError(t, err)
			outgoingJSON, err := common.Marshal(outgoing)
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(originalJSON))
			format := tc.format
			if format == "" {
				format = types.RelayFormatOpenAI
			}
			info := &relaycommon.RelayInfo{
				RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: format,
				ChannelMeta: &relaycommon.ChannelMeta{ChannelSetting: dto.ChannelSettings{
					K3ParameterValidation: true, ChatCompatibilityModels: []string{"target"},
				}},
			}
			_, apiErr := applyChatCompatibility(c, info, outgoingJSON, tc.viaResponses)
			if storage, exists := c.Get(common.KeyBodyStorage); exists {
				t.Cleanup(func() { _ = storage.(common.BodyStorage).Close() })
			}
			if tc.status == 0 {
				require.Nil(t, apiErr)
				return
			}
			require.NotNil(t, apiErr)
			assert.Equal(t, tc.status, apiErr.StatusCode)
			assert.Equal(t, tc.status == 400, types.IsSkipRetryError(apiErr))
			if tc.status == 400 {
				assert.Equal(t, "invalid_request_error", apiErr.RelayError.(types.OpenAIError).Type)
			} else {
				assert.True(t, types.IsChannelError(apiErr))
			}
		})
	}
}

func TestVisibleContentStopRequestForms(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		want  []string
		valid bool
	}{
		{"", nil, true}, {`null`, nil, true}, {`[]`, []string{}, true},
		{`""`, []string{""}, true}, {`"END"`, []string{"END"}, true},
		{`["END","结束"]`, []string{"END", "结束"}, true},
		{`[null]`, nil, false}, {`["END",3]`, nil, false}, {`true`, nil, false}, {`{}`, nil, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			stops, err := parseVisibleContentStops(json.RawMessage(tc.raw))
			if tc.valid {
				require.NoError(t, err)
				assert.Equal(t, tc.want, stops)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestChatCompatibilityIndependentPolicies(t *testing.T) {
	for _, tc := range []struct {
		name, fields    string
		settings        dto.ChannelSettings
		viaResponses    bool
		apiType, status int
	}{
		{name: "all off", fields: `"temperature":0,"stop":false`},
		{name: "empty scope matches nothing", fields: `"temperature":0`, settings: dto.ChannelSettings{K3ParameterValidation: true}},
		{name: "only tools permits non K3 params", fields: `"temperature":0`, settings: dto.ChannelSettings{StrictChatToolMessages: true, ChatCompatibilityModels: []string{"target"}}},
		{name: "only stop without stop keeps Responses path", fields: `"temperature":0`, settings: dto.ChannelSettings{GatewayVisibleContentStop: true, ChatCompatibilityModels: []string{"target"}}, viaResponses: true},
		{name: "stop without sequence keeps unsupported adaptor", fields: `"stop":null`, settings: dto.ChannelSettings{GatewayVisibleContentStop: true, ChatCompatibilityModels: []string{"target"}}, apiType: constant.APITypeGemini},
		{name: "active stop rejects unsupported adaptor", fields: `"stop":"END"`, settings: dto.ChannelSettings{GatewayVisibleContentStop: true, ChatCompatibilityModels: []string{"target"}}, apiType: constant.APITypeGemini, status: 500},
		{name: "active stop rejects Responses conversion", fields: `"stop":"END"`, settings: dto.ChannelSettings{GatewayVisibleContentStop: true, ChatCompatibilityModels: []string{"target"}}, viaResponses: true, status: 500},
		{name: "active stop rejects reasoning relocation", fields: `"stop":"END"`, settings: dto.ChannelSettings{GatewayVisibleContentStop: true, ThinkingToContent: true, ChatCompatibilityModels: []string{"target"}}, status: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"target","messages":[{"role":"user","content":"Hi"}],` + tc.fields + `}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			info := &relaycommon.RelayInfo{
				RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: types.RelayFormatOpenAI,
				ChannelMeta: &relaycommon.ChannelMeta{ApiType: tc.apiType, ChannelSetting: tc.settings},
			}
			result, apiErr := applyChatCompatibility(c, info, body, tc.viaResponses)
			if storage, exists := c.Get(common.KeyBodyStorage); exists {
				t.Cleanup(func() { _ = storage.(common.BodyStorage).Close() })
			}
			if tc.status == 0 {
				require.Nil(t, apiErr)
				assert.Equal(t, body, result)
			} else {
				require.NotNil(t, apiErr)
				assert.Equal(t, tc.status, apiErr.StatusCode)
				assert.False(t, types.IsSkipRetryError(apiErr))
			}
		})
	}
}

func TestChatCompatibilityDynamicToolContentPresence(t *testing.T) {
	for _, tc := range []struct {
		name, original, outgoing string
		wantContent              bool
	}{
		{name: "omitted restored despite inserted system prompt", original: `{"role":"system","tools":[{"type":"function","function":{"name":"lookup"}}]}`, outgoing: `{"role":"system","content":null,"tools":[{"type":"function","function":{"name":"lookup"}}]}`},
		{name: "explicit null retained", original: `{"role":"system","content":null,"tools":[]}`, outgoing: `{"role":"system","content":null,"tools":[]}`, wantContent: true},
		{name: "override content retained", original: `{"role":"system","tools":[]}`, outgoing: `{"role":"system","content":"configured","tools":[]}`, wantContent: true},
		{name: "changed declaration retained", original: `{"role":"system","tools":[]}`, outgoing: `{"role":"system","content":null,"tools":[{"type":"function","function":{"name":"other"}}]}`, wantContent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := []byte(`{"model":"target","messages":[` + tc.original + `,{"role":"user","content":"Hi"}]}`)
			outgoing := []byte(`{"model":"target","messages":[{"role":"system","content":"existing prompt"},` + tc.outgoing + `,{"role":"user","content":"Hi"}]}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(original))
			info := &relaycommon.RelayInfo{
				RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: types.RelayFormatOpenAI,
				ChannelMeta: &relaycommon.ChannelMeta{ChannelSetting: dto.ChannelSettings{
					StrictChatToolMessages: true, ChatCompatibilityModels: []string{"target"},
				}},
			}
			result, apiErr := applyChatCompatibility(c, info, outgoing, false)
			require.Nil(t, apiErr)
			storage, err := common.GetBodyStorage(c)
			require.NoError(t, err)
			t.Cleanup(func() { _ = storage.Close() })
			var response struct {
				Messages []map[string]json.RawMessage `json:"messages"`
			}
			require.NoError(t, common.Unmarshal(result, &response))
			_, exists := response.Messages[1]["content"]
			assert.Equal(t, tc.wantContent, exists)
			assert.JSONEq(t, `"existing prompt"`, string(response.Messages[0]["content"]))
		})
	}
}

func TestRequestDeepCopyResponses(t *testing.T) {
	t.Run("nil source", func(t *testing.T) {
		clone, err := common.DeepCopy[dto.OpenAIResponsesRequest](nil)
		require.Error(t, err)
		assert.Nil(t, clone)
	})

	for _, raw := range []json.RawMessage{nil, {}, []byte(`null`), []byte(`false`), []byte(`0`), []byte(`[]`), []byte(`{"x":1}`)} {
		t.Run(fmt.Sprintf("raw_%q_nil_%t", raw, raw == nil), func(t *testing.T) {
			src := &dto.OpenAIResponsesRequest{}
			populateResponsesCloneFixture(t, reflect.ValueOf(src).Elem(), raw)
			want := &dto.OpenAIResponsesRequest{}
			populateResponsesCloneFixture(t, reflect.ValueOf(want).Elem(), raw)
			clone, err := common.DeepCopy(src)
			require.NoError(t, err)
			assert.Equal(t, src, clone, "including internal state and explicit zero pointers")

			// Mutate every reachable field, including nested pointers and raw bytes.
			// A later channel attempt must still see the original request.
			populateResponsesCloneFixture(t, reflect.ValueOf(clone).Elem(), []byte(`true`))
			assert.Equal(t, want, src)
			retry, err := common.DeepCopy(src)
			require.NoError(t, err)
			assert.Equal(t, want, retry)
		})
	}

	t.Run("absent optional fields", func(t *testing.T) {
		src := &dto.OpenAIResponsesRequest{Model: "gpt-4.1", Input: []byte(`"hello"`)}
		clone, err := common.DeepCopy(src)
		require.NoError(t, err)
		assert.Equal(t, src, clone)
		clone.Input[1] = 'H'
		assert.Equal(t, json.RawMessage(`"hello"`), src.Input)
	})

	t.Run("large input", func(t *testing.T) {
		input := bytes.Repeat([]byte("a"), 1<<20)
		input[0], input[len(input)-1] = '"', '"'
		src := &dto.OpenAIResponsesRequest{Model: "gpt-4.1", Input: input}
		clone, err := common.DeepCopy(src)
		require.NoError(t, err)
		assert.Equal(t, src, clone)
		clone.Input[len(input)/2] = 'b'
		assert.Equal(t, byte('a'), src.Input[len(input)/2])
	})

	t.Run("model mapping on successive channel attempts", func(t *testing.T) {
		src := &dto.OpenAIResponsesRequest{Model: "client-model", Input: []byte(`"hello"`)}
		for _, model := range []string{"first-channel-model", "retry-channel-model"} {
			request, err := common.DeepCopy(src)
			require.NoError(t, err)
			assert.Equal(t, json.RawMessage(`"hello"`), request.Input)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set("model_mapping", fmt.Sprintf(`{"client-model":%q}`, model))
			info := &relaycommon.RelayInfo{OriginModelName: src.Model}
			require.NoError(t, helper.ModelMappedHelper(c, info, request))
			assert.Equal(t, model, request.Model)
			assert.Equal(t, "client-model", src.Model)
			request.Input[1] = 'H'
			assert.Equal(t, json.RawMessage(`"hello"`), src.Input)
		}
	})
}

// Populate all mutable fields so newly added request fields also exercise the
// retry-isolation contract, rather than silently escaping a hand-written list.
func populateResponsesCloneFixture(t *testing.T, value reflect.Value, raw json.RawMessage) {
	t.Helper()
	switch value.Kind() {
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			populateResponsesCloneFixture(t, value.Field(i), raw)
		}
	case reflect.Pointer:
		if value.IsNil() {
			value.Set(reflect.New(value.Type().Elem()))
		}
		populateResponsesCloneFixture(t, value.Elem(), raw)
	case reflect.Slice:
		require.Equal(t, reflect.TypeFor[json.RawMessage](), value.Type())
		if value.Len() > 0 {
			value.Index(0).SetUint('!')
		}
		value.Set(reflect.ValueOf(json.RawMessage(bytes.Clone(raw))))
	case reflect.String:
		value.SetString(value.String() + "gpt-4.1")
	case reflect.Bool:
		value.SetBool(bytes.Equal(raw, []byte(`true`)))
	case reflect.Int:
		if bytes.Equal(raw, []byte(`true`)) {
			value.SetInt(1)
		}
	case reflect.Uint:
		if bytes.Equal(raw, []byte(`true`)) {
			value.SetUint(1)
		}
	case reflect.Float64:
		if bytes.Equal(raw, []byte(`true`)) {
			value.SetFloat(1)
		}
	default:
		require.FailNow(t, "extend clone fixture for new field type", "%s", value.Type())
	}
}

func TestRequestDeepCopyRawMutation(t *testing.T) {
	t.Run("edits before cloning are included", func(t *testing.T) {
		src := &dto.OpenAIResponsesRequest{Input: json.RawMessage(`"hello"`)}
		src.Input[1] = 'H'
		clone, err := common.DeepCopy(src)
		require.NoError(t, err)
		assert.Equal(t, json.RawMessage(`"Hello"`), clone.Input)
	})

	t.Run("edits after cloning are isolated in both directions", func(t *testing.T) {
		input := make(json.RawMessage, len(`"hello"`), 32)
		copy(input, `"hello"`)
		src := &dto.OpenAIResponsesRequest{Input: input}
		clone, err := common.DeepCopy(src)
		require.NoError(t, err)

		clone.Input[1] = 'H'
		assert.Equal(t, json.RawMessage(`"hello"`), src.Input)
		src.Input[2] = 'A'
		assert.Equal(t, json.RawMessage(`"Hello"`), clone.Input)
		clone.Input = append(clone.Input, ' ')
		src.Input = append(src.Input, '\n')
		assert.Equal(t, json.RawMessage("\"Hello\" "), clone.Input)
		assert.Equal(t, json.RawMessage("\"hAllo\"\n"), src.Input)
		clone.Input = json.RawMessage(`"replacement"`)
		assert.Equal(t, json.RawMessage("\"hAllo\"\n"), src.Input)
	})

	t.Run("raw map and slice entries retain nil and empty values", func(t *testing.T) {
		src := &struct {
			Fields map[string]json.RawMessage
			Items  []json.RawMessage
		}{
			Fields: map[string]json.RawMessage{"absent": nil, "empty": {}, "value": []byte(`false`)},
			Items:  []json.RawMessage{nil, {}, []byte(`0`)},
		}
		clone, err := common.DeepCopy(src)
		require.NoError(t, err)
		assert.Equal(t, src, clone)
		clone.Fields["value"][0] = '!'
		clone.Items[2][0] = '1'
		delete(clone.Fields, "absent")
		assert.Contains(t, src.Fields, "absent")
		assert.Equal(t, json.RawMessage(`false`), src.Fields["value"])
		assert.Equal(t, json.RawMessage(`0`), src.Items[2])
	})
}

func TestRequestDeepCopyOtherProtocols(t *testing.T) {
	t.Run("chat nested fields remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"model":"gpt-4.1","max_tokens":0,"stream":false,"thinking_budget":128,
			"messages":[{"role":"assistant","content":[{"type":"text","text":"hello"}],"tool_calls":[{"id":"call_1"}]}],
			"response_format":{"type":"json_schema","json_schema":{"strict":false}}
		}`, func(clone *dto.GeneralOpenAIRequest) {
			clone.Messages[0].ToolCalls[0] = '!'
			clone.Messages[0].Content.([]any)[0].(map[string]any)["text"] = "changed"
			clone.ResponseFormat.JsonSchema[0] = '!'
			*clone.MaxTokens = 1
			*clone.Stream = true
		})
	})

	t.Run("claude dynamic tools and raw fields remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"model":"claude-sonnet-4-5","max_tokens":1024,"temperature":0,
			"tools":[{"name":"lookup","input_schema":{"type":"object"}}],
			"metadata":{"user_id":"test"},"messages":[{"role":"user","content":"hello"}]
		}`, func(clone *dto.ClaudeRequest) {
			clone.Metadata[0] = '!'
			clone.Tools.([]any)[0].(map[string]any)["input_schema"].(map[string]any)["type"] = "array"
			clone.Messages[0].Content = "changed"
			*clone.Temperature = 1
		})
	})

	t.Run("gemini nested parts and configuration remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"contents":[{"role":"model","parts":[{"text":"hello","thoughtSignature":"signature","inlineData":{"mimeType":"text/plain","data":"aGk="}}]}],
			"tools":[{"googleSearch":{}}],"generationConfig":{"responseJsonSchema":{"type":"object"}}
		}`, func(clone *dto.GeminiChatRequest) {
			clone.Contents[0].Parts[0].ThoughtSignature[1] = '!'
			clone.Contents[0].Parts[0].InlineData.Data = "changed"
			clone.Tools[0] = '!'
			clone.GenerationConfig.ResponseJsonSchema[0] = '!'
		})
	})

	t.Run("image extra parameters remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"model":"gpt-image-1","prompt":"a cat","n":1,"watermark":false,
			"image":["https://example.com/image.png"],"parameters":{"seed":0}
		}`, func(clone *dto.ImageRequest) {
			clone.Image[0] = '!'
			clone.Extra["parameters"][0] = '!'
			clone.Extra["new_field"] = json.RawMessage(`true`)
			*clone.N = 2
			*clone.Watermark = true
		})
	})

	t.Run("audio raw parameters and speed remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"model":"tts-1","input":"hello","voice":"alloy","speed":1,"metadata":{"tag":"test"},"ref_audio":"sample"
		}`, func(clone *dto.AudioRequest) {
			clone.Metadata[0] = '!'
			clone.RefAudio[1] = '!'
			*clone.Speed = 2
		})
	})

	t.Run("embedding dynamic input and dimensions remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{"model":"text-embedding-3-small","input":[[1,2],[3]],"dimensions":256}`,
			func(clone *dto.EmbeddingRequest) {
				clone.Input.([]any)[0].([]any)[0] = float64(9)
				*clone.Dimensions = 512
			})
	})

	t.Run("rerank documents and options remain isolated", func(t *testing.T) {
		assertRequestCloneIsolation(t, `{
			"model":"rerank-v3.5","query":"hello","documents":["first","second"],"top_n":1,"return_documents":false
		}`, func(clone *dto.RerankRequest) {
			clone.Documents[0] = "changed"
			*clone.TopN = 2
			*clone.ReturnDocuments = true
		})
	})
}

// A channel attempt may mutate its clone without changing the request used by
// later attempts. Decode the expected request independently to avoid aliases.
func assertRequestCloneIsolation[T any](t *testing.T, body string, mutate func(*T)) {
	t.Helper()
	var src, want T
	require.NoError(t, common.UnmarshalJsonStr(body, &src))
	require.NoError(t, common.UnmarshalJsonStr(body, &want))
	clone, err := common.DeepCopy(&src)
	require.NoError(t, err)
	require.Equal(t, &want, clone)
	wantJSON, err := common.Marshal(&want)
	require.NoError(t, err)
	cloneJSON, err := common.Marshal(clone)
	require.NoError(t, err)
	assert.Equal(t, string(wantJSON), string(cloneJSON), "preserve the outbound JSON")
	mutate(clone)
	assert.Equal(t, want, src, "a channel attempt must not mutate the original request")
	retry, err := common.DeepCopy(&src)
	require.NoError(t, err)
	assert.Equal(t, &want, retry)
}

func BenchmarkRequestDeepCopy(b *testing.B) {
	for _, size := range []int{256, 4 << 10, 1 << 20, 10 << 20, 40 << 20} {
		input := bytes.Repeat([]byte("a"), size)
		input[0], input[len(input)-1] = '"', '"'
		src := &dto.OpenAIResponsesRequest{Model: "gpt-4.1", Input: input}
		for _, method := range []struct {
			name string
			copy func(*dto.OpenAIResponsesRequest) (*dto.OpenAIResponsesRequest, error)
		}{
			{"reflect", func(src *dto.OpenAIResponsesRequest) (*dto.OpenAIResponsesRequest, error) {
				var dst dto.OpenAIResponsesRequest
				err := copier.CopyWithOption(&dst, src, copier.Option{DeepCopy: true, IgnoreEmpty: true})
				return &dst, err
			}},
			{"raw_bytes", common.DeepCopy[dto.OpenAIResponsesRequest]},
		} {
			b.Run(fmt.Sprintf("%dB/%s", size, method.name), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(size))
				for b.Loop() {
					clone, err := method.copy(src)
					require.NoError(b, err)
					require.Len(b, clone.Input, size)
				}
			})
		}
	}
}
