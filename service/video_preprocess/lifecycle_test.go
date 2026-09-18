package video_preprocess

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The three adaptation cases named in the design document: turning the feature
// off, changing the job timeout, and the disabled-total-switch semantics.

// Disabling the feature must stop new preprocessing work without terminating a
// task already running. The queue has no enabled flag of its own — the caller
// stops Acquire and lets admitted leases finish — and this pins that contract:
// shutdown-style refusal plus running tasks that complete normally.
func TestDisableStopsNewWorkButLetsRunningFinish(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1

	// max_waiting = 0 is how the integration expresses "accept nothing new".
	disabled := limits
	disabled.MaxWaitingJobs = 0
	disabled.MaxWaitingPerUser = 0
	q := newTestQueue(t, disabled)

	running := acquireOK(t, context.Background(), q, testRequest("inflight", 1, 10))

	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- q.Execute(context.Background(), running, func(ctx context.Context, stage JobStage) error {
			if stage == JobStageAnalyzing {
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
	}()

	_, err := q.Acquire(context.Background(), testRequest("new", 2, 10), nil)
	requireErrorCode(t, err, CodeQueueFull)

	close(release)
	require.NoError(t, <-finished)
	assert.Equal(t, JobStatusSucceeded, running.Status())
	assert.Equal(t, 0, q.Snapshot().RunningJobs)
}

// Changing the job timeout applies to tasks admitted afterwards; the running
// task keeps the contract it was admitted with and is never cut short.
func TestJobTimeoutChangeDoesNotShortenRunningTask(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.JobTimeout = time.Minute
	q := newTestQueue(t, limits)

	running := acquireOK(t, context.Background(), q, testRequest("inflight", 1, 10))
	require.Equal(t, time.Minute, running.JobTimeout())

	shorter := limits
	shorter.JobTimeout = time.Nanosecond
	require.NoError(t, q.SetLimits(shorter))

	// The running task still finishes under its original budget.
	require.NoError(t, q.Execute(context.Background(), running, noopStage))
	assert.Equal(t, JobStatusSucceeded, running.Status())

	// A task admitted after the change is held to the new budget.
	next := acquireOK(t, context.Background(), q, testRequest("next", 1, 10))
	assert.Equal(t, time.Nanosecond, next.JobTimeout())
}

// WaitEstimate is exported for admission hints, so it must agree with what
// Acquire actually does: a request rejected by the estimated-wait limit reports
// the wait WaitEstimate exposed for it.
func TestWaitEstimateMatchesAdmissionHorizon(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1
	limits.JobTimeout = 30 * time.Second
	limits.MaxEstimatedWait = 45 * time.Second
	q := newTestQueue(t, limits)

	running := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))

	// One wave of wait sits under the horizon, so this request parks.
	assert.Equal(t, 30*time.Second, q.WaitEstimate(2))
	parked := parkAsync(t, context.Background(), q, 2, "r2", 10)

	// Two waves (60s) sit over the horizon, so this one is rejected instead of
	// parked, and the hint reports the wait it would have faced.
	assert.Equal(t, 60*time.Second, q.WaitEstimate(3))
	_, err := q.Acquire(context.Background(), testRequest("r3", 3, 10), nil)
	admission := requireErrorCode(t, err, CodeEstimatedWait)
	assert.Equal(t, 429, admission.HTTPStatus)
	assert.Equal(t, int64(60), admission.RetryAfterSecs)
	assert.Equal(t, 1, q.Snapshot().WaitingJobs)

	// The horizon is what rejected it: raising it lets the same request park.
	relaxed := limits
	relaxed.MaxEstimatedWait = 2 * time.Minute
	require.NoError(t, q.SetLimits(relaxed))
	second := parkAsync(t, context.Background(), q, 3, "r3", 10)

	q.Release(running, nil)
	q.Release(admittedLease(t, q, <-parked), nil)
	q.Release(admittedLease(t, q, <-second), nil)
	assert.Equal(t, 0, q.Snapshot().RunningJobs)
}
