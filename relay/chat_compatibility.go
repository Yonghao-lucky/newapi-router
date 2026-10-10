package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

func chatCompatibilityConfigured(info *relaycommon.RelayInfo) bool {
	settings := info.ChannelSetting
	return info.RelayMode == relayconstant.RelayModeChatCompletions &&
		info.RelayFormat == types.RelayFormatOpenAI && len(settings.ChatCompatibilityModels) > 0 &&
		(settings.StrictChatToolMessages || settings.K3ParameterValidation || settings.K3ReasoningCompatibility || settings.K3ToolChoiceCompatibility || settings.GatewayVisibleContentStop || settings.K3TemperatureCompatibility || settings.K3TopPCompatibility || settings.K3PresencePenaltyCompatibility || settings.K3FrequencyPenaltyCompatibility || settings.K3AllowThinkingDisabled || settings.K3AllowReasoningEffortNone)
}

func chatCompatibilityClientError(param, message string) *types.NewAPIError {
	return types.WithOpenAIError(types.OpenAIError{
		Type: "invalid_request_error", Code: "invalid_request", Param: param, Message: message,
	}, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}

func chatCompatibilityConfigurationError(message string) *types.NewAPIError {
	// A channel's incompatible override/conversion is not a client error. Allow
	// the existing retry policy to select another compatible channel.
	return types.NewErrorWithStatusCode(fmt.Errorf("channel Chat compatibility configuration: %s", message),
		types.ErrorCode("channel:chat_compatibility"), http.StatusInternalServerError)
}

// applyChatCompatibility sees the actual outgoing JSON after conversion and
// overrides. The body storage is the replayable client input, never the previous
// attempt's modified body. No mutation is made to either the storage or Request.
func applyChatCompatibility(c *gin.Context, info *relaycommon.RelayInfo, body []byte, viaResponses bool) ([]byte, *types.NewAPIError) {
	info.VisibleStop = nil
	if !chatCompatibilityConfigured(info) {
		return body, nil
	}
	var outgoing map[string]json.RawMessage
	if err := common.Unmarshal(body, &outgoing); err != nil {
		return nil, chatCompatibilityConfigurationError("outgoing request must be a JSON object")
	}
	var model string
	if err := common.Unmarshal(outgoing["model"], &model); err != nil || model == "" {
		return nil, chatCompatibilityConfigurationError("outgoing model must be a non-empty string")
	}
	if !slices.Contains(info.ChannelSetting.ChatCompatibilityModels, model) {
		return body, nil
	}

	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, chatCompatibilityClientError("", "cannot read original request")
	}
	originalBytes, err := storage.Bytes()
	if err != nil {
		return nil, chatCompatibilityClientError("", "cannot read original request")
	}
	var original map[string]json.RawMessage
	if err := common.Unmarshal(originalBytes, &original); err != nil {
		return nil, chatCompatibilityClientError("", "request must be a JSON object")
	}
	allowNone := info.ChannelSetting.K3AllowReasoningEffortNone
	if allowNone && bytes.Equal(bytes.TrimSpace(original["reasoning_effort"]), []byte(`"none"`)) {
		outgoingEffort := bytes.TrimSpace(outgoing["reasoning_effort"])
		if len(outgoingEffort) > 0 && !bytes.Equal(outgoingEffort, []byte(`"none"`)) {
			return nil, chatCompatibilityConfigurationError("outgoing reasoning_effort conflicts with the channel's none capability")
		}
		if len(outgoingEffort) == 0 {
			outgoing["reasoning_effort"] = json.RawMessage(`"none"`)
			body, err = common.Marshal(outgoing)
			if err != nil {
				return nil, chatCompatibilityConfigurationError("cannot preserve reasoning_effort none")
			}
		}
		info.SetReasoningEffort("none")
	}
	if info.ChannelSetting.StrictChatToolMessages {
		if err := validateChatToolMessages(original["messages"]); err != nil {
			return nil, chatCompatibilityClientError("messages", err.Error())
		}
		if err := validateChatToolMessages(outgoing["messages"]); err != nil {
			return nil, chatCompatibilityConfigurationError("outgoing messages: " + err.Error())
		}
		changed, err := restoreDynamicToolContentOmission(original, outgoing)
		if err != nil {
			return nil, chatCompatibilityConfigurationError("cannot preserve dynamic tool declarations")
		}
		if changed {
			body, err = common.Marshal(outgoing)
			if err != nil {
				return nil, chatCompatibilityConfigurationError("cannot encode dynamic tool declarations")
			}
		}
	}
	if info.ChannelSetting.K3ReasoningCompatibility {
		// Normalize only attempt-local maps. Body storage must remain untouched
		// so another channel can enforce its own compatibility policy.
		originalChanged, err := normalizeK3ReasoningEffort(original, allowNone)
		if err != nil {
			return nil, chatCompatibilityClientError("reasoning_effort", err.Error())
		}
		outgoingChanged, err := normalizeK3ReasoningEffort(outgoing, allowNone)
		if err != nil {
			return nil, chatCompatibilityConfigurationError(err.Error())
		}
		// The DTO omits empty strings and null. Preserve their explicit
		// compatibility default without filling an originally absent field.
		if originalChanged && len(outgoing["reasoning_effort"]) == 0 {
			outgoing["reasoning_effort"] = json.RawMessage(`"high"`)
			outgoingChanged = true
		}
		if outgoingChanged {
			body, err = common.Marshal(outgoing)
			if err != nil {
				return nil, chatCompatibilityConfigurationError("cannot encode reasoning compatibility")
			}
		}
		if originalChanged || outgoingChanged {
			var effort string
			_ = common.Unmarshal(outgoing["reasoning_effort"], &effort)
			info.SetReasoningEffort(effort)
			info.ParamOverrideAudit = append(info.ParamOverrideAudit, "K3 reasoning compatibility applied; outgoing reasoning_effort = "+effort)
		}
	}
	if info.ChannelSetting.K3AllowThinkingDisabled {
		for i, fields := range []map[string]json.RawMessage{original, outgoing} {
			raw, exists := fields["thinking"]
			if !exists {
				continue
			}
			var thinking struct {
				Type string  `json:"type"`
				Keep *string `json:"keep"`
			}
			if common.Unmarshal(raw, &thinking) != nil || (thinking.Type != "enabled" && thinking.Type != "disabled") || (thinking.Keep != nil && *thinking.Keep != "all") {
				message := "thinking must contain type 'enabled' or 'disabled'; keep, when provided, must be 'all'"
				if i == 0 {
					return nil, chatCompatibilityClientError("thinking", message)
				}
				return nil, chatCompatibilityConfigurationError(message)
			}
		}
	}
	if info.ChannelSetting.K3ToolChoiceCompatibility {
		switch info.ApiType {
		case constant.APITypeOpenAI, constant.APITypeOpenRouter, constant.APITypeMoonshot, constant.APITypeNewAPI:
		default:
			return nil, chatCompatibilityConfigurationError("tool choice compatibility requires an OpenAI-compatible upstream protocol")
		}
		if _, err := normalizeK3ToolChoice(original); err != nil {
			return nil, chatCompatibilityClientError("tool_choice", err.Error())
		}
		changed, err := normalizeK3ToolChoice(outgoing)
		if err != nil {
			return nil, chatCompatibilityConfigurationError("outgoing tool_choice: " + err.Error())
		}
		if changed {
			body, err = common.Marshal(outgoing)
			if err != nil {
				return nil, chatCompatibilityConfigurationError("cannot encode tool choice compatibility")
			}
			info.ParamOverrideAudit = append(info.ParamOverrideAudit, "K3 tool_choice compatibility applied")
		}
	}
	// Only normalize numeric fields on attempt-local copies. Never modify the
	// replayable body, response usage, or unrelated request fields.
	samplingChanged := false
	for _, rule := range []struct {
		name, value string
		enabled     bool
	}{
		{"temperature", "1", info.ChannelSetting.K3TemperatureCompatibility},
		{"top_p", "0.95", info.ChannelSetting.K3TopPCompatibility},
		{"presence_penalty", "0", info.ChannelSetting.K3PresencePenaltyCompatibility},
		{"frequency_penalty", "0", info.ChannelSetting.K3FrequencyPenaltyCompatibility},
	} {
		if !rule.enabled {
			continue
		}
		changed := false
		for i, fields := range []map[string]json.RawMessage{original, outgoing} {
			raw := bytes.TrimSpace(fields[rule.name])
			if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
				continue
			}
			if common.GetJsonType(raw) != "number" {
				if i == 0 {
					return nil, chatCompatibilityClientError(rule.name, rule.name+" must be a number")
				}
				return nil, chatCompatibilityConfigurationError(rule.name + " must be a number")
			}
			if !k3FixedNumberMatches(raw, rule.value) {
				fields[rule.name] = json.RawMessage(rule.value)
				changed = true
			}
		}
		if changed {
			samplingChanged = true
			info.ParamOverrideAudit = append(info.ParamOverrideAudit, "K3 sampling compatibility applied: "+rule.name+" = "+rule.value)
		}
	}
	if samplingChanged {
		body, err = common.Marshal(outgoing)
		if err != nil {
			return nil, chatCompatibilityConfigurationError("cannot encode sampling compatibility")
		}
	}
	if info.ChannelSetting.K3ParameterValidation {
		if param, err := validateK3Parameters(original, allowNone); err != nil {
			return nil, chatCompatibilityClientError(param, err.Error())
		}
		if param, err := validateK3Parameters(outgoing, allowNone); err != nil {
			return nil, chatCompatibilityConfigurationError("outgoing " + param + ": " + err.Error())
		}
	}
	// Chat-to-Responses changes both the outgoing protocol and response handler.
	// Do not silently bypass a channel's Chat compatibility contract.
	if viaResponses && (info.ChannelSetting.StrictChatToolMessages || info.ChannelSetting.K3ParameterValidation || info.ChannelSetting.K3ReasoningCompatibility || info.ChannelSetting.K3ToolChoiceCompatibility || info.ChannelSetting.K3AllowThinkingDisabled || info.ChannelSetting.K3AllowReasoningEffortNone || info.ChannelSetting.K3TemperatureCompatibility || info.ChannelSetting.K3TopPCompatibility || info.ChannelSetting.K3PresencePenaltyCompatibility || info.ChannelSetting.K3FrequencyPenaltyCompatibility) {
		return nil, chatCompatibilityConfigurationError("Chat-to-Responses conversion is not supported with the selected compatibility policy")
	}
	if !info.ChannelSetting.GatewayVisibleContentStop {
		return body, nil
	}
	stops, err := parseVisibleContentStops(original["stop"])
	if err != nil {
		return nil, chatCompatibilityClientError("stop", err.Error())
	}
	if len(stops) == 0 {
		return body, nil
	}
	if viaResponses {
		return nil, chatCompatibilityConfigurationError("Chat-to-Responses conversion is not supported with visible content stop")
	}
	switch info.ApiType {
	case constant.APITypeOpenAI, constant.APITypeMoonshot, constant.APITypeNewAPI:
	default:
		return nil, chatCompatibilityConfigurationError("visible content stop requires an OpenAI-compatible response handler")
	}
	if info.ChannelSetting.ThinkingToContent {
		return nil, chatCompatibilityConfigurationError("visible content stop cannot be combined with thinking_to_content")
	}
	delete(outgoing, "stop")
	modified, err := common.Marshal(outgoing)
	if err != nil {
		return nil, chatCompatibilityConfigurationError("cannot encode outgoing request")
	}
	info.VisibleStop = stops
	return modified, nil
}

