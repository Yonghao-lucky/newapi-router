package video_preprocess

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// waitingRequest is one request parked in the queue. It holds the caller's
// metadata and an opaque body handle owned by the caller — never the body
// itself, so a deep queue costs only a few words per entry.
type waitingRequest struct {
	lease    *Lease
	bytes    int64
	enqueued time.Time
	ready    chan struct{}
	// pos is the index inside the ring buffer, -1 while not parked.
	pos int
}

// waitQueue is a FIFO ring buffer with O(1) head pop and O(1) arbitrary
// removal, which the timeout and cancellation paths need.
type waitQueue struct {
	items []*waitingRequest
	head  int
	count int
}

func (w *waitQueue) len() int { return w.count }

func (w *waitQueue) first() *waitingRequest {
	for w.head < len(w.items) {
		if item := w.items[w.head]; item != nil {
			return item
		}
		w.head++
	}
	return nil
}

func (w *waitQueue) push(req *waitingRequest) {
	req.pos = len(w.items)
	w.items = append(w.items, req)
	w.count++
}

// remove drops a parked request. It reports false when the request is no longer
// parked, so the caller never double-decrements the byte budget or finishes a
// request that admission already took.
func (w *waitQueue) remove(req *waitingRequest) bool {
	if req == nil || req.pos < 0 || req.pos < w.head || req.pos >= len(w.items) || w.items[req.pos] != req {
		return false
	}
	pos := req.pos
	w.items[pos] = nil
	req.pos = -1
	w.count--
	if pos == w.head {
		w.head++
	}
	w.compact()
	return true
}

// compact renumbers the parked requests after enough slots were dropped from
// the front, keeping the backing array from growing without bound.
func (w *waitQueue) compact() {
	if w.head == 0 || w.head*2 < len(w.items) {
		return
	}
	remaining := copy(w.items, w.items[w.head:])
	for i := remaining; i < len(w.items); i++ {
		w.items[i] = nil
	}
	w.items = w.items[:remaining]
	w.head = 0
	for i, item := range w.items {
		if item != nil {
			item.pos = i
		}
	}
}

// Queue is the single-instance bounded admission queue. Every admission
// decision goes through one mutex, so the running counters, the per-user maps
// and the parked requests are always consistent with each other. A request
// waits for its slot on the calling goroutine and then runs the whole pipeline
// through Execute.
//
// Multi-node aggregation is intentionally out of scope: each process owns its
// slots and counters.
type Queue struct {
	limits atomic.Pointer[AdmissionLimits]

	// tempStorageBytes and activeFFmpeg are real-time numbers reported by the
	// media layer; the queue merges them into its snapshots but never infers
	// them, so it can never report a media health it does not know.
	tempStorageBytes atomic.Int64
	activeFFmpeg     atomic.Int64

	mu            sync.Mutex
	running       int
	runningByUser map[int]int
	waitingByUser map[int]int
	waitingBytes  int64
	queue         *waitQueue
}

// NewQueue creates a queue from a limits snapshot. The copied values are
// published as the queue's own state; use SetLimits to change them, or
// SetTempStorageCapacity while running.
func NewQueue(limits AdmissionLimits) (*Queue, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	q := &Queue{
		runningByUser: make(map[int]int),
		waitingByUser: make(map[int]int),
		queue:         &waitQueue{},
	}
	snapshot := limits
	q.limits.Store(&snapshot)
	return q, nil
}

func (q *Queue) limitsSnapshot() AdmissionLimits {
	limits := q.limits.Load()
	if limits == nil {
		return DefaultAdmissionLimits()
	}
	return *limits
}

// SetLimits publishes new limits with asymmetric runtime semantics:
//
//   - growing MaxRunningJobs opens slots immediately — parked requests are
//     admitted up to the new cap;
//   - shrinking MaxRunningJobs never terminates a running task; admission stays
//     closed until the running count drops to the new cap;
//   - lowering a waiting cap (jobs, bytes or per user) never evicts a request
//     that is already parked, it only rejects new ones;
//   - QueueTimeout and JobTimeout are snapshotted per request, so a change
//     affects requests queued or admitted afterwards, never one in flight.
//
// Cross-field relations (for example MaxRunningPerUser <= MaxRunningJobs) are
// the configuration layer's job; unusable values still fail here rather than
// silently freezing every admission.
func (q *Queue) SetLimits(next AdmissionLimits) error {
	if err := next.Validate(); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	snapshot := next
	q.limits.Store(&snapshot)
	q.admitLocked()
	return nil
}

