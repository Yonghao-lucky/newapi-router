package video_preprocess

import (
	"path/filepath"
	"time"
)

// Default limits mirror the recommended production values of the development
// plan. They exist so a caller can start from MediaLimits{} and only override what
// it needs; they are not global configuration and are never read from a
// database or a file by this package.
const (
	DefaultTempRoot              = "/tmp/video-preprocess"
	DefaultMaxDecodedVideoBytes  = 256 << 20
	DefaultMaxDurationSeconds    = 1800.0
	DefaultMaxWidth              = 3840
	DefaultMaxHeight             = 2160
	DefaultMaxPixels             = 8294400
	DefaultMaxFPS                = 120.0
	DefaultProbeTimeout          = 10 * time.Second
	DefaultExportTimeout         = 180 * time.Second
	DefaultJPEGQuality           = 82
	DefaultMaxLongSide           = 1280
	DefaultMaxTotalImageBytes    = 20 << 20
	DefaultFFmpegThreads         = 3
	DefaultMaxFramesPerRequest   = 60
	DefaultMinFramesPerVideo     = 1
	DefaultMaxConcurrentExports  = 4
	DefaultMaxAnalyzedFrameCount = 10_000_000
	DefaultMinExportWidth        = 64
	DefaultMinExportQuality      = 40
	DefaultMaxProbeStderrBytes   = 8 << 10
	DefaultMaxFFmpegStderrBytes  = 8 << 10
	DefaultFFprobeBinary         = "ffprobe"
	DefaultFFmpegBinary          = "ffmpeg"
)

// Lower bounds of the deterministic export downgrade ladder.
const (
	minExportLongSide = 256
	minExportQuality  = 32
)

// MediaLimits carries every constraint the engine enforces. Callers own their own
// configuration namespace and map it onto this struct; the engine reads
// nothing else. Setting a numeric field to zero selects the corresponding
// Default constant, except for MaxFramesPerRequest, whose zero value means
// "frames are not budgeted" because the request image hard limit belongs to
// the caller's admission layer.
type MediaLimits struct {
	// Binary paths. Only these two are ever executed, always as argv arrays.
	FFprobePath string
	FFmpegPath  string

	// TempRoot is the parent directory for request-scoped workspaces. It is a
	// deployment-level decision, not a per-request one. Only directories this
	// package created under it are ever removed by CleanupStale.
	TempRoot string

	// MaxDecodedVideoBytes bounds the decoded size of a single video. The
	// decoder stops at the bound instead of materializing the whole video in
	// memory first, so a hostile Data URL cannot exhaust the heap.
	MaxDecodedVideoBytes int64

	// Media acceptance limits, enforced from ffprobe metadata.
	AllowedContainerFormats []string
	AllowedVideoCodecs      []string
	MaxDurationSeconds      float64
	MaxWidth                int
	MaxHeight               int
	MaxPixels               int64
	MaxFPS                  float64
	MaxAnalyzedFrameCount   int64

	// Timeouts. ProbeTimeout bounds one ffprobe call; ExportTimeout bounds the
	// whole export of one video, including every downgrade attempt.
	ProbeTimeout  time.Duration
	ExportTimeout time.Duration

	// Export shape.
	JPEGQuality          int
	MaxLongSide          int
	MaxTotalImageBytes   int64
	FFmpegThreads        int
	MaxFramesPerRequest  int
	MinFramesPerVideo    int
	MaxConcurrentExports int
	MaxProbeStderrBytes  int
	MaxFFmpegStderrBytes int
}

