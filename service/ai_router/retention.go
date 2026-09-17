package ai_router

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"

	"github.com/bytedance/gopkg/util/gopool"
)

const (
	// retentionCleanupInterval is the sweep cadence. Hourly matches the other
	// maintenance tasks in this codebase (auth artifacts, subscription
	// maintenance) and stays far inside the smallest retention window (1 day).
	retentionCleanupInterval = time.Hour
	// retentionCleanupBatchSize bounds one delete. Small enough that a batch is
	// a short statement, large enough that a day of traffic clears in a few
	// rounds.
	retentionCleanupBatchSize = 1000
	// retentionCleanupBatchPause sits between batches so catching up on a large
	// backlog never becomes one long lock-holding transaction on a busy table.
	retentionCleanupBatchPause = 100 * time.Millisecond
)

var (
	retentionCleanupOnce    sync.Once
	retentionCleanupRunning atomic.Bool
)

// StartRetentionCleanupTask starts the AI Router telemetry retention task: one
// round immediately at boot, then one every retentionCleanupInterval. Only the
// master instance runs it, like every other maintenance task in service/.
//
// The gate is re-read from the in-memory settings snapshot on every round, never
// the database and never only at boot: turning telemetry off stops deletion
// without a restart, and turning it back on resumes it. A boot-time-only gate
// would leave an administrator who enables telemetry later with no cleanup until
// the next deploy.
func StartRetentionCleanupTask() {
	retentionCleanupOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			logger.LogInfo(context.Background(), fmt.Sprintf(
				"ai_router retention cleanup task started: tick=%s batch=%d",
				retentionCleanupInterval, retentionCleanupBatchSize,
			))
			runRetentionCleanupOnce()
			ticker := time.NewTicker(retentionCleanupInterval)
			defer ticker.Stop()
			for range ticker.C {
				runRetentionCleanupOnce()
			}
		})
	})
}

// retentionCutoff reports the delete boundary for one round: every telemetry row
// created strictly before it has expired. The second return is false when
// cleanup must not run at all.
//
// Telemetry off means don't touch the data. The rows already written stay
// readable for analysis instead of vanishing while an administrator believes the
// feature is idle — retention is a policy for live telemetry, not a shredder for
// whatever happens to be on disk. An out-of-range retention_days is refused for
// the same reason the model layer refuses a non-positive cutoff: it can only be
// a corrupt or hand-edited value, and acting on it deletes far more than anyone
// intended.
func retentionCutoff(now time.Time, settings ai_router_setting.Settings) (int64, bool) {
	if !settings.TelemetryEnabled {
		return 0, false
	}
	days := settings.RetentionDays
	if days < ai_router_setting.MinRetentionDays || days > ai_router_setting.MaxRetentionDays {
		return 0, false
	}
	return now.Unix() - int64(days)*24*60*60, true
}

// retentionDeleteFunc removes up to limit rows older than cutoff, reporting how
// many were removed. It is the shape of model.DeleteAIRouterAttemptLogsBefore and
// model.DeleteAIRouterLogsBefore, named so a round can be driven directly in
// tests.
type retentionDeleteFunc func(cutoff int64, limit int) (int, error)

// runRetentionCleanupOnce performs one sweep. It owns the two properties that
// must hold no matter what the database does: the running guard, so a slow round
// cannot be joined by the next one, and the panic boundary, so maintenance can
// never take the process down with it.
func runRetentionCleanupOnce() {
	defer recoverRetentionPanic()

	if !retentionCleanupRunning.CompareAndSwap(false, true) {
		return
	}
	defer retentionCleanupRunning.Store(false)

	retentionCleanupRound(
		time.Now(),
		ai_router_setting.GetSettings(),
		model.DeleteAIRouterAttemptLogsBefore,
		model.DeleteAIRouterLogsBefore,
	)
}

// recoverRetentionPanic is deferred directly at the round boundary so recover()
// still sees the unwinding stack. A panic in an hourly maintenance task would
// otherwise kill the whole process, taking live relay traffic with it.
func recoverRetentionPanic() {
	if r := recover(); r != nil {
		common.SysError(fmt.Sprintf("ai_router retention cleanup panic: %v", r))
	}
}

// retentionCleanupRound runs one sweep against the given deletes and reports how
// many rows each table lost. It is split from runRetentionCleanupOnce so the gate,
// the deletion order and the failure behaviour can be exercised without a database.
func retentionCleanupRound(
	now time.Time,
	settings ai_router_setting.Settings,
	deleteAttempts, deleteLogs retentionDeleteFunc,
) (int, int, error) {
	ctx := context.Background()
	cutoff, ok := retentionCutoff(now, settings)
	if !ok {
		return 0, 0, nil
	}
	started := time.Now()

	// Attempt rows go first. They are strictly derived children of a summary
	// row: an interrupted sweep then leaves summaries whose detail drawer shows
	// the "written before attempt telemetry" note, which is the benign state.
	// The other order would leave attempt rows pointing at a request that no
	// longer exists — unreachable rows nobody can read, occupying space until
	// their own created_at expires. Both share the same timestamp, so neither
	// order changes what a completed sweep removes.
	attemptsDeleted, err := deleteAIRouterRowsInBatches(func(limit int) (int, error) {
		return deleteAttempts(cutoff, limit)
	})
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf(
			"ai_router retention cleanup failed on attempt logs (attempts_deleted=%d): %v",
			attemptsDeleted, err,
		))
		// Leave the summaries alone: orphaning attempts is the one outcome worth
		// avoiding, and the next round retries the whole sweep.
		return attemptsDeleted, 0, err
	}

	logsDeleted, err := deleteAIRouterRowsInBatches(func(limit int) (int, error) {
		return deleteLogs(cutoff, limit)
	})
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf(
			"ai_router retention cleanup failed on summary logs (attempts_deleted=%d logs_deleted=%d): %v",
			attemptsDeleted, logsDeleted, err,
		))
		return attemptsDeleted, logsDeleted, err
	}

	// One line per round: an idle round is still worth recording, it is how an
	// operator confirms the task is alive, and the duration is what tells them
	// whether the sweep is keeping up with traffic.
	logger.LogInfo(ctx, fmt.Sprintf(
		"ai_router retention cleanup: attempts_deleted=%d logs_deleted=%d retention_days=%d cutoff=%d duration_ms=%d",
		attemptsDeleted, logsDeleted, settings.RetentionDays, cutoff, time.Since(started).Milliseconds(),
	))
	return attemptsDeleted, logsDeleted, nil
}

// deleteAIRouterRowsInBatches drains expired rows a bounded batch at a time,
// pausing between batches, and reports the total removed. A short batch is the
// end of the backlog.
func deleteAIRouterRowsInBatches(deleteBatch func(limit int) (int, error)) (int, error) {
	total := 0
	for {
		deleted, err := deleteBatch(retentionCleanupBatchSize)
		if err != nil {
			return total, err
		}
		total += deleted
		if deleted < retentionCleanupBatchSize {
			return total, nil
		}
		time.Sleep(retentionCleanupBatchPause)
	}
}