// SetActiveFFmpeg reports how many FFmpeg subprocesses the media layer has
// running. It is a read-only passthrough for runtime snapshots.
func (q *Queue) SetActiveFFmpeg(count int) {
	q.activeFFmpeg.Store(int64(max(count, 0)))
}

// SetTempStorageBytes reports the media layer's current temp storage usage. The
// queue adds its own parked body bytes on top when it snapshots the total.
func (q *Queue) SetTempStorageBytes(bytes int64) {
	q.tempStorageBytes.Store(max(bytes, 0))
}

// Acquire waits for a running slot and returns the lease its pipeline must run
// on. It never retains the body: handle is an opaque caller-owned pointer, and
// req carries metadata only.
//
// On every rejection and on a queue timeout or cancellation, Acquire releases
// whatever it reserved; the caller must not use a nil lease.
func (q *Queue) Acquire(ctx context.Context, req AcquireRequest, handle any) (*Lease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, contextFailure(err)
	}
	if req.BodyBytes < 0 {
		req.BodyBytes = 0
	}

	limits := q.limitsSnapshot()
	lease := &Lease{
		req:        req,
		handle:     handle,
		queuedAt:   time.Now(),
		jobTimeout: limits.JobTimeout,
		status:     JobStatusQueued,
	}

	waiting, err := q.enqueue(lease, limits)
	if err != nil {
		return nil, err
	}
	if waiting == nil {
		return lease, nil
	}

	timer := time.NewTimer(limits.QueueTimeout)
	defer timer.Stop()

	select {
	case <-waiting.ready:
		return waiting.lease, nil
	case <-ctx.Done():
		return q.abandon(waiting, contextFailure(ctx.Err()))
	case <-timer.C:
		return q.abandon(waiting, newAdmissionError(
			CodeQueueTimeout,
			"timed out waiting for a video preprocessing slot",
			503,
			retryAfterSeconds(limits.QueueTimeout, 1, 3600),
		))
	}
}

// Execute runs the pipeline under the lease, enforcing the snapshotted job
// timeout. The slot is released on every exit path — success, handler error,
// timeout, cancellation and a panic recovered at the stage boundary.
//
// A lease belongs to exactly one Execute; calling it twice is a programming
// error whose second call fails without touching the slot.
func (q *Queue) Execute(ctx context.Context, lease *Lease, handler StageHandler) (err error) {
	if lease == nil {
		return &StageError{Code: CodeInternal, Message: "nil lease"}
	}
	if handler == nil {
		q.Release(lease, &StageError{Code: CodeInternal, Message: "missing stage handler"})
		return &StageError{Code: CodeInternal, Message: "missing stage handler"}
	}
	if !lease.claimRun() {
		return &StageError{Code: CodeInternal, Message: "lease already used"}
	}

	q.markRunning(lease)

	runCtx := ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(runCtx, lease.JobTimeout())
	defer cancel()

	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("video_preprocess: stage panic for request %q: %v", lease.req.RequestID, recovered)
			err = &StageError{
				Code:    CodeProcessingFailed,
				Stage:   lease.Stage(),
				Err:     runCtx.Err(),
				Message: "stage panicked",
			}
		}
		q.Release(lease, err)
	}()

	for _, stage := range pipelineStages {
		if ctxErr := runCtx.Err(); ctxErr != nil {
			return contextFailure(ctxErr)
		}

		lease.enterStage(stage)
		started := time.Now()
		stageErr := runStage(runCtx, handler, stage)
		lease.recordStage(stage, time.Since(started))

		if stageErr == nil {
			continue
		}
		if ctxErr := runCtx.Err(); ctxErr != nil {
			return contextFailure(ctxErr)
		}
		var classified *StageError
		if errors.As(stageErr, &classified) {
			return stageErr
		}
		// Preserve the wrapped failure's stable code and message: a stage
		// boundary adds context, it must not re-classify a media or budget
		// rejection into a generic processing failure.
		code := CodeOf(stageErr)
		if code == "" {
			code = CodeProcessingFailed
		}
		message := MessageOf(stageErr)
		if message == "" {
			message = "stage failed"
		}
		return &StageError{Code: code, Stage: stage, Err: stageErr, Message: message}
	}

	lease.enterStage(JobStageHandoff)
	return nil
}

