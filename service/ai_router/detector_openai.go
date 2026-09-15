package ai_router

import (
	"encoding/json"
	"fmt"
	"io"
)

// detectorState accumulates modality evidence for a single request. It is
// created per call and never shared between requests.
type detectorState struct {
	model               string
	modality            Modality
	hasTools            bool
	unknownContentTypes []string
	unknownTypeSeen     map[string]struct{}
	modelSeen           bool
	videoFound          bool
}

func newDetectorState() *detectorState {
	return &detectorState{
		modality:            ModalityText,
		unknownContentTypes: []string{},
		unknownTypeSeen:     map[string]struct{}{},
	}
}

// stop reports whether the scan can be abandoned. Media payloads are only ever
// reached when the model name has not been seen yet, so the scan normally ends
// at the video discriminator without touching the payload that follows it.
func (state *detectorState) stop() bool {
	return state.videoFound && state.modelSeen
}

// detectOpenAIChat scans a Chat Completions request and reports the routing
// demand.
//
// The body is walked as a JSON token stream rather than decoded into structs,
// so field contents are read only when they carry routing meaning. In the
// normal field order the scan abandons the decoder at the video discriminator
// and never reads the base64 payload at all; base64 is never decoded, sized,
// validated, or retained under any ordering.
func detectOpenAIChat(body io.Reader) (RouteDemand, error) {
	if body == nil {
		return RouteDemand{}, fmt.Errorf("invalid OpenAI Chat request: %w", errNilBody)
	}

	state := newDetectorState()
	if err := scanChatRequest(json.NewDecoder(body), state); err != nil {
		return RouteDemand{}, fmt.Errorf("invalid OpenAI Chat request: %w", err)
	}
	return state.routeDemand(), nil
}

// scanChatRequest walks the top-level object. It returns as soon as the stop
// condition holds, leaving any remainder of the body unread.
func scanChatRequest(decoder *json.Decoder, state *detectorState) error {
	opening, err := decoder.Token()
	if err != nil {
		return err
	}
	if delim, ok := opening.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("expected top-level object")
	}

	for decoder.More() {
		key, err := decodeKey(decoder)
		if err != nil {
			return err
		}
		switch key {
		case "model":
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if name, ok := value.(string); ok {
				state.model = name
				state.modelSeen = true
			}
			if err := skipRemainder(decoder, value); err != nil {
				return err
			}
		case "tools":
			hasTools, err := scanTools(decoder)
			if err != nil {
				return err
			}
			state.hasTools = state.hasTools || hasTools
		case "messages":
			stopped, err := scanMessages(decoder, state)
			if err != nil {
				return err
			}
			if stopped {
				return nil
			}
		default:
			if err := skipValue(decoder); err != nil {
				return err
			}
		}
		if state.stop() {
			return nil
		}
	}
	_, err = decoder.Token()
	return err
}

func scanMessages(decoder *json.Decoder, state *detectorState) (bool, error) {
	opening, err := decoder.Token()
	if err != nil {
		return false, err
	}
	if delim, ok := opening.(json.Delim); !ok || delim != '[' {
		if err := skipRemainder(decoder, opening); err != nil {
			return false, err
		}
		return false, nil
	}

	for decoder.More() {
		message, err := decoder.Token()
		if err != nil {
			return false, err
		}
		delim, ok := message.(json.Delim)
		if !ok || delim != '{' {
			if err := skipRemainder(decoder, message); err != nil {
				return false, err
			}
			continue
		}
		stopped, err := scanMessage(decoder, state)
		if err != nil {
			return false, err
		}
		if stopped {
			return true, nil
		}
	}
	if _, err := decoder.Token(); err != nil {
		return false, err
	}
	return false, nil
}

