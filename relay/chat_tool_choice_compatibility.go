package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// normalizeK3ToolChoice normalizes the optional selector without relaxing an
// explicit requirement to call a tool. Only the attempt-local selector changes;
// tool definitions, messages, parallel-tool policy and original body are retained.
func normalizeK3ToolChoice(fields map[string]json.RawMessage) (bool, error) {
	raw, exists := fields["tool_choice"]
	if !exists {
		return false, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		delete(fields, "tool_choice")
		return true, nil
	}
	var choice string
	var name string
	switch common.GetJsonType(raw) {
	case "string":
		if err := common.Unmarshal(raw, &choice); err != nil {
			return false, err
		}
		choice = strings.ToLower(strings.TrimSpace(choice))
		if choice == "" {
			delete(fields, "tool_choice")
			return true, nil
		}
		if choice == "any" {
			choice = "required"
		}
		if choice != "auto" && choice != "none" && choice != "required" {
			choice = "auto"
		}
	case "object":
		var object map[string]json.RawMessage
		if err := common.Unmarshal(raw, &object); err != nil {
			return false, err
		}
		if err := common.Unmarshal(object["type"], &choice); err != nil {
			return false, fmt.Errorf("tool_choice object requires a supported type")
		}
		choice = strings.ToLower(strings.TrimSpace(choice))
		switch choice {
		case "auto", "none", "any", "required":
			if len(object) != 1 {
				return false, fmt.Errorf("tool_choice selector contains unsupported fields")
			}
			if choice == "any" {
				choice = "required"
			}
		case "function", "tool":
			if len(object) != 2 {
				return false, fmt.Errorf("tool_choice must specify exactly one function")
			}
			if nested, ok := object["function"]; ok && choice == "function" {
				var function map[string]json.RawMessage
				if err := common.Unmarshal(nested, &function); err != nil || len(function) != 1 {
					return false, fmt.Errorf("tool_choice.function must contain only a name")
				}
				if err := common.Unmarshal(function["name"], &name); err != nil {
					return false, fmt.Errorf("tool_choice.function.name must be a string")
				}
			} else if err := common.Unmarshal(object["name"], &name); err != nil {
				return false, fmt.Errorf("tool_choice.name must be a string")
			}
			if strings.TrimSpace(name) == "" {
				return false, fmt.Errorf("tool_choice function name must not be empty")
			}
			choice = "function"
		default:
			return false, fmt.Errorf("unsupported tool_choice object type")
		}
	default:
		return false, fmt.Errorf("tool_choice must be a string, object or null")
	}

	// K3 also supports dynamic tool declarations in system messages. Counting
	// those prevents an explicit selector from being removed from dynamic tools.
	var tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	var messages []struct {
		Role  string          `json:"role"`
		Tools json.RawMessage `json:"tools"`
	}
	if len(fields["tools"]) > 0 {
		if err := common.Unmarshal(fields["tools"], &tools); err != nil {
			return false, fmt.Errorf("tools must be an array of tool definitions")
		}
	}
	if len(fields["messages"]) > 0 {
		if err := common.Unmarshal(fields["messages"], &messages); err != nil {
			return false, fmt.Errorf("messages must be an array")
		}
		for _, message := range messages {
			if message.Role != "system" || len(message.Tools) == 0 {
				continue
			}
			var dynamic = tools[:0:0]
			if err := common.Unmarshal(message.Tools, &dynamic); err != nil {
				return false, fmt.Errorf("system tools must be an array of tool definitions")
			}
			tools = append(tools, dynamic...)
		}
	}
	if len(fields["functions"]) > 0 && !bytes.Equal(bytes.TrimSpace(fields["functions"]), []byte("null")) {
		// A legacy functions dialect cannot safely be treated as an empty tools
		// list. Leave conversion to the existing provider adapter.
		return false, fmt.Errorf("tool_choice compatibility requires tools instead of legacy functions")
	}
	if len(tools) == 0 {
		if choice == "required" || choice == "function" {
			return false, fmt.Errorf("tool_choice requires matching tool definitions; cannot downgrade required tool use")
		}
		delete(fields, "tool_choice")
		return true, nil
	}
	var normalized []byte
	var err error
	if choice == "function" {
		found := false
		for _, tool := range tools {
			if tool.Type == "function" && tool.Function.Name == name {
				found = true
				break
			}
		}
		if !found {
			return false, fmt.Errorf("tool_choice names a function not present in tools")
		}
		normalized, err = common.Marshal(map[string]any{"type": "function", "function": map[string]string{"name": name}})
	} else {
		normalized, err = common.Marshal(choice)
	}
	if err != nil {
		return false, err
	}
	// Compare JSON semantically through canonical encoding without changing
	// whitespace/key order for an already canonical named selector.
	var value any
	if err := common.Unmarshal(raw, &value); err != nil {
		return false, err
	}
	canonical, err := common.Marshal(value)
	if err != nil {
		return false, err
	}
	if bytes.Equal(canonical, normalized) {
		return false, nil
	}
	fields["tool_choice"] = normalized
	return true, nil
}
