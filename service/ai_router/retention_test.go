package ai_router

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/setting/ai_router_setting"
)

func TestRetentionCutoffGatesOnSettings(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	tests := []struct {
		name         string
		telemetryOn  bool
		retentionDay int
		wantRun      bool
	}{
		{name: "default settings", telemetryOn: true, retentionDay: 7, wantRun: true},
		{name: "shortest window", telemetryOn: true, retentionDay: ai_router_setting.MinRetentionDays, wantRun: true},
		{name: "longest window", telemetryOn: true, retentionDay: ai_router_setting.MaxRetentionDays, wantRun: true},
		// Telemetry off means the rows already on disk stay readable: retention
		// is a policy for live telemetry, not a shredder for idle data.
		{name: "telemetry disabled", telemetryOn: false, retentionDay: 7, wantRun: false},
		// An out-of-range window can only come from a corrupt or hand-edited
		// option; deleting on it would take far more than anyone intended.
		{name: "zero days", telemetryOn: true, retentionDay: 0, wantRun: false},
		{name: "negative days", telemetryOn: true, retentionDay: -1, wantRun: false},
		{name: "beyond the maximum", telemetryOn: true, retentionDay: ai_router_setting.MaxRetentionDays + 1, wantRun: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := ai_router_setting.DefaultSettings()
			settings.TelemetryEnabled = tt.telemetryOn
			settings.RetentionDays = tt.retentionDay

			cutoff, run := retentionCutoff(now, settings)
			if run != tt.wantRun {
				t.Fatalf("run = %v, want %v", run, tt.wantRun)
			}
			if !run {
				if cutoff != 0 {
					t.Fatalf("cutoff = %d, want 0 when cleanup is skipped", cutoff)
				}
				return
			}
			want := now.Unix() - int64(tt.retentionDay)*24*60*60
			if cutoff != want {
				t.Fatalf("cutoff = %d, want %d", cutoff, want)
			}
		})
	}
}

// The batch loop must drain the backlog and stop, and must not sleep on the
// final partial batch. retentionCleanupBatchPause is not honoured here (the
// clock is real), so the assertion is on call counts, not elapsed time.
func TestDeleteInBatchesDrainsAndStops(t *testing.T) {
	t.Run("exact multiple of the batch size", func(t *testing.T) {
		remaining := 3 * retentionCleanupBatchSize
		calls := 0
		total, err := deleteAIRouterRowsInBatches(func(limit int) (int, error) {
			calls++
			if limit != retentionCleanupBatchSize {
				t.Fatalf("limit = %d, want %d", limit, retentionCleanupBatchSize)
			}
			if remaining < limit {
				n := remaining
				remaining = 0
				return n, nil
			}
			remaining -= limit
			return limit, nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if total != 3*retentionCleanupBatchSize {
			t.Fatalf("total = %d, want %d", total, 3*retentionCleanupBatchSize)
		}
		// Three full batches plus the empty probe that ends the loop.
		if calls != 4 {
			t.Fatalf("calls = %d, want 4", calls)
		}
	})

	t.Run("partial final batch", func(t *testing.T) {
		remaining := retentionCleanupBatchSize + 5
		calls := 0
		total, err := deleteAIRouterRowsInBatches(func(limit int) (int, error) {
			calls++
			n := limit
			if remaining < limit {
				n = remaining
			}
			remaining -= n
			return n, nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if total != retentionCleanupBatchSize+5 {
			t.Fatalf("total = %d, want %d", total, retentionCleanupBatchSize+5)
		}
		if calls != 2 {
			t.Fatalf("calls = %d, want 2", calls)
		}
	})

	t.Run("nothing expired", func(t *testing.T) {
		calls := 0
		total, err := deleteAIRouterRowsInBatches(func(limit int) (int, error) {
			calls++
			return 0, nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if total != 0 || calls != 1 {
			t.Fatalf("total = %d calls = %d, want 0 and 1", total, calls)
		}
	})

	t.Run("mid-drain failure reports progress", func(t *testing.T) {
		calls := 0
		boom := errors.New("db gone")
		total, err := deleteAIRouterRowsInBatches(func(limit int) (int, error) {
			calls++
			if calls == 2 {
				return 0, boom
			}
			return limit, nil
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
		// The caller logs this count, so it must not include the failed batch.
		if total != retentionCleanupBatchSize {
			t.Fatalf("total = %d, want %d", total, retentionCleanupBatchSize)
		}
	})
}

// A round must not run when telemetry is off or the window is unusable, and it
// must be the settings snapshot of *this* round that decides — the task lives for
// weeks while an administrator toggles the feature.
func TestRetentionRoundSkipsWhenGateRejects(t *testing.T) {
	settings := ai_router_setting.DefaultSettings()
	settings.TelemetryEnabled = false

	calls := 0
	record := func(cutoff int64, limit int) (int, error) {
		calls++
		return 0, nil
	}
	attempts, logs, err := retentionCleanupRound(time.Now(), settings, record, record)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 0 || attempts != 0 || logs != 0 {
		t.Fatalf("telemetry off must not touch the database: calls=%d attempts=%d logs=%d", calls, attempts, logs)
	}
}

// Attempt rows are deleted before summary rows: a sweep interrupted midway then
// leaves summaries whose detail drawer degrades gracefully, never attempts
// pointing at a request that no longer exists.
func TestRetentionRoundDeletesAttemptsBeforeSummaries(t *testing.T) {
	settings := ai_router_setting.DefaultSettings()
	settings.RetentionDays = 3

	var order []string
	attempts := func(cutoff int64, limit int) (int, error) {
		order = append(order, "attempts")
		return 0, nil
	}
	logs := func(cutoff int64, limit int) (int, error) {
		order = append(order, "logs")
		return 0, nil
	}

	gotAttempts, gotLogs, err := retentionCleanupRound(time.Unix(1_700_000_000, 0), settings, attempts, logs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 2 || order[0] != "attempts" || order[1] != "logs" {
		t.Fatalf("deletion order = %v, want [attempts logs]", order)
	}
	if gotAttempts != 0 || gotLogs != 0 {
		t.Fatalf("attempts=%d logs=%d, want 0/0", gotAttempts, gotLogs)
	}
}

// If the attempt sweep fails, the summary sweep must not run: deleting summaries
// while their attempts stay behind is the one outcome worth avoiding.
func TestRetentionRoundStopsWhenAttemptSweepFails(t *testing.T) {
	settings := ai_router_setting.DefaultSettings()
	boom := errors.New("attempt delete failed")

	logsCalls := 0
	attempts := func(cutoff int64, limit int) (int, error) { return 0, boom }
	logs := func(cutoff int64, limit int) (int, error) {
		logsCalls++
		return 0, nil
	}

	_, _, err := retentionCleanupRound(time.Now(), settings, attempts, logs)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if logsCalls != 0 {
		t.Fatalf("summary deletes ran %d times after the attempt sweep failed, want 0", logsCalls)
	}
}

// A panic anywhere in a round must be swallowed at the boundary, not unwind into
// the gopool goroutine that hosts the task.
func TestRetentionRoundPanicDoesNotPropagate(t *testing.T) {
	settings := ai_router_setting.DefaultSettings()
	panicking := func(cutoff int64, limit int) (int, error) { panic("database exploded") }

	// The same defer-then-call shape runRetentionCleanupOnce uses.
	run := func() {
		defer recoverRetentionPanic()
		retentionCleanupRound(time.Now(), settings, panicking, panicking)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic escaped the retention boundary: %v", r)
		}
	}()
	run()
}