// Release ends the task, frees its slot and wakes a parked request. It is
// idempotent: only the first call frees the slot, while later calls may refresh
// the recorded outcome.
func (q *Queue) Release(lease *Lease, err error) {
	if lease == nil {
		return
	}
	status, code := terminalOutcome(lease, err)

	q.mu.Lock()
	defer q.mu.Unlock()
	if lease.released.CompareAndSwap(false, true) && lease.admitted.Load() {
		q.running--
		q.runningByUserAddLocked(lease.req.UserID, -1)
	}
	lease.complete(status, code, time.Now())
	q.admitLocked()
}

// Snapshot returns the queue's own runtime counters merged with the externally
// reported media counters. FFmpeg/ffprobe health is deliberately absent: the
// integration layer that owns probes knows it, and the queue must not invent it.
func (q *Queue) Snapshot() RuntimeSnapshot {
	q.mu.Lock()
	defer q.mu.Unlock()
	limits := q.limitsSnapshot()
	return RuntimeSnapshot{
		RunningJobs:      q.running,
		WaitingJobs:      q.queue.len(),
		WaitingBytes:     q.waitingBytes,
		TempStorageBytes: q.tempStorageBytes.Load() + q.waitingBytes,
		ActiveFFmpeg:     int(q.activeFFmpeg.Load()),
		OldestWaitMillis: q.oldestWaitMillisLocked(time.Now()),
		MaxRunningJobs:   limits.MaxRunningJobs,
		MaxWaitingJobs:   limits.MaxWaitingJobs,
		MaxWaitingBytes:  limits.MaxWaitingBytes,
		TempStorageLimit: limits.TempStorageCapacity,
		QueueTimeoutMS:   limits.QueueTimeout.Milliseconds(),
		JobTimeoutMS:     limits.JobTimeout.Milliseconds(),
	}
}

// WaitingForUser reports how many requests of userID are parked.
func (q *Queue) WaitingForUser(userID int) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.waitingByUser[userID]
}

// RunningForUser reports how many slots userID currently holds.
func (q *Queue) RunningForUser(userID int) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.runningByUser[userID]
}

// WaitEstimate projects the queue wait a new request from userID would face. It
// is zero when that request could start right now; otherwise it assumes every
// task takes the full snapshotted job timeout, so the projection is an upper
// bound rather than a measurement. Admission uses the same projection, so a hint
// and the decision that produced it never disagree.
func (q *Queue) WaitEstimate(userID int) time.Duration {
	q.mu.Lock()
	defer q.mu.Unlock()
	return time.Duration(q.waitEstimateMillisLocked(userID, q.limitsSnapshot())) * time.Millisecond
}

// enqueue reserves capacity and either admits the request immediately, in which
// case it returns a nil waiter, or parks it.
func (q *Queue) enqueue(lease *Lease, limits AdmissionLimits) (*waitingRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if err := q.checkStorageLocked(limits, lease.req.BodyBytes); err != nil {
		return nil, err
	}
	if q.canRunLocked(limits, lease.req.UserID) {
		q.admitLeaseLocked(lease)
		return nil, nil
	}
	if err := q.checkWaitingLocked(limits, lease.req); err != nil {
		return nil, err
	}

	if estimated := q.waitEstimateMillisLocked(lease.req.UserID, limits); estimated > limits.MaxEstimatedWait.Milliseconds() {
		return nil, newAdmissionError(
			CodeEstimatedWait,
			"projected queue wait exceeds the admission limit",
			429,
			retryAfterSeconds(time.Duration(estimated)*time.Millisecond, 1, 3600),
		)
	}

	waiting := &waitingRequest{
		lease:    lease,
		bytes:    lease.req.BodyBytes,
		enqueued: time.Now(),
		ready:    make(chan struct{}),
	}
	q.queue.push(waiting)
	q.waitingBytes += waiting.bytes
	q.waitingByUser[lease.req.UserID]++
	return waiting, nil
}

