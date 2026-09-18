package video_preprocess

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLimits() AdmissionLimits {
	return AdmissionLimits{
		MaxRunningJobs:      2,
		MaxRunningPerUser:   2,
		MaxWaitingJobs:      8,
		MaxWaitingPerUser:   8,
		MaxWaitingBytes:     1 << 20,
		MaxEstimatedWait:    time.Hour,
		QueueTimeout:        time.Minute,
		JobTimeout:          time.Minute,
		TempStorageCapacity: 1 << 30,
	}
}

func newTestQueue(t *testing.T, limits AdmissionLimits) *Queue {
	t.Helper()
	q, err := NewQueue(limits)
	require.NoError(t, err)
	return q
}

func testRequest(requestID string, userID int, bytes int64) AcquireRequest {
	return AcquireRequest{RequestID: requestID, UserID: userID, Model: "kimi-k3", BodyBytes: bytes}
}

// noopStage advances every stage without work.
func noopStage(context.Context, JobStage) error { return nil }

func acquireOK(t *testing.T, ctx context.Context, q *Queue, req AcquireRequest) *Lease {
	t.Helper()
	lease, err := q.Acquire(ctx, req, nil)
	require.NoError(t, err)
	require.NotNil(t, lease)
	return lease
}

type acquireResult struct {
	lease *Lease
	err   error
}

// acquireAsync starts an Acquire on its own goroutine so a test can drive the
// queue state while the request is parked. It is never left dangling: every
// caller drains the returned channel.
func acquireAsync(ctx context.Context, q *Queue, req AcquireRequest) chan acquireResult {
	results := make(chan acquireResult, 1)
	go func() {
		lease, err := q.Acquire(ctx, req, nil)
		results <- acquireResult{lease: lease, err: err}
	}()
	return results
}

// parkAsync starts an Acquire and returns only once the request is parked, so a
// test can build a deterministic FIFO order instead of racing goroutine launch.
func parkAsync(t *testing.T, ctx context.Context, q *Queue, userID int, requestID string, bytes int64) chan acquireResult {
	t.Helper()
	before := q.Snapshot().WaitingJobs
	results := acquireAsync(ctx, q, AcquireRequest{RequestID: requestID, UserID: userID, BodyBytes: bytes})
	waitForState(t, func() bool { return q.Snapshot().WaitingJobs == before+1 }, requestID+" must park")
	return results
}

// waitForState polls until the predicate holds. Every call is bounded, so it can
// never hang the suite.
func waitForState(t *testing.T, condition func() bool, message string) {
	t.Helper()
	require.Eventually(t, condition, 3*time.Second, time.Millisecond, message)
}

func requireErrorCode(t *testing.T, err error, wantCode string) *AdmissionError {
	t.Helper()
	require.Error(t, err)
	var admission *AdmissionError
	require.ErrorAs(t, err, &admission)
	assert.Equal(t, wantCode, admission.Code)
	return admission
}

