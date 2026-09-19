package video_preprocess

import (
	"context"
	"log"
	"runtime/debug"
)

// RuntimeSnapshot is the real-time view of one node's video preprocessing
// workers: what the queue owns itself plus the media counters the integration
// layer reported. It carries no media health verdict — only the layer that runs
// FFmpeg and ffprobe can make that claim, so the queue does not invent one.
type RuntimeSnapshot struct {
	RunningJobs          int   `json:"running_jobs"`
	WaitingJobs          int   `json:"waiting_jobs"`
	WaitingBytes         int64 `json:"waiting_bytes"`
	ActiveFFmpeg         int   `json:"active_ffmpeg"`
	ActiveMediaProcesses int   `json:"active_media_processes"`
	TempStorageBytes     int64 `json:"temp_storage_bytes"`
	// TempStorageLimit is the caller-reported budget; zero means unreported.
	TempStorageLimit int64 `json:"temp_storage_limit_bytes"`
	// OldestWaitMillis is the age of the request at the head of the queue.
	OldestWaitMillis int64 `json:"oldest_wait_ms"`
	MaxRunningJobs   int   `json:"max_running_jobs"`
	MaxWaitingJobs   int   `json:"max_waiting_jobs"`
	MaxWaitingBytes  int64 `json:"max_waiting_bytes"`
	QueueTimeoutMS   int64 `json:"queue_timeout_ms"`
	JobTimeoutMS     int64 `json:"job_timeout_ms"`
}

// runStage invokes one pipeline stage with panic containment. Panics surface as
// errors so Execute classifies them and — more importantly — releases the slot
// on the same path as every other failure. The stack goes to the standard log,
// where an operator can find it, and only the classification travels on.
func runStage(ctx context.Context, handler StageHandler, stage JobStage) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("video_preprocess: panic in stage %s\n%s", stage, debug.Stack())
			err = &StageError{Code: CodeProcessingFailed, Stage: stage, Message: "video processing stage panicked"}
		}
	}()
	return handler(ctx, stage)
}
