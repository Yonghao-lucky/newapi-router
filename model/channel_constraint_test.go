package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterCandidateIDs(t *testing.T) {
	alphaSetting := `{"task_plugin_key":"alpha"}`
	betaSetting := `{"task_plugin_key":"beta"}`
	alpha := &Channel{Id: 900001, Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Setting: &alphaSetting}
	beta := &Channel{Id: 900002, Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Setting: &betaSetting}
	ordinary := &Channel{Id: 900003, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled}
	kling := &Channel{Id: 900004, Type: constant.ChannelTypeKling, Status: common.ChannelStatusEnabled}
	jimeng := &Channel{Id: 900005, Type: constant.ChannelTypeJimeng, Status: common.ChannelStatusEnabled}
	matchingCustom := &Channel{Id: 900010, Type: constant.ChannelTypeAdvancedCustom, Status: common.ChannelStatusEnabled}
	matchingCustom.SetOtherSettings(kitdto.ChannelOtherSettings{
		AdvancedCustom: &kitdto.AdvancedCustomConfig{
			Routes: []kitdto.AdvancedCustomRoute{{
				IncomingPath: "/v1/chat/completions",
				Models:       []string{"gpt-4"},
			}},
		},
	})
	otherCustom := &Channel{Id: 900011, Type: constant.ChannelTypeAdvancedCustom, Status: common.ChannelStatusEnabled}
	otherCustom.SetOtherSettings(kitdto.ChannelOtherSettings{
		AdvancedCustom: &kitdto.AdvancedCustomConfig{
			Routes: []kitdto.AdvancedCustomRoute{{
				IncomingPath: "/v1/responses",
				Models:       []string{"gpt-4"},
			}},
		},
	})

	pathFilter := dto.ChannelFilter{Kind: dto.FilterRequestPath, RequestPath: "/v1/chat/completions"}
	emptyPathFilter := dto.ChannelFilter{Kind: dto.FilterRequestPath, RequestPath: ""}

	tests := []struct {
		name      string
		ids       []int
		modelName string
		filters   []dto.ChannelFilter
		wantKept  []int
		wantEmpty dto.ChannelFilterKind
	}{
		{
			name:      "identity keeps matching type-59 key",
			ids:       []int{900001, 900002},
			modelName: "shared",
			filters:   identityFilters("alpha", nil),
			wantKept:  []int{900001},
		},
		{
			name:      "identity empty key drops all type-59",
			ids:       []int{900001, 900002},
			modelName: "shared",
			filters:   identityFilters("", nil),
			wantKept:  []int{},
			wantEmpty: dto.FilterTaskPluginIdentity,
		},
		{
			name:      "identity empty key keeps ordinary channel",
			ids:       []int{900003},
			modelName: "ordinary",
			filters:   identityFilters("", nil),
			wantKept:  []int{900003},
		},
		{
			name:      "identity keeps matching legacy type",
			ids:       []int{900004, 900005},
			modelName: "legacy",
			filters:   identityFilters("legacy-alpha", []int{constant.ChannelTypeKling}),
			wantKept:  []int{900004},
		},
		{
			name:      "identity keeps all listed legacy types",
			ids:       []int{900004, 900005},
			modelName: "legacy",
			filters:   identityFilters("legacy-alpha", []int{constant.ChannelTypeKling, constant.ChannelTypeJimeng}),
			wantKept:  []int{900004, 900005},
		},
		{
			name:      "identity keyed with no types drops legacy",
			ids:       []int{900004, 900005},
			modelName: "legacy",
			filters:   identityFilters("legacy-alpha", nil),
			wantKept:  []int{},
			wantEmpty: dto.FilterTaskPluginIdentity,
		},
		{
			name:      "identity drops missing cache entry",
			ids:       []int{900004, 999999},
			modelName: "legacy",
			filters:   identityFilters("legacy-alpha", []int{constant.ChannelTypeKling}),
			wantKept:  []int{900004},
		},
		{
			name:      "empty request path is a passthrough including missing ids",
			ids:       []int{900003, 900010, 999999},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{emptyPathFilter},
			wantKept:  []int{900003, 900010, 999999},
		},
		{
			name:      "request path keeps missing cache entry for consistency",
			ids:       []int{900003, 999999},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{pathFilter},
			wantKept:  []int{900003, 999999},
		},
		{
			name:      "request path keeps matching type-58 and ordinary",
			ids:       []int{900003, 900010, 900011},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{pathFilter},
			wantKept:  []int{900003, 900010},
		},
		{
			name:      "request path empties when only unmatched type-58 remains",
			ids:       []int{900011},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{pathFilter},
			wantKept:  []int{},
			wantEmpty: dto.FilterRequestPath,
		},
		{
			name:      "intersection attributes empty set to identity after path keeps candidates",
			ids:       []int{900001, 900010},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{pathFilter, identityFilters("missing", nil)[0]},
			wantKept:  []int{},
			wantEmpty: dto.FilterTaskPluginIdentity,
		},
		{
			name:      "intersection attributes empty set to path when path runs first",
			ids:       []int{900011},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{identityFilters("", nil)[0], pathFilter},
			wantKept:  []int{},
			wantEmpty: dto.FilterRequestPath,
		},
	}

	channelSyncLock.Lock()
	previous := channelsIDM
	channelsIDM = map[int]*Channel{
		900001: alpha,
		900002: beta,
		900003: ordinary,
		900004: kling,
		900005: jimeng,
		900010: matchingCustom,
		900011: otherCustom,
	}
	t.Cleanup(func() {
		channelsIDM = previous
		channelSyncLock.Unlock()
	})

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			kept, emptiedBy := filterCandidateIDs(testCase.ids, testCase.modelName, testCase.filters)
			if testCase.wantKept == nil {
				assert.Nil(t, kept)
			} else {
				assert.Equal(t, testCase.wantKept, kept)
			}
			assert.Equal(t, testCase.wantEmpty, emptiedBy)
		})
	}
}

