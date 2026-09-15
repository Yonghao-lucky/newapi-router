package ai_router

import (
	"errors"
	"fmt"
	"io"
)

const (
	DetectorFamilyOpenAIChat = "openai_chat"

	chatCompletionsPath = "/v1/chat/completions"

	contentTypeVideoURL   = "video_url"
	contentTypeImageURL   = "image_url"
	contentTypeImage      = "image"
	contentTypeInputImage = "input_image"
	contentTypeText       = "text"
	contentTypeInputText  = "input_text"
)

var (
	ErrUnsupportedDetectorPath = errors.New("unsupported ai router detector path")
	errNilBody                 = errors.New("request body is required")
)

// DetectRequest analyzes a replayable request body without consuming,
// modifying, decoding media, or retaining any payload data.
func DetectRequest(path string, body io.Reader) (RouteDemand, error) {
	if path != chatCompletionsPath {
		return RouteDemand{}, fmt.Errorf("%w: %s", ErrUnsupportedDetectorPath, path)
	}
	if body == nil {
		return RouteDemand{}, errNilBody
	}

	if seeker, ok := body.(io.Seeker); ok {
		start, err := seeker.Seek(0, io.SeekCurrent)
		if err != nil {
			return RouteDemand{}, err
		}
		defer func() {
			_, _ = seeker.Seek(start, io.SeekStart)
		}()
	}

	return detectOpenAIChat(body)
}
