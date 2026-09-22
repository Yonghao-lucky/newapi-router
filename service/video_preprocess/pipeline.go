package video_preprocess

import (
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/ai_router"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// processorVersion identifies the sampler integration that produced a
// rewritten request. It is reported in the ProcessResult so a later
// persistence phase can attribute an outcome to the code that produced it.
const processorVersion = "adaptive-v1"

// Processor is the request-level entry point the future middleware calls. It
// owns the whole pipeline — one settings snapshot, admission, per-video media
// processing, rewriting and BodyStorage installation — and hides the media
// engine details from its callers.
//
// One Processor exists per process. It is safe for concurrent use.
type Processor struct {
	// cfg is the config generation every request of this processor runs
	// under. NewProcessor installs the construction snapshot; SetConfig
	// publishes a new one. Reading it atomically means a request captures one
	// consistent contract at entry even while an administrator saves new
	// settings mid-flight.
	cfg atomic.Pointer[EngineConfig]

	queue           *Queue
	completions     atomic.Pointer[completionWriter]
	completed       completionMetrics
	configAppliedAt atomic.Int64

	// activeFFmpeg is the media layer's real-time subprocess gauge, merged
	// into the queue's runtime snapshot.
	activeFFmpeg  atomic.Int64
	activeFFprobe atomic.Int64

	// liveWorkspaces are the retained per-video workspaces of requests in
	// flight, guarded by mu. They drive the temp storage gauge.
	mu                   sync.Mutex
	liveWorkspaces       map[*workspace]struct{}
	resourceMu           sync.Mutex
	reservedStorageBytes int64
	downloadMu           sync.Mutex
	activeDownloads      int
	downloadChanged      chan struct{}
}

// NewProcessor builds the process-wide processor from the current settings.
// Call SetConfig to publish a new settings generation; a running task keeps
// the limits it was admitted with.
func NewProcessor(cfg EngineConfig) (*Processor, error) {
	cfg.Media = cfg.Media.withDefaults()
	queue, err := NewQueue(cfg.Admission)
	if err != nil {
		return nil, err
	}
	p := &Processor{queue: queue}
	p.cfg.Store(&cfg)
	p.configAppliedAt.Store(time.Now().Unix())
	p.liveWorkspaces = make(map[*workspace]struct{})
	p.publishMediaCounters()
	return p, nil
}

// SetConfig publishes a new settings generation to the queue with the queue's
// asymmetric resize semantics: growth opens slots immediately, shrink never
// kills a running task.
func (p *Processor) SetConfig(cfg EngineConfig) error {
	cfg.Media = cfg.Media.withDefaults()
	if err := p.queue.SetLimits(cfg.Admission); err != nil {
		return err
	}
	if p.config().Version != cfg.Version {
		p.configAppliedAt.Store(time.Now().Unix())
	}
	p.cfg.Store(&cfg)
	p.publishMediaCounters()
	return nil
}

// config returns the config generation requests run under.
func (p *Processor) config() EngineConfig {
	return *p.cfg.Load()
}

// Snapshot returns the merged runtime snapshot of queue and media layer.
func (p *Processor) Snapshot() RuntimeSnapshot {
	snapshot := p.queue.Snapshot()
	snapshot.TempStorageBytes = p.actualTempBytes(p.config().Media.TempRoot)
	snapshot.ActiveFFmpeg = int(p.activeFFmpeg.Load())
	snapshot.ActiveMediaProcesses = snapshot.ActiveFFmpeg + int(p.activeFFprobe.Load())
	return snapshot
}

func (p *Processor) Enabled() bool { return p.config().Enabled }

// publishMediaCounters pushes the media layer's real-time counters into the
// queue so a snapshot is one consistent view.
func (p *Processor) publishMediaCounters() {
	p.queue.SetActiveFFmpeg(int(p.activeFFmpeg.Load()))
	p.queue.SetTempStorageBytes(p.tempStorageBytes())
}

// tempStorageBytes sums the size of every file in the workspaces this
// processor's requests currently retain. Workspaces the process no longer
// holds are invisible to it — either their request cleaned them up, or the
// stale sweep removed them — so the gauge returns to zero with the work.
func (p *Processor) tempStorageBytes() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tempStorageBytesLocked()
}