// normalizeK3ReasoningEffort accepts only strings or explicit null. Invalid
// strings and null become high; an absent field stays absent. Exact none is
// preserved only for a channel that opted in to the upstream capability.
func normalizeK3ReasoningEffort(fields map[string]json.RawMessage, allowNone bool) (bool, error) {
	raw, exists := fields["reasoning_effort"]
	if !exists {
		return false, nil
	}
	var effort string
	if err := common.Unmarshal(raw, &effort); err != nil {
		return false, fmt.Errorf("reasoning_effort must be a string or null")
	}
	if slices.Contains([]string{"low", "high", "max"}, effort) || (allowNone && effort == "none") {
		return false, nil
	}
	fields["reasoning_effort"] = json.RawMessage(`"high"`)
	return true, nil
}

// restoreDynamicToolContentOmission undoes only the DTO-generated content:null
// on unchanged K3 dynamic tool declarations. Match the complete declaration
// sequence (excluding content), so inserted ordinary system prompts are harmless
// and modified declarations are left to the administrator's outgoing override.
func restoreDynamicToolContentOmission(original, outgoing map[string]json.RawMessage) (bool, error) {
	var originalMessages, outgoingMessages []map[string]json.RawMessage
	if err := common.Unmarshal(original["messages"], &originalMessages); err != nil {
		return false, err
	}
	if err := common.Unmarshal(outgoing["messages"], &outgoingMessages); err != nil {
		return false, err
	}
	var declarations [2][]map[string]json.RawMessage
	for i, messages := range [][]map[string]json.RawMessage{originalMessages, outgoingMessages} {
		for _, message := range messages {
			var role string
			if err := common.Unmarshal(message["role"], &role); err != nil {
				return false, err
			}
			if role == "system" && common.GetJsonType(message["tools"]) == "array" {
				declarations[i] = append(declarations[i], message)
			}
		}
	}
	if len(declarations[0]) != len(declarations[1]) {
		return false, nil
	}
	// Check the entire sequence before editing any declaration. RawMessage
	// marshaling removes whitespace without altering nested tools or key order.
	for i, message := range declarations[0] {
		var canonical [2][]byte
		for j, declaration := range []map[string]json.RawMessage{message, declarations[1][i]} {
			fields := make(map[string]json.RawMessage, len(declaration))
			for key, value := range declaration {
				if key != "content" {
					fields[key] = value
				}
			}
			var err error
			canonical[j], err = common.Marshal(fields)
			if err != nil {
				return false, err
			}
		}
		if !bytes.Equal(canonical[0], canonical[1]) {
			return false, nil
		}
	}
	changed := false
	for i, message := range declarations[0] {
		candidate := declarations[1][i]
		if _, exists := message["content"]; !exists && bytes.Equal(bytes.TrimSpace(candidate["content"]), []byte("null")) {
			delete(candidate, "content")
			changed = true
		}
	}
	if changed {
		messages, err := common.Marshal(outgoingMessages)
		if err != nil {
			return false, err
		}
		outgoing["messages"] = messages
	}
	return changed, nil
}

