package ai_router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// ChatValidationError contains public field information, never request values.
type ChatValidationError struct {
	Param   string
	Message string
	Code    string
}

func (e *ChatValidationError) Error() string { return e.Message }

func (e *ChatValidationError) APIError() *types.NewAPIError {
	code := e.Code
	if code == "" {
		code = "invalid_request"
	}
	return types.WithOpenAIError(types.OpenAIError{Type: "invalid_request_error", Code: code, Param: e.Param, Message: e.Message}, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}

// Keep a single error, ordered by field path so JSON object key order cannot
// change which error wins. Payloads are neither retained nor modified.
type chatValidation struct {
	first            *ChatValidationError
	thinkingDisabled bool
}

func (v *chatValidation) add(param, message string) {
	if v == nil {
		return
	}
	if v.first == nil || param < v.first.Param || (param == v.first.Param && message < v.first.Message) {
		v.first = &ChatValidationError{Param: param, Message: message}
	}
}

func chatControlledField(key string) bool {
	switch key {
	case "model", "messages", "tools", "response_format", "max_tokens", "temperature", "top_p", "thinking":
		return true
	}
	return false
}

func validChatRole(value any) bool {
	role, ok := value.(string)
	return ok && slices.Contains([]string{"system", "developer", "user", "assistant", "tool", "function"}, role)
}

// A compound value in a scalar position is discarded without materializing it.
func readChatScalar(d *common.JSONTokenDecoder) (any, error) {
	value, err := d.Token()
	if err != nil {
		return nil, err
	}
	if _, compound := value.(json.Delim); compound {
		depth := 1
		for depth > 0 {
			token, err := d.StructuralToken()
			if err != nil {
				return nil, err
			}
			if delim, ok := token.(json.Delim); ok {
				if delim == '{' || delim == '[' {
					depth++
				} else {
					depth--
				}
			}
		}
	}
	return value, nil
}

func (v *chatValidation) parameter(d *common.JSONTokenDecoder, name string) error {
	switch name {
	case "thinking", "response_format":
		return v.object(d, name, name)
	default:
		value, err := readChatScalar(d)
		if err != nil {
			return err
		}
		if name == "max_tokens" {
			number, ok := value.(json.Number)
			parsed, parseErr := strconv.ParseUint(string(number), 10, 64)
			if !ok || parseErr != nil || parsed == 0 {
				v.add(name, "max_tokens must be a positive integer")
			} else if parsed > common.MaxRequestTokenLimit {
				v.add(name, fmt.Sprintf("max_tokens must not exceed %d", common.MaxRequestTokenLimit))
			}
		} else if value != nil {
			if _, ok := value.(json.Number); !ok {
				v.add(name, name+" must be a number")
			}
		}
		return nil
	}
}

func scanValidatedTools(d *common.JSONTokenDecoder, v *chatValidation, path string) (bool, error) {
	if v == nil {
		return scanTools(d)
	}
	value, err := d.Token()
	if err != nil {
		return false, err
	}
	// An omitted/null optional tools field retains the existing DTO behavior.
	if value == nil {
		return false, nil
	}
	if value != json.Delim('[') {
		v.add(path, path+" must be an array")
		return false, skipRemainder(d, value)
	}
	index := 0
	for d.More() {
		if err := v.object(d, fmt.Sprintf("%s[%d]", path, index), "tool"); err != nil {
			return false, err
		}
		index++
	}
	_, err = d.Token()
	return index > 0, err
}

// object walks only the shallow protocol envelope. Tool parameter schemas,
// descriptions, content and extension fields use the decoder's bounded Skip.
func (v *chatValidation) object(d *common.JSONTokenDecoder, path, kind string) error {
	value, err := d.Token()
	if err != nil {
		return err
	}
	if value == nil && kind == "response_format" {
		return nil
	}
	if value != json.Delim('{') {
		v.add(path, path+" must be an object")
		return skipRemainder(d, value)
	}
	seen := map[string]bool{}
	var children map[string]*chatValidation
	var typeName string
	for d.More() {
		key, err := decodeKey(d)
		if err != nil {
			return err
		}
		controlled := false
		switch kind {
		case "thinking":
			controlled = key == "type" || key == "keep"
		case "response_format":
			controlled = key == "type" || key == "json_schema"
		case "tool":
			controlled = key == "type" || key == "function" || key == "custom"
		case "function":
			controlled = key == "name" || key == "parameters" || key == "strict"
		case "custom":
			controlled = key == "name" || key == "format"
		case "json_schema":
			controlled = key == "name" || key == "schema" || key == "strict"
		}
		if !controlled {
			if err := d.Skip(); err != nil {
				return err
			}
			continue
		}
		field := path + "." + key
		if seen[key] {
			v.add(field, field+" must be provided once")
		}
		seen[key] = true
		switch key {
		case "function", "custom", "json_schema":
			child := &chatValidation{}
			if err := child.object(d, field, key); err != nil {
				return err
			}
			if children == nil {
				children = make(map[string]*chatValidation)
			}
			children[key] = child
		case "parameters", "schema", "format":
			value, err := d.StructuralToken()
			if err != nil {
				return err
			}
			_, boolean := value.(bool)
			if value != json.Delim('{') && !(kind == "json_schema" && boolean) && !(key == "parameters" && value == nil) {
				v.add(field, field+" must be a schema object")
			}
			// Use Skip for children to avoid capturing schema strings.
			if _, ok := value.(json.Delim); ok {
				depth := 1
				for depth > 0 {
					token, err := d.StructuralToken()
					if err != nil {
						return err
					}
					if delim, ok := token.(json.Delim); ok {
						if delim == '{' || delim == '[' {
							depth++
						} else {
							depth--
						}
					}
				}
			}
		default:
			value, err := readChatScalar(d)
			if err != nil {
				return err
			}
			text, isString := value.(string)
			switch key {
			case "type":
				typeName = text
				if !isString {
					v.add(field, field+" must be a string")
				}
			case "name":
				if !isString || strings.TrimSpace(text) == "" {
					v.add(field, field+" must be a non-empty string")
				}
			case "strict":
				if _, ok := value.(bool); !ok && value != nil {
					v.add(field, field+" must be a boolean")
				}
			case "keep":
				if !isString || text != "all" {
					v.add(field, "thinking.keep must be 'all' when provided for this model")
				}
			}
		}
	}
	if _, err := d.Token(); err != nil {
		return err
	}
	childKey := typeName
	if typeName == "builtin_function" {
		childKey = "function"
	}
	if child := children[childKey]; child != nil && child.first != nil {
		v.add(child.first.Param, child.first.Message)
	}
	switch kind {
	case "thinking":
		switch {
		case typeName == "disabled":
			v.thinkingDisabled = true
		case typeName == "enabled", !seen["type"]:
			// An omitted type is the documented default: thinking stays enabled.
		default:
			v.add(path+".type", "thinking.type must be 'enabled' for this model")
		}
	case "response_format":
		if !slices.Contains([]string{"text", "json_object", "json_schema"}, typeName) {
			v.add(path+".type", "response_format.type must be 'text', 'json_object' or 'json_schema'")
		} else if typeName == "json_schema" && !seen["json_schema"] {
			v.add(path+".json_schema", path+".json_schema is required")
		}
	case "tool":
		if !seen["type"] {
			v.add(path+".type", path+".type is required")
			break
		}
		switch typeName {
		case "function", "builtin_function":
			if !seen["function"] {
				v.add(path+".function", path+".function is required for a function tool")
			}
		case "custom":
			if !seen["custom"] {
				v.add(path+".custom", path+".custom is required for a custom tool")
			}
		default:
			v.add(path+".type", path+".type is not a supported tool type")
		}
	case "function", "custom", "json_schema":
		if !seen["name"] {
			v.add(path+".name", path+".name must be a non-empty string")
		}
		if kind == "json_schema" && !seen["schema"] {
			v.add(path+".schema", path+".schema is required")
		}
	}
	return nil
}