// ProcessInput is everything one request hands to the processor. Either a Gin
// context (the middleware path) or an explicit body (tests and callers without
// HTTP plumbing) may be supplied; with both set, the Gin context wins.
type ProcessInput struct {
	// UserID is the requesting user for admission fairness.
	UserID int
	// Model is the target model, used for admission metadata.
	Model string

	// Ctx is the gin context whose body storage carries the request. When nil,
	// Body is processed without any HTTP side effects (no BodyStorage install).
	Ctx        *gin.Context
	Body       []byte
	Inspection *ai_router.ChatInspection
}

// Bypass reasons carried by a bypassed ProcessResult. They are part of the
// middleware contract: the caller logs or counts them and routes the original
// request unchanged.
const (
	// BypassReasonDisabled means the subsystem's master switch is off.
	BypassReasonDisabled = "video_preprocessing_disabled"
	// BypassReasonModelNotTarget means the request's model is not in
	// target_models.
	BypassReasonModelNotTarget = "model_not_target"
	// BypassReasonNoVideo means the request carries no video_url part.
	BypassReasonNoVideo = "no_video_content"
)

// ProcessResult reports what one Process call did. The middleware publishes
// it as request context metadata; nothing in it carries media, Base64, prompt
// text or temporary paths.
//
// The contract by outcome:
//
//   - processed (Bypassed=false): the request carried video and was rewritten.
//     OriginalModality is "video", EffectiveModality is "image" and the
//     rewritten body was installed on the gin context when one was supplied.
//   - bypassed (Bypassed=true): the request did not need video preprocessing —
//     the switch is off, the model is not a target, or it carries no
//     video_url part. No queue slot was taken, no media was read, no
//     BodyStorage was installed and the original body stays replayable.
//     OriginalModality and EffectiveModality report what was observed, never
//     an assumed rewrite.
//   - error: a real failure — invalid JSON, an unreadable body, a media or
//     budget rejection. Malformed video input is an error, never a bypass.
type ProcessResult struct {
	// Bypassed reports whether the request was left untouched because video
	// preprocessing did not apply to it. Exactly one of Bypassed or a
	// successful rewrite holds; a bypass never co-occurs with an install.
	Bypassed bool
	// BypassReason is one of the BypassReason* constants when Bypassed is
	// true, and empty otherwise.
	BypassReason string

	// OriginalModality is what the request was before preprocessing: "video"
	// when videos were found, "image" for image-only requests and "text"
	// otherwise. Empty on the bypass-before-scan paths (disabled switch,
	// non-target model) where the body was deliberately not read.
	OriginalModality string
	// EffectiveModality is what the AI Router must route on: "image" after a
	// successful rewrite, otherwise the original modality.
	EffectiveModality string

	// VideoCount, ExistingImages and ExtractedFrames are request totals.
	VideoCount      int
	ExistingImages  int
	ExtractedFrames int

	// Stage timings in milliseconds.
	QueueMS           int64
	Videos            []model.VideoPreprocessMedia
	DecodedVideoBytes int64
	OutputImageBytes  int64
	AnalyzeMS         int64
	SelectMS          int64
	DecodeMS          int64
	ProbeMS           int64
	ExportMS          int64
	RewriteMS         int64
	TotalMS           int64

	// SamplerVersion identifies the sampling integration.
	SamplerVersion string

	// OriginalBodyBytes and RewrittenBodyBytes are the size effect of the
	// rewrite, including the removed video Base64.
	OriginalBodyBytes  int64
	RewrittenBodyBytes int64

	// Installed reports whether a new BodyStorage was installed on the Gin
	// context. When false, the rewritten body is only returned to the caller.
	Installed bool

	// rewritten body of the request; the caller owns it when not installed.
	rewrittenBody []byte

	// workspaces are the per-video retained results. Frames are encoded into
	// the rewritten body at handoff time, so Cleanup is safe on every path.
	workspaces []*ProcessVideoResult
}