func TestLimitsValidate(t *testing.T) {
	valid := testLimits()
	require.NoError(t, valid.Validate())

	tests := []struct {
		name    string
		mutate  func(*AdmissionLimits)
		wantErr bool
	}{
		{"max_running_jobs zero", func(l *AdmissionLimits) { l.MaxRunningJobs = 0 }, true},
		{"max_running_jobs above maximum", func(l *AdmissionLimits) { l.MaxRunningJobs = 257 }, true},
		{"max_running_per_user zero", func(l *AdmissionLimits) { l.MaxRunningPerUser = 0 }, true},
		{"max_waiting_jobs negative", func(l *AdmissionLimits) { l.MaxWaitingJobs = -1 }, true},
		{"max_waiting_jobs above maximum", func(l *AdmissionLimits) { l.MaxWaitingJobs = 10001 }, true},
		{"max_waiting_per_user negative", func(l *AdmissionLimits) { l.MaxWaitingPerUser = -1 }, true},
		{"max_waiting_bytes negative", func(l *AdmissionLimits) { l.MaxWaitingBytes = -1 }, true},
		{"max_estimated_wait zero", func(l *AdmissionLimits) { l.MaxEstimatedWait = 0 }, true},
		{"max_wait_seconds zero", func(l *AdmissionLimits) { l.QueueTimeout = 0 }, true},
		{"max_job_seconds zero", func(l *AdmissionLimits) { l.JobTimeout = 0 }, true},
		{"temp storage negative", func(l *AdmissionLimits) { l.TempStorageCapacity = -1 }, true},
		{"zero waiting is the closed queue", func(l *AdmissionLimits) { l.MaxWaitingJobs = 0; l.MaxWaitingPerUser = 0 }, false},
		{"zero temp storage means unreported", func(l *AdmissionLimits) { l.TempStorageCapacity = 0 }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			limits := valid
			tc.mutate(&limits)
			err := limits.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestNewQueueRejectsInvalidLimits(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 0
	q, err := NewQueue(limits)
	require.Error(t, err)
	require.Nil(t, q)
}

func TestSetLimitsRejectsInvalidValues(t *testing.T) {
	q := newTestQueue(t, testLimits())
	require.NoError(t, q.Execute(context.Background(), acquireOK(t, context.Background(), q, testRequest("r1", 1, 10)), noopStage))

	invalid := testLimits()
	invalid.JobTimeout = 0
	require.Error(t, q.SetLimits(invalid))
	assert.Equal(t, time.Minute, q.limitsSnapshot().JobTimeout)

	next := testLimits()
	next.MaxRunningJobs = 5
	require.NoError(t, q.SetLimits(next))
	assert.Equal(t, 5, q.limitsSnapshot().MaxRunningJobs)
}

// A free slot is handed out immediately and the slot comes back after the
// pipeline finishes.
func TestAdmissionAndRelease(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	q := newTestQueue(t, limits)

	lease := acquireOK(t, context.Background(), q, testRequest("r1", 7, 1024))
	assert.Equal(t, JobStatusAdmitted, lease.Status())
	assert.Equal(t, 7, lease.Request().UserID)

	snapshot := q.Snapshot()
	assert.Equal(t, 1, snapshot.RunningJobs)
	assert.Equal(t, 0, snapshot.WaitingJobs)
	assert.Equal(t, 1, q.RunningForUser(7))

	require.NoError(t, q.Execute(context.Background(), lease, noopStage))
	assert.Equal(t, JobStatusSucceeded, lease.Status())
	assert.Equal(t, 0, q.Snapshot().RunningJobs)
	assert.Equal(t, 0, q.RunningForUser(7))

	finished := lease.Snapshot()
	assert.Equal(t, JobStatusSucceeded, finished.Status)
	assert.Equal(t, JobStageHandoff, finished.Stage)
	assert.Len(t, finished.Stages, len(pipelineStages))
}

// Waiting requests are admitted in arrival order once a slot frees up, and the
// caller keeps its own body handle instead of the queue copying it.
func TestWaitingRequestsAreAdmittedInArrivalOrder(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1
	q := newTestQueue(t, limits)

	handleA := &struct{ payload []byte }{payload: make([]byte, 4096)}
	leaseA, err := q.Acquire(context.Background(), testRequest("r1", 1, 4096), handleA)
	require.NoError(t, err)
	require.Same(t, handleA, leaseA.Handle())

	second := parkAsync(t, context.Background(), q, 2, "r2", 2048)
	third := parkAsync(t, context.Background(), q, 3, "r3", 2048)

	snapshot := q.Snapshot()
	assert.Equal(t, 1, snapshot.RunningJobs)
	assert.Equal(t, 2, snapshot.WaitingJobs)
	assert.Equal(t, int64(4096), snapshot.WaitingBytes)
	assert.Greater(t, snapshot.OldestWaitMillis, int64(-1))

	q.Release(leaseA, nil)

	result := <-second
	require.NoError(t, result.err)
	assert.Equal(t, "r2", result.lease.Request().RequestID)
	assert.Equal(t, JobStatusAdmitted, result.lease.Status())

	select {
	case result := <-third:
		t.Fatalf("third request admitted before the second finished: %+v", result)
	default:
	}
	assert.Equal(t, 1, q.Snapshot().WaitingJobs)

	q.Release(result.lease, nil)
	thirdResult := <-third
	require.NoError(t, thirdResult.err)
	assert.Equal(t, "r3", thirdResult.lease.Request().RequestID)
	assert.Equal(t, 0, q.Snapshot().WaitingJobs)
}

// The runtime snapshot stays empty of any request payload or caller handle.
func TestSnapshotCarriesNoRequestPayload(t *testing.T) {
	q := newTestQueue(t, testLimits())
	lease := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))
	snapshot := lease.Snapshot()
	assert.Equal(t, "r1", snapshot.RequestID)
	assert.Equal(t, int64(10), snapshot.BodyBytes)
	assert.Empty(t, snapshot.Stages)
	assert.Empty(t, snapshot.ErrorCode)
}

func TestGlobalWaitingJobLimit(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1
	limits.MaxWaitingJobs = 1
	limits.MaxWaitingPerUser = 1
	q := newTestQueue(t, limits)

	running := acquireOK(t, context.Background(), q, testRequest("r1", 1, 1024))
	parked := parkAsync(t, context.Background(), q, 2, "r2", 1024)

	_, err := q.Acquire(context.Background(), testRequest("r3", 3, 1024), nil)
	admission := requireErrorCode(t, err, CodeQueueFull)
	assert.Equal(t, 429, admission.HTTPStatus)
	assert.Positive(t, admission.RetryAfterSecs)

	q.Release(running, nil)
	require.NoError(t, (<-parked).err)
}

func TestWaitingByteLimit(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1
	limits.MaxWaitingJobs = 8
	limits.MaxWaitingPerUser = 8
	limits.MaxWaitingBytes = 3000
	q := newTestQueue(t, limits)

	running := acquireOK(t, context.Background(), q, testRequest("r1", 1, 100))
	parked := parkAsync(t, context.Background(), q, 2, "r2", 2000)

	_, err := q.Acquire(context.Background(), testRequest("r3", 3, 2000), nil)
	requireErrorCode(t, err, CodeQueueBytes)
	assert.Equal(t, 1, q.Snapshot().WaitingJobs)

	// A request that still fits the byte budget is accepted.
	fitting := parkAsync(t, context.Background(), q, 4, "r4", 900)
	assert.Equal(t, int64(2900), q.Snapshot().WaitingBytes)

	// Only one slot opens per release, so drain one waiter at a time.
	q.Release(running, nil)
	q.Release(admittedLease(t, q, <-parked), nil)
	q.Release(admittedLease(t, q, <-fitting), nil)
	assert.Equal(t, 0, q.Snapshot().RunningJobs)
}

// admittedLease returns the lease of an admission result, requiring that the
// request was admitted rather than rejected.
func admittedLease(t *testing.T, q *Queue, result acquireResult) *Lease {
	t.Helper()
	require.NoError(t, result.err)
	require.NotNil(t, result.lease)
	require.Equal(t, JobStatusAdmitted, result.lease.Status())
	return result.lease
}

// A user at max_running_per_user must wait even while global slots are free,
// and the free slot must go to another user rather than idling.
func TestPerUserRunningLimitDoesNotBlockOtherUsers(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 2
	limits.MaxRunningPerUser = 1
	q := newTestQueue(t, limits)

	first := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))

	second := parkAsync(t, context.Background(), q, 1, "r2", 10)

	other := acquireOK(t, context.Background(), q, testRequest("r3", 2, 10))
	assert.Equal(t, JobStatusAdmitted, other.Status())
	assert.Equal(t, 2, q.Snapshot().RunningJobs)
	assert.Equal(t, 1, q.RunningForUser(2))

	select {
	case result := <-second:
		t.Fatalf("user 1 exceeded its per-user running share: %+v", result)
	default:
	}

	q.Release(first, nil)
	admitted := <-second
	require.NoError(t, admitted.err)
	assert.Equal(t, "r2", admitted.lease.Request().RequestID)

	q.Release(other, nil)
	q.Release(admitted.lease, nil)
	assert.Equal(t, 0, q.Snapshot().RunningJobs)
}

