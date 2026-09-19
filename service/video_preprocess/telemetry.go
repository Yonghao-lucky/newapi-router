package video_preprocess

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/video_preprocess_setting"
)

type completionWriter struct {
	mu      sync.Mutex
	closed  bool
	pending chan *model.VideoPreprocessLog
	done    chan struct{}
	dropped atomic.Int64
	persist func(context.Context, *model.VideoPreprocessLog) error
}

// The completion channel is deliberately bounded. A database outage cannot
// grow request goroutines or retain media. Lost records are surfaced explicitly
// in runtime health and server logs instead of stalling customer requests.
func newCompletionWriter(capacity int, persist func(context.Context, *model.VideoPreprocessLog) error) *completionWriter {
	writer := &completionWriter{pending: make(chan *model.VideoPreprocessLog, capacity), done: make(chan struct{}), persist: persist}
	go writer.run()
	return writer
}

func (w *completionWriter) run() {
	defer close(w.done)
	for entry := range w.pending {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := w.persist(ctx, entry)
		cancel()
		if err != nil {
			w.dropped.Add(1)
			common.SysError("video_preprocess: completion persistence failed")
		}
	}
}

func (w *completionWriter) enqueue(entry *model.VideoPreprocessLog) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		select {
		case w.pending <- entry:
			return
		default:
		}
	}
	w.dropped.Add(1)
}

func (w *completionWriter) close(ctx context.Context) error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.pending)
	}
	w.mu.Unlock()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type throughputBucket struct {
	second int64
	count  int64
}
type completionMetrics struct {
	mu      sync.Mutex
	buckets [900]throughputBucket
}

func (p *Processor) recordCompletion(cfg EngineConfig, input ProcessInput, result *ProcessResult, err error) {
	now := time.Now().Unix()
	p.completed.mu.Lock()
	bucket := &p.completed.buckets[now%900]
	if bucket.second != now {
		*bucket = throughputBucket{second: now}
	}
	bucket.count++
	p.completed.mu.Unlock()
	writer := p.completions.Load()
	if !cfg.TelemetryEnabled || writer == nil {
		return
	}
	status, code := StatusForError(err)
	switch HTTPStatusForError(err) {
	case 408:
		status = JobStatusTimedOut
	case 499:
		status = JobStatusCanceled
	case 429:
		status = JobStatusRejected
	}
	if err == nil {
		status, code = JobStatusSucceeded, ""
	}
	entry := &model.VideoPreprocessLog{
		RequestId: requestIDOf(input.Ctx), UserId: input.UserID, Model: input.Model, Status: string(status), ErrorCode: code,
		VideoCount: result.VideoCount, ExistingImages: result.ExistingImages, ExtractedFrames: result.ExtractedFrames,
		OriginalBodyBytes: result.OriginalBodyBytes, DecodedVideoBytes: result.DecodedVideoBytes, OutputImageBytes: result.OutputImageBytes, OutputBodyBytes: result.RewrittenBodyBytes,
		QueueMS: result.QueueMS, DecodeMS: result.DecodeMS, ProbeMS: result.ProbeMS, AnalyzeMS: result.AnalyzeMS, SelectMS: result.SelectMS, ExportMS: result.ExportMS, RewriteMS: result.RewriteMS, TotalMS: result.TotalMS,
		SamplerVersion: result.SamplerVersion, ConfigVersion: cfg.Version, CreatedAt: now,
	}
	var codecs []string
	for _, video := range result.Videos {
		if video.DurationMS > math.MaxInt64-entry.VideoDurationMS {
			entry.VideoDurationMS = math.MaxInt64
		} else {
			entry.VideoDurationMS += video.DurationMS
		}
		entry.Width = max(entry.Width, video.Width)
		entry.Height = max(entry.Height, video.Height)
		if video.Codec != "" && !slices.Contains(codecs, video.Codec) {
			codecs = append(codecs, video.Codec)
		}
	}
	if len(codecs) == 1 {
		entry.Codec = codecs[0]
	} else if len(codecs) > 1 {
		entry.Codec = "mixed"
	}
	entry.Codecs = "," + strings.Join(codecs, ",") + ","
	if cfg.RecordPerVideoDetails {
		entry.Videos = slices.Clone(result.Videos)
	}
	writer.enqueue(entry)
}

func (p *Processor) recentThroughput() [3]int64 {
	now := time.Now().Unix()
	p.completed.mu.Lock()
	defer p.completed.mu.Unlock()
	var counts [3]int64
	for _, bucket := range p.completed.buckets {
		age := now - bucket.second
		if age < 0 {
			continue
		}
		if age < 60 {
			counts[0] += bucket.count
		}
		if age < 300 {
			counts[1] += bucket.count
		}
		if age < 900 {
			counts[2] += bucket.count
		}
	}
	return counts
}

func StopVideoPreprocessRuntime(ctx context.Context) error {
	p := GetProcessor()
	if p == nil {
		return nil
	}
	writer := p.completions.Swap(nil)
	if writer == nil {
		return nil
	}
	return writer.close(ctx)
}

func cleanupCompletionLogs(ctx context.Context, now time.Time, days int) error {
	if days < 1 || days > 90 {
		return fmt.Errorf("invalid video retention period")
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour).Unix()
	for {
		deleted, err := model.DeleteVideoPreprocessLogsBefore(ctx, cutoff, 1000)
		if err != nil {
			return err
		}
		if deleted < 1000 {
			return nil
		}
	}
}

func runCompletionRetention() {
	if !common.IsMasterNode {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := cleanupCompletionLogs(ctx, time.Now(), video_preprocess_setting.GetSettings().RetentionDays); err != nil {
		common.SysError("video_preprocess: completion retention failed")
	}
}
