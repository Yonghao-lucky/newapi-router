package ai_router

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/QuantumNous/new-api/common"
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
	inspectAll          bool
	videoCount          int
	imageCount          int
	validation          *chatValidation
	modelNames          []string
	contentError        *ChatValidationError
	unknownParam        string
	thinkingDisabled    bool
	reasoningEffortNone bool
	enableThinking      bool
	lastContentNull     bool
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
	return !state.inspectAll && state.videoFound && state.modelSeen
}

// ChatInspection retains routing metadata and media counts only. The full scan
// is used before video admission; it validates model ambiguity and trailing JSON
// while sharing the router's content vocabulary and traversal.
type ChatInspection struct {
	Demand          RouteDemand
	VideoCount      int
	ExistingImages  int
	ValidationError *ChatValidationError
	ModelNames      []string
}

func InspectChatRequest(body io.Reader) (ChatInspection, error) {
	return inspectChatRequest(body, false)
}

// InspectChatRequestWithValidation shares the media scan without decoding or
// retaining content/Base64. Validation is applied later to the matching policy.
func InspectChatRequestWithValidation(body io.Reader) (ChatInspection, error) {
	return inspectChatRequest(body, true)
}

func inspectChatRequest(body io.Reader, validate bool) (ChatInspection, error) {
	if body == nil {
		return ChatInspection{}, errNilBody
	}
	state := newDetectorState()
	state.inspectAll = true
	if validate {
		state.validation = &chatValidation{}
	}
	decoder := common.NewJSONTokenDecoder(body)
	if err := scanChatRequest(decoder, state); err != nil {
		return ChatInspection{Demand: state.routeDemand(), ModelNames: state.modelNames}, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ChatInspection{Demand: state.routeDemand(), ModelNames: state.modelNames}, fmt.Errorf("unexpected data after request")
	}
	demand := state.routeDemand()
	result := ChatInspection{Demand: demand, VideoCount: state.videoCount, ExistingImages: state.imageCount, ModelNames: state.modelNames}
	if state.validation != nil {
		result.ValidationError = state.validation.first
		if result.ValidationError == nil && state.thinkingDisabled {
			result.ValidationError = &ChatValidationError{Param: "thinking.type", Message: "this route does not support disabling thinking; omit thinking or use a route supporting disabled", Code: "unsupported_thinking_mode"}
		}
	}
	return result, nil
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
	if err := scanChatRequest(common.NewJSONTokenDecoder(body), state); err != nil {
		return RouteDemand{}, fmt.Errorf("invalid OpenAI Chat request: %w", err)
	}
	return state.routeDemand(), nil
}

// scanChatRequest walks the top-level object. It returns as soon as the stop
// condition holds, leaving any remainder of the body unread.
func scanChatRequest(decoder *common.JSONTokenDecoder, state *detectorState) error {
	opening, err := decoder.Token()
	if err != nil {
		return err
	}
	if delim, ok := opening.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("expected top-level object")
	}

	messagesSeen := false
	var seen map[string]bool
	if state.validation != nil {
		seen = make(map[string]bool)
	}
	for decoder.More() {
		key, err := decodeKey(decoder)
		if err != nil {
			return err
		}
		if state.validation != nil && chatControlledField(key) {
			if seen[key] {
				state.validation.add(key, key+" must be provided once")
			}
			seen[key] = true
		}
		switch key {
		case "reasoning_effort":
			value, err := readChatScalar(decoder)
			if err != nil {
				return err
			}
			state.reasoningEffortNone = value == "none"
		case "enable_thinking":
			// Accepted for compatibility: the field is skipped unread and forwarded
			// upstream unchanged. The opt-in RejectEnableThinking policy switch
			// restores the 400 for routes that require its absence.
			state.enableThinking = true
			if err := skipValue(decoder); err != nil {
				return err
			}
		case "thinking":
			validation := state.validation
			if validation == nil {
				validation = &chatValidation{}
			}
			if err := validation.object(decoder, "thinking", "thinking"); err != nil {
				return err
			}
			state.thinkingDisabled = validation.thinkingDisabled
		case "model":
			if state.inspectAll && state.modelSeen && state.validation == nil {
				return fmt.Errorf("model must be provided once")
			}
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if name, ok := value.(string); ok {
				if state.validation != nil {
					state.modelNames = append(state.modelNames, name)
				}
				state.model = name
				state.modelSeen = true
			} else if state.inspectAll {
				return fmt.Errorf("model must be a string")
			}
			if err := skipRemainder(decoder, value); err != nil {
				return err
			}
		case "tools":
			hasTools, err := scanValidatedTools(decoder, state.validation, "tools")
			if err != nil {
				return err
			}
			state.hasTools = state.hasTools || hasTools
		case "messages":
			if state.inspectAll && messagesSeen && state.validation == nil {
				return fmt.Errorf("messages must be provided once")
			}
			messagesSeen = true
			stopped, err := scanMessages(decoder, state)
			if err != nil {
				return err
			}
			if stopped {
				return nil
			}
		default:
			if state.validation != nil && chatControlledField(key) {
				if err := state.validation.parameter(decoder, key); err != nil {
					return err
				}
				continue
			}
			if err := skipValue(decoder); err != nil {
				return err
			}
		}
		if state.stop() {
			return nil
		}
	}
	if state.validation != nil && !messagesSeen {
		state.validation.add("messages", "messages must be a non-empty array")
	}
	_, err = decoder.Token()
	return err
}