// withDefaults returns a copy of l with every unset field replaced by its
// default. It never mutates the receiver, so a caller's MediaLimits value can be
// reused across requests and concurrent engines.
func (l MediaLimits) withDefaults() MediaLimits {
	out := l
	if out.FFprobePath == "" {
		out.FFprobePath = DefaultFFprobeBinary
	}
	if out.FFmpegPath == "" {
		out.FFmpegPath = DefaultFFmpegBinary
	}
	if out.TempRoot == "" {
		out.TempRoot = DefaultTempRoot
	}
	if out.MaxDecodedVideoBytes <= 0 {
		out.MaxDecodedVideoBytes = DefaultMaxDecodedVideoBytes
	}
	if len(out.AllowedContainerFormats) == 0 {
		out.AllowedContainerFormats = []string{"mov", "mp4", "m4a", "3gp", "3g2", "mj2", "matroska", "webm"}
	}
	if len(out.AllowedVideoCodecs) == 0 {
		out.AllowedVideoCodecs = []string{"h264", "hevc", "vp9", "av1"}
	}
	if out.MaxDurationSeconds <= 0 {
		out.MaxDurationSeconds = DefaultMaxDurationSeconds
	}
	if out.MaxWidth <= 0 {
		out.MaxWidth = DefaultMaxWidth
	}
	if out.MaxHeight <= 0 {
		out.MaxHeight = DefaultMaxHeight
	}
	if out.MaxPixels <= 0 {
		out.MaxPixels = DefaultMaxPixels
	}
	if out.MaxFPS <= 0 {
		out.MaxFPS = DefaultMaxFPS
	}
	if out.MaxAnalyzedFrameCount <= 0 {
		out.MaxAnalyzedFrameCount = DefaultMaxAnalyzedFrameCount
	}
	if out.ProbeTimeout <= 0 {
		out.ProbeTimeout = DefaultProbeTimeout
	}
	if out.ExportTimeout <= 0 {
		out.ExportTimeout = DefaultExportTimeout
	}
	if out.JPEGQuality <= 0 {
		out.JPEGQuality = DefaultJPEGQuality
	}
	if out.MaxLongSide <= 0 {
		out.MaxLongSide = DefaultMaxLongSide
	}
	if out.MaxTotalImageBytes <= 0 {
		out.MaxTotalImageBytes = DefaultMaxTotalImageBytes
	}
	if out.FFmpegThreads <= 0 {
		out.FFmpegThreads = DefaultFFmpegThreads
	}
	if out.MinFramesPerVideo <= 0 {
		out.MinFramesPerVideo = DefaultMinFramesPerVideo
	}
	if out.MaxConcurrentExports <= 0 {
		out.MaxConcurrentExports = DefaultMaxConcurrentExports
	}
	if out.MaxProbeStderrBytes <= 0 {
		out.MaxProbeStderrBytes = DefaultMaxProbeStderrBytes
	}
	if out.MaxFFmpegStderrBytes <= 0 {
		out.MaxFFmpegStderrBytes = DefaultMaxFFmpegStderrBytes
	}
	return out
}

// validate rejects caller limits that would make the engine unsafe or
// meaningless. It runs on the defaulted copy so an unset field is valid.
func (l MediaLimits) validate() error {
	if l.MaxFramesPerRequest < 0 {
		return invalidConfig("max frames per request must not be negative, got %d", l.MaxFramesPerRequest)
	}
	if l.MaxFramesPerRequest > 0 && l.MinFramesPerVideo > l.MaxFramesPerRequest {
		return invalidConfig("min frames per video %d exceeds max frames per request %d",
			l.MinFramesPerVideo, l.MaxFramesPerRequest)
	}
	if l.JPEGQuality < minExportQuality || l.JPEGQuality > 95 {
		return invalidConfig("jpeg quality must be between %d and 95, got %d", minExportQuality, l.JPEGQuality)
	}
	if l.MaxLongSide < minExportLongSide {
		return invalidConfig("max long side must be at least %d, got %d", minExportLongSide, l.MaxLongSide)
	}
	if l.FFmpegThreads > 64 {
		return invalidConfig("ffmpeg threads per job is unreasonably high: %d", l.FFmpegThreads)
	}
	if l.MaxConcurrentExports > 256 {
		return invalidConfig("max concurrent exports is unreasonably high: %d", l.MaxConcurrentExports)
	}
	if l.MaxProbeStderrBytes > 1<<20 || l.MaxFFmpegStderrBytes > 1<<20 {
		return invalidConfig("subprocess stderr limit is unreasonably high")
	}
	if !filepath.IsAbs(l.TempRoot) && l.TempRoot != "." {
		return invalidConfig("temp root must be an absolute path or %q, got %q", ".", l.TempRoot)
	}
	if filepath.Clean(l.TempRoot) == string(filepath.Separator) {
		return invalidConfig("temp root must not be the filesystem root")
	}
	return nil
}

// containsFold reports whether list contains value, comparing ASCII
// case-insensitively. Container names and codec names are always ASCII, so a
// full Unicode fold would only add cost without widening the allowlist.
func containsFold(list []string, value string) bool {
	for _, item := range list {
		if equalFoldASCII(item, value) {
			return true
		}
	}
	return false
}

// equalFoldASCII compares two ASCII strings case-insensitively.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}

// lowerASCII lowercases a single ASCII byte and leaves every other byte
// untouched.
func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}
