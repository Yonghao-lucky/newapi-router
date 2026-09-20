package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
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
			param, err := validateK3Parameters(fields)
			if tc.invalid == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Equal(t, tc.invalid, param)
			}
		})
	}
}

func TestChatCompatibilityAttemptIsolation(t *testing.T) {
	original := `{"model":"customer-alias","messages":[{"role":"user","content":"hello"}],"stop":["END","结束"],"temperature":null,"stream":false}`
	outgoing := strings.Replace(original, "customer-alias", "upstream-model", 1)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(original))
	info := &relaycommon.RelayInfo{
		RelayMode: relayconstant.RelayModeChatCompletions, RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{ApiType: constant.APITypeOpenAI, ChannelSetting: dto.ChannelSettings{
			StrictChatToolMessages: true, K3ParameterValidation: true, GatewayVisibleContentStop: true,
			ChatCompatibilityModels: []string{"upstream-model"},
		}},
	}
	body, apiErr := applyChatCompatibility(c, info, []byte(outgoing), false)
	require.Nil(t, apiErr)
	assert.NotContains(t, string(body), `"stop"`)
	assert.Contains(t, string(body), `"temperature":null`)
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
