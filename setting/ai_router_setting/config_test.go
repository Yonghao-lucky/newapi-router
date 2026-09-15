package ai_router_setting

import (
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultSettings(t *testing.T) {
	settings := DefaultSettings()
	assert.False(t, settings.Enabled)
	assert.Equal(t, []string{"kimi-k3"}, settings.TargetModels)
	assert.Equal(t, 3, settings.MaxAttempts)
	assert.Equal(t, "full_capability_only", settings.UnknownPolicy)
	assert.False(t, settings.ToolFilterEnabled)
	assert.True(t, settings.TelemetryEnabled)
	assert.Equal(t, 7, settings.RetentionDays)
	assert.NoError(t, settings.Validate())
}

func TestValidateTargetModels(t *testing.T) {
	settings := DefaultSettings()
	settings.TargetModels = []string{" kimi-k3 ", "", "kimi-k3", "gpt-4o"}
	settings.Normalize()
	assert.Equal(t, []string{"kimi-k3", "gpt-4o"}, settings.TargetModels)

	emptyAfterNormalize := Settings{TargetModels: []string{""}, MaxAttempts: 3, RetentionDays: 7, UnknownPolicy: UnknownPolicyFullCapOnly}
	emptyAfterNormalize.Normalize()
	assert.Error(t, emptyAfterNormalize.Validate())
}

func TestValidateRangeAndPolicy(t *testing.T) {
	tests := []struct {
		name      string
		max       int
		retention int
		policy    string
		wantErr   bool
	}{
		{name: "min bounds", max: 1, retention: 1, policy: "full_capability_only"},
		{name: "max bounds", max: 10, retention: 30, policy: "full_capability_only"},
		{name: "max attempts too low", max: 0, retention: 7, policy: "full_capability_only", wantErr: true},
		{name: "max attempts too high", max: 11, retention: 7, policy: "full_capability_only", wantErr: true},
		{name: "retention too low", max: 3, retention: 0, policy: "full_capability_only", wantErr: true},
		{name: "retention too high", max: 3, retention: 31, policy: "full_capability_only", wantErr: true},
		{name: "unknown policy", max: 3, retention: 7, policy: "regex", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := DefaultSettings()
			settings.MaxAttempts = test.max
			settings.RetentionDays = test.retention
			settings.UnknownPolicy = test.policy
			if test.wantErr {
				assert.Error(t, settings.Validate())
			} else {
				assert.NoError(t, settings.Validate())
			}
		})
	}
}

func TestConfigureValidatesAndPublishesSnapshot(t *testing.T) {
	require.NoError(t, Configure(map[string]string{
		"enabled":           "true",
		"target_models":     `[" kimi-k3 ", "", "kimi-k3", "gpt-4o"]`,
		"max_attempts":      "5",
		"unknown_policy":    "full_capability_only",
		"retention_days":    "14",
		"telemetry_enabled": "false",
	}))

	snapshot := GetSettings()
	assert.True(t, snapshot.Enabled)
	assert.Equal(t, []string{"kimi-k3", "gpt-4o"}, snapshot.TargetModels)
	assert.Equal(t, 5, snapshot.MaxAttempts)
	assert.Equal(t, 14, snapshot.RetentionDays)
	assert.False(t, snapshot.TelemetryEnabled)
	assert.True(t, snapshot.IsTargetModel("kimi-k3"))
	assert.False(t, snapshot.IsTargetModel("KIMI-K3"))
	assert.False(t, snapshot.IsTargetModel("kimi-k3-*"))

	assert.Error(t, Configure(map[string]string{"max_attempts": "11"}))
	assert.Equal(t, 5, GetSettings().MaxAttempts)
}

func TestStrictParsingRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "enabled not bool", key: "enabled", value: "not-bool"},
		{name: "tool_filter not bool", key: "tool_filter_enabled", value: "not-bool"},
		{name: "telemetry not bool", key: "telemetry_enabled", value: "not-bool"},
		{name: "max_attempts not int", key: "max_attempts", value: "abc"},
		{name: "retention not int", key: "retention_days", value: "abc"},
		{name: "target_models not json", key: "target_models", value: "not-json"},
		{name: "target_models element not string", key: "target_models", value: `[1,2]`},
		{name: "target_models empty after trim", key: "target_models", value: `[""]`},
		{name: "unknown policy", key: "unknown_policy", value: "regex"},
		{name: "max attempts too low", key: "max_attempts", value: "0"},
		{name: "max attempts too high", key: "max_attempts", value: "11"},
		{name: "retention too low", key: "retention_days", value: "0"},
		{name: "retention too high", key: "retention_days", value: "31"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := GetSettings()
			require.Error(t, Configure(map[string]string{test.key: test.value}))
			assert.Equal(t, before, GetSettings())
		})
	}
}

func TestSnapshotImmutability(t *testing.T) {
	require.NoError(t, Configure(map[string]string{"target_models": `["old-model"]`, "max_attempts": "3"}))
	old := GetSettings()
	require.Equal(t, []string{"old-model"}, old.TargetModels)

	require.NoError(t, Configure(map[string]string{"target_models": `["new-model"]`, "max_attempts": "5"}))
	assert.Equal(t, []string{"old-model"}, old.TargetModels)
	assert.Equal(t, 3, old.MaxAttempts)
	assert.Equal(t, []string{"new-model"}, GetSettings().TargetModels)
	assert.Equal(t, 5, GetSettings().MaxAttempts)

	old.TargetModels = append(old.TargetModels, "mutated")
	old.Enabled = true
	current := GetSettings()
	assert.Equal(t, []string{"new-model"}, current.TargetModels)
}

func TestConcurrentReadersWithOldSnapshot(t *testing.T) {
	require.NoError(t, Configure(map[string]string{
		"target_models":  `["initial"]`,
		"max_attempts":   "3",
		"retention_days": "7",
	}))
	old := GetSettings()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snapshot := old
				assert.Equal(t, []string{"initial"}, snapshot.TargetModels)
				assert.Equal(t, 3, snapshot.MaxAttempts)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			require.NoError(t, Configure(map[string]string{
				"target_models":  `["kimi-k3"]`,
				"max_attempts":   strconv.Itoa(1 + i%(MaxMaxAttempts-MinMaxAttempts+1)),
				"retention_days": strconv.Itoa(1 + i%(MaxRetentionDays-MinRetentionDays+1)),
			}))
		}
		close(stop)
	}()
	wg.Wait()
}
