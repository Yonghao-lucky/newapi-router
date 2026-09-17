package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func seedAIRouterTelemetry(t *testing.T, attemptCreatedAts []int64, logCreatedAts []int64) {
	t.Helper()
	attempts := make([]*AIRouterAttemptLog, 0, len(attemptCreatedAts))
	for i, createdAt := range attemptCreatedAts {
		attempts = append(attempts, &AIRouterAttemptLog{
			RequestLogId:  i + 1,
			AttemptSeq:    1,
			ChannelId:     4,
			ChannelName:   "ch-4",
			ErrorCategory: AttemptErrorCategorySuccess,
			Success:       true,
			CreatedAt:     createdAt,
		})
	}
	require.NoError(t, InsertAIRouterAttemptLogs(attempts))

	logs := make([]*AIRouterLog, 0, len(logCreatedAts))
	for i, createdAt := range logCreatedAts {
		logs = append(logs, &AIRouterLog{
			RequestId: "req",
			Model:     "kimi-k3",
			GroupId:   "default",
			Attempts:  1,
			Success:   true,
			UserId:    i + 1,
			CreatedAt: createdAt,
		})
	}
	if len(logs) > 0 {
		require.NoError(t, DB.Create(&logs).Error)
	}
}

func countAIRouterAttempts(t *testing.T) int64 {
	t.Helper()
	var total int64
	require.NoError(t, DB.Model(&AIRouterAttemptLog{}).Count(&total).Error)
	return total
}

func countAIRouterLogs(t *testing.T) int64 {
	t.Helper()
	var total int64
	require.NoError(t, DB.Model(&AIRouterLog{}).Count(&total).Error)
	return total
}

// The cutoff is exclusive: a row created exactly at the boundary is retained.
// "Older than retention_days" must not quietly become "retention_days and older",
// which would make the window one second short of what the setting promises.
func TestDeleteAIRouterTelemetryBeforeIsExclusive(t *testing.T) {
	truncateTables(t)
	const cutoff = 1_700_000_000
	seedAIRouterTelemetry(t,
		[]int64{cutoff - 1, cutoff, cutoff + 1},
		[]int64{cutoff - 1, cutoff, cutoff + 1},
	)

	attempts, err := DeleteAIRouterAttemptLogsBefore(cutoff, 100)
	require.NoError(t, err)
	require.Equal(t, 1, attempts)

	logs, err := DeleteAIRouterLogsBefore(cutoff, 100)
	require.NoError(t, err)
	require.Equal(t, 1, logs)

	// The boundary row and the fresh row survive in both tables.
	require.Equal(t, int64(2), countAIRouterAttempts(t))
	require.Equal(t, int64(2), countAIRouterLogs(t))

	windows := []*AIRouterAttemptLog{}
	require.NoError(t, DB.Order("created_at asc").Find(&windows).Error)
	require.Equal(t, []int64{cutoff, cutoff + 1}, []int64{windows[0].CreatedAt, windows[1].CreatedAt})
}

// The batch bound must be real. This is the invariant that rules out
// `DELETE ... LIMIT`: SQLite accepts that syntax and ignores the LIMIT, so a
// "bounded" batch would silently take the whole backlog in one statement and the
// caller's pacing would mean nothing.
func TestDeleteAIRouterTelemetryHonoursBatchLimit(t *testing.T) {
	truncateTables(t)
	const cutoff = 1_700_000_000
	expired := make([]int64, 0, 10)
	for i := 0; i < 10; i++ {
		expired = append(expired, cutoff-int64(i)-1)
	}
	seedAIRouterTelemetry(t, expired, nil)

	deleted, err := DeleteAIRouterAttemptLogsBefore(cutoff, 3)
	require.NoError(t, err)
	require.Equal(t, 3, deleted, "batch limit must cap one delete")
	require.Equal(t, int64(7), countAIRouterAttempts(t))

	// Oldest first, so an interrupted sweep makes monotonic progress and a
	// range scan can use the created_at index instead of the primary key.
	var remaining []*AIRouterAttemptLog
	require.NoError(t, DB.Order("created_at asc").Find(&remaining).Error)
	require.Equal(t, int64(cutoff-7), remaining[0].CreatedAt)
	require.Equal(t, int64(cutoff-1), remaining[len(remaining)-1].CreatedAt)

	// A non-positive limit falls back to the default rather than deleting
	// everything or nothing.
	deleted, err = DeleteAIRouterAttemptLogsBefore(cutoff, 0)
	require.NoError(t, err)
	require.Equal(t, 7, deleted)
	require.Equal(t, int64(0), countAIRouterAttempts(t))
}

// A non-positive cutoff would match the whole table; refuse it instead of
// emptying the store.
func TestDeleteAIRouterTelemetryRejectsNonPositiveCutoff(t *testing.T) {
	truncateTables(t)
	seedAIRouterTelemetry(t, []int64{100}, []int64{100})

	for _, cutoff := range []int64{0, -1} {
		_, err := DeleteAIRouterAttemptLogsBefore(cutoff, 100)
		require.Error(t, err)
		_, err = DeleteAIRouterLogsBefore(cutoff, 100)
		require.Error(t, err)
	}

	require.Equal(t, int64(1), countAIRouterAttempts(t))
	require.Equal(t, int64(1), countAIRouterLogs(t))
}

// Nothing expired is a normal outcome, not an error: an idle hourly round.
func TestDeleteAIRouterTelemetryWithNothingExpired(t *testing.T) {
	truncateTables(t)
	seedAIRouterTelemetry(t, []int64{2_000_000_000}, []int64{2_000_000_000})

	attempts, err := DeleteAIRouterAttemptLogsBefore(1_700_000_000, 100)
	require.NoError(t, err)
	require.Equal(t, 0, attempts)

	logs, err := DeleteAIRouterLogsBefore(1_700_000_000, 100)
	require.NoError(t, err)
	require.Equal(t, 0, logs)

	require.Equal(t, int64(1), countAIRouterAttempts(t))
	require.Equal(t, int64(1), countAIRouterLogs(t))
}