// Cleanup releases every temporary resource the result owns. It is idempotent.
func (r *ProcessResult) Cleanup() {
	if r == nil {
		return
	}
	for _, ws := range r.workspaces {
		_ = ws.Cleanup()
	}
	r.workspaces = nil
}

// RewrittenBody returns the rewritten request body. It is valid after a
// successful Process regardless of whether the body was installed.
func (r *ProcessResult) RewrittenBody() []byte { return r.rewrittenBody }

// probedVideo is one video that decoded and probed successfully and whose
// workspace is retained for the export pass.
type probedVideo struct {
	index  int
	result *ProcessVideoResult
}

// Process runs the whole pipeline for one request:
//
//	gate -> scan -> admission -> probe all videos -> multi-video budget ->
//	export all videos -> rewrite -> install
//
// The gates come first: a disabled switch, a non-target model or a request
// without video content bypasses with Bypassed=true before any queue, media
// or BodyStorage work. Every failure path likewise leaves the request
// untouched: no new BodyStorage is installed, the original stays replayable,
// and every workspace created on the way is cleaned up before the error
// returns.
func (p *Processor) Process(ctx context.Context, input ProcessInput) (out *ProcessResult, processErr error) {
	start := time.Now()

	// One config generation per request: every stage below reads the same
	// contract even if an administrator saves new settings mid-request. The
	// generation is owned by the Processor (NewProcessor/SetConfig), so tests
	// and callers can inject exact limits.
	cfg := p.config()
	cfg.Media.processChanged = p.recordFFmpeg
	cfg.Media.probeChanged = func(delta int) { p.activeFFprobe.Add(int64(delta)) }

	// Gate 1: the master switch. The body is deliberately not read — a
	// disabled subsystem must cost a request nothing, not even a storage read.
	if !cfg.Enabled {
		return bypassResult(BypassReasonDisabled), nil
	}

	// Gate 2: the model. Same contract: no body read, no queue, no media.
	if !cfg.IsTargetModel(input.Model) {
		return bypassResult(BypassReasonModelNotTarget), nil
	}

	ginCtx := input.Ctx
	var storage common.BodyStorage
	var err error
	if ginCtx != nil {
		storage, err = common.GetBodyStorage(ginCtx)
	} else {
		storage, err = common.CreateBodyStorage(input.Body)
		if err == nil {
			defer storage.Close()
		}
	}
	input.Body = nil
	if err != nil {
		return nil, wrapError(CodeInvalidRequest, "failed to read request storage", err)
	}
	if storage.Size() == 0 {
		return nil, newError(CodeInvalidRequest, "request body is empty")
	}
	inspection := input.Inspection
	if inspection == nil {
		reader, err := storage.NewReader()
		if err != nil {
			return nil, wrapError(CodeInvalidRequest, "failed to inspect request", err)
		}
		observed, scanErr := ai_router.InspectChatRequest(reader)
		_ = reader.Close()
		if scanErr != nil {
			return nil, wrapError(CodeInvalidRequest, "invalid chat request", scanErr)
		}
		inspection = &observed
	}
	if inspection.VideoCount == 0 {
		// A text or image-only request is not an error — it is exactly the
		// traffic video preprocessing must leave alone. The caller routes it
		// as usual; the BodyStorage keeps replaying the original body.
		return &ProcessResult{Bypassed: true, BypassReason: BypassReasonNoVideo, OriginalModality: string(inspection.Demand.Modality), EffectiveModality: string(inspection.Demand.Modality)}, nil
	}

	result := &ProcessResult{
		OriginalModality:  "video",
		EffectiveModality: "image",
		VideoCount:        inspection.VideoCount,
		ExistingImages:    inspection.ExistingImages,
		SamplerVersion:    processorVersion,
		OriginalBodyBytes: storage.Size(),
	}

	defer func() {
		result.TotalMS = time.Since(start).Milliseconds()
		p.recordCompletion(cfg, input, result, processErr)
	}()

	// Request-level input limits before queueing.
	if max := cfg.MaxVideosPerRequest; max > 0 && result.VideoCount > max {
		return nil, newError(CodeTooLarge,
			fmt.Sprintf("request carries %d videos, above the configured limit of %d",
				result.VideoCount, max))
	}
	if max := cfg.Rewrite.MaxRequestBytes; max > 0 && storage.Size() > max {
		return nil, newError(CodeTooLarge, "request body exceeds the configured video preprocessing limit")
	}

	if cfg.Rewrite.MaxImagesPerRequest-result.ExistingImages-cfg.ReserveImageSlots < result.VideoCount*cfg.Media.MinFramesPerVideo {
		return nil, newError(CodeImageLimitExceeded, "request does not leave the minimum image allowance for each video")
	}
	if err := p.spoolVideoBody(ctx, cfg, storage); err != nil {
		return nil, err
	}
	if ginCtx != nil {
		ginCtx.Set(common.KeyRequestBody, nil)
		ginCtx.Request.Body = io.NopCloser(storage)
		ginCtx.Request.GetBody = storage.NewReader
	}

	// Stage 2: admission. The queue parks metadata and the body handle only;
	// the full body is never copied into the queue. Materialization for the
	// rewrite happens after admission, from the caller's own body bytes.
	queueStart := time.Now()
	lease, admitErr := p.queue.Acquire(ctx, AcquireRequest{
		RequestID: requestIDOf(ginCtx),
		UserID:    input.UserID,
		Model:     input.Model,
		BodyBytes: storage.Size(),
	}, storage)
	result.QueueMS = time.Since(queueStart).Milliseconds()
	if admitErr != nil {
		return nil, admitErr
	}

	// The queue slot is released exactly once, here, on every path after a
	// successful Acquire. Execute is the only thing that runs stages. A failed
	// request also drops every workspace it created: a half-processed request
	// never keeps media on disk.
	failed := true
	defer func() {
		if failed {
			p.releaseResult(result)
			p.queue.Release(lease, contextFailure(ctxErrOr(ctx)))
		}
	}()

	// Stages 3..6 run under the lease via Execute, which classifies stage
	// failures, enforces the snapshotted job timeout and contains panics.
	var rewriteResult *RewriteResult
	var body []byte
	var layout *RequestLayout
	var releaseStorage func()
	defer func() {
		if releaseStorage != nil {
			releaseStorage()
		}
	}()
	execErr := p.queue.Execute(ctx, lease, func(stageCtx context.Context, stage JobStage) error {
		switch stage {
		case JobStageDecoding:
			body, err = storage.Bytes()
			if err != nil {
				return wrapError(CodeInvalidRequest, "failed to read admitted request", err)
			}
			layout, err = ScanChatRequest(body)
			if err != nil {
				return err
			}
			// A remote URL's body size says nothing about its media size. Reserve
			// the full per-video download allowance before any network activity.
			var mediaReservation int64
			for _, part := range layout.Videos {
				url := extractVideoURL(body, part)
				if strings.HasPrefix(url, "data:") {
					mediaReservation += min(part.URLBytes, cfg.Media.MaxDecodedVideoBytes)
				} else {
					mediaReservation += cfg.Media.MaxDecodedVideoBytes
				}
			}
			releaseStorage, err = p.reserveStorage(cfg, mediaReservation+cfg.Rewrite.MaxOutputImageBytes+cfg.Rewrite.MaxRewrittenBodyBytes)
			return err
		case JobStageExporting:
			if err := p.processVideos(stageCtx, cfg, layout, body, result); err != nil {
				return err
			}
			return nil
		case JobStageRewriting:
			rewriteStart := time.Now()
			rewrite, err := p.rewriteStage(stageCtx, cfg, layout, body, result)
			result.RewriteMS = time.Since(rewriteStart).Milliseconds()
			if err != nil {
				return err
			}
			rewriteResult = rewrite
			return nil
		default:
			// Per-video decode, probe, analysis and selection run inside
			// processVideos and record their own measured stage durations.
			return nil
		}
	})
	if execErr != nil {
		return nil, execErr
	}
	if rewriteResult == nil {
		return nil, &StageError{Code: CodeProcessingFailed, Stage: JobStageRewriting, Message: "rewrite produced no result"}
	}

	// Stage 7: install. Building and validating the new storage happens before
	// the swap, so a failure here leaves the original body replayable.
	if ginCtx != nil {
		if _, err := InstallRewrittenBody(ginCtx, rewriteResult.Body); err != nil {
			return nil, err
		}
		result.Installed = true
	}
	result.rewrittenBody = rewriteResult.Body
	result.RewrittenBodyBytes = rewriteResult.RewrittenBodyBytes
	result.TotalMS = time.Since(start).Milliseconds()

	// The frames are encoded into the rewritten body now, so the workspaces
	// leave the live set here rather than at the caller's Cleanup. The
	// caller-facing Cleanup stays for direct-engine callers and belt-and-suspenders.
	p.releaseResult(result)

	failed = false
	return result, nil
}