// A queue head whose owner is at max_running_per_user must be skipped rather
// than head-of-line block a different user behind it.
func TestFairnessSkipsThrottledOwnerInsteadOfBlockingQueue(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 2
	limits.MaxRunningPerUser = 1
	q := newTestQueue(t, limits)

	// User 1 holds one slot and user 3 holds the other, so the queue is full and
	// user 1's next request is throttled by its own share, not by capacity.
	holderOne := acquireOK(t, context.Background(), q, testRequest("holder", 1, 10))
	holderThree := acquireOK(t, context.Background(), q, testRequest("holder", 3, 10))

	throttled := parkAsync(t, context.Background(), q, 1, "throttled", 10)
	victim := parkAsync(t, context.Background(), q, 2, "victim", 10)
	assert.Equal(t, 2, q.Snapshot().WaitingJobs)

	// User 3's release frees the only global slot while the queue head's owner is
	// still at its share: the victim must get it.
	q.Release(holderThree, nil)
	admittedVictim := admittedLease(t, q, <-victim)
	assert.Equal(t, 2, admittedVictim.Request().UserID)

	select {
	case result := <-throttled:
		t.Fatalf("throttled request bypassed its per-user share: %+v", result)
	default:
	}

	// Once user 1's own slot frees up, its parked request proceeds — still in
	// arrival order relative to nothing behind it.
	q.Release(holderOne, nil)
	admittedThrottled := admittedLease(t, q, <-throttled)
	assert.Equal(t, 1, admittedThrottled.Request().UserID)

	q.Release(admittedVictim, nil)
	q.Release(admittedThrottled, nil)
	assert.Equal(t, 0, q.Snapshot().RunningJobs)
	assert.Equal(t, 0, q.Snapshot().WaitingJobs)
}

func TestPerUserWaitingLimit(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1
	limits.MaxWaitingJobs = 8
	limits.MaxWaitingPerUser = 1
	// The second waiter would face a two-wave projection, so the estimated-wait
	// limit must stay above it for the per-user limit to be what rejects.
	limits.MaxEstimatedWait = time.Hour
	q := newTestQueue(t, limits)

	running := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))
	parked := parkAsync(t, context.Background(), q, 1, "r2", 10)

	_, err := q.Acquire(context.Background(), testRequest("r3", 1, 10), nil)
	requireErrorCode(t, err, CodeUserLimit)

	// The limit is per user, so another user still gets in.
	other := parkAsync(t, context.Background(), q, 2, "r4", 10)

	q.Release(running, nil)
	q.Release(admittedLease(t, q, <-parked), nil)
	q.Release(admittedLease(t, q, <-other), nil)
	assert.Equal(t, 0, q.Snapshot().RunningJobs)
}

func TestZeroWaitingLimitsRejectInsteadOfQueueing(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1
	limits.MaxWaitingJobs = 0
	limits.MaxWaitingPerUser = 0
	q := newTestQueue(t, limits)

	running := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))
	_, err := q.Acquire(context.Background(), testRequest("r2", 2, 10), nil)
	requireErrorCode(t, err, CodeQueueFull)
	assert.Equal(t, 0, q.Snapshot().WaitingJobs)
	q.Release(running, nil)
}

