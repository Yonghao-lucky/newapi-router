package video_preprocess

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	ai_router "github.com/QuantumNous/new-api/service/ai_router"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// jpegBytes is a stand-in for an exported frame. The rewriter treats JPEG as an
// opaque payload, so a short tagged slice is enough to prove placement, order
// and byte accounting.
func jpegBytes(tag string) []byte { return []byte{0xFF, 0xD8, 0xFF, 0xE0, 'j', 'p', 'g', tag[0]} }

func frameSet(videoIndex int, frames ...Frame) FrameSet {
	return FrameSet{VideoIndex: videoIndex, Frames: frames}
}

func frame(ms int, jpeg []byte) Frame {
	return Frame{Timestamp: time.Duration(ms) * time.Millisecond, JPEG: jpeg}
}

// textAndVideoBody returns a request with a leading text part, a video, and a
// trailing text part, so ordering can be asserted directly.
const textAndVideoBody = `{"model":"kimi-k3","stream":true,"unknown_top":{"keep":true},"messages":[` +
	`{"role":"user","content":[{"type":"text","text":"before"},{"type":"video_url","video_url":{"url":"data:video/mp4;base64,VIDEOONE"}},{"type":"text","text":"after"}]}` +
	`],"unknown_tail":[1,2]}`

func rewriteBody(t *testing.T, body string, frames []FrameSet) *RewriteResult {
	t.Helper()
	layout, err := ScanChatRequest([]byte(body))
	require.NoError(t, err)
	result, err := Rewrite(context.Background(), RewriteInput{
		Body:              []byte(body),
		Layout:            layout,
		Frames:            frames,
		IncludeTimestamps: true,
	})
	require.NoError(t, err)
	return result
}

func TestScanChatRequestLocatesVideosAndImages(t *testing.T) {
	body := `{"model":"m","messages":[` +
		`{"role":"user","content":"plain text"},` +
		`{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,IMG1"}},` +
		`{"type":"video_url","video_url":{"url":"data:video/mp4;base64,VIDEOONE"}},` +
		`{"type":"text","text":"spoken"}]},` +
		`{"role":"user","content":[` +
		`{"type":"input_image","image_url":{"url":"data:image/png;base64,IMG2"}},` +
		`{"type":"video_url","video_url":"data:video/webm;base64,VIDEOTWO"}]}]}`

	layout, err := ScanChatRequest([]byte(body))
	require.NoError(t, err)
	require.Len(t, layout.Videos, 2)
	assert.Equal(t, 2, layout.ExistingImages)

	assert.Equal(t, VideoPart{Index: 1, MessageIndex: 1, PartIndex: 1, URLBytes: int64(len("data:video/mp4;base64,VIDEOONE"))}, VideoPart{
		Index: layout.Videos[0].Index, MessageIndex: layout.Videos[0].MessageIndex,
		PartIndex: layout.Videos[0].PartIndex, URLBytes: layout.Videos[0].URLBytes,
	})
	assert.Equal(t, 2, layout.Videos[1].Index)
	assert.Equal(t, 2, layout.Videos[1].MessageIndex)
	assert.Equal(t, int64(len("data:video/webm;base64,VIDEOTWO")), layout.Videos[1].URLBytes)

	// Locations must be exact slices of the scanned body, which is what makes
	// the in-place splice safe.
	for _, video := range layout.Videos {
		raw := body[video.offset : video.offset+video.length]
		assert.True(t, strings.HasPrefix(raw, `{"type":"video_url"`), raw)
		assert.True(t, strings.HasSuffix(raw, `}`), raw)
	}
}

func TestScanChatRequestWithoutVideo(t *testing.T) {
	for _, body := range []string{
		`{"model":"m"}`,
		`{"model":"m","messages":"not an array"}`,
		`{"model":"m","messages":[{"role":"user","content":[]}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`,
	} {
		layout, err := ScanChatRequest([]byte(body))
		require.NoError(t, err)
		assert.Empty(t, layout.Videos, body)
	}

	_, err := ScanChatRequest([]byte(`{"model":`))
	assert.Equal(t, CodeRewriteFailed, CodeOf(err))
}