// releaseResult removes every workspace a result holds and drops them from the
// live set. It is idempotent: Cleanup is, and so is the map delete.
func (p *Processor) releaseResult(result *ProcessResult) {
	if result == nil {
		return
	}
	for _, ws := range result.workspaces {
		p.trackWorkspace(ws.workspace, false)
		_ = ws.Cleanup()
	}
	result.workspaces = nil
}

// bypassResult builds the result for a request video preprocessing did not
// apply to. The gates that skip the body scan (disabled switch, non-target
// model) use it: no modality is claimed because the body was not read.
func bypassResult(reason string) *ProcessResult {
	return &ProcessResult{Bypassed: true, BypassReason: reason}
}

// processVideos probes every video, allocates the multi-video frame budget and
// exports the frames. Any video failure aborts the whole request: partial
// results are cleaned up and never rewritten into a half-processed request.
func (p *Processor) processVideos(stageCtx context.Context, cfg EngineConfig, layout *RequestLayout, body []byte, result *ProcessResult) error {
	// Decode + probe pass. The decoded video stays in its workspace so the
	// export pass never re-decodes.
	probed := make([]probedVideo, 0, len(layout.Videos))
	var probeErr error
	for _, part := range layout.Videos {
		url := extractVideoURL(body, part)
		if url == "" {
			probeErr = newError(CodeInvalidDataURL,
				"video "+fmt.Sprint(part.Index)+" has no readable url")
			break
		}
		vr, err := processVideoStage(stageCtx, ProcessVideoInput{Video: VideoInput{URL: url, Index: part.Index}}, Options{
			Limits: cfg.Media, AllowedMIMETypes: cfg.AllowedMIMETypes, RemoteURL: cfg.RemoteURL,
			acquireDownload: func(ctx context.Context) (func(), error) { return p.acquireDownload(ctx, cfg.RemoteURL.MaxDownloads) },
		}, false)
		if vr != nil {
			result.DecodeMS += vr.DurationMillis.Decode
			result.ProbeMS += vr.DurationMillis.Probe
			result.DecodedVideoBytes += vr.DecodedBytes
			duration := min(vr.Metadata.DurationSeconds*1000, float64(math.MaxInt64-1024))
			result.Videos = append(result.Videos, model.VideoPreprocessMedia{
				Index: part.Index, DurationMS: int64(duration), Width: vr.Metadata.Width, Height: vr.Metadata.Height, Codec: vr.Metadata.VideoCodec, DecodedBytes: vr.DecodedBytes,
				SourceType: vr.SourceType, SourceHost: vr.Download.SourceHost, DownloadBytes: vr.Download.Bytes, DownloadMS: vr.Download.DurationMS, DownloadErrorStage: vr.Download.FailureStage,
			})
		}
		if err != nil {
			probeErr = err
			break
		}

		p.trackWorkspace(vr.workspace, true)
		probed = append(probed, probedVideo{index: part.Index, result: vr})
		analyzeStart := time.Now()
		vr.analysis, err = analyzeVideo(stageCtx, vr, cfg.Analysis)
		result.AnalyzeMS += time.Since(analyzeStart).Milliseconds()
		if err != nil {
			probeErr = err
			break
		}
	}
	if probeErr != nil {
		cleanupProbed(p, probed)
		return probeErr
	}

	// Multi-video frame budget over the whole request.
	inputs := make([]VideoBudgetInput, len(probed))
	for i, v := range probed {
		ceiling := cfg.DefaultMaxFrames
		if v.result.analysis.Complexity > 1.5 {
			ceiling = cfg.HighDetailMaxFrames
		}
		inputs[i] = VideoBudgetInput{VideoIndex: v.index, DurationSeconds: v.result.Metadata.DurationSeconds, Complexity: v.result.analysis.Complexity, MaxFrames: ceiling}
	}
	available := cfg.Rewrite.MaxImagesPerRequest - result.ExistingImages - cfg.ReserveImageSlots
	budget, err := allocateFrameBudget(available, cfg.Media.MinFramesPerVideo, cfg.DefaultMaxFrames, inputs)
	if err != nil {
		cleanupProbed(p, probed)
		return err
	}

	// Reserve JSON/Base64 overhead first. All videos share one output allowance;
	// no video can consume bytes reserved for its siblings' minimum coverage.
	preserved := int64(len(body))
	for _, part := range layout.Videos {
		preserved -= int64(part.length)
	}
	bodyAllowance := cfg.Rewrite.MaxRewrittenBodyBytes - preserved - int64(len(probed)*256+budget.TotalFrames*256)
	remainingBytes := min(cfg.Rewrite.MaxOutputImageBytes, bodyAllowance*3/4)
	if remainingBytes <= 0 {
		cleanupProbed(p, probed)
		return newError(CodeOutputTooLarge, "request leaves no space for video frames")
	}
	remainingFrames := budget.TotalFrames
	fallbackCount := 0
	for i, v := range probed {
		kept := v.result
		frames := budget.Videos[i].Frames
		selectStart := time.Now()
		kept.selected, err = selectAdaptiveFrames(stageCtx, kept.analysis, kept.Metadata.DurationSeconds, frames, cfg.Media.MinFramesPerVideo, cfg.Analysis)
		if err != nil {
			cleanupProbed(p, probed)
			return err
		}
		if len(kept.selected) == 0 {
			// A successfully decoded clip with no useful visual candidates (for
			// example an all-black leader) may use the stable coverage baseline.
			// Decoder errors, limits, cancellation and resource failures never reach
			// this fallback: they propagate before frame selection.
			kept.selected = selectUniformCandidates(kept.analysis.Candidates, kept.Metadata.DurationSeconds, frames)
			kept.samplerVersion = "uniform-v1"
			fallbackCount++
		} else {
			kept.samplerVersion = processorVersion
		}
		timestamps := make([]float64, 0, len(kept.selected))
		for _, candidate := range kept.selected {
			timestamps = append(timestamps, candidate.Timestamp)
		}
		result.SelectMS += time.Since(selectStart).Milliseconds()
		limits := kept.limits
		limits.MaxTotalImageBytes = remainingBytes * int64(frames) / int64(remainingFrames)
		if limits.MaxTotalImageBytes <= 0 {
			cleanupProbed(p, probed)
			return newError(CodeOutputTooLarge, "video output budget is exhausted")
		}
		exportStart := time.Now()
		exportErr := exportJobWithTimestamps(stageCtx, limits, kept.workspace.path("video.bin"), timestamps, kept.workspace.dir, kept.Metadata.RotationDegrees, kept)
		result.ExportMS += time.Since(exportStart).Milliseconds()
		if exportErr != nil {
			cleanupProbed(p, probed)
			return exportErr
		}
		for _, frame := range kept.Frames {
			remainingBytes -= frame.Bytes
			result.OutputImageBytes += frame.Bytes
			result.Videos[i].ImageBytes += frame.Bytes
		}
		remainingFrames -= frames
		result.ExtractedFrames += len(kept.Frames)
		result.workspaces = append(result.workspaces, kept)
		result.Videos[i].Frames = len(kept.Frames)
		result.Videos[i].SamplerVersion = kept.samplerVersion
	}
	if fallbackCount == len(probed) {
		result.SamplerVersion = "uniform-v1"
	} else if fallbackCount > 0 {
		result.SamplerVersion = "adaptive-v1+uniform-v1"
	}

	return nil
}