// abandon removes a parked request after a queue timeout or a cancellation. It
// returns a terminal error, or nil when admission had already won the race — in
// which case the lease is admitted, belongs to the caller, and must be released
// exactly once.
func (q *Queue) abandon(waiting *waitingRequest, cause error) (*Lease, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.queue.remove(waiting) {
		return waiting.lease, nil
	}
	q.unparkLocked(waiting)
	q.admitLocked()
	return nil, cause
}

// admitLocked opens as many slots as fairness allows. Every decision is checked
// against the current limits, so a shrink that arrived while a request waited is
// honored without evicting anything.
//
// Fairness has two rules: the queue is FIFO for requests that compete for the
// same slot, and max_running_per_user caps how many slots one user may hold. A
// user at their cap is skipped rather than blocking the queue, so a deep backlog
// from one user cannot stall everyone behind it — while still never letting that
// user exceed their share.
func (q *Queue) admitLocked() {
	limits := q.limitsSnapshot()
	for q.running < limits.MaxRunningJobs {
		waiting := q.nextAdmissibleLocked(limits)
		if waiting == nil {
			return
		}
		if !q.queue.remove(waiting) {
			continue
		}
		q.unparkLocked(waiting)
		q.admitLeaseLocked(waiting.lease)
		close(waiting.ready)
	}
}

// nextAdmissibleLocked returns the oldest parked request whose owner is below
// their running share, skipping throttled owners. The caller holds q.mu.
//
// The admitted request keeps its slot until Release, which is why a parked
// request needs no second storage check: it was admitted to the queue under the
// storage budget, and admitting it does not add a new body.
func (q *Queue) nextAdmissibleLocked(limits AdmissionLimits) *waitingRequest {
	for i := q.queue.head; i < len(q.queue.items); i++ {
		waiting := q.queue.items[i]
		if waiting == nil || waiting.pos != i {
			continue
		}
		if !q.canRunUserLocked(limits, waiting.lease.req.UserID) {
			continue
		}
		return waiting
	}
	return nil
}

// unparkLocked returns a request's waiting budget. It must run exactly once per
// parked request, which the waitQueue.remove contract guarantees.
func (q *Queue) unparkLocked(waiting *waitingRequest) {
	q.waitingBytes -= waiting.bytes
	q.waitingByUserAddLocked(waiting.lease.req.UserID, -1)
}

// admitLeaseLocked consumes one running slot for the lease and publishes the
// admitted state. The caller holds q.mu.
func (q *Queue) admitLeaseLocked(lease *Lease) {
	q.running++
	q.runningByUserAddLocked(lease.req.UserID, 1)
	lease.admitted.Store(true)
	lease.mu.Lock()
	lease.status = JobStatusAdmitted
	lease.admittedAt = time.Now()
	lease.mu.Unlock()
}

func (q *Queue) markRunning(lease *Lease) {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	lease.status = JobStatusRunning
	lease.startedAt = time.Now()
}

func (q *Queue) canRunLocked(limits AdmissionLimits, userID int) bool {
	if q.running >= limits.MaxRunningJobs {
		return false
	}
	return q.canRunUserLocked(limits, userID)
}

func (q *Queue) canRunUserLocked(limits AdmissionLimits, userID int) bool {
	if limits.MaxRunningPerUser <= 0 {
		return false
	}
	return q.runningByUserLocked(userID) < limits.MaxRunningPerUser
}

func (q *Queue) checkWaitingLocked(limits AdmissionLimits, req AcquireRequest) error {
	// A rejection answers "when should I come back?", so the hint is the wait
	// this request itself would have faced at the tail of the queue.
	retryAfter := q.waitEstimateMillisLocked(req.UserID, limits)
	if q.queue.len() >= limits.MaxWaitingJobs {
		return q.rejectLocked(CodeQueueFull, "video preprocessing queue is full", 429, retryAfter)
	}
	if limits.MaxWaitingBytes > 0 && q.waitingBytes+req.BodyBytes > limits.MaxWaitingBytes {
		return q.rejectLocked(CodeQueueBytes, "video preprocessing queue byte budget is exhausted", 429, retryAfter)
	}
	if q.waitingByUserLocked(req.UserID) >= limits.MaxWaitingPerUser {
		return q.rejectLocked(CodeUserLimit, "user reached the video preprocessing queue limit", 429, retryAfter)
	}
	return nil
}