func TestRewriteReplacesVideoInPlaceAndKeepsOrderAndUnknownFields(t *testing.T) {
	result := rewriteBody(t, textAndVideoBody, []FrameSet{
		frameSet(1, frame(200, jpegBytes("A")), frame(1000, jpegBytes("B"))),
	})

	assert.Equal(t, 1, result.Videos)
	assert.Equal(t, 2, result.ExtractedFrames)
	assert.Equal(t, 0, result.ExistingImages)
	assert.Equal(t, 2, result.TotalImages)
	assert.Equal(t, int64(len(jpegBytes("A"))+len(jpegBytes("B"))), result.OutputImageBytes)

	// The original Base64 is gone and neither the body nor the rewrite shrank
	// the request by accident.
	assert.NotContains(t, string(result.Body), "VIDEOONE")

	parts := contentPartsOf(t, string(result.Body), 0)
	require.Len(t, parts, 7)
	assert.Equal(t, "before", parts[0].Get("text").String())
	assert.Equal(t, "以下图片来自视频 1，按时间顺序动态采样。", parts[1].Get("text").String())
	assert.Equal(t, "视频 1，帧 1/2，时间戳 00:00.200", parts[2].Get("text").String())
	assert.Equal(t, "image_url", parts[3].Get("type").String())
	assert.Equal(t,
		jpegDataURLPrefix+base64.StdEncoding.EncodeToString(jpegBytes("A")),
		parts[3].Get("image_url.url").String())
	assert.Equal(t, "视频 1，帧 2/2，时间戳 00:01.000", parts[4].Get("text").String())
	assert.Equal(t,
		jpegDataURLPrefix+base64.StdEncoding.EncodeToString(jpegBytes("B")),
		parts[5].Get("image_url.url").String())
	// The trailing customer text keeps its position after the video content.
	assert.Equal(t, "text", parts[6].Get("type").String())
	assert.Equal(t, "after", parts[6].Get("text").String())

	// Unknown top-level extension fields are preserved as they were sent.
	rewritten := string(result.Body)
	assert.True(t, gjson.Get(rewritten, "stream").Bool())
	assert.True(t, gjson.Get(rewritten, "unknown_top.keep").Bool())
	assert.Equal(t, "[1,2]", gjson.Get(rewritten, "unknown_tail").Raw)
	assert.Equal(t, "kimi-k3", gjson.Get(rewritten, "model").String())
}

func TestRewriteMultipleVideosAndMixedImages(t *testing.T) {
	body := `{"model":"m","messages":[` +
		`{"role":"user","content":[` +
		`{"type":"text","text":"one"},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,EXISTING"}},` +
		`{"type":"video_url","video_url":{"url":"data:video/mp4;base64,V1"}}]},` +
		`{"role":"user","content":[` +
		`{"type":"video_url","video_url":{"url":"data:video/mp4;base64,V2"}},` +
		`{"type":"text","text":"two"}]}]}`

	result := rewriteBody(t, body, []FrameSet{
		frameSet(1, frame(0, jpegBytes("A"))),
		frameSet(2, frame(500, jpegBytes("B")), frame(900, jpegBytes("C"))),
	})

	assert.Equal(t, 2, result.Videos)
	assert.Equal(t, 3, result.ExtractedFrames)
	assert.Equal(t, 1, result.ExistingImages)
	assert.Equal(t, 4, result.TotalImages)
	assert.NotContains(t, string(result.Body), "base64,V1")
	assert.NotContains(t, string(result.Body), "base64,V2")
	// The pre-existing image is untouched.
	assert.Contains(t, string(result.Body), "data:image/png;base64,EXISTING")

	first := contentPartsOf(t, string(result.Body), 0)
	assert.Equal(t, "one", first[0].Get("text").String())
	assert.Equal(t, "image_url", first[1].Get("type").String())
	assert.Equal(t, "以下图片来自视频 1，按时间顺序动态采样。", first[2].Get("text").String())
	assert.Equal(t, "视频 1，帧 1/1，时间戳 00:00.000", first[3].Get("text").String())

	second := contentPartsOf(t, string(result.Body), 1)
	assert.Equal(t, "以下图片来自视频 2，按时间顺序动态采样。", second[0].Get("text").String())
	assert.Equal(t, "视频 2，帧 1/2，时间戳 00:00.500", second[1].Get("text").String())
	assert.Equal(t, "视频 2，帧 2/2，时间戳 00:00.900", second[3].Get("text").String())
	// The message's trailing text stays last.
	assert.Equal(t, "two", second[5].Get("text").String())
}

