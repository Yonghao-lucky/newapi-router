// Package video_preprocess_setting owns the Video Preprocessing V1 control
// plane: the Web-editable settings document, its strict validation, and the
// atomic runtime snapshot other modules read.
//
// Deployment-level parameters (FFmpeg binaries, temp directory, container
// limits) deliberately live outside this model — see deploy.go.
package video_preprocess_setting

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

const (
	// ConfigPrefix namespaces every option this module owns.
	ConfigPrefix = "video_preprocess."

	// SettingsOptionKey stores the whole settings document as one JSON value.
	// A single row is what makes an update all-or-nothing: there is no state in
	// which half of a candidate has been persisted.
	SettingsOptionKey = ConfigPrefix + "settings"

	// ConfigVersion is the schema version of the settings document. It is
	// persisted with the settings so a future build can recognise a document
	// written by an older one.
	ConfigVersion = 1
)

// ModeAlwaysFrames is the only sampler mode in V1. The enum exists so a future
// adaptive mode is an addition to a validated set rather than a schema change.
const ModeAlwaysFrames = "always_frames"

// bytesPerMB and bytesPerGB convert the MB/GB limits administrators edit into
// the byte figures the admission and storage code compares against.
const (
	bytesPerMB = int64(1024 * 1024)
	bytesPerGB = bytesPerMB * 1024
)

// weightSumTolerance is how far the four scoring weights may drift from 1.0.
// Round-tripping through the Option table is the only expected source of error.
const weightSumTolerance = 1e-6

// Settings is the complete Web-editable Video Preprocessing configuration.
//
// The struct has only scalar and slice fields: a shallow copy plus cloning the
// slices yields a fully independent snapshot, which is what makes the published
// runtime state safe to hand to concurrent callers.
type Settings struct {
	// Feature and scope.
	Enabled          bool     `json:"enabled"`
	Mode             string   `json:"mode"`
	TargetModels     []string `json:"target_models"`
	AllowedMimeTypes []string `json:"allowed_mime_types"`
	AllowedCodecs    []string `json:"allowed_codecs"`

	// Input limits.
	MaxVideosPerRequest int `json:"max_videos_per_request"`
	MaxDecodedVideoMB   int `json:"max_decoded_video_mb"`
	MaxDurationSeconds  int `json:"max_duration_seconds"`
	MaxWidth            int `json:"max_width"`
	MaxHeight           int `json:"max_height"`
	MaxPixels           int `json:"max_pixels"`
	MaxFPS              int `json:"max_fps"`
	ProbeTimeoutSeconds int `json:"probe_timeout_seconds"`

	// Image and sampling budget.
	MaxImagesPerRequest     int     `json:"max_images_per_request"`
	DefaultMaxFrames        int     `json:"default_max_frames"`
	HighDetailMaxFrames     int     `json:"high_detail_max_frames"`
	MinFramesPerVideo       int     `json:"min_frames_per_video"`
	ReserveImageSlots       int     `json:"reserve_image_slots"`
	AnalysisWidth           int     `json:"analysis_width"`
	CandidateLimit          int     `json:"candidate_limit"`
	ShortScanFPS            float64 `json:"short_scan_fps"`
	MediumScanFPS           float64 `json:"medium_scan_fps"`
	LongScanFPS             float64 `json:"long_scan_fps"`
	MinSelectedGapMS        int     `json:"min_selected_gap_ms"`
	SceneWeight             float64 `json:"scene_weight"`
	MotionWeight            float64 `json:"motion_weight"`
	SharpnessWeight         float64 `json:"sharpness_weight"`
	TemporalWeight          float64 `json:"temporal_weight"`
	PhashDuplicateThreshold float64 `json:"phash_duplicate_threshold"`
	BlackFrameThreshold     float64 `json:"black_frame_threshold"`

	// Output quality.
	JPEGQuality        int  `json:"jpeg_quality"`
	MaxLongSide        int  `json:"max_long_side"`
	MaxTotalImageMB    int  `json:"max_total_image_mb"`
	MaxRewrittenBodyMB int  `json:"max_rewritten_body_mb"`
	IncludeTimestamps  bool `json:"include_timestamps"`

	// Concurrency, queueing and per-user fairness.
	MaxRunningJobs          int `json:"max_running_jobs"`
	FFmpegThreadsPerJob     int `json:"ffmpeg_threads_per_job"`
	MaxWaitingJobs          int `json:"max_waiting_jobs"`
	MaxWaitingBytesMB       int `json:"max_waiting_bytes_mb"`
	MaxWaitSeconds          int `json:"max_wait_seconds"`
	MaxJobSeconds           int `json:"max_job_seconds"`
	MaxRunningPerUser       int `json:"max_running_per_user"`
	MaxWaitingPerUser       int `json:"max_waiting_per_user"`
	MaxEstimatedWaitSeconds int `json:"max_estimated_wait_seconds"`

	// Storage and observability.
	MaxTempStorageGB      int  `json:"max_temp_storage_gb"`
	StaleFileTTLMinutes   int  `json:"stale_file_ttl_minutes"`
	TelemetryEnabled      bool `json:"telemetry_enabled"`
	RetentionDays         int  `json:"retention_days"`
	RecordPerVideoDetails bool `json:"record_per_video_details"`
}