// Baseline fixture for AI Router Phase 0: three enabled OpenAI channels
// offering the same model at two priority levels, mirroring the planned
// A/B(priority=100)/C(priority=50) fallback topology.
func baselinePriorityChannels(t *testing.T) {
	t.Helper()
	high, low := int64(100), int64(50)
	weight := uint(0)
	channels := []*Channel{
		{Id: 920001, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "A", Models: "kimi-k3", Group: "default", Priority: &high, Weight: &weight},
		{Id: 920002, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "B", Models: "kimi-k3", Group: "default", Priority: &high, Weight: &weight},
		{Id: 920003, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "C", Models: "kimi-k3", Group: "default", Priority: &low, Weight: &weight},
	}
	for _, channel := range channels {
		require.NoError(t, channel.Insert())
	}
}

// TestGetRandomSatisfiedChannelRetrySkipsSamePriority pins the current retry
// semantics that AI Router V1 will change for target models: the retry index
// maps directly onto a priority tier, so a same-priority sibling is never
// retried after a failure, and an exhausted tier index clamps to the last
// tier instead of failing over to a lower one.
func TestGetRandomSatisfiedChannelRetrySkipsSamePriority(t *testing.T) {
	originalMemoryCache := common.MemoryCacheEnabled
	originalChannels := channelsIDM
	originalGroups := group2model2channels
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = originalMemoryCache
		channelsIDM = originalChannels
		group2model2channels = originalGroups
	})
	baselinePriorityChannels(t)
	InitChannelCache()

	// retry=0 selects the highest tier (A or B).
	first, err := GetRandomSatisfiedChannel("default", "kimi-k3", 0, nil)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Contains(t, []string{"A", "B"}, first.Name)

	// retry=1 jumps straight to the lower tier: same-priority sibling B is
	// unreachable through the retry index.
	second, err := GetRandomSatisfiedChannel("default", "kimi-k3", 1, nil)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.Equal(t, "C", second.Name)

	// retry beyond the tier count clamps to the last tier, so a repeated
	// call with a clamped index can return the same channel that already
	// failed this request (there is no per-request exclusion set).
	for _, clamped := range []int{2, 5} {
		again, err := GetRandomSatisfiedChannel("default", "kimi-k3", clamped, nil)
		require.NoError(t, err)
		require.NotNil(t, again)
		assert.Equal(t, "C", again.Name)
	}
}