func TestRewriteTimestampToggle(t *testing.T) {
	layout, err := ScanChatRequest([]byte(textAndVideoBody))
	require.NoError(t, err)

	result, err := Rewrite(context.Background(), RewriteInput{
		Body:              []byte(textAndVideoBody),
		Layout:            layout,
		Frames:            []FrameSet{frameSet(1, frame(200, jpegBytes("A")), frame(1000, jpegBytes("B")))},
		IncludeTimestamps: false,
	})
	require.NoError(t, err)

	parts := contentPartsOf(t, string(result.Body), 0)
	require.Len(t, parts, 5)
	assert.Equal(t, "以下图片来自视频 1，按时间顺序动态采样。", parts[1].Get("text").String())
	assert.Equal(t, "image_url", parts[2].Get("type").String())
	assert.Equal(t, "image_url", parts[3].Get("type").String())
	assert.Equal(t, "after", parts[4].Get("text").String())
}

func TestFormatTimestamp(t *testing.T) {
	assert.Equal(t, "00:00.000", formatTimestamp(0))
	assert.Equal(t, "00:00.200", formatTimestamp(200*time.Millisecond))
	assert.Equal(t, "02:03.004", formatTimestamp(2*time.Minute+3*time.Second+4*time.Millisecond))
	assert.Equal(t, "61:01.500", formatTimestamp(61*time.Minute+1500*time.Millisecond))
}