// DefaultSettings returns the recommended V1 production defaults (plan §17).
// The master switch stays false: preprocessing is enabled by an operator after
// the baseline and load tests, not by a deploy.
func DefaultSettings() Settings {
	return Settings{
		Enabled:          false,
		Mode:             ModeAlwaysFrames,
		TargetModels:     []string{"kimi-k3"},
		AllowedMimeTypes: []string{"video/mp4", "video/webm", "video/quicktime"},
		AllowedCodecs:    []string{"h264", "hevc", "vp9", "av1"},

		MaxVideosPerRequest: 4,
		MaxDecodedVideoMB:   75,
		MaxDurationSeconds:  1800,
		MaxWidth:            3840,
		MaxHeight:           2160,
		MaxPixels:           8294400,
		MaxFPS:              120,
		ProbeTimeoutSeconds: 10,

		MaxImagesPerRequest:     60,
		DefaultMaxFrames:        32,
		HighDetailMaxFrames:     56,
		MinFramesPerVideo:       4,
		ReserveImageSlots:       0,
		AnalysisWidth:           320,
		CandidateLimit:          3600,
		ShortScanFPS:            4.0,
		MediumScanFPS:           2.0,
		LongScanFPS:             0.5,
		MinSelectedGapMS:        400,
		SceneWeight:             0.40,
		MotionWeight:            0.20,
		SharpnessWeight:         0.15,
		TemporalWeight:          0.25,
		PhashDuplicateThreshold: 0.08,
		BlackFrameThreshold:     0.90,

		JPEGQuality:        82,
		MaxLongSide:        1280,
		MaxTotalImageMB:    20,
		MaxRewrittenBodyMB: 32,
		IncludeTimestamps:  true,

		MaxRunningJobs:          16,
		FFmpegThreadsPerJob:     3,
		MaxWaitingJobs:          500,
		MaxWaitingBytesMB:       61440,
		MaxWaitSeconds:          600,
		MaxJobSeconds:           180,
		MaxRunningPerUser:       2,
		MaxWaitingPerUser:       20,
		MaxEstimatedWaitSeconds: 600,

		MaxTempStorageGB:      100,
		StaleFileTTLMinutes:   30,
		TelemetryEnabled:      true,
		RetentionDays:         7,
		RecordPerVideoDetails: true,
	}
}

// Queue admission ceiling in bytes.
func (s Settings) MaxWaitingBytes() int64 {
	return int64(s.MaxWaitingBytesMB) * bytesPerMB
}

// Temp-storage budget in bytes.
func (s Settings) MaxTempStorageBytes() int64 {
	return int64(s.MaxTempStorageGB) * bytesPerGB
}

// Per-video decoded size ceiling in bytes.
func (s Settings) MaxDecodedVideoBytes() int64 {
	return int64(s.MaxDecodedVideoMB) * bytesPerMB
}

// JPEG payload ceiling for one rewritten request, in bytes.
func (s Settings) MaxTotalImageBytes() int64 {
	return int64(s.MaxTotalImageMB) * bytesPerMB
}

// Whole rewritten request body ceiling in bytes.
func (s Settings) MaxRewrittenBodyBytes() int64 {
	return int64(s.MaxRewrittenBodyMB) * bytesPerMB
}

// IsTargetModel reports whether a model takes the video path. The match is
// exact: a model name is a capability identity, not a pattern.
func (s Settings) IsTargetModel(model string) bool {
	if model == "" {
		return false
	}
	return slices.Contains(s.TargetModels, model)
}

// AllowsMimeType reports whether a media type may be decoded.
func (s Settings) AllowsMimeType(mimeType string) bool {
	return slices.Contains(s.AllowedMimeTypes, mimeType)
}

// AllowsCodec reports whether a video codec may be decoded.
func (s Settings) AllowsCodec(codec string) bool {
	return slices.Contains(s.AllowedCodecs, codec)
}

