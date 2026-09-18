package video_preprocess

import (
	"bytes"
	"context"
	"encoding/base64"
	"sort"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// jpegDataURLPrefix is the Data URL prefix every exported frame is written
// with. Frames arrive JPEG-encoded from the media engine, so the rewriter only
// base64-encodes them; it never re-encodes the image itself.
const jpegDataURLPrefix = "data:image/jpeg;base64,"

// Frame is one exported JPEG frame of a video, in video-timeline order at the
// position the sampler chose. The rewriter treats JPEG as an opaque byte string
// and only measures it.
type Frame struct {
	// Timestamp is the frame position in the source video timeline.
	Timestamp time.Duration
	// JPEG is the encoded frame, including its container header.
	JPEG []byte
}

// FrameSet holds the frames extracted from one video of the request, matched to
// VideoPart.Index.
type FrameSet struct {
	// VideoIndex is the 1-based video number reported by ScanChatRequest.
	VideoIndex int
	Frames     []Frame
}

// RewriteLimits are the caller-supplied budgets for one rewrite. The rewriter
// owns no configuration; the caller reads these from its own settings and
// passes the effective values in. A zero budget means "not enforced" and exists
// only for tests and for callers that deliberately run unbounded.
type RewriteLimits struct {
	// MaxImagesPerRequest caps the images the request carries after rewriting,
	// counting the images that were already there.
	MaxImagesPerRequest int
	// MaxOutputImageBytes caps the total encoded size of the frames added by
	// this rewrite.
	MaxOutputImageBytes int64
	// MaxRewrittenBodyBytes caps the size of the rewritten request body.
	MaxRewrittenBodyBytes int64
	// MaxRequestBytes caps the incoming request body. Exceeding it is reported
	// as CodeTooLarge so the middleware can answer 413.
	MaxRequestBytes int64
}

// RewriteInput is the complete input of one rewrite.
type RewriteInput struct {
	// Body is the original Chat Completions request.
	Body []byte
	// Layout is the scan of exactly that body.
	Layout *RequestLayout
	// Frames carries one set per video of the layout.
	Frames []FrameSet
	// IncludeTimestamps controls whether each frame is preceded by its
	// timestamp marker. The per-video header is written either way.
	IncludeTimestamps bool
	Limits            RewriteLimits
}

// RewriteResult describes a successful rewrite.
type RewriteResult struct {
	// Body is the rewritten request, valid JSON of the same content type.
	Body []byte
	// Videos is the number of replaced video parts.
	Videos int
	// ExtractedFrames is the number of frames written into the request.
	ExtractedFrames int
	// ExistingImages is the number of image parts the request already had.
	ExistingImages int
	// TotalImages is ExistingImages plus ExtractedFrames: the image count the
	// AI Router detector will see.
	TotalImages int
	// OutputImageBytes is the total encoded size of the written frames.
	OutputImageBytes int64
	// OriginalBodyBytes and RewrittenBodyBytes describe the size effect of the
	// rewrite, including the removed video Base64.
	OriginalBodyBytes  int64
	RewrittenBodyBytes int64
}

// Rewrite replaces every located `video_url` part with its video header, one
// timestamped image part per frame and the corresponding JPEG Data URL, in
// place. Customer text, tools, existing images, unknown extension fields and
// the relative order of all of them are preserved, and the original Base64 is
// removed from the result.
//
// Frames are spliced into the original JSON from the last video backwards, so a
// part never has to be re-encoded and no parser round trip can reorder or drop
// unknown fields. The input body is never modified.
func Rewrite(ctx context.Context, input RewriteInput) (*RewriteResult, error) {
	if err := timeoutOrCancel(ctx); err != nil {
		return nil, err
	}
	layout := input.Layout
	if layout == nil {
		return nil, newError(CodeRewriteFailed, "request video layout is required")
	}
	if len(layout.Videos) == 0 {
		return nil, newError(CodeRewriteFailed, "request contains no video part to rewrite")
	}
	if len(input.Body) != layout.bodyBytes {
		return nil, newError(CodeRewriteFailed, "request body does not match its video layout")
	}
	if max := input.Limits.MaxRequestBytes; max > 0 && int64(len(input.Body)) > max {
		return nil, newError(CodeTooLarge, "request body exceeds the configured video preprocessing limit")
	}
	if err := validateLimits(input.Limits); err != nil {
		return nil, err
	}

	framesByVideo, err := indexFrames(layout, input.Frames)
	if err != nil {
		return nil, err
	}

	// Budget check first: a request that cannot fit inside the image budget is
	// rejected before a single byte of output is built.
	extracted := 0
	for _, video := range layout.Videos {
		extracted += len(framesByVideo[video.Index])
	}
	result := &RewriteResult{
		Videos:             len(layout.Videos),
		ExtractedFrames:    extracted,
		ExistingImages:     layout.ExistingImages,
		TotalImages:        layout.ExistingImages + extracted,
		OriginalBodyBytes:  int64(len(input.Body)),
		RewrittenBodyBytes: int64(len(input.Body)),
	}
	if max := input.Limits.MaxImagesPerRequest; max > 0 && result.TotalImages > max {
		return nil, newError(CodeImageLimitExceeded,
			"request would carry "+strconv.Itoa(result.TotalImages)+" images, above the configured limit of "+strconv.Itoa(max))
	}

	segments := make([]replacement, 0, len(layout.Videos))
	for _, video := range layout.Videos {
		marker, err := buildVideoMarker(video, framesByVideo[video.Index], input.IncludeTimestamps)
		if err != nil {
			return nil, err
		}
		for _, frame := range framesByVideo[video.Index] {
			result.OutputImageBytes += int64(len(frame.JPEG))
		}
		segments = append(segments, replacement{
			offset: video.offset,
			length: video.length,
			text:   marker,
		})
	}
	if max := input.Limits.MaxOutputImageBytes; max > 0 && result.OutputImageBytes > max {
		return nil, newError(CodeOutputTooLarge, "extracted frames exceed the configured output image budget")
	}

	// Splice from the end so earlier offsets stay valid.
	sort.Slice(segments, func(i, j int) bool { return segments[i].offset > segments[j].offset })
	body := input.Body
	for _, segment := range segments {
		if segment.offset < 0 || segment.offset+segment.length > len(body) {
			return nil, newError(CodeRewriteFailed, "request video position is out of range")
		}
		replaced := make([]byte, 0, len(body)-segment.length+len(segment.text))
		replaced = append(replaced, body[:segment.offset]...)
		replaced = append(replaced, segment.text...)
		replaced = append(replaced, body[segment.offset+segment.length:]...)
		body = replaced
	}

	if max := input.Limits.MaxRewrittenBodyBytes; max > 0 && int64(len(body)) > max {
		return nil, newError(CodeOutputTooLarge, "rewritten request body exceeds the configured body budget")
	}
	result.Body = body
	result.RewrittenBodyBytes = int64(len(body))
	return result, nil
}

// replacement is one in-place video substitution.
type replacement struct {
	offset int
	length int
	text   []byte
}

// indexFrames maps each video number onto its frames, rejecting missing,
// duplicated and unknown frame sets. Every video of the request must produce at
// least one frame; silently dropping a video would change what the model sees.
func indexFrames(layout *RequestLayout, sets []FrameSet) (map[int][]Frame, error) {
	byVideo := make(map[int][]Frame, len(sets))
	for _, set := range sets {
		if _, duplicate := byVideo[set.VideoIndex]; duplicate {
			return nil, newError(CodeRewriteFailed, "duplicate frame set for video "+strconv.Itoa(set.VideoIndex))
		}
		if set.VideoIndex < 1 || set.VideoIndex > len(layout.Videos) {
			return nil, newError(CodeRewriteFailed, "frame set for unknown video "+strconv.Itoa(set.VideoIndex))
		}
		if len(set.Frames) == 0 {
			return nil, newError(CodeRewriteFailed, "video "+strconv.Itoa(set.VideoIndex)+" produced no frames")
		}
		byVideo[set.VideoIndex] = set.Frames
	}
	for _, video := range layout.Videos {
		if _, ok := byVideo[video.Index]; !ok {
			return nil, newError(CodeRewriteFailed, "no frames were extracted for video "+strconv.Itoa(video.Index))
		}
	}
	return byVideo, nil
}

// buildVideoMarker renders what replaces one video_url part: the header text,
// then for every frame an optional timestamp marker followed by the JPEG Data
// URL part.
//
// The result is the comma-separated run of parts without an enclosing array,
// because the part is always an element of the message content array — the
// caller splices it exactly where the video part was, so the parts stay
// siblings of the customer's text and images instead of becoming a nested
// array that providers would reject.
func buildVideoMarker(video VideoPart, frames []Frame, includeTimestamps bool) ([]byte, error) {
	number := strconv.Itoa(video.Index)
	// The frame count is rendered once: it is the denominator of every marker
	// and must match the number of images that actually follow.
	total := strconv.Itoa(len(frames))

	// The parts are collected in render order, so a marker and its image are
	// always adjacent and frames stay in timeline order.
	parts := make([][]byte, 0, 1+2*len(frames))
	appendPart := func(part map[string]any) error {
		rendered, err := common.Marshal(part)
		if err != nil {
			return wrapError(CodeRewriteFailed, "failed to render a rewritten content part", err)
		}
		parts = append(parts, rendered)
		return nil
	}

	if err := appendPart(map[string]any{
		"type": contentTypeText,
		"text": "以下图片来自视频 " + number + "，按时间顺序动态采样。",
	}); err != nil {
		return nil, err
	}
	for frameIndex, frame := range frames {
		if len(frame.JPEG) == 0 {
			return nil, newError(CodeRewriteFailed, "video "+number+" produced an empty frame")
		}
		if includeTimestamps {
			if err := appendPart(map[string]any{
				"type": contentTypeText,
				"text": "视频 " + number +
					"，帧 " + strconv.Itoa(frameIndex+1) + "/" + total +
					"，时间戳 " + formatTimestamp(frame.Timestamp),
			}); err != nil {
				return nil, err
			}
		}
		if err := appendPart(map[string]any{
			"type":      contentTypeImageURL,
			"image_url": map[string]any{"url": jpegDataURLPrefix + base64.StdEncoding.EncodeToString(frame.JPEG)},
		}); err != nil {
			return nil, err
		}
	}
	return bytes.Join(parts, []byte(",")), nil
}

// formatTimestamp renders a frame position as mm:ss.mmm, growing the minutes
// field without bound for videos longer than an hour.
func formatTimestamp(offset time.Duration) string {
	if offset < 0 {
		offset = 0
	}
	minutes := offset / time.Minute
	seconds := (offset % time.Minute) / time.Second
	millis := (offset % time.Second) / time.Millisecond
	return pad2(int(minutes)) + ":" + pad2(int(seconds)) + "." + pad3(int(millis))
}

func pad2(value int) string {
	if value < 10 {
		return "0" + strconv.Itoa(value)
	}
	return strconv.Itoa(value)
}

func pad3(value int) string {
	switch {
	case value < 10:
		return "00" + strconv.Itoa(value)
	case value < 100:
		return "0" + strconv.Itoa(value)
	default:
		return strconv.Itoa(value)
	}
}

// validateLimits rejects non-sensical budgets before any work is done.
func validateLimits(limits RewriteLimits) error {
	if limits.MaxImagesPerRequest < 0 {
		return invalidConfig("MaxImagesPerRequest must not be negative")
	}
	if limits.MaxOutputImageBytes < 0 {
		return invalidConfig("MaxOutputImageBytes must not be negative")
	}
	if limits.MaxRewrittenBodyBytes < 0 {
		return invalidConfig("MaxRewrittenBodyBytes must not be negative")
	}
	if limits.MaxRequestBytes < 0 {
		return invalidConfig("MaxRequestBytes must not be negative")
	}
	return nil
}