func TestEstimatedWaitRejection(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1
	limits.MaxEstimatedWait = time.Millisecond
	q := newTestQueue(t, limits)

	running := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))

	_, err := q.Acquire(context.Background(), testRequest("r2", 2, 10), nil)
	admission := requireErrorCode(t, err, CodeEstimatedWait)
	assert.Equal(t, 429, admission.HTTPStatus)
	assert.Positive(t, admission.RetryAfterSecs)
	assert.Equal(t, 0, q.Snapshot().WaitingJobs)

	q.Release(running, nil)
}

func TestStorageCapacityAdmission(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1
	limits.MaxWaitingBytes = 1 << 20
	limits.TempStorageCapacity = 3000
	q := newTestQueue(t, limits)

	running := acquireOK(t, context.Background(), q, testRequest("r1", 1, 100))

	// The media layer already holds 1000 bytes of temp storage, so an 800 byte
	// body parks but a 3000 byte one cannot fit.
	q.SetTempStorageBytes(1000)
	parked := parkAsync(t, context.Background(), q, 2, "r2", 800)

	_, err := q.Acquire(context.Background(), testRequest("r3", 3, 3000), nil)
	admission := requireErrorCode(t, err, CodeStorageExhausted)
	assert.Equal(t, 503, admission.HTTPStatus)
	assert.Positive(t, admission.RetryAfterSecs)

	// Releasing temp storage reopens admission for a body that now fits.
	q.SetTempStorageBytes(100)
	accepted := parkAsync(t, context.Background(), q, 3, "r4", 2000)

	q.Release(running, nil)
	q.Release(admittedLease(t, q, <-parked), nil)
	q.Release(admittedLease(t, q, <-accepted), nil)
	assert.Equal(t, 0, q.Snapshot().RunningJobs)
}

// A caller that reports no temp storage budget opts out of disk admission
// instead of being rejected permanently.
func TestZeroTempStorageCapacitySkipsDiskAdmission(t *testing.T) {
	limits := testLimits()
	limits.TempStorageCapacity = 0
	q := newTestQueue(t, limits)

	lease := acquireOK(t, context.Background(), q, testRequest("r1", 1, 1<<40))
	require.NoError(t, q.Execute(context.Background(), lease, noopStage))
	assert.Zero(t, q.Snapshot().TempStorageLimit)
}

func TestQueueTimeoutReleasesWaiter(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1
	limits.QueueTimeout = 25 * time.Millisecond
	q := newTestQueue(t, limits)

	running := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))
	results := acquireAsync(context.Background(), q, testRequest("r2", 2, 10))

	select {
	case result := <-results:
		admission := requireErrorCode(t, result.err, CodeQueueTimeout)
		assert.Equal(t, 503, admission.HTTPStatus)
		assert.Positive(t, admission.RetryAfterSecs)
	case <-time.After(3 * time.Second):
		t.Fatal("parked request never timed out")
	}

	assert.Equal(t, 0, q.Snapshot().WaitingJobs)
	assert.Equal(t, int64(0), q.Snapshot().WaitingBytes)
	assert.Equal(t, 0, q.WaitingForUser(2))

	// The queue is fully usable afterwards.
	next := acquireAsync(context.Background(), q, testRequest("r3", 2, 10))
	q.Release(running, nil)
	require.NoError(t, (<-next).err)
}

func TestQueueTimeoutSnapshotIsPerRequest(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1
	limits.QueueTimeout = time.Minute
	q := newTestQueue(t, limits)

	leases := make(chan *Lease, 1)
	running := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))
	results := parkAsync(t, context.Background(), q, 2, "r2", 10)

	// Shrinking max_wait_seconds only applies to requests queued afterwards.
	shorter := testLimits()
	shorter.MaxRunningJobs = 1
	shorter.MaxRunningPerUser = 1
	shorter.QueueTimeout = time.Millisecond
	require.NoError(t, q.SetLimits(shorter))

	q.Release(running, nil)
	select {
	case result := <-results:
		require.NoError(t, result.err)
		leases <- result.lease
	case <-time.After(3 * time.Second):
		t.Fatal("already parked request was cut short by the new queue timeout")
	}
	q.Release(<-leases, nil)
}

func TestQueueCancellationReleasesWaiter(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1
	q := newTestQueue(t, limits)

	running := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))

	ctx, cancel := context.WithCancel(context.Background())
	results := parkAsync(t, ctx, q, 2, "r2", 10)
	cancel()

	select {
	case result := <-results:
		require.Nil(t, result.lease)
		_, code := StatusForError(result.err)
		assert.Equal(t, CodeProcessingCanceled, code)
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation never released the parked request")
	}

	assert.Equal(t, 0, q.Snapshot().WaitingJobs)
	assert.Equal(t, 0, q.WaitingForUser(2))
	q.Release(running, nil)
}

func TestAcquireWithCanceledContext(t *testing.T) {
	q := newTestQueue(t, testLimits())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	lease, err := q.Acquire(ctx, testRequest("r1", 1, 10), nil)
	require.Nil(t, lease)
	status, code := StatusForError(err)
	assert.Equal(t, JobStatusCanceled, status)
	assert.Equal(t, CodeProcessingCanceled, code)
	assert.Equal(t, 0, q.Snapshot().RunningJobs)
	assert.Equal(t, 0, q.Snapshot().WaitingJobs)
}