// validateChatToolMessages follows each assistant turn independently. System
// instructions, including K3 dynamic system.tools declarations, do not consume
// results or begin a new conversation turn.
func validateChatToolMessages(raw json.RawMessage) error {
	var messages []struct {
		Role       string          `json:"role"`
		ToolCalls  json.RawMessage `json:"tool_calls"`
		ToolCallID string          `json:"tool_call_id"`
	}
	if err := common.Unmarshal(raw, &messages); err != nil || len(messages) == 0 {
		return fmt.Errorf("messages must be a non-empty array")
	}
	pending := make(map[string]bool)
	remaining := 0
	for index, message := range messages {
		if message.Role == "" {
			return fmt.Errorf("messages[%d].role is required", index)
		}
		if message.Role == "system" {
			continue
		}
		if message.Role == "tool" {
			completed, exists := pending[message.ToolCallID]
			if !exists {
				return fmt.Errorf("messages[%d].tool_call_id has no pending assistant tool call", index)
			}
			if completed {
				return fmt.Errorf("messages[%d] repeats a tool result", index)
			}
			pending[message.ToolCallID] = true
			remaining--
			continue
		}
		if remaining != 0 {
			return fmt.Errorf("messages[%d] starts a new turn before all tool results are supplied", index)
		}
		clear(pending)
		if message.Role != "assistant" || len(message.ToolCalls) == 0 || bytes.Equal(bytes.TrimSpace(message.ToolCalls), []byte("null")) {
			continue
		}
		var calls []struct {
			ID string `json:"id"`
		}
		if err := common.Unmarshal(message.ToolCalls, &calls); err != nil {
			return fmt.Errorf("messages[%d].tool_calls must be an array", index)
		}
		for _, call := range calls {
			if call.ID == "" {
				return fmt.Errorf("messages[%d].tool_calls requires non-empty IDs", index)
			}
			if _, exists := pending[call.ID]; exists {
				return fmt.Errorf("messages[%d].tool_calls contains duplicate IDs", index)
			}
			pending[call.ID] = false
			remaining++
		}
	}
	if remaining != 0 {
		return fmt.Errorf("messages is missing results for %d assistant tool calls", remaining)
	}
	return nil
}

