package model

import "fmt"

// AI Router telemetry retention: bounded deletion of rows that fell out of the
// configured retention window. The task that drives this lives in
// service/ai_router/retention.go; the SQL lives here so both backends (MySQL in
// production, SQLite in tests) exercise the same code.

// DefaultAIRouterRetentionBatchSize bounds one retention delete. It is the
// fallback for a non-positive limit, not the configured value: the task owns
// the batch size so it can pace its loop.
const DefaultAIRouterRetentionBatchSize = 1000

// errRetentionCutoffInvalid guards a cutoff that would delete everything:
// created_at is a unix second, so a non-positive cutoff matches (almost) the
// whole table. Refusing is cheaper than explaining the data loss.
func errRetentionCutoffInvalid(cutoff int64) error {
	return fmt.Errorf("ai router retention cutoff must be a positive unix second, got %d", cutoff)
}

// deleteAIRouterRowsBefore deletes at most limit rows created before cutoff,
// returning how many rows went away.
//
// Two statements on purpose. `DELETE ... LIMIT` is not portable: SQLite accepts
// the syntax and then silently ignores the LIMIT — asking it for 3 of 10
// matching rows deletes all 10 — so the batch bound would be a lie and the
// caller's pacing meaningless. Selecting the ids first keeps the bound real on
// both backends, and the SELECT rides the created_at index (the DELETEs are
// primary-key lookups). The alternative, a DELETE with a subquery on its own
// table, is rejected by MySQL with error 1093.
func deleteAIRouterRowsBefore(value any, cutoff int64, limit int) (int, error) {
	if cutoff <= 0 {
		return 0, errRetentionCutoffInvalid(cutoff)
	}
	if limit <= 0 {
		limit = DefaultAIRouterRetentionBatchSize
	}

	var ids []int
	if err := DB.Model(value).
		Where("created_at < ?", cutoff).
		// created_at first so the scan follows the retention index instead of
		// the primary key; id breaks ties deterministically.
		Order("created_at asc, id asc").
		Limit(limit).
		Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	result := DB.Where("id IN ?", ids).Delete(value)
	if result.Error != nil {
		return 0, result.Error
	}
	return int(result.RowsAffected), nil
}

// DeleteAIRouterAttemptLogsBefore removes up to limit attempt rows older than
// the cutoff unix second.
func DeleteAIRouterAttemptLogsBefore(cutoff int64, limit int) (int, error) {
	return deleteAIRouterRowsBefore(&AIRouterAttemptLog{}, cutoff, limit)
}

// DeleteAIRouterLogsBefore removes up to limit request summary rows older than
// the cutoff unix second.
func DeleteAIRouterLogsBefore(cutoff int64, limit int) (int, error) {
	return deleteAIRouterRowsBefore(&AIRouterLog{}, cutoff, limit)
}