// A job that overruns its snapshotted budget is terminated, classified as a
// processing timeout and still releases its slot.
func TestJobTimeoutReleasesSlot(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.JobTimeout = 25 * time.Millisecond
	q := newTestQueue(t, limits)

	lease := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))
	require.Equal(t, 25*time.Millisecond, lease.JobTimeout())

	stalled := func(ctx context.Context, stage JobStage) error {
		if stage != JobStageProbing {
			return nil
		}
		<-ctx.Done()
		return ctx.Err()
	}

	err := q.Execute(context.Background(), lease, stalled)
	status, code := StatusForError(err)
	assert.Equal(t, JobStatusTimedOut, status)
	assert.Equal(t, CodeProcessingTimeout, code)
	assert.Equal(t, JobStatusTimedOut, lease.Status())
	assert.Equal(t, 0, q.Snapshot().RunningJobs)

	// The slot is reusable.
	next := acquireOK(t, context.Background(), q, testRequest("r2", 1, 10))
	require.NoError(t, q.Execute(context.Background(), next, noopStage))
	assert.Equal(t, 0, q.Snapshot().RunningJobs)
}

func TestJobTimeoutSnapshotIsPerLease(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.JobTimeout = time.Minute
	q := newTestQueue(t, limits)

	lease := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))

	longer := testLimits()
	longer.MaxRunningJobs = 1
	longer.JobTimeout = time.Hour
	require.NoError(t, q.SetLimits(longer))

	// The already-admitted lease keeps the timeout it was admitted with.
	assert.Equal(t, time.Minute, lease.JobTimeout())
	require.NoError(t, q.Execute(context.Background(), lease, noopStage))
}

func TestParentCancellationStopsRunningJob(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	q := newTestQueue(t, limits)

	lease := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	handler := func(runCtx context.Context, stage JobStage) error {
		if stage != JobStageDecoding {
			return nil
		}
		close(started)
		<-runCtx.Done()
		return nil
	}

	go func() {
		<-started
		cancel()
	}()

	err := q.Execute(ctx, lease, handler)
	status, code := StatusForError(err)
	assert.Equal(t, JobStatusCanceled, status)
	assert.Equal(t, CodeProcessingCanceled, code)
	assert.Equal(t, 0, q.Snapshot().RunningJobs)
}

// A panic inside a stage must not escape, must be classified, and must not
// leak the slot or the counters.
func TestStagePanicReleasesSlot(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	q := newTestQueue(t, limits)

	lease := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))
	panicking := func(_ context.Context, stage JobStage) error {
		if stage == JobStageAnalyzing {
			panic("decoder exploded")
		}
		return nil
	}

	var err error
	require.NotPanics(t, func() {
		err = q.Execute(context.Background(), lease, panicking)
	})
	status, code := StatusForError(err)
	assert.Equal(t, JobStatusFailed, status)
	assert.Equal(t, CodeProcessingFailed, code)
	assert.Contains(t, err.Error(), "decoder exploded")
	assert.Contains(t, err.Error(), string(JobStageAnalyzing))

	assert.Equal(t, JobStatusFailed, lease.Status())
	assert.Equal(t, 0, q.Snapshot().RunningJobs)
	assert.Equal(t, 0, q.RunningForUser(1))

	next := acquireOK(t, context.Background(), q, testRequest("r2", 1, 10))
	require.NoError(t, q.Execute(context.Background(), next, noopStage))
}

// A stage failure keeps the failed stage visible for the task record and stops
// the pipeline at that point.
func TestStageFailureStopsPipelineAndKeepsStage(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	q := newTestQueue(t, limits)

	lease := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))
	visited := []JobStage{}
	failing := func(_ context.Context, stage JobStage) error {
		visited = append(visited, stage)
		if stage == JobStageExporting {
			return context.DeadlineExceeded
		}
		return nil
	}

	err := q.Execute(context.Background(), lease, failing)
	// A stage may report a sub-operation deadline for an otherwise healthy job,
	// so the status stays a plain task failure.
	status, code := StatusForError(err)
	assert.Equal(t, JobStatusFailed, status)
	assert.Equal(t, CodeProcessingFailed, code)
	assert.Equal(t, []JobStage{JobStageDecoding, JobStageProbing, JobStageAnalyzing, JobStageSelecting, JobStageExporting}, visited)

	snapshot := lease.Snapshot()
	assert.Equal(t, JobStatusFailed, snapshot.Status)
	assert.Equal(t, CodeProcessingFailed, snapshot.ErrorCode)
	assert.Equal(t, JobStageExporting, snapshot.Stage)
	assert.Len(t, snapshot.Stages, 5)
	assert.GreaterOrEqual(t, snapshot.RunMS, int64(0))
	assert.GreaterOrEqual(t, snapshot.TotalMS, snapshot.RunMS)

	// A media error raised by the engine keeps its own stable code.
	lease2 := acquireOK(t, context.Background(), q, testRequest("r2", 1, 10))
	mediaError := &StageError{Code: "video_probe_failed", Stage: JobStageProbing, Message: "not a container"}
	err = q.Execute(context.Background(), lease2, func(context.Context, JobStage) error { return mediaError })
	status, code = StatusForError(err)
	assert.Equal(t, JobStatusFailed, status)
	assert.Equal(t, "video_probe_failed", code)
}