func scanMessage(decoder *json.Decoder, state *detectorState) (bool, error) {
	for decoder.More() {
		key, err := decodeKey(decoder)
		if err != nil {
			return false, err
		}
		switch key {
		case "content":
			stopped, err := scanContent(decoder, state)
			if err != nil {
				return false, err
			}
			if stopped {
				return true, nil
			}
		case "tools":
			hasTools, err := scanTools(decoder)
			if err != nil {
				return false, err
			}
			state.hasTools = state.hasTools || hasTools
		default:
			if err := skipValue(decoder); err != nil {
				return false, err
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return false, err
	}
	return false, nil
}

// scanContent inspects a message content value without retaining it. A plain
// string is the text baseline; a list is walked part by part; anything else is
// conservatively unknown.
func scanContent(decoder *json.Decoder, state *detectorState) (bool, error) {
	value, err := decoder.Token()
	if err != nil {
		return false, err
	}
	switch typed := value.(type) {
	case nil, string:
		return false, nil
	case json.Delim:
		if typed == '[' {
			return scanContentParts(decoder, state)
		}
		if err := skipRemainder(decoder, value); err != nil {
			return false, err
		}
		state.observeUnknown("")
		return false, nil
	default:
		state.observeUnknown("")
		return false, nil
	}
}

func scanContentParts(decoder *json.Decoder, state *detectorState) (bool, error) {
	for decoder.More() {
		part, err := decoder.Token()
		if err != nil {
			return false, err
		}
		delim, ok := part.(json.Delim)
		if !ok || delim != '{' {
			if err := skipRemainder(decoder, part); err != nil {
				return false, err
			}
			state.observeUnknown("")
			continue
		}
		stopped, err := scanContentPart(decoder, state)
		if err != nil {
			return false, err
		}
		if stopped {
			return true, nil
		}
	}
	if _, err := decoder.Token(); err != nil {
		return false, err
	}
	return false, nil
}

// scanContentPart reads only the discriminator field, so sibling fields such
// as a base64 payload are not read unless the scan must continue to find the
// model name. It reports whether the whole request scan can stop.
func scanContentPart(decoder *json.Decoder, state *detectorState) (bool, error) {
	typeSeen := false
	for decoder.More() {
		key, err := decodeKey(decoder)
		if err != nil {
			return false, err
		}
		if key != "type" {
			if err := skipValue(decoder); err != nil {
				return false, err
			}
			continue
		}
		typeSeen = true
		value, err := decoder.Token()
		if err != nil {
			return false, err
		}
		contentType, _ := value.(string)
		if state.observeContentType(contentType) {
			if state.stop() {
				return true, nil
			}
			// The model name is still unknown, so the scan must continue. Drain
			// the rest of this part before moving on, otherwise the decoder
			// would be left positioned inside the part.
			return false, drainObject(decoder)
		}
		if err := skipRemainder(decoder, value); err != nil {
			return false, err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return false, err
	}
	if !typeSeen {
		state.observeUnknown("")
	}
	return false, nil
}

// scanTools reports whether a tools value is a non-empty array. Tool
// definitions are skipped, never materialized.
func scanTools(decoder *json.Decoder) (bool, error) {
	value, err := decoder.Token()
	if err != nil {
		return false, err
	}
	delim, ok := value.(json.Delim)
	if !ok || delim != '[' {
		if err := skipRemainder(decoder, value); err != nil {
			return false, err
		}
		return false, nil
	}
	hasTools := decoder.More()
	for decoder.More() {
		if err := skipValue(decoder); err != nil {
			return hasTools, err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return hasTools, err
	}
	return hasTools, nil
}

func decodeKey(decoder *json.Decoder) (string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	key, _ := token.(string)
	return key, nil
}

// drainObject consumes the remaining members and the closing brace of an
// object whose opening brace has already been read.
func drainObject(decoder *json.Decoder) error {
	for decoder.More() {
		if err := skipValue(decoder); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}

// skipValue discards one complete JSON value.
func skipValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	return skipRemainder(decoder, token)
}

// skipRemainder discards the rest of a value whose first token has already been
// consumed. Scalar first tokens are complete and need no further work.
func skipRemainder(decoder *json.Decoder, first json.Token) error {
	if _, ok := first.(json.Delim); !ok {
		return nil
	}
	depth := 1
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

// observeContentType records one content part and reports whether it was a
// video part, which makes the scan eligible to stop.
func (state *detectorState) observeContentType(contentType string) bool {
	switch contentType {
	case contentTypeVideoURL:
		state.modality = ModalityVideo
		state.videoFound = true
		return true
	case contentTypeImageURL, contentTypeImage, contentTypeInputImage:
		if !state.videoFound {
			state.modality = ModalityImage
		}
	case contentTypeText, contentTypeInputText:
		// Text never upgrades or downgrades another modality.
	default:
		state.observeUnknown(contentType)
	}
	return false
}

func (state *detectorState) observeUnknown(contentType string) {
	if !state.videoFound {
		state.modality = ModalityUnknown
	}
	if _, exists := state.unknownTypeSeen[contentType]; exists {
		return
	}
	state.unknownTypeSeen[contentType] = struct{}{}
	state.unknownContentTypes = append(state.unknownContentTypes, contentType)
}

func (state *detectorState) routeDemand() RouteDemand {
	return RouteDemand{
		Model:               state.model,
		Modality:            state.modality,
		HasTools:            state.hasTools,
		UnknownContentTypes: state.unknownContentTypes,
		DetectorFamily:      DetectorFamilyOpenAIChat,
	}
}