// Rules are from K3, not K2.x: https://platform.kimi.com/docs/api/models-overview
// and https://platform.kimi.com/docs/guide/kimi-k3-quickstart . Unset and null
// retain the existing request behavior; no default value is supplied here.
func validateK3Parameters(fields map[string]json.RawMessage, allowNone bool) (string, error) {
	for _, rule := range []struct {
		name, value string
	}{{"temperature", "1"}, {"top_p", "0.95"}, {"n", "1"}, {"presence_penalty", "0"}, {"frequency_penalty", "0"}} {
		raw := bytes.TrimSpace(fields[rule.name])
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			continue
		}
		if !k3FixedNumberMatches(raw, rule.value) {
			return rule.name, fmt.Errorf("%s must be the number %s for this model", rule.name, rule.value)
		}
		if rule.name == "n" {
			var n int
			if err := common.Unmarshal(raw, &n); err != nil {
				return rule.name, fmt.Errorf("n must be the integer 1 for this model")
			}
		}
	}
	if raw := bytes.TrimSpace(fields["reasoning_effort"]); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var value string
		if err := common.Unmarshal(raw, &value); err != nil || (!slices.Contains([]string{"low", "high", "max"}, value) && !(allowNone && value == "none")) {
			return "reasoning_effort", fmt.Errorf("reasoning_effort must be low, high or max for this model")
		}
	}
	return "", nil
}