func TestExecuteRejectsReusedLease(t *testing.T) {
	q := newTestQueue(t, testLimits())
	lease := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))

	require.NoError(t, q.Execute(context.Background(), lease, noopStage))
	err := q.Execute(context.Background(), lease, noopStage)
	require.Error(t, err)
	_, code := StatusForError(err)
	assert.Equal(t, CodeInternal, code)
	assert.Equal(t, 0, q.Snapshot().RunningJobs)
}

func TestExecuteWithoutHandlerReleasesSlot(t *testing.T) {
	q := newTestQueue(t, testLimits())
	lease := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))

	err := q.Execute(context.Background(), lease, nil)
	require.Error(t, err)
	_, code := StatusForError(err)
	assert.Equal(t, CodeInternal, code)
	assert.Equal(t, 0, q.Snapshot().RunningJobs)
}

func TestReleaseIsIdempotent(t *testing.T) {
	q := newTestQueue(t, testLimits())
	lease := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))

	q.Release(lease, nil)
	q.Release(lease, &StageError{Code: CodeProcessingFailed, Message: "late"})
	q.Release(nil, nil)

	assert.Equal(t, 0, q.Snapshot().RunningJobs)
	assert.Equal(t, 0, q.RunningForUser(1))
	assert.Equal(t, CodeProcessingFailed, lease.ErrorCode())
}

// Growing max_running_jobs opens slots immediately and parks nothing.
func TestGrowingRunningLimitAdmitsWaiters(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1
	q := newTestQueue(t, limits)

	first := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))
	second := parkAsync(t, context.Background(), q, 2, "r2", 10)
	third := parkAsync(t, context.Background(), q, 3, "r3", 10)

	grown := testLimits()
	grown.MaxRunningJobs = 3
	grown.MaxRunningPerUser = 3
	require.NoError(t, q.SetLimits(grown))

	// Two free slots appear, so both parked requests proceed.
	require.NoError(t, (<-second).err)
	require.NoError(t, (<-third).err)
	assert.Equal(t, 3, q.Snapshot().RunningJobs)
	assert.Equal(t, 0, q.Snapshot().WaitingJobs)
	q.Release(first, nil)
}

// Shrinking max_running_jobs never terminates a running task; admission simply
// stays closed until the running count drops to the new cap.
func TestShrinkingRunningLimitDoesNotKillRunningJobs(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 2
	limits.MaxRunningPerUser = 2
	q := newTestQueue(t, limits)

	first := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))
	second := acquireOK(t, context.Background(), q, testRequest("r2", 2, 10))
	parked := parkAsync(t, context.Background(), q, 3, "r3", 10)

	shrunk := testLimits()
	shrunk.MaxRunningJobs = 1
	shrunk.MaxRunningPerUser = 1
	require.NoError(t, q.SetLimits(shrunk))

	// Both running tasks survive; the parked one stays parked.
	assert.Equal(t, 2, q.Snapshot().RunningJobs)
	select {
	case result := <-parked:
		t.Fatalf("admission ignored the shrunk running limit: %+v", result)
	default:
	}

	q.Release(first, nil)
	select {
	case result := <-parked:
		t.Fatalf("admission opened while running count was still above the new cap: %+v", result)
	default:
	}
	assert.Equal(t, 1, q.Snapshot().RunningJobs)

	q.Release(second, nil)
	admitted := <-parked
	require.NoError(t, admitted.err)
	assert.Equal(t, "r3", admitted.lease.Request().RequestID)
	q.Release(admitted.lease, nil)
}

// Lowering a waiting cap keeps everything already parked and only turns away
// new arrivals.
func TestShrinkingWaitingLimitKeepsParkedRequests(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1
	limits.MaxWaitingJobs = 4
	limits.MaxWaitingPerUser = 4
	q := newTestQueue(t, limits)

	running := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))
	first := parkAsync(t, context.Background(), q, 2, "r2", 10)
	second := parkAsync(t, context.Background(), q, 3, "r3", 10)

	shrunk := testLimits()
	shrunk.MaxRunningJobs = 1
	shrunk.MaxRunningPerUser = 1
	shrunk.MaxWaitingJobs = 1
	shrunk.MaxWaitingPerUser = 1
	require.NoError(t, q.SetLimits(shrunk))

	assert.Equal(t, 2, q.Snapshot().WaitingJobs)
	_, err := q.Acquire(context.Background(), testRequest("r4", 4, 10), nil)
	requireErrorCode(t, err, CodeQueueFull)

	// Both preserved requests still complete normally.
	q.Release(running, nil)
	firstResult := <-first
	require.NoError(t, firstResult.err)
	q.Release(firstResult.lease, nil)
	secondResult := <-second
	require.NoError(t, secondResult.err)
	q.Release(secondResult.lease, nil)
	assert.Equal(t, 0, q.Snapshot().RunningJobs)
	assert.Equal(t, 0, q.WaitingForUser(2))
	assert.Equal(t, 0, q.WaitingForUser(3))
}

