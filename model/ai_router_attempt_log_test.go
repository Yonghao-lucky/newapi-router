package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAIRouterAttemptLogInsertAndQuery(t *testing.T) {
	truncateTables(t)

	logs := []*AIRouterAttemptLog{
		{RequestLogId: 1, RequestId: "req-1", AttemptSeq: 1, ChannelRetryNo: 0, ChannelId: 4, ChannelName: "ch-4", Modality: "text", ErrorCategory: AttemptErrorCategory429, Retryable: true, DurationMs: 120, Success: false, CreatedAt: 1726500000},
		{RequestLogId: 1, RequestId: "req-1", AttemptSeq: 2, ChannelRetryNo: 0, ChannelId: 5, ChannelName: "ch-5", Modality: "text", ErrorCategory: AttemptErrorCategorySuccess, DurationMs: 300, TtftMs: int64Ptr(90), Success: true, CreatedAt: 1726500100},
	}
	require.NoError(t, InsertAIRouterAttemptLogs(logs))

	got, err := GetAIRouterAttemptLogsByRequestLogId(1)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, 4, got[0].ChannelId)
	require.Equal(t, "ch-4", got[0].ChannelName)
	require.Nil(t, got[0].HttpStatusCode)
	require.Equal(t, int64(90), *got[1].TtftMs)

	// Unknown request: empty result, not an error (old summary rows).
	empty, err := GetAIRouterAttemptLogsByRequestLogId(999)
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestAIRouterAttemptChannelStatsGroupsByCategory(t *testing.T) {
	truncateTables(t)

	logs := []*AIRouterAttemptLog{
		// Channel 4: 1 success + 1 other failure.
		{RequestLogId: 1, AttemptSeq: 1, ChannelId: 4, ChannelName: "ch-4", ErrorCategory: AttemptErrorCategorySuccess, DurationMs: 100, Success: true, CreatedAt: 1726500000},
		{RequestLogId: 2, AttemptSeq: 1, ChannelId: 4, ChannelName: "ch-4", ErrorCategory: AttemptErrorCategoryOther, DurationMs: 50, CreatedAt: 1726500001},
		// Channel 5: 2 attempts, one 429 one 5xx.
		{RequestLogId: 2, AttemptSeq: 2, ChannelId: 5, ChannelName: "ch-5", ErrorCategory: AttemptErrorCategory429, DurationMs: 70, CreatedAt: 1726500002},
		{RequestLogId: 3, AttemptSeq: 1, ChannelId: 5, ChannelName: "ch-5", ErrorCategory: AttemptErrorCategory5xx, DurationMs: 80, CreatedAt: 1726500003},
	}
	require.NoError(t, InsertAIRouterAttemptLogs(logs))

	stats, err := GetAIRouterAttemptChannelStats(0, 0)
	require.NoError(t, err)
	require.Len(t, stats, 2)

	s4 := stats[4]
	require.NotNil(t, s4)
	require.Equal(t, int64(2), s4.Attempts)
	require.Equal(t, int64(1), s4.SuccessCount)
	require.Equal(t, int64(1), s4.CountOther)
	require.Equal(t, int64(150), s4.TotalDurationMs)

	s5 := stats[5]
	require.NotNil(t, s5)
	require.Equal(t, int64(2), s5.Attempts)
	require.Equal(t, int64(1), s5.Count429)
	require.Equal(t, int64(1), s5.Count5xx)
	require.Equal(t, int64(150), s5.TotalDurationMs)

	// Time-window filtering excludes everything before start.
	limited, err := GetAIRouterAttemptChannelStats(1726500002, 0)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	require.Equal(t, int64(2), limited[5].Attempts)
}

func TestCountAIRouterAttemptStats(t *testing.T) {
	truncateTables(t)

	logs := []*AIRouterAttemptLog{
		{RequestLogId: 1, ChannelId: 4, ErrorCategory: AttemptErrorCategorySuccess, Success: true, CreatedAt: 100},
		{RequestLogId: 2, ChannelId: 5, ErrorCategory: AttemptErrorCategory429, Success: false, CreatedAt: 200},
	}
	require.NoError(t, InsertAIRouterAttemptLogs(logs))

	total, success, err := CountAIRouterAttemptStats(0, 0)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Equal(t, int64(1), success)

	// Only the failed row is inside the window [150, ∞).
	total, success, err = CountAIRouterAttemptStats(150, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, int64(0), success)
}

func int64Ptr(v int64) *int64 {
	return &v
}