// TestGetRandomSatisfiedChannelSingleTierReturnsAnySibling documents that a
// single tier with weight 0 channels may return any member on every call —
// including one already chosen earlier in the same request — because channel
// exclusion is not part of the current selection contract.
func TestGetRandomSatisfiedChannelSingleTierReturnsAnySibling(t *testing.T) {
	originalMemoryCache := common.MemoryCacheEnabled
	originalChannels := channelsIDM
	originalGroups := group2model2channels
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = originalMemoryCache
		channelsIDM = originalChannels
		group2model2channels = originalGroups
	})
	high := int64(100)
	weight := uint(0)
	channels := []*Channel{
		{Id: 920011, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "A", Models: "kimi-k3", Group: "default", Priority: &high, Weight: &weight},
		{Id: 920012, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "B", Models: "kimi-k3", Group: "default", Priority: &high, Weight: &weight},
	}
	for _, channel := range channels {
		require.NoError(t, channel.Insert())
	}
	InitChannelCache()

	seen := map[string]bool{}
	for range 50 {
		channel, err := GetRandomSatisfiedChannel("default", "kimi-k3", 0, nil)
		require.NoError(t, err)
		require.NotNil(t, channel)
		seen[channel.Name] = true
	}
	// Both siblings are reachable at the same tier; selection cannot exclude
	// a previously failed channel today.
	assert.Contains(t, seen, "A")
	assert.Contains(t, seen, "B")
}

func TestChannelSatisfiesFilters(t *testing.T) {
	alphaSetting := `{"task_plugin_key":"alpha"}`
	alpha := &Channel{Id: 1, Type: constant.ChannelTypeTaskPlugin, Setting: &alphaSetting}
	ordinary := &Channel{Id: 2, Type: constant.ChannelTypeOpenAI}
	custom := &Channel{Id: 3, Type: constant.ChannelTypeAdvancedCustom}
	custom.SetOtherSettings(kitdto.ChannelOtherSettings{
		AdvancedCustom: &kitdto.AdvancedCustomConfig{
			Routes: []kitdto.AdvancedCustomRoute{{
				IncomingPath: "/v1/chat/completions",
				Models:       []string{"gpt-4"},
			}},
		},
	})

	ok, kind := ChannelSatisfiesFilters(nil, "gpt-4", nil)
	assert.False(t, ok)
	assert.Equal(t, dto.ChannelFilterKind(""), kind)

	ok, kind = ChannelSatisfiesFilters(alpha, "shared", identityFilters("alpha", nil))
	require.True(t, ok)
	assert.Equal(t, dto.ChannelFilterKind(""), kind)

	ok, kind = ChannelSatisfiesFilters(alpha, "shared", identityFilters("beta", nil))
	assert.False(t, ok)
	assert.Equal(t, dto.FilterTaskPluginIdentity, kind)

	ok, kind = ChannelSatisfiesFilters(ordinary, "gpt-4", []dto.ChannelFilter{{
		Kind:        dto.FilterRequestPath,
		RequestPath: "/v1/chat/completions",
	}})
	require.True(t, ok)
	assert.Equal(t, dto.ChannelFilterKind(""), kind)

	ok, kind = ChannelSatisfiesFilters(custom, "gpt-4", []dto.ChannelFilter{{
		Kind:        dto.FilterRequestPath,
		RequestPath: "/v1/responses",
	}})
	assert.False(t, ok)
	assert.Equal(t, dto.FilterRequestPath, kind)
}
