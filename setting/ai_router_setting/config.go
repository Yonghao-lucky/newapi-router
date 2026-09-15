package ai_router_setting

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	ConfigPrefix = "ai_router."

	EnabledOptionKey       = ConfigPrefix + "enabled"
	TargetModelsOptionKey  = ConfigPrefix + "target_models"
	MaxAttemptsOptionKey   = ConfigPrefix + "max_attempts"
	UnknownPolicyOptionKey = ConfigPrefix + "unknown_policy"
	ToolFilterOptionKey    = ConfigPrefix + "tool_filter_enabled"
	TelemetryOptionKey     = ConfigPrefix + "telemetry_enabled"
	RetentionDaysOptionKey = ConfigPrefix + "retention_days"

	UnknownPolicyFullCapOnly = "full_capability_only"
	MinMaxAttempts           = 1
	MaxMaxAttempts           = 10
	MinRetentionDays         = 1
	MaxRetentionDays         = 30
)

type Settings struct {
	Enabled           bool     `json:"enabled"`
	TargetModels      []string `json:"target_models"`
	MaxAttempts       int      `json:"max_attempts"`
	UnknownPolicy     string   `json:"unknown_policy"`
	ToolFilterEnabled bool     `json:"tool_filter_enabled"`
	TelemetryEnabled  bool     `json:"telemetry_enabled"`
	RetentionDays     int      `json:"retention_days"`
}

func DefaultSettings() Settings {
	return Settings{
		Enabled:           false,
		TargetModels:      []string{"kimi-k3"},
		MaxAttempts:       3,
		UnknownPolicy:     UnknownPolicyFullCapOnly,
		ToolFilterEnabled: false,
		TelemetryEnabled:  true,
		RetentionDays:     7,
	}
}

func NormalizeTargetModels(models []string) []string {
	normalized := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || slices.Contains(normalized, model) {
			continue
		}
		normalized = append(normalized, model)
	}
	return normalized
}

func (s *Settings) Normalize() {
	if s == nil {
		return
	}
	s.TargetModels = NormalizeTargetModels(s.TargetModels)
	if s.UnknownPolicy == "" {
		s.UnknownPolicy = UnknownPolicyFullCapOnly
	}
}

func (s *Settings) Validate() error {
	if s == nil {
		return fmt.Errorf("ai router settings cannot be empty")
	}
	if len(s.TargetModels) == 0 {
		return fmt.Errorf("target_models cannot be empty")
	}
	if slices.Contains(s.TargetModels, "") {
		return fmt.Errorf("target_models cannot contain empty model names")
	}
	if s.MaxAttempts < MinMaxAttempts || s.MaxAttempts > MaxMaxAttempts {
		return fmt.Errorf("max_attempts must be between %d and %d", MinMaxAttempts, MaxMaxAttempts)
	}
	if s.UnknownPolicy != UnknownPolicyFullCapOnly {
		return fmt.Errorf("unknown_policy must be %s", UnknownPolicyFullCapOnly)
	}
	if s.RetentionDays < MinRetentionDays || s.RetentionDays > MaxRetentionDays {
		return fmt.Errorf("retention_days must be between %d and %d", MinRetentionDays, MaxRetentionDays)
	}
	return nil
}

func (s *Settings) clone() Settings {
	cloned := *s
	cloned.TargetModels = slices.Clone(s.TargetModels)
	return cloned
}

func (s *Settings) IsTargetModel(model string) bool {
	if s == nil || model == "" {
		return false
	}
	return slices.Contains(s.TargetModels, model)
}

var runtimeSnapshot atomic.Pointer[Settings]

var publishMu sync.Mutex

func init() {
	settings := DefaultSettings()
	runtimeSnapshot.Store(&settings)
}

func GetSettings() Settings {
	settings := runtimeSnapshot.Load()
	if settings == nil {
		return DefaultSettings()
	}
	cloned := *settings
	cloned.TargetModels = slices.Clone(settings.TargetModels)
	return cloned
}

func publishSnapshot(settings Settings) {
	settings.Normalize()
	if err := settings.Validate(); err != nil {
		return
	}
	published := settings.clone()
	runtimeSnapshot.Store(&published)
}

func buildCandidate(current Settings, values map[string]string) (Settings, error) {
	candidate := current.clone()
	for key, value := range values {
		if err := applyStrictValue(&candidate, key, value); err != nil {
			return Settings{}, err
		}
	}
	candidate.Normalize()
	if err := candidate.Validate(); err != nil {
		return Settings{}, err
	}
	return candidate, nil
}

func applyStrictValue(settings *Settings, key string, value string) error {
	switch key {
	case "enabled":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid enabled value: %s", value)
		}
		settings.Enabled = parsed
	case "target_models":
		var models []string
		if err := json.Unmarshal([]byte(value), &models); err != nil {
			return fmt.Errorf("invalid target_models value: %s", value)
		}
		settings.TargetModels = models
	case "max_attempts":
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid max_attempts value: %s", value)
		}
		settings.MaxAttempts = parsed
	case "unknown_policy":
		settings.UnknownPolicy = value
	case "tool_filter_enabled":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid tool_filter_enabled value: %s", value)
		}
		settings.ToolFilterEnabled = parsed
	case "telemetry_enabled":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid telemetry_enabled value: %s", value)
		}
		settings.TelemetryEnabled = parsed
	case "retention_days":
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid retention_days value: %s", value)
		}
		settings.RetentionDays = parsed
	default:
		return fmt.Errorf("unknown ai_router config key: %s", key)
	}
	return nil
}

func BuildCandidate(values map[string]string) (Settings, error) {
	publishMu.Lock()
	defer publishMu.Unlock()
	return buildCandidate(GetSettings(), values)
}

// ValidateOption validates a single persisted option. Keys outside the
// ai_router namespace are not ours to judge, so they are accepted here and
// left to their own validators.
func ValidateOption(key string, value string) error {
	if !strings.HasPrefix(key, ConfigPrefix) {
		return nil
	}
	_, err := BuildCandidate(map[string]string{strings.TrimPrefix(key, ConfigPrefix): value})
	return err
}

func Configure(values map[string]string) error {
	publishMu.Lock()
	defer publishMu.Unlock()
	candidate, err := buildCandidate(GetSettings(), values)
	if err != nil {
		return err
	}
	published := candidate.clone()
	runtimeSnapshot.Store(&published)
	return nil
}