// normalizeEntries trims and lowercases each entry, drops empties and
// duplicates, and expands a bare subtype to its canonical "video/" media type
// so the plan's short-form Web control ("mp4") stores as "video/mp4".
func normalizeEntries(entries []string, canonicalizeMimeType bool) []string {
	normalized := make([]string, 0, len(entries))
	for _, entry := range entries {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if canonicalizeMimeType && !strings.Contains(entry, "/") {
			entry = "video/" + entry
		}
		if slices.Contains(normalized, entry) {
			continue
		}
		normalized = append(normalized, entry)
	}
	return normalized
}

// Normalize brings the document into the canonical form validation and the
// runtime snapshot assume. It never rejects: values it cannot repair are
// reported by Validate.
func (s *Settings) Normalize() {
	if s == nil {
		return
	}
	// Target models keep their case — they are opaque upstream identifiers.
	models := make([]string, 0, len(s.TargetModels))
	for _, model := range s.TargetModels {
		model = strings.TrimSpace(model)
		if model == "" || slices.Contains(models, model) {
			continue
		}
		models = append(models, model)
	}
	s.TargetModels = models
	s.AllowedMimeTypes = normalizeEntries(s.AllowedMimeTypes, true)
	s.AllowedCodecs = normalizeEntries(s.AllowedCodecs, false)
	if s.Mode == "" {
		s.Mode = ModeAlwaysFrames
	}
}

// clone returns a copy that shares no mutable state with the receiver.
func (s Settings) clone() Settings {
	cloned := s
	cloned.TargetModels = slices.Clone(s.TargetModels)
	cloned.AllowedMimeTypes = slices.Clone(s.AllowedMimeTypes)
	cloned.AllowedCodecs = slices.Clone(s.AllowedCodecs)
	return cloned
}

