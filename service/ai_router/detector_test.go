package ai_router

import (
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const detectorChatPath = "/v1/chat/completions"

func TestContentDiagnosticsAndMixedOrder(t *testing.T) {
	for _, tc := range []struct{ content, param, code string }{
		{`[{}]`, "messages[0].content[0].type", "missing_required_parameter"},
		{`[{"type":4}]`, "messages[0].content[0].type", "invalid_parameter_type"},
		{`[false]`, "messages[0].content[0]", "invalid_parameter_type"},
		{`42`, "messages[0].content", "invalid_parameter_type"},
		{`null`, "messages[0].content", "invalid_parameter_type"},
	} {
		inspection, err := InspectChatRequest(strings.NewReader(`{"model":"kimi-k3","messages":[{"role":"user","content":` + tc.content + `}]}`))
		require.NoError(t, err)
		require.NotNil(t, inspection.Demand.ContentError)
		assert.Equal(t, tc.param, inspection.Demand.ContentError.Param)
		assert.Equal(t, tc.code, inspection.Demand.ContentError.Code)
		assert.Equal(t, 400, inspection.Demand.ContentError.APIError().StatusCode)
	}
	for _, content := range []string{`[{"type":"document"},{"type":"image_url"}]`, `[{"type":"image_url"},{"type":"document"}]`} {
		inspection, err := InspectChatRequest(strings.NewReader(`{"model":"kimi-k3","messages":[{"role":"user","content":` + content + `}]}`))
		require.NoError(t, err)
		assert.Equal(t, ModalityUnknown, inspection.Demand.Modality)
		assert.Equal(t, []string{"document"}, inspection.Demand.UnknownContentTypes)
		assert.NotEmpty(t, inspection.Demand.UnknownParam)
	}
}

func TestStrictInspectionCacheAndPolicySnapshot(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body, err := common.CreateBodyStorage([]byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"Hi"}],"thinking":{"type":"disabled"}}`))
	require.NoError(t, err)
	defer body.Close()
	first, err := InspectStoredChatRequest(c, body, true)
	require.NoError(t, err)
	require.NotNil(t, first.ValidationError)
	second, err := InspectStoredChatRequest(c, body, true)
	require.NoError(t, err)
	assert.Same(t, first.ValidationError, second.ValidationError, "same body reuses inspection")
	_, found := CachedStoredChatDemand(c, body)
	assert.False(t, found)
	rewritten, err := common.CreateBodyStorage([]byte(`{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,AA"}}]}]}`))
	require.NoError(t, err)
	defer rewritten.Close()
	third, err := InspectStoredChatRequest(c, rewritten, false)
	require.NoError(t, err)
	assert.Equal(t, ModalityImage, third.Demand.Modality)
	assert.Nil(t, third.ValidationError)
	demand, found := CachedStoredChatDemand(c, rewritten)
	require.True(t, found)
	assert.Equal(t, ModalityImage, demand.Modality)
	previous := GetTargetPolicySnapshot()
	t.Cleanup(func() { PublishTargetPolicySnapshot(previous) })
	snapshot := BuildTargetPolicySnapshot([]TargetPolicy{{Model: "kimi-k3", GroupId: "default", Enabled: true, K3StrictValidation: true}})
	PinRequestPolicySnapshot(c, snapshot)
	PublishTargetPolicySnapshot(BuildTargetPolicySnapshot(nil))
	assert.Same(t, snapshot, RequestPolicySnapshot(c))
}

func TestReasoningEffortNoneIsDetectedWithoutInspectingMessageContent(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		want        bool
	}{
		{"none", `"none"`, true},
		{"high", `"high"`, false},
		{"case sensitive", `"None"`, false},
		{"null", `null`, false},
		{"object", `{"value":"none"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"kimi-k3","messages":[{"role":"user","content":"none is an ordinary word"}],"reasoning_effort":` + tc.field + `}`
			inspection, err := InspectChatRequest(strings.NewReader(body))
			require.NoError(t, err)
			assert.Equal(t, tc.want, inspection.Demand.ReasoningEffortNone)
		})
	}
}