func TestRewriteBudgets(t *testing.T) {
	layout, err := ScanChatRequest([]byte(textAndVideoBody))
	require.NoError(t, err)

	// Two frames in one video, plus the one image of the mixed variant below.
	base := RewriteInput{
		Body:              []byte(textAndVideoBody),
		Layout:            layout,
		Frames:            []FrameSet{frameSet(1, frame(0, jpegBytes("A")), frame(1, jpegBytes("B")))},
		IncludeTimestamps: true,
	}
	mixedBody := `{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,E"}},` +
		`{"type":"video_url","video_url":{"url":"data:video/mp4;base64,V"}}]}]}`
	mixedLayout, err := ScanChatRequest([]byte(mixedBody))
	require.NoError(t, err)
	mixed := base
	mixed.Body = []byte(mixedBody)
	mixed.Layout = mixedLayout

	// budget is one named limit applied to one named input.
	type budget struct {
		name        string
		input       RewriteInput
		limit       RewriteLimits
		wantCode    string
		wantTotal   int
		description string
	}

	tests := []budget{
		{
			name:      "image budget admits the exact count",
			input:     base,
			limit:     RewriteLimits{MaxImagesPerRequest: 2},
			wantTotal: 2,
		},
		{
			name:        "image budget rejects one over",
			input:       base,
			limit:       RewriteLimits{MaxImagesPerRequest: 1},
			wantCode:    CodeImageLimitExceeded,
			description: "request would carry 2 images, above the configured limit of 1",
		},
		{
			// The one existing image plus two frames is three, so the budget
			// counts what the request carries, not only what was extracted.
			name:        "existing images count against the budget",
			input:       mixed,
			limit:       RewriteLimits{MaxImagesPerRequest: 2},
			wantCode:    CodeImageLimitExceeded,
			description: "request would carry 3 images, above the configured limit of 2",
		},
		{
			name:     "output image budget",
			input:    base,
			limit:    RewriteLimits{MaxOutputImageBytes: int64(len(jpegBytes("A")))},
			wantCode: CodeOutputTooLarge,
		},
		{
			name:     "rewritten body budget",
			input:    base,
			limit:    RewriteLimits{MaxRewrittenBodyBytes: int64(len(textAndVideoBody))},
			wantCode: CodeOutputTooLarge,
		},
		{
			name:     "incoming request budget",
			input:    base,
			limit:    RewriteLimits{MaxRequestBytes: int64(len(textAndVideoBody)) - 1},
			wantCode: CodeTooLarge,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.input
			input.Limits = tc.limit
			result, rewriteErr := Rewrite(context.Background(), input)
			if tc.wantCode == "" {
				require.NoError(t, rewriteErr)
				assert.Equal(t, tc.wantTotal, result.TotalImages)
				return
			}
			require.Error(t, rewriteErr)
			assert.Equal(t, tc.wantCode, CodeOf(rewriteErr))
			if tc.description != "" {
				assert.Equal(t, tc.description, MessageOf(rewriteErr))
			}
			// Operator-facing messages never leak payload bytes.
			assert.NotContains(t, rewriteErr.Error(), "base64")
		})
	}

	t.Run("stale layout is rejected", func(t *testing.T) {
		input := base
		input.Body = []byte(textAndVideoBody + " ")
		_, rewriteErr := Rewrite(context.Background(), input)
		require.Error(t, rewriteErr)
		assert.Equal(t, CodeRewriteFailed, CodeOf(rewriteErr))
	})
}

func TestRewriteRejectsIncompleteFrameSets(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":[` +
		`{"type":"video_url","video_url":{"url":"data:video/mp4;base64,V1"}},` +
		`{"type":"video_url","video_url":{"url":"data:video/mp4;base64,V2"}}]}]}`
	layout, err := ScanChatRequest([]byte(body))
	require.NoError(t, err)

	tests := []struct {
		name   string
		frames []FrameSet
	}{
		{name: "missing video", frames: []FrameSet{frameSet(1, frame(0, jpegBytes("A")))}},
		{name: "unknown video", frames: []FrameSet{
			frameSet(1, frame(0, jpegBytes("A"))), frameSet(2, frame(0, jpegBytes("B"))), frameSet(3, frame(0, jpegBytes("C"))),
		}},
		{name: "duplicate video", frames: []FrameSet{frameSet(1, frame(0, jpegBytes("A"))), frameSet(1, frame(0, jpegBytes("B")))}},
		{name: "empty video", frames: []FrameSet{frameSet(1, frame(0, jpegBytes("A"))), frameSet(2)}},
		{name: "empty frame", frames: []FrameSet{frameSet(1, frame(0, jpegBytes("A"))), frameSet(2, Frame{Timestamp: 0})}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, rewriteErr := Rewrite(context.Background(), RewriteInput{
				Body:   []byte(body),
				Layout: layout,
				Frames: tc.frames,
			})
			require.Error(t, rewriteErr)
			assert.Equal(t, CodeRewriteFailed, CodeOf(rewriteErr))
		})
	}
}

func TestRewriteIsDetectedAsImageByAIRouter(t *testing.T) {
	body := `{"model":"kimi-k3","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"describe"},` +
		`{"type":"video_url","video_url":{"url":"data:video/mp4;base64,VIDEOONE"}}]}]}`
	result := rewriteBody(t, body, []FrameSet{frameSet(1, frame(0, jpegBytes("A")), frame(700, jpegBytes("B")))})

	demand, err := ai_router.DetectRequest("/v1/chat/completions", strings.NewReader(string(result.Body)))
	require.NoError(t, err)
	assert.Equal(t, ai_router.ModalityImage, demand.Modality)
	assert.Equal(t, "kimi-k3", demand.Model)
	assert.Empty(t, demand.UnknownContentTypes)

	// The original body must still be detected as video: this is what the
	// middleware uses to decide that preprocessing is needed at all.
	original, err := ai_router.DetectRequest("/v1/chat/completions", strings.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, ai_router.ModalityVideo, original.Modality)
}