func scanMessages(decoder *common.JSONTokenDecoder, state *detectorState) (bool, error) {
	opening, err := decoder.Token()
	if err != nil {
		return false, err
	}
	if delim, ok := opening.(json.Delim); !ok || delim != '[' {
		state.validation.add("messages", "messages must be a non-empty array")
		if err := skipRemainder(decoder, opening); err != nil {
			return false, err
		}
		return false, nil
	}

	index := 0
	for decoder.More() {
		path := fmt.Sprintf("messages[%d]", index)
		index++
		message, err := decoder.Token()
		if err != nil {
			return false, err
		}
		delim, ok := message.(json.Delim)
		if !ok || delim != '{' {
			state.validation.add(path, path+" must be an object")
			if err := skipRemainder(decoder, message); err != nil {
				return false, err
			}
			continue
		}
		stopped, err := scanMessage(decoder, state, path)
		if err != nil {
			return false, err
		}
		if stopped {
			return true, nil
		}
	}
	if index == 0 {
		state.validation.add("messages", "messages must be a non-empty array")
	}
	if _, err := decoder.Token(); err != nil {
		return false, err
	}
	return false, nil
}

func scanMessage(decoder *common.JSONTokenDecoder, state *detectorState, path string) (bool, error) {
	contentSeen := false
	contentNull := false
	role := ""
	roleSeen := false
	toolsSeen := false
	for decoder.More() {
		key, err := decodeKey(decoder)
		if err != nil {
			return false, err
		}
		switch key {
		case "role":
			if roleSeen {
				state.validation.add(path+".role", path+".role must be provided once")
			}
			roleSeen = true
			value, err := readChatScalar(decoder)
			if err != nil {
				return false, err
			}
			if !validChatRole(value) {
				state.validation.add(path+".role", path+".role is not a supported role")
			}
			role, _ = value.(string)
		case "content":
			if state.inspectAll && contentSeen {
				if state.validation == nil {
					return false, fmt.Errorf("content must be provided once")
				}
				state.validation.add(path+".content", path+".content must be provided once")
			}
			contentSeen = true
			stopped, err := scanContent(decoder, state, path+".content")
			contentNull = state.lastContentNull
			if err != nil {
				return false, err
			}
			if stopped {
				return true, nil
			}
		case "tools":
			if toolsSeen {
				state.validation.add(path+".tools", path+".tools must be provided once")
			}
			toolsSeen = true
			hasTools, err := scanValidatedTools(decoder, state.validation, path+".tools")
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
	if !roleSeen {
		state.validation.add(path+".role", path+".role is required")
	}
	if contentNull && (role == "user" || role == "system" || role == "developer" || role == "tool") {
		state.invalidContent(path+".content", "must not be null for this message role", "invalid_parameter_type")
	}
	if _, err := decoder.Token(); err != nil {
		return false, err
	}
	return false, nil
}

// scanContent inspects a message content value without retaining it. A plain
// string is the text baseline; a list is walked part by part; anything else is
// conservatively unknown.
func scanContent(decoder *common.JSONTokenDecoder, state *detectorState, path string) (bool, error) {
	state.lastContentNull = false
	value, err := decoder.StructuralToken()
	if err != nil {
		return false, err
	}
	switch typed := value.(type) {
	case nil:
		state.lastContentNull = true
		return false, nil
	case string:
		return false, nil
	case json.Delim:
		if typed == '[' {
			return scanContentParts(decoder, state, path)
		}
		if err := skipRemainder(decoder, value); err != nil {
			return false, err
		}
		state.observeUnknown("")
		state.invalidContent(path, "must be a string or an array of content objects", "invalid_parameter_type")
		return false, nil
	default:
		state.observeUnknown("")
		state.invalidContent(path, "must be a string or an array of content objects", "invalid_parameter_type")
		return false, nil
	}
}

func scanContentParts(decoder *common.JSONTokenDecoder, state *detectorState, path string) (bool, error) {
	index := 0
	for decoder.More() {
		partPath := fmt.Sprintf("%s[%d]", path, index)
		index++
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
			state.invalidContent(partPath, "must be an object containing a type field", "invalid_parameter_type")
			continue
		}
		stopped, err := scanContentPart(decoder, state, partPath)
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
func scanContentPart(decoder *common.JSONTokenDecoder, state *detectorState, path string) (bool, error) {
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
		if state.inspectAll && typeSeen {
			state.invalidContent(path+".type", "must be provided once", "invalid_request")
			return false, state.contentError
		}
		typeSeen = true
		value, err := decoder.Token()
		if err != nil {
			return false, err
		}
		contentType, isString := value.(string)
		if !isString || contentType == "" {
			state.invalidContent(path+".type", "must be a non-empty string", "invalid_parameter_type")
		}
		beforeUnknown := len(state.unknownContentTypes)
		if state.observeContentType(contentType) {
			if state.stop() {
				return true, nil
			}
			// The model name is still unknown, so the scan must continue. Drain
			// the rest of this part before moving on, otherwise the decoder
			// would be left positioned inside the part.
			if !state.inspectAll {
				return false, drainObject(decoder)
			}
		}
		if len(state.unknownContentTypes) > beforeUnknown && state.unknownParam == "" {
			state.unknownParam = path + ".type"
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
		state.invalidContent(path+".type", "is required and must be a non-empty string", "missing_required_parameter")
	}
	return false, nil
}

// scanTools reports whether a tools value is a non-empty array. Tool
// definitions are skipped, never materialized.
func scanTools(decoder *common.JSONTokenDecoder) (bool, error) {
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

func decodeKey(decoder *common.JSONTokenDecoder) (string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	key, _ := token.(string)
	return key, nil
}

// drainObject consumes the remaining members and the closing brace of an
// object whose opening brace has already been read.
func drainObject(decoder *common.JSONTokenDecoder) error {
	for decoder.More() {
		if err := skipValue(decoder); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}

// skipValue discards one complete JSON value.
func skipValue(decoder *common.JSONTokenDecoder) error {
	return decoder.Skip()
}

// skipRemainder discards the rest of a value whose first token has already been
// consumed. Scalar first tokens are complete and need no further work.
func skipRemainder(decoder *common.JSONTokenDecoder, first json.Token) error {
	if _, ok := first.(json.Delim); !ok {
		return nil
	}
	depth := 1
	for depth > 0 {
		token, err := decoder.StructuralToken()
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
		state.videoCount++
		state.modality = ModalityVideo
		state.videoFound = true
		return true
	case contentTypeImageURL, contentTypeImage, contentTypeInputImage:
		state.imageCount++
		if !state.videoFound && len(state.unknownContentTypes) == 0 {
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
		ContentError:        state.contentError,
		UnknownParam:        state.unknownParam,
		ThinkingDisabled:    state.thinkingDisabled,
		EnableThinking:      state.enableThinking,
		ReasoningEffortNone: state.reasoningEffortNone,
	}
}

func (state *detectorState) invalidContent(param, message, code string) {
	if state.contentError == nil {
		state.contentError = &ChatValidationError{Param: param, Message: param + " " + message, Code: code}
	}
}
