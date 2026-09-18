// Package video_preprocess implements the request-side pipeline that turns a
// client video input into timestamped images before the AI Router picks a
// channel.
//
// This file owns the admission budget, the job lifecycle vocabulary and the
// task snapshot. The queue itself lives in admission.go and never reads,
// copies or retains request payloads: integrations keep their own body handle
// and pass only metadata here.
package video_preprocess

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// JobStatus is the lifecycle state of one video preprocessing task.
type JobStatus string

const (
	// JobStatusQueued is the state before admission. The queue tracks waiting
	// tasks as counters rather than per-task records, so integrations label
	// their own task records with this value.
	JobStatusQueued    JobStatus = "queued"
	JobStatusAdmitted  JobStatus = "admitted"
	JobStatusRunning   JobStatus = "running"
	JobStatusSucceeded JobStatus = "succeeded"
	JobStatusFailed    JobStatus = "failed"
	JobStatusTimedOut  JobStatus = "timed_out"
	JobStatusCanceled  JobStatus = "canceled"
	JobStatusRejected  JobStatus = "rejected"
)

// IsTerminal reports whether no further state transition may follow.
func (s JobStatus) IsTerminal() bool {
	switch s {
	case JobStatusSucceeded, JobStatusFailed, JobStatusTimedOut, JobStatusCanceled, JobStatusRejected:
		return true
	default:
		return false
	}
}

// JobStage is one heavy step of the preprocessing pipeline.
type JobStage string

const (
	JobStageDecoding  JobStage = "decoding"
	JobStageProbing   JobStage = "probing"
	JobStageAnalyzing JobStage = "analyzing"
	JobStageSelecting JobStage = "selecting"
	JobStageExporting JobStage = "exporting"
	JobStageRewriting JobStage = "rewriting"
	JobStageHandoff   JobStage = "handoff"
)

var pipelineStages = []JobStage{
	JobStageDecoding,
	JobStageProbing,
	JobStageAnalyzing,
	JobStageSelecting,
	JobStageExporting,
	JobStageRewriting,
	JobStageHandoff,
}

// JobPipelineStages returns the stages in execution order.
func JobPipelineStages() []JobStage {
	return slices.Clone(pipelineStages)
}

// StageTiming is the measured duration of one entered pipeline stage.
type StageTiming struct {
	Stage JobStage `json:"stage"`
	MS    int64    `json:"ms"`
}

const (
	minRunningJobsLimit    = 1
	maxRunningJobsLimit    = 256
	minRunningPerUserLimit = 1
	maxRunningPerUserLimit = 256
	maxWaitingJobsLimit    = 10000
	maxWaitingPerUserLimit = 10000
)

// Limits is the admission budget a queue runs with. Persistence and cross-field
// relations (for example max_running_per_user <= max_running_jobs) belong to the
// configuration layer; this type only guarantees that every value it accepts is
// usable by the queue.
type AdmissionLimits struct {
	MaxRunningJobs    int
	MaxRunningPerUser int
	// MaxWaitingJobs is the number of requests that may wait for a slot.
	// Zero closes the queue: a request that cannot be admitted immediately is
	// rejected instead of queued.
	MaxWaitingJobs int
	// MaxWaitingPerUser is the number of waiting requests one user may hold.
	// Zero forbids waiting for that user.
	MaxWaitingPerUser int
	// MaxWaitingBytes caps the total BodyBytes of waiting requests.
	MaxWaitingBytes int64
	// MaxEstimatedWait rejects a request whose projected queue wait exceeds it.
	MaxEstimatedWait time.Duration
	// QueueTimeout bounds how long a request may wait for a slot. It is
	// snapshotted when the request enters the queue.
	QueueTimeout time.Duration
	// JobTimeout bounds one admitted task's processing time. It is snapshotted
	// when the request is admitted.
	JobTimeout time.Duration
	// TempStorageCapacity is the caller-reported temp storage budget in bytes.
	// Zero means the caller reports no capacity, so disk admission is skipped.
	TempStorageCapacity int64
}

// DefaultLimits returns the commissioning defaults of the design document.
func DefaultAdmissionLimits() AdmissionLimits {
	return AdmissionLimits{
		MaxRunningJobs:      16,
		MaxRunningPerUser:   2,
		MaxWaitingJobs:      500,
		MaxWaitingPerUser:   20,
		MaxWaitingBytes:     61440 << 20,
		MaxEstimatedWait:    600 * time.Second,
		QueueTimeout:        600 * time.Second,
		JobTimeout:          180 * time.Second,
		TempStorageCapacity: 100 << 30,
	}
}

// Validate rejects limits the queue cannot operate with.
func (l AdmissionLimits) Validate() error {
	if l.MaxRunningJobs < minRunningJobsLimit || l.MaxRunningJobs > maxRunningJobsLimit {
		return fmt.Errorf("max_running_jobs must be between %d and %d", minRunningJobsLimit, maxRunningJobsLimit)
	}
	if l.MaxRunningPerUser < minRunningPerUserLimit || l.MaxRunningPerUser > maxRunningPerUserLimit {
		return fmt.Errorf("max_running_per_user must be between %d and %d", minRunningPerUserLimit, maxRunningPerUserLimit)
	}
	if l.MaxWaitingJobs < 0 || l.MaxWaitingJobs > maxWaitingJobsLimit {
		return fmt.Errorf("max_waiting_jobs must be between 0 and %d", maxWaitingJobsLimit)
	}
	if l.MaxWaitingPerUser < 0 || l.MaxWaitingPerUser > maxWaitingPerUserLimit {
		return fmt.Errorf("max_waiting_per_user must be between 0 and %d", maxWaitingPerUserLimit)
	}
	if l.MaxWaitingBytes < 0 {
		return fmt.Errorf("max_waiting_bytes cannot be negative")
	}
	if l.MaxEstimatedWait <= 0 {
		return fmt.Errorf("max_estimated_wait_seconds must be positive")
	}
	if l.QueueTimeout <= 0 {
		return fmt.Errorf("max_wait_seconds must be positive")
	}
	if l.JobTimeout <= 0 {
		return fmt.Errorf("max_job_seconds must be positive")
	}
	if l.TempStorageCapacity < 0 {
		return fmt.Errorf("max_temp_storage_gb cannot be negative")
	}
	return nil
}