func TestWaitEstimateGrowsWithBacklog(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1
	limits.JobTimeout = 10 * time.Second
	q := newTestQueue(t, limits)

	assert.Zero(t, q.WaitEstimate(9), "an idle queue projects no wait")

	running := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))
	// The only slot is taken, so the next request waits one job.
	assert.Equal(t, 10*time.Second, q.WaitEstimate(9))
	assert.Equal(t, 10*time.Second, q.WaitEstimate(1))

	parked := parkAsync(t, context.Background(), q, 2, "r2", 10)
	// Now a newcomer waits for the running task and then for the parked one.
	assert.Equal(t, 20*time.Second, q.WaitEstimate(9))
	assert.Equal(t, 20*time.Second, q.WaitEstimate(1))

	q.Release(running, nil)
	require.NoError(t, (<-parked).err)
	assert.Equal(t, 10*time.Second, q.WaitEstimate(9))
}

func TestSnapshotMergesExternalMediaCounters(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1
	q := newTestQueue(t, limits)

	lease := acquireOK(t, context.Background(), q, testRequest("r1", 1, 4096))
	parked := parkAsync(t, context.Background(), q, 2, "r2", 2048)

	q.SetActiveFFmpeg(5)
	q.SetTempStorageBytes(1 << 20)

	snapshot := q.Snapshot()
	assert.Equal(t, 1, snapshot.RunningJobs)
	assert.Equal(t, 1, snapshot.WaitingJobs)
	assert.Equal(t, int64(2048), snapshot.WaitingBytes)
	assert.Equal(t, 5, snapshot.ActiveFFmpeg)
	// Externally reported usage plus this queue's parked bodies.
	assert.Equal(t, int64(1<<20)+2048, snapshot.TempStorageBytes)
	assert.Equal(t, int64(1<<30), snapshot.TempStorageLimit)
	assert.Equal(t, time.Minute.Milliseconds(), snapshot.JobTimeoutMS)
	assert.Equal(t, time.Minute.Milliseconds(), snapshot.QueueTimeoutMS)
	assert.Greater(t, snapshot.OldestWaitMillis, int64(-1))

	q.SetActiveFFmpeg(-3)
	assert.Equal(t, 0, q.Snapshot().ActiveFFmpeg)

	q.Release(lease, nil)
	require.NoError(t, (<-parked).err)
}

func TestOldestWaitTracksQueueHead(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 1
	limits.MaxRunningPerUser = 1
	q := newTestQueue(t, limits)

	running := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))
	parked := parkAsync(t, context.Background(), q, 2, "r2", 10)

	headQueuedAt := q.queue.first().enqueued
	expected := time.Since(headQueuedAt).Milliseconds()
	assert.InDelta(t, expected, q.Snapshot().OldestWaitMillis, 1000)

	q.Release(running, nil)
	require.NoError(t, (<-parked).err)
	assert.Zero(t, q.Snapshot().OldestWaitMillis)
}

// The running counter must never exceed the limit and no slot may leak, even
// under a burst that mixes admit, wait, run and failure paths.
func TestConcurrentAdmissionNeverExceedsLimits(t *testing.T) {
	limits := testLimits()
	limits.MaxRunningJobs = 4
	limits.MaxRunningPerUser = 2
	q := newTestQueue(t, limits)

	const jobs = 40
	var peak atomic.Int64
	var completed atomic.Int64
	release := make(chan struct{})

	handler := func(ctx context.Context, stage JobStage) error {
		if stage != JobStageDecoding {
			return nil
		}
		current := int64(q.Snapshot().RunningJobs)
		for {
			observed := peak.Load()
			if current <= observed || peak.CompareAndSwap(observed, current) {
				break
			}
		}
		<-release
		return nil
	}

	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := q.Acquire(context.Background(), testRequest("burst", i%5, 128), nil)
			if err != nil {
				return
			}
			if err := q.Execute(context.Background(), lease, handler); err == nil {
				completed.Add(1)
			}
		}()
	}

	waitForState(t, func() bool { return q.Snapshot().RunningJobs == 4 }, "burst must fill every slot")
	waitForState(t, func() bool { return q.Snapshot().WaitingJobs > 0 }, "burst must park the overflow")
	close(release)
	wg.Wait()

	assert.LessOrEqual(t, peak.Load(), int64(4))
	// Every job that was admitted must also have completed; a leaked slot would
	// leave the queue permanently short and strand the tail of the burst.
	assert.Positive(t, completed.Load())
	assert.Equal(t, int64(4), peak.Load())
	final := q.Snapshot()
	assert.Equal(t, 0, final.RunningJobs)
	assert.Equal(t, 0, final.WaitingJobs)
	assert.Zero(t, final.WaitingBytes)
	for user := range 5 {
		assert.Zero(t, q.RunningForUser(user))
		assert.Zero(t, q.WaitingForUser(user))
	}
}

func TestStatusForErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantStat JobStatus
		wantCode string
	}{
		{"nil is success", nil, JobStatusSucceeded, ""},
		{"admission rejection", &AdmissionError{Code: CodeQueueFull}, JobStatusRejected, CodeQueueFull},
		{"context canceled", context.Canceled, JobStatusCanceled, CodeProcessingCanceled},
		{"context deadline", context.DeadlineExceeded, JobStatusTimedOut, CodeProcessingTimeout},
		{"queue-reported cancellation", &StageError{Status: JobStatusCanceled, Err: context.Canceled}, JobStatusCanceled, CodeProcessingCanceled},
		{"queue-reported deadline", &StageError{Status: JobStatusTimedOut, Err: context.DeadlineExceeded}, JobStatusTimedOut, CodeProcessingTimeout},
		// A wrapped context error without an asserted status is an ordinary
		// media failure: a stage may hit its own sub-operation deadline inside
		// a job that is otherwise healthy.
		{"unclassified wrapped cancellation", &StageError{Err: context.Canceled}, JobStatusFailed, CodeProcessingFailed},
		{"media code", &StageError{Code: "video_too_long", Stage: JobStageProbing}, JobStatusFailed, "video_too_long"},
		{"plain failure", assert.AnError, JobStatusFailed, CodeProcessingFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, code := StatusForError(tc.err)
			assert.Equal(t, tc.wantStat, status)
			assert.Equal(t, tc.wantCode, code)
		})
	}
}

func TestHTTPStatusForErrorMapping(t *testing.T) {
	assert.Equal(t, 429, HTTPStatusForError(&AdmissionError{Code: CodeQueueFull, HTTPStatus: 429}))
	assert.Equal(t, 503, HTTPStatusForError(&AdmissionError{Code: CodeStorageExhausted, HTTPStatus: 503}))
	assert.Equal(t, 429, HTTPStatusForError(&AdmissionError{Code: CodeUserLimit}))
	assert.Equal(t, 499, HTTPStatusForError(context.Canceled))
	assert.Equal(t, 408, HTTPStatusForError(context.DeadlineExceeded))
	assert.Equal(t, 500, HTTPStatusForError(assert.AnError))

	retryAfter, ok := RetryAfterForError(&AdmissionError{Code: CodeQueueFull, HTTPStatus: 429, RetryAfterSecs: 42})
	assert.True(t, ok)
	assert.Equal(t, int64(42), retryAfter)
	_, ok = RetryAfterForError(assert.AnError)
	assert.False(t, ok)
}

// A rejection must stay a rejection before the pipeline starts and become a task
// failure once a stage was entered.
func TestTerminalOutcomeDependsOnStageEntry(t *testing.T) {
	q := newTestQueue(t, testLimits())
	lease := acquireOK(t, context.Background(), q, testRequest("r1", 1, 10))

	status, code := terminalOutcome(lease, &AdmissionError{Code: CodeQueueFull})
	assert.Equal(t, JobStatusRejected, status)
	assert.Equal(t, CodeQueueFull, code)

	lease.enterStage(JobStageExporting)
	status, code = terminalOutcome(lease, &AdmissionError{Code: CodeQueueFull})
	assert.Equal(t, JobStatusFailed, status)
	assert.Equal(t, CodeQueueFull, code)
	q.Release(lease, nil)
}

func TestRetryAfterSecondsIsClamped(t *testing.T) {
	assert.Equal(t, int64(1), retryAfterSeconds(0, 1, 3600))
	assert.Equal(t, int64(1), retryAfterSeconds(time.Millisecond, 1, 3600))
	assert.Equal(t, int64(1), retryAfterSeconds(1500*time.Millisecond, 1, 3600))
	assert.Equal(t, int64(600), retryAfterSeconds(10*time.Minute, 1, 3600))
	assert.Equal(t, int64(3600), retryAfterSeconds(2*time.Hour, 1, 3600))

	assert.Equal(t, int64(1), retryAfterMillisToSeconds(0))
	assert.Equal(t, int64(1), retryAfterMillisToSeconds(999))
	assert.Equal(t, int64(2), retryAfterMillisToSeconds(2000))
	assert.Equal(t, int64(3600), retryAfterMillisToSeconds(10_000_000))
}

// Queue bookkeeping supports interleaved removal from the middle, which the
// timeout and cancellation paths rely on.
func TestWaitQueueRemovalFromMiddleAndFront(t *testing.T) {
	queue := &waitQueue{}
	items := make([]*waitingRequest, 4)
	for i := range items {
		items[i] = &waitingRequest{ready: make(chan struct{})}
		queue.push(items[i])
	}
	require.Equal(t, 4, queue.len())
	require.Same(t, items[0], queue.first())

	require.True(t, queue.remove(items[2]))
	require.Equal(t, 3, queue.len())
	require.False(t, queue.remove(items[2]))
	require.False(t, queue.remove(nil))
	require.Same(t, items[0], queue.first())

	require.True(t, queue.remove(items[0]))
	require.Same(t, items[1], queue.first())
	require.Equal(t, 2, queue.len())

	require.True(t, queue.remove(items[3]))
	require.True(t, queue.remove(items[1]))
	require.Equal(t, 0, queue.len())
	require.Nil(t, queue.first())
}
