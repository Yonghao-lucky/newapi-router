package helper

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

var (
	kimiPromptCacheModes = []string{"implicit", "explicit"}
	// Messages accepts prompt_cache_options only as a compatibility alias of
	// its top-level cache_control, so ephemeral (the cache_control type) is
	// also a valid mode there.
	kimiMessagesPromptCacheModes = []string{"implicit", "explicit", "ephemeral"}
	kimiCacheTTLs                = []string{"5m", "1h"}
	kimiCacheTypes               = []string{"ephemeral"}
)

func isKimiModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(model), "kimi-")
}

// kimiCacheControlFromPromptCacheOptions maps a validated Messages
// prompt_cache_options onto the top-level cache_control that Kimi-compatible
// Messages upstreams honor, so its ttl takes effect. Without a ttl the
// upstream default (5m) applies and nothing is returned.
func kimiCacheControlFromPromptCacheOptions(raw json.RawMessage) (json.RawMessage, error) {
	var options struct {
		TTL string `json:"ttl"`
	}
	if err := common.Unmarshal(raw, &options); err != nil || options.TTL == "" {
		return nil, err
	}
	return common.Marshal(map[string]string{"type": "ephemeral", "ttl": options.TTL})
}

// validateKimiCacheWriteOptions enforces Kimi's documented cache-write
// contract for kimi-* models: `prompt_cache_options` (mode, ttl) and
// Messages top-level `cache_control` (type, ttl). Some
// Kimi-compatible upstreams silently accept invalid values (an unknown ttl
// falls back to 5m), so the gateway rejects them with a 400 naming the
// offending field, as the official API does. Absent options are untouched.
func validateKimiCacheWriteOptions(model string, field string, raw json.RawMessage, enumFields map[string][]string) error {
	if !isKimiModel(model) {
		return nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var options map[string]json.RawMessage
	if err := common.Unmarshal(raw, &options); err != nil || options == nil {
		return invalidKimiCacheOptionError(field, fmt.Sprintf("%s must be an object", field))
	}
	for key, allowed := range enumFields {
		value, ok := options[key]
		if !ok {
			continue
		}
		param := field + "." + key
		var s string
		if err := common.Unmarshal(value, &s); err != nil {
			return invalidKimiCacheOptionError(param, fmt.Sprintf("invalid %s: must be one of %s", param, strings.Join(allowed, ", ")))
		}
		if !slices.Contains(allowed, s) {
			return invalidKimiCacheOptionError(param, fmt.Sprintf("invalid %s %q: must be one of %s", param, s, strings.Join(allowed, ", ")))
		}
	}
	return nil
}

func invalidKimiCacheOptionError(param string, message string) error {
	return types.WithOpenAIError(types.OpenAIError{
		Message: message,
		Type:    "invalid_request_error",
		Param:   param,
		Code:    "invalid_request",
	}, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}