// AcquireRequest describes the task asking for admission. It carries metadata
// only, so a queue holding hundreds of requests keeps no large buffers alive.
type AcquireRequest struct {
	RequestID string
	UserID    int
	Model     string
	BodyBytes int64
}

// StageHandler executes one pipeline stage with the job context. A non-nil
// error ends the task with that error; nil advances the pipeline. Panics are
// contained at the stage boundary and released as a stage failure.
type StageHandler func(ctx context.Context, stage JobStage) error

// Snapshot is the observable state of one task. It never carries request
// payload: no media bytes, prompt, URL or temporary path.
type Snapshot struct {
	RequestID  string        `json:"request_id"`
	UserID     int           `json:"user_id"`
	Model      string        `json:"model"`
	Status     JobStatus     `json:"status"`
	ErrorCode  string        `json:"error_code"`
	Stage      JobStage      `json:"stage"`
	BodyBytes  int64         `json:"body_bytes"`
	QueuedAt   time.Time     `json:"queued_at"`
	StartedAt  time.Time     `json:"started_at"`
	ReleasedAt time.Time     `json:"released_at"`
	QueueMS    int64         `json:"queue_ms"`
	RunMS      int64         `json:"run_ms"`
	TotalMS    int64         `json:"total_ms"`
	Stages     []StageTiming `json:"stages"`
}

// Lease is one admitted task's claim on a running slot. Exactly one goroutine
// owns a lease; Release may be called once and is idempotent.
type Lease struct {
	req        AcquireRequest
	queuedAt   time.Time
	admittedAt time.Time
	jobTimeout time.Duration

	admitted   atomic.Bool
	runClaimed atomic.Bool
	released   atomic.Bool

	handle any // opaque caller handle; the queue stores it, never inspects it

	mu         sync.Mutex
	status     JobStatus
	errorCode  string
	stage      JobStage
	startedAt  time.Time
	releasedAt time.Time
	stages     []StageTiming
}

// Handle returns the caller-owned body handle this lease was admitted with. The
// queue keeps the pointer alive but never reads through it.
func (l *Lease) Handle() any { return l.handle }

// Request returns the metadata the lease was admitted with.
func (l *Lease) Request() AcquireRequest { return l.req }

// JobTimeout returns the per-job deadline snapshotted at admission. Limits
// published afterwards do not change an admitted task's contract.
func (l *Lease) JobTimeout() time.Duration { return l.jobTimeout }

// Status returns the current lifecycle state.
func (l *Lease) Status() JobStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

// ErrorCode returns the stable terminal error code, or "" when the task has not
// ended or ended without a classified error.
func (l *Lease) ErrorCode() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.errorCode
}

// Stage returns the last stage the task entered.
func (l *Lease) Stage() JobStage {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stage
}

// Snapshot reads the task's current state. It stays valid after release, so a
// caller can persist the final record without racing the queue.
func (l *Lease) Snapshot() Snapshot {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	end := now
	if !l.releasedAt.IsZero() {
		end = l.releasedAt
	}
	snapshot := Snapshot{
		RequestID:  l.req.RequestID,
		UserID:     l.req.UserID,
		Model:      l.req.Model,
		Status:     l.status,
		ErrorCode:  l.errorCode,
		Stage:      l.stage,
		BodyBytes:  l.req.BodyBytes,
		QueuedAt:   l.queuedAt,
		StartedAt:  l.startedAt,
		ReleasedAt: l.releasedAt,
		QueueMS:    max(l.admittedAt.Sub(l.queuedAt).Milliseconds(), 0),
		TotalMS:    max(end.Sub(l.queuedAt).Milliseconds(), 0),
		Stages:     slices.Clone(l.stages),
	}
	if !l.startedAt.IsZero() {
		snapshot.RunMS = max(end.Sub(l.startedAt).Milliseconds(), 0)
	}
	return snapshot
}

func (l *Lease) markRunning() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.status = JobStatusRunning
	l.startedAt = time.Now()
}

func (l *Lease) enterStage(stage JobStage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stage = stage
	l.status = JobStatusRunning
}

func (l *Lease) recordStage(stage JobStage, elapsed time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stage = stage
	l.stages = append(l.stages, StageTiming{Stage: stage, MS: max(elapsed.Milliseconds(), 0)})
}

func (l *Lease) complete(status JobStatus, code string, releasedAt time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.status = status
	l.errorCode = code
	l.releasedAt = releasedAt
}

// claimRun reserves the single pipeline execution of this lease. It fails when
// the lease was already released or already ran.
func (l *Lease) claimRun() bool {
	if l.released.Load() {
		return false
	}
	return l.runClaimed.CompareAndSwap(false, true)
}

// terminalOutcome classifies the error a task ended with. A rejection stays a
// rejection only while the task never entered a stage; once processing started,
// the same condition is a task failure.
func terminalOutcome(lease *Lease, err error) (JobStatus, string) {
	if err == nil {
		return JobStatusSucceeded, ""
	}
	status, code := StatusForError(err)
	if status == JobStatusRejected && lease.Stage() != "" {
		return JobStatusFailed, code
	}
	return status, code
}