func TestStrictChatInspection(t *testing.T) {
	for _, tc := range []struct{ name, fields, messages, param string }{
		{name: "bad role", messages: `[{"role":"badrole","content":"x"}]`, param: "messages[0].role"},
		{name: "missing role", messages: `[{"content":"x"}]`, param: "messages[0].role"},
		{name: "null role", messages: `[{"role":null}]`, param: "messages[0].role"},
		{name: "empty messages", messages: `[]`, param: "messages"},
		{name: "object messages", messages: `{}`, param: "messages"},
		{name: "scalar message", messages: `[true]`, param: "messages[0]"},
		{name: "tools missing type", fields: `,"tools":[{"bad":"x"}]`, param: "tools[0].type"},
		{name: "tools missing function", fields: `,"tools":[{"type":"function"}]`, param: "tools[0].function"},
		{name: "tools missing name", fields: `,"tools":[{"type":"function","function":{}}]`, param: "tools[0].function.name"},
		{name: "tools wrong type", fields: `,"tools":{}`, param: "tools"},
		{name: "thinking disabled", fields: `,"thinking":{"type":"disabled"}`, param: "thinking.type"},
		{name: "thinking invalid", fields: `,"thinking":{"type":"bogus"}`, param: "thinking.type"},
		{name: "thinking null", fields: `,"thinking":null`, param: "thinking"},
		{name: "thinking keep", fields: `,"thinking":{"type":"enabled","keep":"bogus"}`, param: "thinking.keep"},
		{name: "legacy enable thinking false", fields: `,"enable_thinking":false`},
		{name: "legacy enable thinking true", fields: `,"enable_thinking":true`},
		{name: "legacy enable thinking null", fields: `,"enable_thinking":null`},
		{name: "legacy enable thinking string", fields: `,"enable_thinking":"true"`},
		{name: "legacy enable thinking object", fields: `,"enable_thinking":{"type":"enabled"}`},
		{name: "legacy enable thinking duplicate", fields: `,"enable_thinking":true,"enable_thinking":false`},
		{name: "invalid format", fields: `,"response_format":{"type":"bad"}`, param: "response_format.type"},
		{name: "missing schema", fields: `,"response_format":{"type":"json_schema"}`, param: "response_format.json_schema"},
		{name: "invalid schema", fields: `,"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":[]}}`, param: "response_format.json_schema.schema"},
		{name: "zero tokens", fields: `,"max_tokens":0`, param: "max_tokens"},
		{name: "negative tokens", fields: `,"max_tokens":-5`, param: "max_tokens"},
		{name: "fractional tokens", fields: `,"max_tokens":0.5`, param: "max_tokens"},
		{name: "null tokens", fields: `,"max_tokens":null`, param: "max_tokens"},
		{name: "oversized tokens", fields: `,"max_tokens":18446744073686646784`, param: "max_tokens"},
		{name: "string temperature", fields: `,"temperature":"1"`, param: "temperature"},
		{name: "string top p", fields: `,"top_p":"0.95"`, param: "top_p"},
		{name: "duplicate effort-independent field", fields: `,"thinking":{"type":"enabled","type":"enabled"}`, param: "thinking.type"},
		{name: "duplicate messages", fields: `,"messages":[{"role":"user","content":"x"}]`, param: "messages"},
		{name: "escaped duplicate role", messages: `[{"role":"user","\u0072ole":"user"}]`, param: "messages[0].role"},
		{name: "duplicate model", fields: `,"model":"other"`, param: "model"},
		{name: "valid omit"},
		{name: "enabled thinking", fields: `,"thinking":{"type":"enabled","keep":"all"}`},
		{name: "thinking without type", fields: `,"thinking":{"keep":"all"}`},
		{name: "empty thinking object", fields: `,"thinking":{}`},
		{name: "compatibility left to channel", fields: `,"reasoning_effort":"medium","tool_choice":"any"`},
		{name: "valid custom", fields: `,"tools":[{"type":"custom","custom":{"name":"lookup","format":{"type":"text"}}}]`},
		{name: "irrelevant schema envelope", fields: `,"response_format":{"type":"text","json_schema":null}`},
		{name: "valid builtin", fields: `,"tools":[{"type":"builtin_function","function":{"name":"$web_search"}}]`},
		{name: "dynamic tools", messages: `[{"role":"system","tools":[{"type":"function","function":{"name":"weather"}}]}]`},
		{name: "system null content with dynamic tools", messages: `[{"role":"system","content":null,"tools":[{"type":"function","function":{"name":"weather"}}]}]`},
		{name: "valid schema refs", fields: `,"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":{"$defs":{"x":{"type":"string"}},"$ref":"#/$defs/x","thinking":{"type":"disabled"}}}}`},
		{name: "boolean schema", fields: `,"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":true}}`},
		{name: "payload not parameter", messages: `[{"role":"user","content":"thinking disabled enable_thinking false"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := tc.messages
			if messages == "" {
				messages = `[{"role":"user","content":"Hi"}]`
			}
			body := `{"model":"kimi-k3","messages":` + messages + tc.fields + `}`
			inspection, err := InspectChatRequestWithValidation(strings.NewReader(body))
			require.NoError(t, err)
			if tc.param == "" {
				require.Nil(t, inspection.ValidationError)
				return
			}
			require.NotNil(t, inspection.ValidationError)
			assert.Equal(t, tc.param, inspection.ValidationError.Param)
			apiErr := inspection.ValidationError.APIError()
			assert.Equal(t, 400, apiErr.StatusCode)
			assert.True(t, types.IsSkipRetryError(apiErr))
			assert.Equal(t, "invalid_request_error", apiErr.ToOpenAIError().Type)
			if tc.name == "thinking disabled" {
				assert.Equal(t, "unsupported_thinking_mode", apiErr.ToOpenAIError().Code)
			} else {
				assert.Equal(t, "invalid_request", apiErr.ToOpenAIError().Code)
			}
		})
	}
}

// A system message may omit its content only when it declares dynamic tools.
// The null-content rejection is reported as a content diagnostic, so both the
// strict validation slot and Demand.ContentError are asserted.
func TestSystemToolContentNullValidation(t *testing.T) {
	for _, tc := range []struct{ name, message, param string }{
		{name: "dynamic tools make content optional", message: `{"role":"system","content":null,"tools":[{"type":"function","function":{"name":"weather"}}]}`},
		{name: "without tools still rejected", message: `{"role":"system","content":null}`, param: "messages[0].content"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"kimi-k3","messages":[` + tc.message + `]}`
			inspection, err := InspectChatRequestWithValidation(strings.NewReader(body))
			require.NoError(t, err)
			if tc.param == "" {
				assert.Nil(t, inspection.ValidationError)
				assert.Nil(t, inspection.Demand.ContentError)
				return
			}
			assert.Nil(t, inspection.ValidationError)
			require.NotNil(t, inspection.Demand.ContentError)
			assert.Equal(t, tc.param, inspection.Demand.ContentError.Param)
			assert.Equal(t, "invalid_parameter_type", inspection.Demand.ContentError.Code)
			assert.Equal(t, tc.param+" must not be null for this message role", inspection.Demand.ContentError.Message)
		})
	}
}