func TestInstallRewrittenBodyReplacesStorage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("replayable after install", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(textAndVideoBody))
		original, err := common.GetBodyStorage(c)
		require.NoError(t, err)

		rewritten := rewriteBody(t, textAndVideoBody, []FrameSet{frameSet(1, frame(0, jpegBytes("A")))})

		installed, err := InstallRewrittenBody(c, rewritten.Body)
		require.NoError(t, err)
		assert.Equal(t, int64(len(rewritten.Body)), installed.Bytes)
		assert.Equal(t, int64(len(rewritten.Body)), c.Request.ContentLength)

		// The previous storage was closed by the swap.
		_, err = original.Bytes()
		assert.ErrorIs(t, err, common.ErrStorageClosed)

		// Every subsequent read goes through the installed storage: the AI
		// Router detector, the relays and provider attempts all replay it.
		for i := range 3 {
			reader, readerErr := installed.Storage().NewReader()
			require.NoError(t, readerErr)
			raw, readErr := io.ReadAll(reader)
			require.NoError(t, readErr)
			reader.Close()
			assert.Equal(t, rewritten.Body, raw, "replay %d", i)
		}

		stored, err := common.GetBodyStorage(c)
		require.NoError(t, err)
		assert.Same(t, installed.Storage(), stored)

		demand, err := ai_router.DetectRequest(c.Request.URL.Path, strings.NewReader(string(rewritten.Body)))
		require.NoError(t, err)
		assert.Equal(t, ai_router.ModalityImage, demand.Modality)

		common.CleanupBodyStorage(c)
		_, err = stored.Bytes()
		assert.ErrorIs(t, err, common.ErrStorageClosed)
	})

	t.Run("request that was never buffered", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(textAndVideoBody))
		rewritten := rewriteBody(t, textAndVideoBody, []FrameSet{frameSet(1, frame(0, jpegBytes("A")))})

		installed, err := InstallRewrittenBody(c, rewritten.Body)
		require.NoError(t, err)
		raw, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		assert.Equal(t, rewritten.Body, raw)
		assert.Equal(t, installed.Storage(), mustStorage(t, c))
	})

	t.Run("invalid rewrite leaves the request untouched", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(textAndVideoBody))
		original, err := common.GetBodyStorage(c)
		require.NoError(t, err)

		installed, err := InstallRewrittenBody(c, []byte(`{"model":`))
		require.Error(t, err)
		assert.Nil(t, installed)
		assert.Equal(t, CodeRewriteFailed, CodeOf(err))

		// Failure is atomic: the original storage is still installed, still
		// open and still replayable.
		assert.Same(t, original, mustStorage(t, c))
		raw, err := original.Bytes()
		require.NoError(t, err)
		assert.Equal(t, textAndVideoBody, string(raw))
		assert.Equal(t, int64(len(textAndVideoBody)), c.Request.ContentLength)
	})
}

func mustStorage(t *testing.T, c *gin.Context) common.BodyStorage {
	t.Helper()
	storage, err := common.GetBodyStorage(c)
	require.NoError(t, err)
	return storage
}

// contentPartsOf returns the content parts of one message of a rewritten body.
func contentPartsOf(t *testing.T, body string, messageIndex int) []gjson.Result {
	t.Helper()
	message := gjson.Get(body, "messages."+strconv.Itoa(messageIndex))
	require.True(t, message.Exists(), body)
	return message.Get("content").Array()
}
