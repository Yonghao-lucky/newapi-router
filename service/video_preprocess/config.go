package video_preprocess

import (
	"slices"
	"time"

	"github.com/QuantumNous/new-api/setting/video_preprocess_setting"
)

// EngineConfig is one request's consistent view of the whole subsystem: the
// bypass gates, the media engine limits, the rewriter budgets, the admission
// limits and the deployment paths. The Processor takes it from a single
// settings snapshot, so a hot reload can never hand different stages of one
// request different contracts, and every value crossing an
// MB/second/millisecond unit boundary is converted in exactly one place —
// here.
type EngineConfig struct {
	Media     MediaLimits
	Rewrite   RewriteLimits
	Admission AdmissionLimits

	// Enabled is the subsystem's master switch. A request arriving under a
	// disabled config bypasses without reading the body, entering the queue or
	// touching media.
	Enabled bool
	// TargetModels lists the models routed through video preprocessing. The
	// slice is a private copy: mutating it never reaches the published
	// settings.
	TargetModels []string

	// Allowlist of video/* MIME types accepted from Data URLs. Empty means the
	// media engine accepts every video/* type, but the container and codec
	// allowlists below still apply.
	AllowedMIMETypes []string
	// Allowlist of container formats and codecs enforced after ffprobe.
	AllowedContainers []string
	AllowedCodecs     []string

	// StaleTTL bounds how long an abandoned workspace may survive on disk.
	StaleTTL time.Duration
	// MaxVideosPerRequest caps how many video_url parts one request may carry.
	MaxVideosPerRequest int
	// ReserveImageSlots is the image capacity held back from video frames.
	ReserveImageSlots int
	// DefaultMaxFrames is the per-video default frame ceiling.
	DefaultMaxFrames int
	// IncludeTimestamps controls the per-frame timestamp markers.
	IncludeTimestamps bool
	// Version is the settings generation this config was built from.
	Version int64
}

// BuildEngineConfig maps one published settings snapshot plus the deployment
// configuration onto the engine. It is the only place that knows both worlds:
// the settings package owns the administrator's document, the engine owns its
// own limit types, and neither imports the other.
//
// The mapping never invents values: every field comes from the snapshot or
// from the deployment defaults. The engine's own withDefaults/validate still
// run, so a mapping bug fails loudly instead of silently relaxing a bound.
func BuildEngineConfig(snapshot video_preprocess_setting.Snapshot, deploy video_preprocess_setting.DeploymentConfig) EngineConfig {
	s := snapshot.Settings

	cfg := EngineConfig{
		Version:             snapshot.Version,
		Enabled:             s.Enabled,
		TargetModels:        slices.Clone(s.TargetModels),
		StaleTTL:            time.Duration(s.StaleFileTTLMinutes) * time.Minute,
		MaxVideosPerRequest: s.MaxVideosPerRequest,
		ReserveImageSlots:   s.ReserveImageSlots,
		DefaultMaxFrames:    s.DefaultMaxFrames,
		IncludeTimestamps:   s.IncludeTimestamps,
	}

	cfg.AllowedMIMETypes = append([]string(nil), s.AllowedMimeTypes...)
	cfg.AllowedCodecs = append([]string(nil), s.AllowedCodecs...)
	cfg.AllowedContainers = append([]string(nil), defaultContainers...)

	// Media engine limits. Byte/second figures are converted from the settings
	// document's MB/GB/seconds here and nowhere else.
	cfg.Media = MediaLimits{
		FFprobePath: deploy.FFprobePath,
		FFmpegPath:  deploy.FFmpegPath,
		TempRoot:    deploy.TempDir,

		MaxDecodedVideoBytes: s.MaxDecodedVideoBytes(),

		AllowedContainerFormats: cfg.AllowedContainers,
		AllowedVideoCodecs:      cfg.AllowedCodecs,
		MaxDurationSeconds:      float64(s.MaxDurationSeconds),
		MaxWidth:                s.MaxWidth,
		MaxHeight:               s.MaxHeight,
		MaxPixels:               int64(s.MaxPixels),
		MaxFPS:                  float64(s.MaxFPS),

		ProbeTimeout:  time.Duration(s.ProbeTimeoutSeconds) * time.Second,
		ExportTimeout: time.Duration(s.MaxJobSeconds) * time.Second,

		JPEGQuality:          s.JPEGQuality,
		MaxLongSide:          s.MaxLongSide,
		MaxTotalImageBytes:   s.MaxTotalImageBytes(),
		FFmpegThreads:        s.FFmpegThreadsPerJob,
		MaxFramesPerRequest:  s.MaxImagesPerRequest,
		MinFramesPerVideo:    s.MinFramesPerVideo,
		MaxConcurrentExports: DefaultMaxConcurrentExports,
	}

	// Rewriter budgets. MaxOutputImageBytes covers the whole request: it is the
	// JPEG payload budget every video's frames draw from together, never a
	// per-video allowance.
	cfg.Rewrite = RewriteLimits{
		MaxImagesPerRequest:   s.MaxImagesPerRequest,
		MaxOutputImageBytes:   s.MaxTotalImageBytes(),
		MaxRewrittenBodyBytes: s.MaxRewrittenBodyBytes(),
	}

	// Admission limits, in the queue's own units.
	cfg.Admission = AdmissionLimits{
		MaxRunningJobs:      s.MaxRunningJobs,
		MaxRunningPerUser:   s.MaxRunningPerUser,
		MaxWaitingJobs:      s.MaxWaitingJobs,
		MaxWaitingPerUser:   s.MaxWaitingPerUser,
		MaxWaitingBytes:     s.MaxWaitingBytes(),
		MaxEstimatedWait:    time.Duration(s.MaxEstimatedWaitSeconds) * time.Second,
		QueueTimeout:        time.Duration(s.MaxWaitSeconds) * time.Second,
		JobTimeout:          time.Duration(s.MaxJobSeconds) * time.Second,
		TempStorageCapacity: s.MaxTempStorageBytes(),
	}

	return cfg
}

// IsTargetModel reports whether a model takes the video path under this
// config. The match is exact, mirroring the settings layer: a model name is a
// capability identity, not a pattern.
func (c EngineConfig) IsTargetModel(model string) bool {
	if model == "" {
		return false
	}
	return slices.Contains(c.TargetModels, model)
}

// defaultContainers are the container formats ffprobe reports for the MIME
// types V1 accepts. mp4/quicktime/mov share the mov,mp4 family label; webm
// reports matroska. Administrators control the codec and MIME allowlists, the
// container list follows them.
var defaultContainers = []string{"mov", "mp4", "m4a", "3gp", "3g2", "mj2", "matroska", "webm", "avi", "flv"}