// rewriteStage adapts the exported frames into FrameSets and splices them into
// the request.
func (p *Processor) rewriteStage(stageCtx context.Context, cfg EngineConfig, layout *RequestLayout, body []byte, result *ProcessResult) (*RewriteResult, error) {
	sets := make([]FrameSet, 0, len(result.workspaces))
	for _, ws := range result.workspaces {
		set := FrameSet{VideoIndex: ws.VideoIndex}
		for _, frame := range ws.Frames {
			data, err := readFrameJPEG(frame.Path)
			if err != nil {
				return nil, err
			}
			set.Frames = append(set.Frames, Frame{
				Timestamp: time.Duration(frame.TimestampSeconds * float64(time.Second)),
				JPEG:      data,
			})
		}
		sets = append(sets, set)
	}

	return Rewrite(stageCtx, RewriteInput{
		Body:              body,
		Layout:            layout,
		Frames:            sets,
		IncludeTimestamps: cfg.IncludeTimestamps,
		Limits:            cfg.Rewrite,
	})
}

// trackWorkspace adds or removes one retained workspace from the temp storage
// gauge. add=false happens when a workspace leaves the live set — because its
// request succeeded, failed or was cleaned up.
func (p *Processor) trackWorkspace(ws *workspace, add bool) {
	if ws == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.liveWorkspaces == nil {
		return // tests construct Processors without the map
	}
	if add {
		p.liveWorkspaces[ws] = struct{}{}
	} else {
		delete(p.liveWorkspaces, ws)
	}
	p.queue.SetTempStorageBytes(p.tempStorageBytesLocked())
}

