package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func intPtrM(v int) *int {
	return &v
}

func int64PtrM(v int64) *int64 {
	return &v
}

// Seed one summary with attempts and return its id.
func seedRouterLogWithAttempts(t *testing.T, success bool, attempts int, channelsJSON string, attemptsSeed []*AIRouterAttemptLog) int {
	t.Helper()
	log := &AIRouterLog{
		RequestId:        "req-seed",
		UserId:           1,
		GroupId:          "default",
		Model:            "kimi-k3",
		Modality:         "text",
		Attempts:         attempts,
		MaxAttempts:      3,
		SelectedChannels: channelsJSON,
		FinalChannelId:   4,
		Success:          success,
		TotalDurationMs:  500,
		CreatedAt:        1726500000,
	}
	require.NoError(t, DB.Create(log).Error)
	for _, attempt := range attemptsSeed {
		attempt.RequestLogId = log.Id
	}
	require.NoError(t, InsertAIRouterAttemptLogs(attemptsSeed))
	return log.Id
}

func TestGetAIRouterLogsWithErrorFilters(t *testing.T) {
	truncateTables(t)

	// Request A: a 429 attempt. Request B: only success. Request C: no
	// attempt rows at all (pre-Phase-3 shape).
	seedRouterLogWithAttempts(t, false, 1, "[4,5]", []*AIRouterAttemptLog{
		{AttemptSeq: 1, ChannelRetryNo: 0, ChannelId: 4, ErrorCategory: AttemptErrorCategory429, HttpStatusCode: intPtrM(429), CreatedAt: 1726500000},
		{AttemptSeq: 2, ChannelRetryNo: 0, ChannelId: 5, ErrorCategory: AttemptErrorCategorySuccess, Success: true, CreatedAt: 1726500001},
	})
	seedRouterLogWithAttempts(t, true, 1, "[6]", []*AIRouterAttemptLog{
		{AttemptSeq: 1, ChannelRetryNo: 0, ChannelId: 6, ErrorCategory: AttemptErrorCategorySuccess, Success: true, TtftMs: int64PtrM(40), CreatedAt: 1726500002},
	})
	seedRouterLogWithAttempts(t, true, 1, "[7]", nil)

	// status filter matches only request A.
	_, total, err := GetAIRouterLogs(0, 0, "", 0, "", nil, "", "", 0, 429, "", nil, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)

	// category filter matches only request A.
	_, total, err = GetAIRouterLogs(0, 0, "", 0, "", nil, "", "", 0, 0, AttemptErrorCategory429, nil, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)

	// stream_interrupted=true matches nothing here.
	_, total, err = GetAIRouterLogs(0, 0, "", 0, "", nil, "", "", 0, 0, "", boolPtrM(true), 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(0), total)

	// stream_interrupted=false matches everything without an interrupted
	// attempt: A and B (success only) and C (no attempt rows at all).
	_, total, err = GetAIRouterLogs(0, 0, "", 0, "", nil, "", "", 0, 0, "", boolPtrM(false), 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(3), total)

	// channel_id still matches the attempt chain (request A has channel 5).
	_, total, err = GetAIRouterLogs(0, 0, "", 0, "", nil, "", "", 5, 0, "", nil, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
}

func boolPtrM(v bool) *bool {
	return &v
}

func TestGetAIRouterLogDetail(t *testing.T) {
	truncateTables(t)

	id := seedRouterLogWithAttempts(t, false, 1, "[4,5]", []*AIRouterAttemptLog{
		{AttemptSeq: 1, ChannelRetryNo: 0, ChannelId: 4, ChannelName: "alpha", ErrorCategory: AttemptErrorCategory5xx, HttpStatusCode: intPtrM(503), Retryable: true, DurationMs: 84, CreatedAt: 1726500000},
		{AttemptSeq: 2, ChannelRetryNo: 0, ChannelId: 5, ChannelName: "beta", ErrorCategory: AttemptErrorCategorySuccess, Success: true, TtftMs: int64PtrM(250), DurationMs: 1120, CreatedAt: 1726500001},
	})

	detail, err := GetAIRouterLogDetail(id)
	require.NoError(t, err)
	require.NotNil(t, detail)
	require.Equal(t, id, detail.Log.Id)
	require.Len(t, detail.Attempts, 2)
	// Ordered by attempt_seq: 4 before 5 despite insert order.
	require.Equal(t, 1, detail.Attempts[0].AttemptSeq)
	require.Equal(t, 4, detail.Attempts[0].ChannelId)
	require.Equal(t, 503, *detail.Attempts[0].HttpStatusCode)
	require.Equal(t, 2, detail.Attempts[1].AttemptSeq)
	require.Equal(t, "beta", detail.Attempts[1].ChannelName)

	// Same-channel retries keep their retry order.
	sameChannelId := seedRouterLogWithAttempts(t, false, 1, "[8]", []*AIRouterAttemptLog{
		{AttemptSeq: 1, ChannelRetryNo: 2, ChannelId: 8, ErrorCategory: AttemptErrorCategoryTimeout, CreatedAt: 1726500010},
		{AttemptSeq: 1, ChannelRetryNo: 0, ChannelId: 8, ErrorCategory: AttemptErrorCategoryTimeout, CreatedAt: 1726500008},
		{AttemptSeq: 1, ChannelRetryNo: 1, ChannelId: 8, ErrorCategory: AttemptErrorCategoryTimeout, CreatedAt: 1726500009},
	})
	detail, err = GetAIRouterLogDetail(sameChannelId)
	require.NoError(t, err)
	require.Len(t, detail.Attempts, 3)
	require.Equal(t, []int{0, 1, 2}, []int{
		detail.Attempts[0].ChannelRetryNo,
		detail.Attempts[1].ChannelRetryNo,
		detail.Attempts[2].ChannelRetryNo,
	})

	// Pre-Phase-3 log: summary exists, attempts list is empty (not nil, no error).
	oldId := seedRouterLogWithAttempts(t, true, 1, "[9]", nil)
	detail, err = GetAIRouterLogDetail(oldId)
	require.NoError(t, err)
	require.NotNil(t, detail)
	require.Empty(t, detail.Attempts)
	require.NotNil(t, detail.Attempts)

	// Unknown id -> nil, no error.
	missing, err := GetAIRouterLogDetail(99999)
	require.NoError(t, err)
	require.Nil(t, missing)
}

func TestAIRouterStatsFallbackRateAndAttemptConsistency(t *testing.T) {
	truncateTables(t)

	// Two requests: A falls back (2 distinct channels, 3 attempts incl.
	// same-channel retry), B direct success (1 attempt).
	logA := seedRouterLogWithAttempts(t, false, 2, "[4,5]", []*AIRouterAttemptLog{
		{AttemptSeq: 1, ChannelRetryNo: 0, ChannelId: 4, ErrorCategory: AttemptErrorCategory429, HttpStatusCode: intPtrM(429), Retryable: true, DurationMs: 100, CreatedAt: 1726500000},
		{AttemptSeq: 2, ChannelRetryNo: 0, ChannelId: 5, ErrorCategory: AttemptErrorCategoryTimeout, DurationMs: 200, CreatedAt: 1726500001},
	})
	_ = logA
	logB := seedRouterLogWithAttempts(t, true, 1, "[6]", []*AIRouterAttemptLog{
		{AttemptSeq: 1, ChannelRetryNo: 0, ChannelId: 6, ErrorCategory: AttemptErrorCategorySuccess, Success: true, DurationMs: 300, CreatedAt: 1726500002},
	})
	_ = logB

	stats, err := GetAIRouterStats(0, 0, "", "")
	require.NoError(t, err)
	require.Equal(t, int64(2), stats.TotalRequests)
	require.Equal(t, int64(1), stats.FallbackCount)
	require.InDelta(t, 0.5, stats.FallbackRate, 0.0001)

	// Attempt ledger is self-consistent: per-channel sums equal totals.
	attemptStats, err := GetAIRouterAttemptChannelStats(0, 0)
	require.NoError(t, err)
	var totalAttempts, sumByChannel int64
	for _, s := range attemptStats {
		sumByChannel += s.Attempts
	}
	totalAttempts, _, err = CountAIRouterAttemptStats(0, 0)
	require.NoError(t, err)
	require.Equal(t, totalAttempts, sumByChannel)
	require.Equal(t, int64(3), totalAttempts)

	// Channel 4 has exactly one 429 and no successes.
	require.Equal(t, int64(1), attemptStats[4].Count429)
	require.Equal(t, int64(0), attemptStats[4].SuccessCount)
	require.Equal(t, int64(1), attemptStats[5].CountTimeout)
}
