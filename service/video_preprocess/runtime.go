package video_preprocess

import (
	"os/exec"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/video_preprocess_setting"

	"github.com/bytedance/gopkg/util/gopool"
)

// singleton is the process-wide Processor. Every request and every admin read
// observes exactly one queue, so the counters on the runtime page are the real
// ones for this node. A build failure is retried on the next call: a settings
// publish that fixes the underlying inconsistency must unblock the subsystem
// without a restart.
var (
	singletonMu sync.Mutex
	singleton   *Processor
	singletonFx error
	runtimeOnce sync.Once
	applyMu     sync.Mutex
)

// GetProcessor returns the process-wide processor, building it from the
// published settings on first use. The queue starts even when video
// preprocessing is disabled: an administrator can enable it without a restart,
// and an idle queue costs nothing.
func GetProcessor() *Processor {
	singletonMu.Lock()
	defer singletonMu.Unlock()
	if singleton != nil {
		return singleton
	}
	cfg := BuildEngineConfig(video_preprocess_setting.GetSnapshot(), video_preprocess_setting.GetDeploymentConfig())
	p, err := NewProcessor(cfg)
	if err != nil {
		// The published settings have already passed ValidateCandidate, so
		// this can only be an adapter/engine inconsistency. Log once per
		// distinct failure — this can be called on every settings publish —
		// and report the processor as unavailable instead of serving counters
		// from a queue that does not exist.
		if singletonFx == nil || singletonFx.Error() != err.Error() {
			common.SysError("video_preprocess: failed to build processor: " + err.Error())
		}
		singletonFx = err
		return nil
	}
	singleton = p
	singletonFx = nil
	return p
}

// ApplyPublishedSettings rebuilds the engine config from the published
// settings snapshot and pushes it into the singleton processor. The settings
// store calls this after every publish — boot load, admin save, periodic
// option reload — so new limits take effect without a restart, with the
// queue's asymmetric resize semantics: growth opens slots immediately, shrink
// never kills a running task.
func ApplyPublishedSettings() {
	// Serialize snapshot reads with application so a delayed caller cannot
	// install an older engine configuration after a newer caller finishes.
	applyMu.Lock()
	defer applyMu.Unlock()
	p := GetProcessor()
	if p == nil {
		return // build failure already logged; the runtime API reports it
	}
	cfg := BuildEngineConfig(video_preprocess_setting.GetSnapshot(), video_preprocess_setting.GetDeploymentConfig())
	if err := p.SetConfig(cfg); err != nil {
		common.SysError("video_preprocess: failed to apply published settings: " + err.Error())
	}
}

// StartVideoPreprocessRuntime initializes the singleton queue and starts the
// per-node background maintenance. It is idempotent and safe to call from any
// node: each process manages only its own temp directory and its own queue.
func StartVideoPreprocessRuntime() {
	p := GetProcessor()
	if p == nil {
		// GetProcessor logged the build failure. Do not start a ticker that
		// would log the same nil-processor condition every minute.
		return
	}
	runtimeOnce.Do(func() {
		p.completions.Store(newCompletionWriter(4096, model.InsertVideoPreprocessLog))
		runStaleCleanupOnce(p)
		startStaleWorkspaceCleanup(p)
		gopool.Go(func() {
			runCompletionRetention()
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for range ticker.C {
				runCompletionRetention()
			}
		})
	})
}

// staleCleanupInterval is the sweep cadence. Workspaces are request-scoped and
// normally cleaned by their owning request; this loop only catches what a
// crash or kill left behind, so it does not need to be aggressive.
const staleCleanupInterval = time.Minute

// startStaleWorkspaceCleanup launches the periodic stale workspace sweep for
// this node. The TTL is re-read from the processor's config generation every
// round, so a settings change takes effect without a restart.
func startStaleWorkspaceCleanup(p *Processor) {
	gopool.Go(func() {
		common.SysLog("video_preprocess: stale workspace cleanup task started")
		ticker := time.NewTicker(staleCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			runStaleCleanupOnce(p)
		}
	})
}

// runStaleCleanupOnce performs one sweep under the current config generation.
// It runs regardless of the Enabled master switch: a workspace a previously
// enabled configuration abandoned must still be reclaimed.
func runStaleCleanupOnce(p *Processor) {
	cfg := p.config()
	if cfg.StaleTTL <= 0 {
		return
	}
	tempRoot := cfg.Media.TempRoot
	if tempRoot == "" {
		tempRoot = DefaultTempRoot
	}
	if err := CleanupStale(tempRoot, cfg.StaleTTL); err != nil {
		common.SysError("video_preprocess: stale workspace cleanup failed: " + err.Error())
	}
}

// binaryHealth reports the non-sensitive availability of one media binary: a
// lookup that never executes it and never reports its location.
func binaryHealth(name string) bool {
	if name == "" {
		return false
	}
	_, err := exec.LookPath(name)
	return err == nil
}

// RuntimeStatus is the payload of GET /api/ai-router/video/runtime. Every
// number is a live counter from this node's queue, media layer or completion
// writer. Historical aggregation is served separately from the primary DB.
type RuntimeStatus struct {
	// Available is false only when the processor failed to build; the
	// remaining fields are zero and Reason says why.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`

	Queue   RuntimeSnapshot `json:"queue"`
	Version int64           `json:"config_version"`

	// Health is binary availability as seen from this node. It carries no
	// path, version string or client-influenced value.
	FFmpegAvailable  bool `json:"ffmpeg_available"`
	FFprobeAvailable bool `json:"ffprobe_available"`

	// Enabled mirrors the published master switch so the page can show
	// "configured off" next to a healthy runtime.
	Enabled          bool                                  `json:"enabled"`
	ConfigAppliedAt  int64                                 `json:"config_applied_at"`
	CompletedLast1M  int64                                 `json:"completed_last_1m"`
	CompletedLast5M  int64                                 `json:"completed_last_5m"`
	CompletedLast15M int64                                 `json:"completed_last_15m"`
	PendingLogs      int                                   `json:"pending_logs"`
	DroppedLogs      int64                                 `json:"dropped_logs"`
	Disk             video_preprocess_setting.TempDiskInfo `json:"disk"`
}

// GetRuntimeStatus builds the runtime payload from the singleton processor and
// the published settings. It reads only in-process state; a call never touches
// the database and never triggers media work.
func GetRuntimeStatus() RuntimeStatus {
	p := GetProcessor()
	if p == nil {
		reason := "processor unavailable"
		singletonMu.Lock()
		if singletonFx != nil {
			reason = singletonFx.Error()
		}
		singletonMu.Unlock()
		return RuntimeStatus{Available: false, Reason: reason}
	}
	cfg := p.config()
	deploy := video_preprocess_setting.GetDeploymentConfig()
	counts := p.recentThroughput()
	status := RuntimeStatus{
		ConfigAppliedAt: p.configAppliedAt.Load(),
		CompletedLast1M: counts[0], CompletedLast5M: counts[1], CompletedLast15M: counts[2],
		Disk:             video_preprocess_setting.GetTempDiskInfo(),
		Available:        true,
		Queue:            p.Snapshot(),
		Version:          cfg.Version,
		FFmpegAvailable:  binaryHealth(deploy.FFmpegPath),
		FFprobeAvailable: binaryHealth(deploy.FFprobePath),
		Enabled:          cfg.Enabled,
	}
	if writer := p.completions.Load(); writer != nil {
		status.PendingLogs = len(writer.pending)
		status.DroppedLogs = writer.dropped.Load()
	}
	return status
}