// Compare coefficients without exponent rescaling or float rounding. This
// preserves equivalent numeric spellings without allocating enormous powers.
func k3FixedNumberMatches(raw json.RawMessage, fixed string) bool {
	if common.GetJsonType(raw) != "number" {
		return false
	}
	value, err := decimal.NewFromString(string(raw))
	if err != nil {
		return false
	}
	expected := decimal.RequireFromString(fixed)
	if value.IsZero() || expected.IsZero() {
		return value.IsZero() && expected.IsZero()
	}
	coefficient := value.Coefficient().String()
	digits := strings.TrimRight(coefficient, "0")
	exponent := int64(value.Exponent()) + int64(len(coefficient)-len(digits))
	return digits == expected.Coefficient().String() && exponent == int64(expected.Exponent())
}

func parseVisibleContentStops(raw json.RawMessage) ([]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var single string
	if raw[0] == '"' {
		if err := common.Unmarshal(raw, &single); err != nil {
			return nil, fmt.Errorf("stop must be a string or an array of strings")
		}
		return []string{single}, nil
	}
	var items []json.RawMessage
	if raw[0] != '[' || common.Unmarshal(raw, &items) != nil {
		return nil, fmt.Errorf("stop must be a string or an array of strings")
	}
	stops := make([]string, 0, len(items))
	for _, item := range items {
		item = bytes.TrimSpace(item)
		if len(item) == 0 || item[0] != '"' || common.Unmarshal(item, &single) != nil {
			return nil, fmt.Errorf("stop must contain only strings")
		}
		stops = append(stops, single)
	}
	return stops, nil
}
