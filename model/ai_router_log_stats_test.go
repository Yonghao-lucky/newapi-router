package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertAIRouterLog(t *testing.T, log *AIRouterLog) {
	t.Helper()
	require.NoError(t, DB.Create(log).Error)
}

// The fallback filter separates single-channel chains from multi-channel
// (fallback) chains; the channel filter matches any position of the attempt
// chain including single-element arrays.
func TestGetAIRouterLogsFallbackAndChannelFilters(t *testing.T) {
	truncateTables(t)
	base := int64(1758000000)
	insertAIRouterLog(t, &AIRouterLog{RequestId: "r1", Model: "m", Attempts: 1, SelectedChannels: "[4]", FinalChannelId: 4, Success: true, CreatedAt: base})
	insertAIRouterLog(t, &AIRouterLog{RequestId: "r2", Model: "m", Attempts: 3, SelectedChannels: "[4,9,10]", FinalChannelId: 10, Success: true, CreatedAt: base + 1})
	insertAIRouterLog(t, &AIRouterLog{RequestId: "r3", Model: "m", Attempts: 3, SelectedChannels: "[14,15]", FinalChannelId: 15, Success: false, CreatedAt: base + 2})
	insertAIRouterLog(t, &AIRouterLog{RequestId: "r4", Model: "m", Attempts: 0, SelectedChannels: "", FinalChannelId: 0, Success: true, CreatedAt: base + 3})

	_, fallbackTrue, err := GetAIRouterLogs(0, 0, "m", 0, "", nil, "", "true", 0, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), fallbackTrue, "attempts>1 rows are fallback")

	_, fallbackFalse, err := GetAIRouterLogs(0, 0, "m", 0, "", nil, "", "false", 0, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), fallbackFalse, "attempts<=1 rows are non-fallback")

	_, ch4, err := GetAIRouterLogs(0, 0, "m", 0, "", nil, "", "", 4, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), ch4, "channel 4 appears in two chains")

	_, ch14, err := GetAIRouterLogs(0, 0, "m", 0, "", nil, "", "", 14, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(1), ch14, "channel 14 must not match channel 4 chains")
	_, ch1, err := GetAIRouterLogs(0, 0, "m", 0, "", nil, "", "", 1, 1, 20)
	require.NoError(t, err)
	assert.Zero(t, ch1, "channel 1 must not match channel 14 via substring")

	_, final10, err := GetAIRouterLogs(0, 0, "m", 0, "", nil, "", "", 10, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(1), final10)
}

// Stats expose the fallback count/rate next to success metrics; trend buckets
// split success/failure per bucket; empty ranges return no points.
func TestGetAIRouterStatsFallbackAndTrend(t *testing.T) {
	truncateTables(t)
	start := int64(1758000000) // aligned to an hour
	insertAIRouterLog(t, &AIRouterLog{RequestId: "a", Model: "m", Attempts: 1, SelectedChannels: "[4]", FinalChannelId: 4, Success: true, CreatedAt: start + 10})
	insertAIRouterLog(t, &AIRouterLog{RequestId: "b", Model: "m", Attempts: 2, SelectedChannels: "[4,9]", FinalChannelId: 9, Success: true, CreatedAt: start + 20})
	insertAIRouterLog(t, &AIRouterLog{RequestId: "c", Model: "m", Attempts: 2, SelectedChannels: "[4,9]", FinalChannelId: 9, Success: false, CreatedAt: start + 3600 + 30})

	stats, err := GetAIRouterStats(start, start+7200, "m", "")
	require.NoError(t, err)
	assert.Equal(t, int64(3), stats.TotalRequests)
	assert.Equal(t, int64(2), stats.FallbackCount)
	assert.InDelta(t, 2.0/3.0, stats.FallbackRate, 0.0001)
	assert.InDelta(t, 2.0/3.0, stats.SuccessRate, 0.0001)

	trend, err := GetAIRouterTrend(start, start+7200, "m", "")
	require.NoError(t, err)
	require.Len(t, trend, 2)
	assert.Equal(t, int64(2), trend[0].Total)
	assert.Equal(t, int64(2), trend[0].Success)
	assert.Equal(t, int64(1), trend[1].Total)
	assert.Zero(t, trend[1].Success)
	assert.Equal(t, start, trend[0].Time)

	empty, err := GetAIRouterTrend(start+7200, start+14400, "m", "")
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// Per-channel aggregates count every chain that includes the channel, with
// final/success attribution from final_channel_id.
func TestGetAIRouterChannelStatsAggregates(t *testing.T) {
	truncateTables(t)
	start := int64(1758000000)
	mk := func(id string, chain string, final int, success bool, duration int64, ttft int64) *AIRouterLog {
		return &AIRouterLog{RequestId: id, Model: "m", Attempts: 1, SelectedChannels: chain,
			FinalChannelId: final, Success: success, TotalDurationMs: duration, TTFTMs: ttft, CreatedAt: start}
	}
	insertAIRouterLog(t, mk("a", "[4,9]", 9, true, 1000, 200))
	insertAIRouterLog(t, mk("b", "[4,9]", 9, false, 2000, 0))
	insertAIRouterLog(t, mk("c", "[4]", 4, true, 300, 100))
	insertAIRouterLog(t, mk("d", "[8]", 8, true, 500, 0))

	stats, err := GetAIRouterChannelStats(start, start+3600, "m", "")
	require.NoError(t, err)
	byID := map[int]AIRouterChannelStat{}
	for _, s := range stats {
		byID[s.ChannelId] = s
	}

	ch4 := byID[4]
	assert.Equal(t, int64(3), ch4.Attempts, "channel 4 is on three chains")
	assert.Equal(t, int64(1), ch4.Finals)
	assert.Equal(t, int64(1), ch4.SuccessFinals)
	assert.InDelta(t, 300.0, ch4.AvgDurationMs, 0.001)

	ch9 := byID[9]
	assert.Equal(t, int64(2), ch9.Attempts)
	assert.Equal(t, int64(2), ch9.Finals)
	assert.Equal(t, int64(1), ch9.SuccessFinals)
	assert.InDelta(t, 0.5, ch9.SuccessRate, 0.0001)
	assert.InDelta(t, 1500.0, ch9.AvgDurationMs, 0.001)
	assert.InDelta(t, 200.0, ch9.AvgTTFTMs, 0.001, "zero TTFT rows are excluded from the TTFT average")

	ch8 := byID[8]
	assert.Equal(t, int64(1), ch8.Attempts)

	// Model scoping excludes other models' chains.
	other := &AIRouterLog{RequestId: "x", Model: "other", SelectedChannels: "[4]", FinalChannelId: 4, Success: true, CreatedAt: start}
	insertAIRouterLog(t, other)
	scoped, err := GetAIRouterChannelStats(start, start+3600, "m", "")
	require.NoError(t, err)
	scopedByID := map[int]AIRouterChannelStat{}
	for _, s := range scoped {
		scopedByID[s.ChannelId] = s
	}
	assert.Equal(t, int64(3), scopedByID[4].Attempts, "the other-model chain must not be counted under model=m")
}