// checkStorageLocked rejects a request when the caller-reported temp storage
// budget cannot hold the parked bodies plus the incoming one. A zero capacity
// means the caller reports no budget, so the check is skipped.
func (q *Queue) checkStorageLocked(limits AdmissionLimits, pendingBytes int64) error {
	if limits.TempStorageCapacity <= 0 {
		return nil
	}
	used := q.tempStorageBytes.Load() + q.waitingBytes + pendingBytes
	if used <= limits.TempStorageCapacity {
		return nil
	}
	return q.rejectLocked(
		CodeStorageExhausted,
		"video preprocessing temp storage budget is exhausted",
		503,
		limits.JobTimeout.Milliseconds(),
	)
}

func (q *Queue) rejectLocked(code, message string, status int, retryAfterMillis int64) error {
	return newAdmissionError(code, message, status, retryAfterMillisToSeconds(retryAfterMillis))
}

// waitEstimateMillisLocked projects the wait a new request from userID faces. A
// request that can start immediately waits for nothing; otherwise everything
// running and parked is ahead of it, and the admission limits keep that set
// bounded.
func (q *Queue) waitEstimateMillisLocked(userID int, limits AdmissionLimits) int64 {
	if q.canRunLocked(limits, userID) {
		return 0
	}
	return projectedWaitMillis(q.running+q.queue.len(), q.running, limits)
}

// projectedWaitMillis estimates the wait of a request that cannot start yet,
// under the assumption that every task consumes the full snapshotted job
// timeout. Running tasks form the wave already in flight and the parked ones the
// waves behind it, so the projection grows with the backlog depth. Admission and
// the Retry-After hint both come from this model, so a rejected client is told
// the wait it actually would have faced.
func projectedWaitMillis(backlog, runningJobs int, limits AdmissionLimits) int64 {
	if backlog <= 0 {
		return 0
	}
	perJob := limits.JobTimeout.Milliseconds()
	if perJob <= 0 {
		return 0
	}
	capacity := min(max(runningJobs, 1), limits.MaxRunningJobs)
	waves := int64(1)
	if parked := max(backlog-runningJobs, 0); parked > 0 && capacity > 0 {
		waves += int64((parked + capacity - 1) / capacity)
	}
	return waves * perJob
}

func (q *Queue) oldestWaitMillisLocked(now time.Time) int64 {
	head := q.queue.first()
	if head == nil {
		return 0
	}
	return max(now.Sub(head.enqueued).Milliseconds(), 0)
}

func (q *Queue) runningByUserLocked(userID int) int {
	return q.runningByUser[userID]
}

func (q *Queue) waitingByUserLocked(userID int) int {
	return q.waitingByUser[userID]
}

// runningByUserAddLocked adjusts a user's running share and drops the entry once
// it reaches zero, so the map tracks only users that hold slots.
func (q *Queue) runningByUserAddLocked(userID, delta int) {
	next := q.runningByUser[userID] + delta
	if next <= 0 {
		delete(q.runningByUser, userID)
		return
	}
	q.runningByUser[userID] = next
}

// waitingByUserAddLocked adjusts a user's waiting share and drops the entry once
// it reaches zero, so the map tracks only users that have requests parked.
func (q *Queue) waitingByUserAddLocked(userID, delta int) {
	next := q.waitingByUser[userID] + delta
	if next <= 0 {
		delete(q.waitingByUser, userID)
		return
	}
	q.waitingByUser[userID] = next
}

func retryAfterMillisToSeconds(millis int64) int64 {
	if millis <= 0 {
		return 1
	}
	if millis < 1000 {
		return 1
	}
	return min(millis/1000, 3600)
}

func retryAfterSeconds(d time.Duration, minSeconds, maxSeconds int64) int64 {
	return max(min(int64(d/time.Second), maxSeconds), minSeconds)
}