// ValidationIssue is one finding from validating a candidate document. Errors
// block a save; warnings are advisory. Code and Field are stable so the Web
// form can attach a message to the right input.
type ValidationIssue struct {
	Level   string `json:"level"` // "error" | "warning"
	Code    string `json:"code"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

// numericLimit is one field checked against an inclusive [min, max] bound.
type numericLimit struct {
	field string
	value int
	min   int
	max   int
}

// floatLimit is one field checked against an inclusive finite [min, max] bound.
type floatLimit struct {
	field string
	value float64
	min   float64
	max   float64
}

// Validate checks the complete document and returns every finding rather than
// stopping at the first. It performs no I/O and never mutates the receiver, so
// a candidate can be validated and then discarded.
func (s *Settings) Validate() []ValidationIssue {
	if s == nil {
		return []ValidationIssue{{
			Level:   "error",
			Code:    "settings_empty",
			Message: "video preprocessing settings cannot be empty",
		}}
	}

	var issues []ValidationIssue
	report := func(level string, code string, field string, format string, args ...any) {
		issues = append(issues, ValidationIssue{
			Level:   level,
			Code:    code,
			Field:   field,
			Message: fmt.Sprintf(format, args...),
		})
	}
	errorf := func(code string, field string, format string, args ...any) {
		report("error", code, field, format, args...)
	}

	// Feature and scope.
	if s.Mode != ModeAlwaysFrames {
		errorf("mode_invalid", "mode", "mode must be %s", ModeAlwaysFrames)
	}
	if len(s.TargetModels) == 0 {
		errorf("target_models_required", "target_models", "target_models must contain at least one model")
	}
	if len(s.AllowedMimeTypes) == 0 {
		errorf("mime_types_required", "allowed_mime_types", "allowed_mime_types must contain at least one media type")
	}
	for _, mimeType := range s.AllowedMimeTypes {
		if !strings.Contains(mimeType, "/") || strings.HasPrefix(mimeType, "/") || strings.HasSuffix(mimeType, "/") {
			errorf("mime_type_invalid", "allowed_mime_types", "allowed_mime_types entry %q is not a type/subtype media type", mimeType)
		}
	}
	if len(s.AllowedCodecs) == 0 {
		errorf("codecs_required", "allowed_codecs", "allowed_codecs must contain at least one codec")
	}

	// Per-field inclusive bounds (plan §8.2). A field outside its bound is
	// reported here; the cross-field relations follow in their own section.
	// Fields whose ceiling is another field appear only in the relation
	// section, so one mistake produces one finding.
	for _, limit := range []numericLimit{
		{"max_videos_per_request", s.MaxVideosPerRequest, 1, 16},
		{"max_decoded_video_mb", s.MaxDecodedVideoMB, 1, 1024},
		{"max_duration_seconds", s.MaxDurationSeconds, 1, 21600},
		{"max_width", s.MaxWidth, 64, 16384},
		{"max_height", s.MaxHeight, 64, 16384},
		{"max_pixels", s.MaxPixels, 4096, 268435456},
		{"max_fps", s.MaxFPS, 1, 240},
		{"probe_timeout_seconds", s.ProbeTimeoutSeconds, 1, 120},

		{"max_images_per_request", s.MaxImagesPerRequest, 1, 200},
		{"analysis_width", s.AnalysisWidth, 64, 1024},
		{"candidate_limit", s.CandidateLimit, 16, 50000},
		{"min_selected_gap_ms", s.MinSelectedGapMS, 0, 60000},

		{"jpeg_quality", s.JPEGQuality, 40, 95},
		{"max_long_side", s.MaxLongSide, 256, 4096},
		{"max_total_image_mb", s.MaxTotalImageMB, 1, 256},
		{"max_rewritten_body_mb", s.MaxRewrittenBodyMB, 1, 512},

		{"max_running_jobs", s.MaxRunningJobs, 1, 256},
		{"ffmpeg_threads_per_job", s.FFmpegThreadsPerJob, 1, 32},
		{"max_waiting_jobs", s.MaxWaitingJobs, 0, 10000},
		{"max_waiting_bytes_mb", s.MaxWaitingBytesMB, 1, 1048576},
		{"max_wait_seconds", s.MaxWaitSeconds, 1, 3600},
		{"max_job_seconds", s.MaxJobSeconds, 1, 3600},
		{"max_running_per_user", s.MaxRunningPerUser, 1, 256},
		{"max_waiting_per_user", s.MaxWaitingPerUser, 0, 10000},
		{"max_estimated_wait_seconds", s.MaxEstimatedWaitSeconds, 1, 3600},

		{"max_temp_storage_gb", s.MaxTempStorageGB, 1, 4096},
		{"stale_file_ttl_minutes", s.StaleFileTTLMinutes, 5, 1440},
		{"retention_days", s.RetentionDays, 1, 90},
	} {
		if limit.value < limit.min || limit.value > limit.max {
			errorf("value_out_of_range", limit.field, "%s must be between %d and %d", limit.field, limit.min, limit.max)
		}
	}

	// reserve_image_slots is bounded by the image budget rather than by a fixed
	// ceiling: reserving every slot would leave no room for a single frame.
	if s.ReserveImageSlots < 0 || s.ReserveImageSlots >= s.MaxImagesPerRequest {
		errorf("reserve_slots_out_of_range", "reserve_image_slots",
			"reserve_image_slots must be between 0 and max_images_per_request minus 1")
	}

	// Scan rates and 0..1 ratios. NaN and ±Inf get their own code: they compare
	// false against every bound, so a plain range check would let them through.
	for _, limit := range []floatLimit{
		{"short_scan_fps", s.ShortScanFPS, 0.1, 30},
		{"medium_scan_fps", s.MediumScanFPS, 0.1, 30},
		{"long_scan_fps", s.LongScanFPS, 0.05, 10},
		{"scene_weight", s.SceneWeight, 0, 1},
		{"motion_weight", s.MotionWeight, 0, 1},
		{"sharpness_weight", s.SharpnessWeight, 0, 1},
		{"temporal_weight", s.TemporalWeight, 0, 1},
		{"phash_duplicate_threshold", s.PhashDuplicateThreshold, 0, 1},
		{"black_frame_threshold", s.BlackFrameThreshold, 0, 1},
	} {
		if math.IsNaN(limit.value) || math.IsInf(limit.value, 0) {
			errorf("value_not_finite", limit.field, "%s must be a finite number", limit.field)
			continue
		}
		if limit.value < limit.min || limit.value > limit.max {
			errorf("value_out_of_range", limit.field, "%s must be between %g and %g", limit.field, limit.min, limit.max)
		}
	}

	// Frame budget (plan §8.4). The two frame targets must fit the image slots
	// left after the reservation, and the three frame counts must be ordered.
	// The frame ceiling compares against a clamped copy of reserve_image_slots
	// so an out-of-range reservation is reported once, above, rather than
	// distorting this relation too. When max_images_per_request is itself out of
	// range the ceiling is unknown, so only the ordering checks run.
	frameCeiling := -1
	if s.MaxImagesPerRequest >= 1 {
		reserve := min(max(s.ReserveImageSlots, 0), s.MaxImagesPerRequest-1)
		frameCeiling = s.MaxImagesPerRequest - reserve
	}
	for _, frameCount := range []numericLimit{
		{"default_max_frames", s.DefaultMaxFrames, 1, frameCeiling},
		{"high_detail_max_frames", s.HighDetailMaxFrames, 1, frameCeiling},
		{"min_frames_per_video", s.MinFramesPerVideo, 1, frameCeiling},
	} {
		if frameCount.value < frameCount.min {
			errorf("value_out_of_range", frameCount.field, "%s must be at least %d", frameCount.field, frameCount.min)
			continue
		}
		if frameCount.max > 0 && frameCount.value > frameCount.max {
			errorf("frame_budget_exceeds_image_limit", frameCount.field,
				"%s (%d) must not exceed max_images_per_request (%d) minus reserve_image_slots (%d)",
				frameCount.field, frameCount.value, s.MaxImagesPerRequest, s.ReserveImageSlots)
		}
	}
	if s.DefaultMaxFrames > s.HighDetailMaxFrames {
		errorf("frame_order_invalid", "default_max_frames", "default_max_frames must not exceed high_detail_max_frames")
	}
	if s.MinFramesPerVideo > s.DefaultMaxFrames {
		errorf("min_frames_exceeds_default", "min_frames_per_video", "min_frames_per_video must not exceed default_max_frames")
	}

	// The four scoring weights must sum to 1 so a candidate score stays
	// comparable between requests that share a frame budget.
	sum := s.SceneWeight + s.MotionWeight + s.SharpnessWeight + s.TemporalWeight
	if !math.IsNaN(sum) && !math.IsInf(sum, 0) && math.Abs(sum-1) > weightSumTolerance {
		errorf("weights_sum_invalid", "scene_weight",
			"scene_weight, motion_weight, sharpness_weight and temporal_weight must sum to 1 (got %g)", sum)
	}

	// Global and per-user limits. A per-user ceiling above the global one can
	// never be reached, which means the fairness limit silently does nothing.
	if s.MaxRunningPerUser > s.MaxRunningJobs {
		errorf("user_running_limit_exceeds_global", "max_running_per_user", "max_running_per_user must not exceed max_running_jobs")
	}
	if s.MaxWaitingJobs == 0 {
		// Queueing is disabled, so a per-user wait limit is meaningless and
		// must be zero rather than merely consistent.
		if s.MaxWaitingPerUser != 0 {
			errorf("user_waiting_limit_without_queue", "max_waiting_per_user", "max_waiting_per_user must be 0 when max_waiting_jobs is 0")
		}
	} else if s.MaxWaitingPerUser > s.MaxWaitingJobs {
		errorf("user_waiting_limit_exceeds_global", "max_waiting_per_user", "max_waiting_per_user must not exceed max_waiting_jobs")
	}

	// The rewritten body replaces the client body in the request pipeline, so it
	// must stay inside the server-wide request body limit.
	//
	// A non-positive limit means the server value has not been resolved (it is
	// read from the environment during startup, and is zero in contexts that
	// never run that step). Reporting a violation then would reject every
	// candidate — including the defaults — so the relation is checked only once
	// the limit is actually known.
	if constant.MaxRequestBodyMB > 0 && s.MaxRewrittenBodyMB > constant.MaxRequestBodyMB {
		errorf("rewritten_body_exceeds_request_limit", "max_rewritten_body_mb",
			"max_rewritten_body_mb (%d) must not exceed the server MAX_REQUEST_BODY_MB (%d)",
			s.MaxRewrittenBodyMB, constant.MaxRequestBodyMB)
	}

	// Temp storage is a business budget that must not invite the node to fill a
	// filesystem it shares with the database and logs. This is a warning, not an
	// error: free space is transient, and a node that is currently tight on disk
	// must still be able to save an unrelated correction.
	if disk := common.GetDiskSpaceInfo(); disk.Total > 0 && s.MaxTempStorageBytes() > int64(disk.Total) {
		report("warning", "temp_storage_exceeds_disk", "max_temp_storage_gb",
			"max_temp_storage_gb (%d) exceeds the %d GiB filesystem holding the temp directory",
			s.MaxTempStorageGB, disk.Total/uint64(bytesPerGB))
	}

	return issues
}

// BlockingIssues filters findings down to the ones that prevent a save.
func BlockingIssues(issues []ValidationIssue) []ValidationIssue {
	blocking := make([]ValidationIssue, 0, len(issues))
	for _, issue := range issues {
		if issue.Level == "error" {
			blocking = append(blocking, issue)
		}
	}
	return blocking
}

// HasBlockingIssue reports whether any finding blocks a save.
func HasBlockingIssue(issues []ValidationIssue) bool {
	for _, issue := range issues {
		if issue.Level == "error" {
			return true
		}
	}
	return false
}
