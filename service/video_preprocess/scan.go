package video_preprocess

import (
	"github.com/tidwall/gjson"
)

// Chat Completions content part types. The wire strings are repeated here
// rather than imported from the routing detector so that the rewriter stays a
// self-contained JSON transform: the detector's vocabulary is a routing
// concern, this one is the OpenAI request format.
const (
	contentTypeText       = "text"
	contentTypeInputText  = "input_text"
	contentTypeVideoURL   = "video_url"
	contentTypeImageURL   = "image_url"
	contentTypeImage      = "image"
	contentTypeInputImage = "input_image"
)

// VideoPart is one `video_url` content part located inside a Chat Completions
// request. Its Index is the 1-based video number used in the rewritten marker
// text; MessageIndex and PartIndex state where the part sits in the original
// request so operators can follow a rewrite without seeing any payload.
//
// Positions are only meaningful for the exact body that was scanned.
type VideoPart struct {
	// Index is the 1-based video number within the request.
	Index int
	// MessageIndex is the 0-based index of the owning messages[] element.
	MessageIndex int
	// PartIndex is the 0-based index of the part in messages[].content[].
	PartIndex int
	// URLBytes is the raw JSON payload length of the video_url.url value, i.e.
	// the number of bytes the rewrite removes from the request for this video.
	URLBytes int64

	// offset and length locate the part value inside the scanned body so the
	// rewrite can splice it in place without re-marshalling the whole request.
	offset int
	length int
}

// RequestLayout is the result of locating video content in a request body. It
// holds positions only: no media, no prompt text and no Base64 is retained.
type RequestLayout struct {
	// Videos lists the video parts in request order.
	Videos []VideoPart
	// ExistingImages counts the image parts already present in the request.
	ExistingImages int

	// bodyBytes is the length of the scanned body; Rewrite refuses a body of a
	// different length so stale offsets can never corrupt a request.
	bodyBytes int
}

// ScanChatRequest locates every `video_url` content part and counts the
// existing image parts of a Chat Completions request body.
//
// The body must be valid JSON. A request without a messages array, without
// message content arrays, or without any video reports an empty layout and no
// error, which is how a caller decides to bypass preprocessing.
func ScanChatRequest(body []byte) (*RequestLayout, error) {
	if !gjson.ValidBytes(body) {
		return nil, newError(CodeRewriteFailed, "request body is not valid JSON")
	}

	layout := &RequestLayout{bodyBytes: len(body)}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return layout, nil
	}

	for messageIndex, message := range messages.Array() {
		if !message.IsObject() {
			continue
		}
		content := message.Get("content")
		if !content.IsArray() {
			continue
		}
		for partIndex, part := range content.Array() {
			if !part.IsObject() {
				continue
			}
			switch part.Get("type").String() {
			case contentTypeVideoURL:
				layout.Videos = append(layout.Videos, VideoPart{
					Index:        len(layout.Videos) + 1,
					MessageIndex: messageIndex,
					PartIndex:    partIndex,
					URLBytes:     rawURLBytes(videoURLValue(part)),
					offset:       part.Index,
					length:       len(part.Raw),
				})
			case contentTypeImageURL, contentTypeImage, contentTypeInputImage:
				layout.ExistingImages++
			}
		}
	}
	return layout, nil
}

// videoURLValue resolves the url of a `video_url` part. The OpenAI format uses
// an object, but the sibling detector also accepts a bare string, so both
// spellings are located and rewritten.
func videoURLValue(part gjson.Result) gjson.Result {
	value := part.Get("video_url")
	switch {
	case value.IsObject():
		return value.Get("url")
	case value.Type == gjson.String:
		return value
	default:
		return gjson.Result{}
	}
}

// rawURLBytes reports the payload length of a JSON string value, excluding the
// enclosing quotes. Values that are not strings report their raw length so the
// removed-byte accounting stays truthful for malformed parts.
func rawURLBytes(url gjson.Result) int64 {
	raw := url.Raw
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		return int64(len(raw) - 2)
	}
	return int64(len(raw))
}
