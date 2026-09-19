package ai_router

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"strings"
	"sync"
	"testing"
)

const detectorChatPath = "/v1/chat/completions"

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
