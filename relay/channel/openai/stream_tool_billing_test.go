package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectStreamFunctionCallNamesDedupesSameIndex(t *testing.T) {
	seen := make(map[string]struct{})
	var names []string

	chunks := []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"c2","type":"function","function":{"name":"get_time","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{}"}}]}}]}`,
	}
	for _, chunk := range chunks {
		collectStreamFunctionCallNames(chunk, seen, &names)
	}

	require.Len(t, names, 2)
	assert.Equal(t, []string{"get_weather", "get_time"}, names)
}

func TestVisibleStopNonStreamPreservesUsageAndNonContentFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content any
		stops   []string
		want    any
		finish  string
	}{
		{"single", "1,2,3,4,5,6", []string{"5"}, "1,2,3,4,", "stop"},
		{"empty_stop", "abc", []string{""}, "", "stop"},
		{"earliest", "a中文结束STOPz", []string{"STOP", "中文结束"}, "a", "stop"},
		{"unmatched_prefix", "abST", []string{"STOP"}, "abST", "length"},
		{"no_match", "abc", []string{"STOP"}, "abc", "length"},
		{"empty", "", []string{"STOP"}, "", "length"},
		{"null", nil, []string{"STOP"}, nil, "length"},
		{"disabled", "aSTOPz", nil, "aSTOPz", "length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := common.Marshal(map[string]any{
				"id": "fixture", "vendor": json.RawMessage(`{"large":9007199254740993}`),
				"choices": []any{map[string]any{
					"index": 0, "finish_reason": "length", "message": map[string]any{
						"role": "assistant", "content": tc.content, "reasoning_content": "keep STOP 中文结束",
						"tool_calls": []any{map[string]any{"id": "tool1", "type": "function", "function": map[string]any{"name": "test", "arguments": `{"value":"STOP"}`}}},
					},
				}},
				"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 100, "total_tokens": 110, "vendor_tokens": 73},
			})
			require.NoError(t, err)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI, UpstreamModelName: "kimi-k3"},
				RelayFormat: types.RelayFormatOpenAI, VisibleStop: tc.stops,
			}
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body)))}
			usage, apiErr := OpenaiHandler(c, info, resp)
			require.Nil(t, apiErr)
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, 100, usage.CompletionTokens)
			var got struct {
				Vendor  json.RawMessage `json:"vendor"`
				Usage   json.RawMessage `json:"usage"`
				Choices []struct {
					Finish  string         `json:"finish_reason"`
					Message map[string]any `json:"message"`
				} `json:"choices"`
			}
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &got))
			require.Len(t, got.Choices, 1)
			assert.Equal(t, tc.want, got.Choices[0].Message["content"])
			assert.Equal(t, tc.finish, got.Choices[0].Finish)
			assert.Equal(t, "keep STOP 中文结束", got.Choices[0].Message["reasoning_content"])
			assert.Contains(t, got.Choices[0].Message, "tool_calls")
			assert.Contains(t, string(got.Vendor), "9007199254740993")
			assert.JSONEq(t, `{"prompt_tokens":10,"completion_tokens":100,"total_tokens":110,"vendor_tokens":73}`, string(got.Usage))
		})
	}
}

func TestVisibleStopMatcherEarliestStartIndependentOfChunking(t *testing.T) {
	for _, tc := range []struct {
		name    string
		chunks  []string
		stops   []string
		want    string
		matched bool
	}{
		{"earlier_longer_stop", []string{"x", "ab", "cdtail"}, []string{"abcd", "b"}, "x", true},
		{"earlier_prefix_disproven", []string{"xa", "b", "x"}, []string{"abcd", "b"}, "xa", true},
		{"empty_stop", []string{"", "text"}, []string{""}, "", true},
		{"unmatched_partial", []string{"x", "终"}, []string{"终止"}, "x终", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &visibleStopChoice{}
			var got strings.Builder
			for _, chunk := range tc.chunks {
				got.WriteString(state.consume(chunk, tc.stops, false))
			}
			got.WriteString(state.consume("", tc.stops, true))
			assert.Equal(t, tc.want, got.String())
			assert.Equal(t, tc.matched, state.matched)
		})
	}
}

func TestVisibleStopStreamAcrossChunksDrainsUsageAndKeepsBoundaries(t *testing.T) {
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	frames := []string{
		`{"id":"fixture","choices":[{"index":0,"delta":{"role":"assistant","content":"你好终","reasoning_content":"STOP终止"},"finish_reason":null},{"index":1,"delta":{"content":"aST"},"finish_reason":null},{"index":2,"delta":{"tool_calls":[{"index":0,"id":"call1","type":"function","function":{"name":"test","arguments":"{\"value\":\"STOP\"}"}}]},"finish_reason":null}]}`,
		`{"id":"fixture","choices":[{"index":0,"delta":{"content":"止STOPdiscard"},"finish_reason":null},{"index":1,"delta":{"content":"X尾S"},"finish_reason":null}]}`,
		`{"id":"fixture","choices":[{"index":0,"delta":{"content":"discard too","reasoning_content":"still real"},"finish_reason":null},{"index":2,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"fixture","choices":[{"index":0,"delta":{},"finish_reason":"length"},{"index":1,"delta":{},"finish_reason":"length"},{"index":2,"delta":{"content":"after finish"},"finish_reason":"stop"}]}`,
		`{"id":"fixture","choices":[{"index":0,"delta":{"content":"after finish"},"finish_reason":"stop"}]}`,
		`{"id":"fixture","vendor":{"large":9007199254740993},"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":100,"total_tokens":110,"vendor_tokens":73}}`,
	}
	var stream strings.Builder
	for _, frame := range frames {
		stream.WriteString("data: " + frame + "\n\n")
	}
	stream.WriteString("data: [DONE]\n\n")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI, UpstreamModelName: "kimi-k3"},
		RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions,
		VisibleStop: []string{"STOP", "终止"}, ShouldIncludeUsage: true, DisablePing: true,
	}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream.String()))}
	usage, apiErr := OaiStreamHandler(c, info, resp)
	require.Nil(t, apiErr)
	assert.Equal(t, 100, usage.CompletionTokens)
	assert.Equal(t, 110, usage.TotalTokens)
	content := map[int]string{}
	finished := map[int]string{}
	var reasoning, tools strings.Builder
	usageCount := 0
	for line := range strings.SplitSeq(w.Body.String(), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var frame struct {
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Content   string          `json:"content"`
					Reasoning string          `json:"reasoning_content"`
					Tools     json.RawMessage `json:"tool_calls"`
				} `json:"delta"`
				Finish *string `json:"finish_reason"`
			} `json:"choices"`
			Usage  json.RawMessage `json:"usage"`
			Vendor json.RawMessage `json:"vendor"`
		}
		require.NoError(t, common.UnmarshalJsonStr(data, &frame))
		for _, choice := range frame.Choices {
			assert.NotContains(t, finished, choice.Index, "no events after finish")
			content[choice.Index] += choice.Delta.Content
			reasoning.WriteString(choice.Delta.Reasoning)
			tools.Write(choice.Delta.Tools)
			if choice.Finish != nil && *choice.Finish != "" {
				finished[choice.Index] = *choice.Finish
			}
		}
		if len(frame.Usage) > 0 {
			usageCount++
			assert.Len(t, finished, 3, "finish events precede final usage")
			assert.JSONEq(t, `{"prompt_tokens":10,"completion_tokens":100,"total_tokens":110,"vendor_tokens":73}`, string(frame.Usage))
			assert.Contains(t, string(frame.Vendor), "9007199254740993")
		}
	}
	assert.Equal(t, map[int]string{0: "你好", 1: "aSTX尾S", 2: ""}, content)
	assert.Equal(t, map[int]string{0: "stop", 1: "length", 2: "tool_calls"}, finished)
	assert.Equal(t, "STOP终止still real", reasoning.String())
	assert.Contains(t, tools.String(), "STOP")
	assert.Equal(t, 1, usageCount)
	assert.Equal(t, 1, strings.Count(w.Body.String(), "data: [DONE]"))
	assert.True(t, strings.HasSuffix(w.Body.String(), "data: [DONE]\n\n"))
}

func TestVisibleStopStreamTerminalFrameAndUnfinishedSuffix(t *testing.T) {
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	for _, tc := range []struct {
		name   string
		frames string
		want   string
		finish string
	}{
		{"terminal_usage", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"aSTOPb\"},\"finish_reason\":\"length\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":9,\"total_tokens\":10}}\n\n", "a", "stop"},
		{"unfinished_prefix", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"aST\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":9,\"total_tokens\":10}}\n\n", "aST", ""},
		{"matched_without_upstream_finish", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"aSTOP\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":9,\"total_tokens\":10}}\n\n", "a", "stop"},
		{"empty_content", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\"},\"finish_reason\":\"length\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":9,\"total_tokens\":10}}\n\n", "", "length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI, UpstreamModelName: "kimi-k3"},
				RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions,
				VisibleStop: []string{"STOP"}, DisablePing: true,
			}
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.frames + "data: [DONE]\n\n"))}
			usage, apiErr := OaiStreamHandler(c, info, resp)
			require.Nil(t, apiErr)
			assert.Equal(t, 9, usage.CompletionTokens)
			var content, finish string
			for line := range strings.SplitSeq(w.Body.String(), "\n") {
				data, ok := strings.CutPrefix(line, "data: ")
				if !ok || data == "[DONE]" {
					continue
				}
				var frame struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
						Finish string `json:"finish_reason"`
					} `json:"choices"`
				}
				require.NoError(t, common.UnmarshalJsonStr(data, &frame))
				for _, choice := range frame.Choices {
					content += choice.Delta.Content
					if choice.Finish != "" {
						finish = choice.Finish
					}
				}
			}
			assert.Equal(t, tc.want, content)
			assert.Equal(t, tc.finish, finish)
		})
	}
}

func TestVisibleStopMissingUsageCountsFullUpstreamOutput(t *testing.T) {
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	frame, err := common.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"index": 0, "finish_reason": "length",
			"delta": map[string]string{"content": "aSTOP" + strings.Repeat(" retained upstream text", 20)},
		}},
	})
	require.NoError(t, err)
	var tokens []int
	for _, stops := range [][]string{nil, {"STOP"}} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		info := &relaycommon.RelayInfo{
			ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI, UpstreamModelName: "kimi-k3"},
			RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions,
			VisibleStop: stops, DisablePing: true,
		}
		resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: " + string(frame) + "\n\ndata: [DONE]\n\n"))}
		usage, apiErr := OaiStreamHandler(c, info, resp)
		require.Nil(t, apiErr)
		tokens = append(tokens, usage.CompletionTokens)
		if len(stops) > 0 {
			assert.NotContains(t, w.Body.String(), "retained upstream text")
			assert.Contains(t, w.Body.String(), `"content":"a"`)
		}
	}
	assert.Positive(t, tokens[0])
	assert.Equal(t, tokens[0], tokens[1], "missing usage estimation must use original full output")
}

type cancelStopBody struct {
	prefix      io.Reader
	readStarted chan struct{}
	closed      chan struct{}
	readOnce    sync.Once
	closeOnce   sync.Once
}

func (b *cancelStopBody) Read(p []byte) (int, error) {
	if n, err := b.prefix.Read(p); err != io.EOF {
		return n, err
	}
	b.readOnce.Do(func() { close(b.readStarted) })
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *cancelStopBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

type observeStopWriter struct {
	*httptest.ResponseRecorder
	written chan struct{}
	once    sync.Once
}

func (w *observeStopWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(body)
	w.once.Do(func() { close(w.written) })
	return n, err
}

func TestVisibleStopClientCancellationClosesUpstream(t *testing.T) {
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &observeStopWriter{ResponseRecorder: httptest.NewRecorder(), written: make(chan struct{})}
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	body := &cancelStopBody{
		readStarted: make(chan struct{}), closed: make(chan struct{}),
		prefix: strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"aSTOPb\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"draining\"},\"finish_reason\":null}]}\n\n"),
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI, UpstreamModelName: "kimi-k3"},
		RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions,
		VisibleStop: []string{"STOP"}, DisablePing: true,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = OaiStreamHandler(c, info, &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body})
	}()
	select {
	case <-body.readStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream read did not start")
	}
	select {
	case <-w.written:
	case <-time.After(5 * time.Second):
		t.Fatal("matched content was not sent while upstream remains open")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not release handler")
	}
	select {
	case <-body.closed:
	default:
		t.Fatal("upstream body not closed")
	}
	assert.Contains(t, w.Body.String(), `"content":"a"`)
	assert.NotContains(t, w.Body.String(), "[DONE]", "do not write final response after cancellation")
	assert.NotContains(t, w.Body.String(), "draining")
}