// tempStorageBytesLocked is tempStorageBytes without re-taking p.mu.
func (p *Processor) tempStorageBytesLocked() int64 {
	var total int64
	for ws := range p.liveWorkspaces {
		total += ws.bytesOnDisk()
	}
	return total
}

// cleanupProbed removes every retained workspace. It runs on the failure paths
// of processVideos so a partially processed request never leaks media.
func cleanupProbed(p *Processor, probed []probedVideo) {
	for _, v := range probed {
		p.trackWorkspace(v.result.workspace, false)
		_ = v.result.Cleanup()
	}
}

// requestIDOf reports the request id for admission metadata, or an empty
// string outside a gin request.
func requestIDOf(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return c.GetString(common.RequestIdKey)
}

// extractVideoURL reads the video_url.url value of one located part from the
// request body. The part offsets belong to exactly this body.
func extractVideoURL(body []byte, part VideoPart) string {
	raw := body[part.offset : part.offset+part.length]
	if value := gjson.GetBytes(raw, "video_url.url"); value.Type == gjson.String {
		return value.String()
	}
	// A bare-string spelling: {"type":"video_url","video_url":"data:..."}.
	if obj := gjson.GetBytes(raw, "video_url"); obj.Type == gjson.String {
		return obj.String()
	}
	return ""
}

// ctxErrOr reports the context's error, or nil when ctx is nil.
func ctxErrOr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// recordFFmpeg counts actual running FFmpeg subprocesses, including downgrade
// attempts, rather than the number of videos in a request.
func (p *Processor) recordFFmpeg(delta int) {
	p.queue.SetActiveFFmpeg(int(p.activeFFmpeg.Add(int64(delta))))
}