func BenchmarkChatIngressInspection(b *testing.B) {
	for _, size := range []int{64, 128 * 1024, 16 * 1024 * 1024} {
		body := `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"data:video/mp4;base64,` + strings.Repeat("A", size) + `"}}]}]}`
		for _, strict := range []bool{false, true} {
			b.Run(fmt.Sprintf("bytes=%d/strict=%t", size, strict), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				for b.Loop() {
					if _, err := inspectChatRequest(strings.NewReader(body), strict); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// TestDetectRequest covers the eight Phase 0 captures plus the detection rules
// that protect them: modality precedence, history scanning, tools, unknown
// content types, and malformed input.
func TestDetectRequest(t *testing.T) {
	tests := []struct {
		name             string
		body             string
		path             string
		wantModel        string
		wantModality     Modality
		wantHasTools     bool
		wantUnknownTypes []string
		wantErr          string
	}{
		{
			name:         "capture 002 text with video_url in prose",
			body:         `{"model":"kimi-k3","messages":[{"role":"user","content":"hello, this is a plain text request mentioning the word video_url in prose"}]}`,
			wantModel:    "kimi-k3",
			wantModality: ModalityText,
		},
		{
			name:         "capture 003 image url object",
			body:         `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"text","text":"describe this image"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`,
			wantModel:    "kimi-k3",
			wantModality: ModalityImage,
		},
		{
			name:         "capture 004 string video url",
			body:         `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"text","text":"describe this video"},{"type":"video_url","video_url":"data:video/mp4;base64,AAAA"}]}]}`,
			wantModel:    "kimi-k3",
			wantModality: ModalityVideo,
		},
		{
			name:         "capture 005 object video url",
			body:         `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"text","text":"describe this video"},{"type":"video_url","video_url":{"url":"data:video/mp4;base64,AAAA"}}]}]}`,
			wantModel:    "kimi-k3",
			wantModality: ModalityVideo,
		},
		{
			name:             "capture 006 unknown content type",
			body:             `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"some_future_type","some_future_type":{"data":"xyz"}}]}]}`,
			wantModel:        "kimi-k3",
			wantModality:     ModalityUnknown,
			wantUnknownTypes: []string{"some_future_type"},
		},
		{
			name:         "capture 007 tools",
			body:         `{"model":"kimi-k3","messages":[{"role":"user","content":"what is the weather"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{}}}]}`,
			wantModel:    "kimi-k3",
			wantModality: ModalityText,
			wantHasTools: true,
		},
		{
			name:         "capture 008 stream request",
			body:         `{"model":"kimi-k3","messages":[{"role":"user","content":"streamed hello"}],"stream":true,"stream_options":{"include_usage":true}}`,
			wantModel:    "kimi-k3",
			wantModality: ModalityText,
		},
		{
			name:         "model after messages is still detected",
			body:         `{"messages":[{"content":[{"type":"video_url","video_url":"data:video/mp4;base64,AAAA"}]}],"model":"kimi-k3"}`,
			wantModel:    "kimi-k3",
			wantModality: ModalityVideo,
		},
		{
			name:         "model after messages with image",
			body:         `{"messages":[{"content":[{"type":"image_url"}]}],"model":"kimi-k3"}`,
			wantModel:    "kimi-k3",
			wantModality: ModalityImage,
		},
		{
			name:         "capture 009 image in history not downgraded by later text",
			body:         `{"model":"kimi-k3","messages":[{"role":"user","content":"first turn"},{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,AAAA"}}]},{"role":"user","content":"last turn"}]}`,
			wantModel:    "kimi-k3",
			wantModality: ModalityImage,
		},
		{
			name:         "video in history beats later text",
			body:         `{"model":"m","messages":[{"content":[{"type":"video_url","video_url":"u"}]},{"content":"later"}]}`,
			wantModality: ModalityVideo,
		},
		{
			name:         "video beats image across messages",
			body:         `{"model":"m","messages":[{"content":[{"type":"image_url"}]},{"content":[{"type":"video_url","video_url":{"url":"u"}}]}]}`,
			wantModality: ModalityVideo,
		},
		{
			name:             "video wins modality but unknown type still recorded",
			body:             `{"model":"m","messages":[{"content":[{"type":"custom"}]},{"content":[{"type":"video_url","video_url":"u"}]}]}`,
			wantModality:     ModalityVideo,
			wantUnknownTypes: []string{"custom"},
		},
		{
			name:             "unknown beats text",
			body:             `{"model":"m","messages":[{"content":[{"type":"text"},{"type":"custom"}]}]}`,
			wantModality:     ModalityUnknown,
			wantUnknownTypes: []string{"custom"},
		},
		{
			name:         "null content stays text",
			body:         `{"model":"m","messages":[{"content":null}]}`,
			wantModality: ModalityText,
		},
		{
			name:         "input text part",
			body:         `{"model":"m","messages":[{"content":[{"type":"input_text","input_text":"hello"}]}]}`,
			wantModality: ModalityText,
		},
		{
			name:         "input image type",
			body:         `{"model":"m","messages":[{"content":[{"type":"input_image"}]}]}`,
			wantModality: ModalityImage,
		},
		{
			name:         "empty tools array is not tools",
			body:         `{"model":"m","messages":[{"content":"hello"}],"tools":[]}`,
			wantModality: ModalityText,
		},
		{
			name:         "per message tools detected",
			body:         `{"model":"m","messages":[{"role":"system","content":"hello","tools":[{"type":"function"}]}]}`,
			wantModality: ModalityText,
			wantHasTools: true,
		},
		{
			name:             "non object content part is unknown",
			body:             `{"model":"m","messages":[{"content":[{"type":"text"},"oops"]}]}`,
			wantModality:     ModalityUnknown,
			wantUnknownTypes: []string{""},
		},
		{
			name:             "numeric content is unknown",
			body:             `{"model":"m","messages":[{"content":2}]}`,
			wantModality:     ModalityUnknown,
			wantUnknownTypes: []string{""},
		},
		{
			name:    "unsupported path",
			path:    "/v1/responses",
			body:    `{}`,
			wantErr: ErrUnsupportedDetectorPath.Error(),
		},
		{
			name:    "invalid json",
			body:    `{"model":"m","messages":[{"content":`,
			wantErr: "invalid OpenAI Chat request",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := test.path
			if path == "" {
				path = detectorChatPath
			}
			demand, err := DetectRequest(path, strings.NewReader(test.body))
			if test.wantErr != "" {
				if !errors.Is(err, ErrUnsupportedDetectorPath) {
					if err == nil || !strings.Contains(err.Error(), test.wantErr) {
						t.Fatalf("got err %v, want containing %q", err, test.wantErr)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if demand.Modality != test.wantModality {
				t.Errorf("modality = %q, want %q", demand.Modality, test.wantModality)
			}
			if demand.HasTools != test.wantHasTools {
				t.Errorf("hasTools = %v, want %v", demand.HasTools, test.wantHasTools)
			}
			if test.wantModel != "" && demand.Model != test.wantModel {
				t.Errorf("model = %q, want %q", demand.Model, test.wantModel)
			}
			if demand.DetectorFamily != DetectorFamilyOpenAIChat {
				t.Errorf("detectorFamily = %q", demand.DetectorFamily)
			}
			if len(demand.UnknownContentTypes) != len(test.wantUnknownTypes) {
				t.Fatalf("unknownContentTypes = %v, want %v", demand.UnknownContentTypes, test.wantUnknownTypes)
			}
			for i, want := range test.wantUnknownTypes {
				if demand.UnknownContentTypes[i] != want {
					t.Errorf("unknownContentTypes[%d] = %q, want %q", i, demand.UnknownContentTypes[i], want)
				}
			}
		})
	}
}

// TestDetectRequestDoesNotConsumeBody ensures the detector can be run against a
// replayable body without disturbing the caller's copy, which is what lets
// Phase 3 inspect a request and still relay it unchanged.
func TestDetectRequestDoesNotConsumeBody(t *testing.T) {
	body := `{"model":"kimi-k3","messages":[{"role":"user","content":"hello"}]}`
	reader := strings.NewReader(body)

	demand, err := DetectRequest(detectorChatPath, reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if demand.Model != "kimi-k3" {
		t.Fatalf("model = %q", demand.Model)
	}

	remaining, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read remaining: %v", err)
	}
	if string(remaining) != body {
		t.Fatalf("caller body not restored: got %q", remaining)
	}
}

// TestDetectRequestIsIsolated runs mixed valid and malformed requests
// concurrently and asserts each keeps its own result. Bodies are supplied as
// plain readers over independent strings, so any cross-request state leak
// shows up as a wrong modality or a spurious error.
func TestDetectRequestIsIsolated(t *testing.T) {
	bodies := map[string]struct {
		body         string
		wantModality Modality
		wantErr      bool
	}{
		"image":   {body: `{"model":"m","messages":[{"content":[{"type":"image_url"}]}]}`, wantModality: ModalityImage},
		"video":   {body: `{"model":"m","messages":[{"content":[{"type":"video_url","video_url":"` + strings.Repeat("A", 4096) + `"}]}]}`, wantModality: ModalityVideo},
		"text":    {body: `{"model":"m","messages":[{"content":"hello"}]}`, wantModality: ModalityText},
		"invalid": {body: `{"model":"m","messages":[{"content":`, wantErr: true},
	}

	const rounds = 50
	var wg sync.WaitGroup
	for name, tc := range bodies {
		for range rounds {
			wg.Add(1)
			go func() {
				defer wg.Done()
				demand, err := DetectRequest(detectorChatPath, strings.NewReader(tc.body))
				if tc.wantErr {
					if err == nil {
						t.Errorf("%s: expected error", name)
					}
					return
				}
				if err != nil {
					t.Errorf("%s: unexpected error: %v", name, err)
					return
				}
				if demand.Modality != tc.wantModality {
					t.Errorf("%s: modality = %q, want %q", name, demand.Modality, tc.wantModality)
				}
			}()
		}
	}
	wg.Wait()
}

func BenchmarkDetectRequestSmallChat(b *testing.B) {
	body := `{"model":"kimi-k3","messages":[{"role":"user","content":"hello"}]}`
	b.ReportAllocs()
	for b.Loop() {
		_, _ = DetectRequest(detectorChatPath, strings.NewReader(body))
	}
}

// BenchmarkDetectRequestVideo measures the cost of a 1 MiB base64 video part.
// The payload string is built once so the result reflects the detector rather
// than benchmark scaffolding. The payload is expected to be skipped, not read,
// so allocated bytes should stay in the kilobyte range.
func BenchmarkDetectRequestVideo(b *testing.B) {
	body := `{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"video_url","video_url":{"url":"data:video/mp4;base64,` + strings.Repeat("A", 1<<20) + `"}}]}]}`
	b.ReportAllocs()
	for b.Loop() {
		_, _ = DetectRequest(detectorChatPath, strings.NewReader(body))
	}
}

func TestInspectChatRequestValidatesSkippedPayloadsAndCountsEveryVideo(t *testing.T) {
	body := `{"messages":[{"content":[{"video_url":{"url":"data:video/mp4;base64,` + strings.Repeat("AAAA", 1<<15) + `"},"type":"video_url"},{"type":"image_url","image_url":{"url":"image"}},{"type":"video_url","video_url":"second"}]}],"model":"kimi-k3","extra":{"escaped":"\\\"\u4e2d","number":-2.5e+12,"list":[null,true,false,{}]}}`
	result, err := InspectChatRequest(strings.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, "kimi-k3", result.Demand.Model)
	assert.Equal(t, 2, result.VideoCount)
	assert.Equal(t, 1, result.ExistingImages)
	assert.Equal(t, ModalityVideo, result.Demand.Modality)
	for _, bad := range []string{
		`{"model":"a","model":"b"}`,
		`{"model":42}`,
		`{"model":"a","extra":"bad\q"}`,
		`{"model":"a","extra":"bad\u000x"}`,
		`{"model":"a","extra":[1,]}`,
		`{"model":"a","extra":{"trailing":true,}}`,
		`{"model":"a","extra":01}`,
		`{"model":"a","extra":-.2}`,
		`{"model":"a","extra":1.}`,
		`{"model":"a","extra":1e+}`,
		`{"model":"a","extra":trueX}`,
		`{"model":"a","extra":{"unclosed":true}`,
		`{"model":"a"} {}`,
		`{"model":"a","messages":[],"messages":[]}`,
		`{"model":"a","messages":[{"content":[],"content":[]}]}`,
		`{"model":"a","messages":[{"content":[{"type":"video_url","type":"text"}]}]}`,
	} {
		_, err := InspectChatRequest(strings.NewReader(bad))
		require.Error(t, err, bad)
	}
}

func TestEnableThinkingIsAcceptedAndReported(t *testing.T) {
	for _, tc := range []struct {
		name, field string
	}{
		{"true", `true`},
		{"false", `false`},
		{"string", `"true"`},
		{"number", `1`},
		{"object", `{"type":"enabled"}`},
		{"array", `["enabled"]`},
		{"null", `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"kimi-k3","messages":[{"role":"user","content":"Hi"}],"enable_thinking":` + tc.field + `}`
			inspection, err := InspectChatRequestWithValidation(strings.NewReader(body))
			require.NoError(t, err)
			assert.Nil(t, inspection.ValidationError)
			assert.True(t, inspection.Demand.EnableThinking)
		})
	}

	inspection, err := InspectChatRequestWithValidation(strings.NewReader(`{"model":"kimi-k3","messages":[{"role":"user","content":"Hi"}]}`))
	require.NoError(t, err)
	assert.False(t, inspection.Demand.EnableThinking)
}
