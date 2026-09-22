package video_preprocess_setting

import (
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireValid asserts a settings value has no blocking findings.
func requireValid(t *testing.T, settings Settings) {
	t.Helper()
	settings.Normalize()
	assert.Empty(t, BlockingIssues(settings.Validate()), "expected %+v to be valid", settings)
}

// encodeSettings renders a settings value the way Save persists it.
func encodeSettings(t *testing.T, settings Settings, version int64) string {
	t.Helper()
	encoded, err := Encode(settings, version)
	require.NoError(t, err)
	return encoded
}

// encodeSettingsObject renders just the settings object, the shape the admin
// API receives from the Web client.
func encodeSettingsObject(t *testing.T, settings Settings) common.RawMessage {
	t.Helper()
	encoded, err := common.Marshal(settings)
	require.NoError(t, err)
	return encoded
}

// recordingPersist returns a writer that captures the stored document.
func recordingPersist(stored *string) PersistFunc {
	return func(value string) error {
		*stored = value
		return nil
	}
}

// hasCode reports whether any finding carries the given code.
func hasCode(issues []ValidationIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

// settingsWithModel builds a valid settings value with the given target model.
func settingsWithModel(model string) Settings {
	settings := DefaultSettings()
	settings.TargetModels = []string{model}
	settings.Normalize()
	return settings
}

func TestDefaultSettingsAreValid(t *testing.T) {
	settings := DefaultSettings()
	assert.False(t, settings.Enabled)
	assert.Equal(t, ModeAlwaysFrames, settings.Mode)
	assert.Equal(t, []string{"kimi-k3"}, settings.TargetModels)
	assert.Equal(t, 60, settings.MaxImagesPerRequest)
	assert.Equal(t, 32, settings.DefaultMaxFrames)
	assert.Equal(t, 56, settings.HighDetailMaxFrames)
	assert.Equal(t, 16, settings.MaxRunningJobs)
	assert.Equal(t, 180, settings.MaxJobSeconds)
	assert.True(t, settings.TelemetryEnabled)
	assert.Equal(t, 7, settings.RetentionDays)
	assert.False(t, settings.RemoteURLEnabled)
	assert.False(t, settings.RemoteURLAllowlistEnabled)
	assert.Empty(t, settings.RemoteURLAllowedHosts)
	assert.True(t, settings.RemoteURLAllowUnknownSize)
	assert.Equal(t, 120, settings.RemoteURLDownloadTimeoutSeconds)
	assert.Equal(t, 8, settings.RemoteURLMaxDownloads)
	requireValid(t, settings)
}

// TestDefaultByteConversions guards the MB/GB to byte conversions the media
// engine compares against; a wrong factor silently changes every limit.
func TestDefaultByteConversions(t *testing.T) {
	settings := DefaultSettings()
	assert.Equal(t, int64(20)*1024*1024, settings.MaxTotalImageBytes())
	assert.Equal(t, int64(32)*1024*1024, settings.MaxRewrittenBodyBytes())
	assert.Equal(t, int64(75)*1024*1024, settings.MaxDecodedVideoBytes())
	assert.Equal(t, int64(61440)*1024*1024, settings.MaxWaitingBytes())
	assert.Equal(t, int64(100)*1024*1024*1024, settings.MaxTempStorageBytes())
}

func TestNormalizeCanonicalizesEntries(t *testing.T) {
	settings := DefaultSettings()
	settings.TargetModels = []string{" kimi-k3 ", "", "kimi-k3", "gpt-4o"}
	settings.AllowedMimeTypes = []string{"MP4", " video/webm ", "video/mp4", ""}
	settings.AllowedCodecs = []string{" H264 ", "h264", "HEVC"}
	settings.Normalize()

	assert.Equal(t, []string{"kimi-k3", "gpt-4o"}, settings.TargetModels)
	assert.Equal(t, []string{"video/mp4", "video/webm"}, settings.AllowedMimeTypes)
	assert.Equal(t, []string{"h264", "hevc"}, settings.AllowedCodecs)
	// Target models are opaque upstream identifiers; their case is preserved.
	assert.True(t, settings.IsTargetModel("kimi-k3"))
	assert.False(t, settings.IsTargetModel("KIMI-K3"))
}

func TestValidationRejectsEmptyCollections(t *testing.T) {
	settings := DefaultSettings()
	settings.TargetModels = []string{"", "  "}
	settings.AllowedMimeTypes = nil
	settings.AllowedCodecs = []string{}
	settings.Normalize()

	issues := BlockingIssues(settings.Validate())
	require.NotEmpty(t, issues)
	assert.True(t, hasCode(issues, "target_models_required"))
	assert.True(t, hasCode(issues, "mime_types_required"))
	assert.True(t, hasCode(issues, "codecs_required"))
}

func TestRemoteURLHostNormalization(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{" Media.Example.COM. ", "media.example.com"},
		{"媒体.example.com", "xn--tqqy7y.example.com"},
		{"pub-ef3013c53fb4493f8d5fd64a42eaf234.r2.dev", "pub-ef3013c53fb4493f8d5fd64a42eaf234.r2.dev"},
		{"video.example.test", "video.example.test"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := NormalizeRemoteHost(tc.input)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	for _, input := range []string{
		"", "localhost", "metadata.google.internal", "video.local", "video.lan", "video.home",
		"https://media.example.com", "*.r2.dev", "media.example.com:443", "name@media.example.com",
		"media.example.com/path", "media.example.com?token=secret", "media.example.com#fragment",
		"127.0.0.1", "[::1]", "::ffff:127.0.0.1", "media..example.com", "-media.example.com",
		"media-.example.com", "media_name.example.com", "media.example.com..", "media.example.com%00",
		strings.Repeat("a", 64) + ".example.com", strings.Repeat("a.", 127) + "com",
	} {
		t.Run(input, func(t *testing.T) {
			_, err := NormalizeRemoteHost(input)
			require.Error(t, err)
		})
	}
}

func TestRemoteURLSettingsPolicy(t *testing.T) {
	settings := DefaultSettings()
	settings.RemoteURLEnabled = true
	// An operator can enable remote downloads without a domain allowlist.
	requireValid(t, settings)

	settings.RemoteURLAllowlistEnabled = true
	assert.True(t, hasCode(settings.Validate(), "remote_url_hosts_required"))
	settings.RemoteURLAllowedHosts = []string{" Media.Example.COM. ", "media.example.com", "", "媒体.example.com"}
	settings.Normalize()
	requireValid(t, settings)
	assert.Equal(t, []string{"media.example.com", "xn--tqqy7y.example.com"}, settings.RemoteURLAllowedHosts)

	settings.RemoteURLAllowedHosts = []string{"https://media.example.com/private?signature=secret"}
	settings.Normalize()
	issues := BlockingIssues(settings.Validate())
	require.Len(t, issues, 1)
	assert.Equal(t, "remote_host_invalid", issues[0].Code)
	assert.Equal(t, "remote_url_allowed_hosts", issues[0].Field)
	assert.NotContains(t, issues[0].Message, "secret", "validation must not echo a pasted signed URL")

	settings = DefaultSettings()
	settings.MaxRunningJobs = 1
	settings.MaxRunningPerUser = 1
	// The runtime takes the lower concurrency ceiling; the independent download
	// setting must not prevent a legacy small-job configuration from loading.
	requireValid(t, settings)
}

func TestFieldBounds(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Settings)
	}{
		{name: "remote timeout zero", mutate: func(s *Settings) { s.RemoteURLDownloadTimeoutSeconds = 0 }},
		{name: "remote timeout too high", mutate: func(s *Settings) { s.RemoteURLDownloadTimeoutSeconds = 601 }},
		{name: "remote downloads zero", mutate: func(s *Settings) { s.RemoteURLMaxDownloads = 0 }},
		{name: "remote downloads too high", mutate: func(s *Settings) { s.RemoteURLMaxDownloads = 129 }},
		{name: "max_videos_per_request too low", mutate: func(s *Settings) { s.MaxVideosPerRequest = 0 }},
		{name: "max_videos_per_request too high", mutate: func(s *Settings) { s.MaxVideosPerRequest = 17 }},
		{name: "max_decoded_video_mb too high", mutate: func(s *Settings) { s.MaxDecodedVideoMB = 1025 }},
		{name: "max_duration_seconds too high", mutate: func(s *Settings) { s.MaxDurationSeconds = 21601 }},
		{name: "max_width too low", mutate: func(s *Settings) { s.MaxWidth = 63 }},
		{name: "max_height too high", mutate: func(s *Settings) { s.MaxHeight = 16385 }},
		{name: "max_pixels too low", mutate: func(s *Settings) { s.MaxPixels = 4095 }},
		{name: "max_fps too high", mutate: func(s *Settings) { s.MaxFPS = 241 }},
		{name: "probe_timeout_seconds zero", mutate: func(s *Settings) { s.ProbeTimeoutSeconds = 0 }},
		{name: "max_images_per_request too high", mutate: func(s *Settings) { s.MaxImagesPerRequest = 201 }},
		{name: "analysis_width too low", mutate: func(s *Settings) { s.AnalysisWidth = 63 }},
		{name: "candidate_limit too low", mutate: func(s *Settings) { s.CandidateLimit = 15 }},
		{name: "min_selected_gap_ms negative", mutate: func(s *Settings) { s.MinSelectedGapMS = -1 }},
		{name: "jpeg_quality too low", mutate: func(s *Settings) { s.JPEGQuality = 39 }},
		{name: "jpeg_quality too high", mutate: func(s *Settings) { s.JPEGQuality = 96 }},
		{name: "max_long_side too low", mutate: func(s *Settings) { s.MaxLongSide = 255 }},
		{name: "max_total_image_mb too high", mutate: func(s *Settings) { s.MaxTotalImageMB = 257 }},
		{name: "max_running_jobs zero", mutate: func(s *Settings) { s.MaxRunningJobs = 0 }},
		{name: "ffmpeg_threads_per_job too high", mutate: func(s *Settings) { s.FFmpegThreadsPerJob = 33 }},
		{name: "max_waiting_jobs negative", mutate: func(s *Settings) { s.MaxWaitingJobs = -1 }},
		{name: "max_waiting_bytes_mb zero", mutate: func(s *Settings) { s.MaxWaitingBytesMB = 0 }},
		{name: "max_wait_seconds too high", mutate: func(s *Settings) { s.MaxWaitSeconds = 3601 }},
		{name: "max_job_seconds zero", mutate: func(s *Settings) { s.MaxJobSeconds = 0 }},
		{name: "max_running_per_user zero", mutate: func(s *Settings) { s.MaxRunningPerUser = 0 }},
		{name: "max_estimated_wait_seconds too high", mutate: func(s *Settings) { s.MaxEstimatedWaitSeconds = 3601 }},
		{name: "max_temp_storage_gb too high", mutate: func(s *Settings) { s.MaxTempStorageGB = 4097 }},
		{name: "stale_file_ttl_minutes too low", mutate: func(s *Settings) { s.StaleFileTTLMinutes = 4 }},
		{name: "retention_days too low", mutate: func(s *Settings) { s.RetentionDays = 0 }},
		{name: "retention_days too high", mutate: func(s *Settings) { s.RetentionDays = 91 }},
		{name: "mode not in enum", mutate: func(s *Settings) { s.Mode = "adaptive" }},
		{name: "mime type without subtype", mutate: func(s *Settings) { s.AllowedMimeTypes = []string{"video/"} }},
		{name: "scan fps out of range", mutate: func(s *Settings) { s.ShortScanFPS = 0.05 }},
		{name: "black frame threshold above one", mutate: func(s *Settings) { s.BlackFrameThreshold = 1.5 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := DefaultSettings()
			test.mutate(&settings)
			settings.Normalize()

			issues := BlockingIssues(settings.Validate())
			require.NotEmpty(t, issues, "expected a blocking issue")
			// Each finding must name its field and carry a code, so the Web form
			// can attach the message to the right input.
			for _, issue := range issues {
				assert.NotEmpty(t, issue.Field)
				assert.NotEmpty(t, issue.Code)
			}
		})
	}
}

// TestFrameBudgetRelations covers the plan's frame-count relation rules,
// including the interaction between the image budget and the reserved slots.
func TestFrameBudgetRelations(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Settings)
		wantErr bool
	}{
		{
			name:    "default above high detail",
			mutate:  func(s *Settings) { s.DefaultMaxFrames = 40; s.HighDetailMaxFrames = 30 },
			wantErr: true,
		},
		{
			name:    "min frames above default",
			mutate:  func(s *Settings) { s.MinFramesPerVideo = 33 },
			wantErr: true,
		},
		{
			name:    "default frames exceed image budget",
			mutate:  func(s *Settings) { s.MaxImagesPerRequest = 20; s.DefaultMaxFrames = 32 },
			wantErr: true,
		},
		{
			name:    "high detail frames exceed image budget",
			mutate:  func(s *Settings) { s.MaxImagesPerRequest = 40; s.HighDetailMaxFrames = 56 },
			wantErr: true,
		},
		{
			name: "reserve consumes the room the frames need",
			mutate: func(s *Settings) {
				s.MaxImagesPerRequest = 40
				s.ReserveImageSlots = 20
				s.DefaultMaxFrames = 24
				s.HighDetailMaxFrames = 24
				s.MinFramesPerVideo = 4
			},
			wantErr: true,
		},
		{
			name: "frames fit exactly after the reservation",
			mutate: func(s *Settings) {
				s.MaxImagesPerRequest = 40
				s.ReserveImageSlots = 8
				s.DefaultMaxFrames = 32
				s.HighDetailMaxFrames = 32
				s.MinFramesPerVideo = 4
			},
		},
		{
			name:    "reserve must leave one slot",
			mutate:  func(s *Settings) { s.MaxImagesPerRequest = 60; s.ReserveImageSlots = 60 },
			wantErr: true,
		},
		{
			name: "reserve at the last usable slot",
			mutate: func(s *Settings) {
				s.ReserveImageSlots = 59
				s.DefaultMaxFrames = 1
				s.HighDetailMaxFrames = 1
				s.MinFramesPerVideo = 1
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := DefaultSettings()
			test.mutate(&settings)
			settings.Normalize()

			blocking := BlockingIssues(settings.Validate())
			if test.wantErr {
				assert.NotEmpty(t, blocking)
				return
			}
			assert.Empty(t, blocking)
		})
	}
}

// TestUserLimitsAgainstGlobalLimits checks that a per-user ceiling can never
// exceed the global one, and that disabling the queue forces the per-user wait
// limit to zero.
func TestUserLimitsAgainstGlobalLimits(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Settings)
		wantErr bool
	}{
		{name: "running per user above global", mutate: func(s *Settings) { s.MaxRunningPerUser = 17 }, wantErr: true},
		{name: "running per user equal to global", mutate: func(s *Settings) { s.MaxRunningPerUser = 16 }},
		{name: "waiting per user above global", mutate: func(s *Settings) { s.MaxWaitingPerUser = 501 }, wantErr: true},
		{name: "waiting per user equal to global", mutate: func(s *Settings) { s.MaxWaitingPerUser = 500 }},
		{
			name:    "queue disabled with a per-user wait limit",
			mutate:  func(s *Settings) { s.MaxWaitingJobs = 0; s.MaxWaitingPerUser = 1 },
			wantErr: true,
		},
		{
			name:   "queue disabled with both limits cleared",
			mutate: func(s *Settings) { s.MaxWaitingJobs = 0; s.MaxWaitingPerUser = 0 },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := DefaultSettings()
			test.mutate(&settings)
			settings.Normalize()

			blocking := BlockingIssues(settings.Validate())
			if test.wantErr {
				assert.NotEmpty(t, blocking)
				return
			}
			assert.Empty(t, blocking)
		})
	}
}

// TestNonFiniteValuesAreRejected guards against NaN and ±Inf, which compare
// false against every bound and would otherwise pass a range check.
func TestNonFiniteValuesAreRejected(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Settings)
	}{
		{name: "scene weight NaN", mutate: func(s *Settings) { s.SceneWeight = math.NaN() }},
		{name: "motion weight Inf", mutate: func(s *Settings) { s.MotionWeight = math.Inf(1) }},
		{name: "sharpness weight negative Inf", mutate: func(s *Settings) { s.SharpnessWeight = math.Inf(-1) }},
		{name: "temporal weight NaN", mutate: func(s *Settings) { s.TemporalWeight = math.NaN() }},
		{name: "short scan fps NaN", mutate: func(s *Settings) { s.ShortScanFPS = math.NaN() }},
		{name: "black frame threshold Inf", mutate: func(s *Settings) { s.BlackFrameThreshold = math.Inf(1) }},
		{name: "phash threshold NaN", mutate: func(s *Settings) { s.PhashDuplicateThreshold = math.NaN() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := DefaultSettings()
			test.mutate(&settings)
			settings.Normalize()

			issues := BlockingIssues(settings.Validate())
			require.NotEmpty(t, issues)
			assert.True(t, hasCode(issues, "value_not_finite"), "expected a value_not_finite finding, got %+v", issues)
		})
	}
}

// TestScoreWeightsMustSumToOne covers the weighting rule, including a weight
// pushed out of its own 0..1 bound.
func TestScoreWeightsMustSumToOne(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Settings)
		wantErr bool
	}{
		{name: "defaults sum to one"},
		{name: "rebalanced but still one", mutate: func(s *Settings) {
			s.SceneWeight, s.MotionWeight, s.SharpnessWeight, s.TemporalWeight = 0.25, 0.25, 0.25, 0.25
		}},
		{name: "sum below one", mutate: func(s *Settings) { s.SceneWeight = 0.10 }, wantErr: true},
		{name: "sum above one", mutate: func(s *Settings) { s.TemporalWeight = 0.50 }, wantErr: true},
		{name: "weight outside zero to one", mutate: func(s *Settings) {
			s.SceneWeight, s.MotionWeight, s.SharpnessWeight, s.TemporalWeight = 1.50, 0.20, 0.15, 0.25
		}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := DefaultSettings()
			if test.mutate != nil {
				test.mutate(&settings)
			}
			settings.Normalize()

			blocking := BlockingIssues(settings.Validate())
			if test.wantErr {
				assert.NotEmpty(t, blocking)
				return
			}
			assert.Empty(t, blocking)
		})
	}
}

// TestRewrittenBodyMustFitRequestLimit pins the relation between the rewritten
// body budget and the server-wide request body limit. The boundary itself is
// the accepted case.
func TestRewrittenBodyMustFitRequestLimit(t *testing.T) {
	original := constant.MaxRequestBodyMB
	t.Cleanup(func() { constant.MaxRequestBodyMB = original })

	constant.MaxRequestBodyMB = 64
	settings := DefaultSettings()

	settings.MaxRewrittenBodyMB = 64
	requireValid(t, settings)

	settings.MaxRewrittenBodyMB = 65
	settings.Normalize()
	issues := BlockingIssues(settings.Validate())
	require.NotEmpty(t, issues)
	assert.True(t, hasCode(issues, "rewritten_body_exceeds_request_limit"), "got %+v", issues)
}

// TestValidateDoesNotMutateOrPublish checks that validating a candidate has no
// side effects: neither the receiver nor the published snapshot changes.
func TestValidateDoesNotMutateOrPublish(t *testing.T) {
	resetStore(t)
	before := GetSnapshot()

	candidate := DefaultSettings()
	candidate.TargetModels = []string{" gpt-4o ", "", "gpt-4o"}
	candidate.MaxRunningJobs = 999

	issues := ValidateCandidate(candidate)
	require.NotEmpty(t, BlockingIssues(issues))
	// Validation does not normalize in place; the caller's value is untouched.
	assert.Equal(t, []string{" gpt-4o ", "", "gpt-4o"}, candidate.TargetModels)
	assert.Equal(t, before, GetSnapshot())
}

// TestFailedValidationDoesNotReserveVersion proves a rejected candidate does
// not consume a version number, so the next successful save is still the one
// the client expects.
func TestFailedValidationDoesNotReserveVersion(t *testing.T) {
	resetStore(t)

	var stored string
	persist := recordingPersist(&stored)
	require.NoError(t, func() error {
		_, err := Save(DefaultSettings(), 0, persist)
		return err
	}())

	invalid := DefaultSettings()
	invalid.JPEGQuality = 200
	_, err := Save(invalid, 1, persist)
	require.Error(t, err)

	snapshot, err := Save(DefaultSettings(), 1, persist)
	require.NoError(t, err)
	assert.Equal(t, int64(2), snapshot.Version)
}

func TestSavePersistsAndPublishesWholeDocument(t *testing.T) {
	resetStore(t)

	var stored string
	candidate := DefaultSettings()
	candidate.Enabled = true
	candidate.TargetModels = []string{" kimi-k3 ", "", "gpt-4o"}
	candidate.MaxRunningJobs = 24
	candidate.MaxRunningPerUser = 4
	candidate.RetentionDays = 30
	candidate.ShortScanFPS = 1.25
	candidate.MediumScanFPS = 1.375
	candidate.LongScanFPS = 0.125
	candidate.SceneWeight = 0.405
	candidate.MotionWeight = 0.195
	candidate.SharpnessWeight = 0.155
	candidate.TemporalWeight = 0.245
	candidate.PhashDuplicateThreshold = 0.085
	candidate.BlackFrameThreshold = 0.905

	snapshot, err := Save(candidate, 0, recordingPersist(&stored))
	require.NoError(t, err)

	assert.Equal(t, int64(1), snapshot.Version)
	assert.True(t, snapshot.Settings.Enabled)
	assert.Equal(t, []string{"kimi-k3", "gpt-4o"}, snapshot.Settings.TargetModels)
	assert.Equal(t, 24, snapshot.Settings.MaxRunningJobs)

	// The stored document is the generation the caller received, and it
	// round-trips back to an identical snapshot.
	decoded, err := Decode(stored)
	require.NoError(t, err)
	assert.Equal(t, snapshot, decoded)
	assert.Equal(t, snapshot, GetSnapshot())
}

func TestDelayedSaveCacheRefreshUsesPublishedGeneration(t *testing.T) {
	resetStore(t)
	common.OptionMapRWMutex.Lock()
	previousMap := common.OptionMap
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousMap
		common.OptionMapRWMutex.Unlock()
	})

	var stored string
	// Pause the first save between its persist/publish and cache phases without
	// relying on scheduler timing. A second complete save overtakes its cache.
	first, err := saveExclusive(settingsWithModel("first"), 0, recordingPersist(&stored))
	require.NoError(t, err)
	second, err := Save(settingsWithModel("second"), first.Version, recordingPersist(&stored))
	require.NoError(t, err)
	require.NoError(t, cacheDocument())
	common.OptionMapRWMutex.RLock()
	cached := common.OptionMap[SettingsOptionKey]
	common.OptionMapRWMutex.RUnlock()
	decoded, err := Decode(cached)
	require.NoError(t, err)
	assert.Equal(t, second, decoded)
	assert.Equal(t, second, GetSnapshot())
	assert.Equal(t, stored, cached)
}

// TestFailedSaveLeavesNothingBehind covers the two rejection paths that must not
// produce a partial write: a candidate with a blocking finding, and a writer
// that fails. In both cases storage and the snapshot stay as they were.
func TestFailedSaveLeavesNothingBehind(t *testing.T) {
	resetStore(t)

	var stored string
	persist := recordingPersist(&stored)
	require.NoError(t, func() error {
		_, err := Save(DefaultSettings(), 0, persist)
		return err
	}())
	before := GetSnapshot()
	require.Equal(t, int64(1), before.Version)

	t.Run("validation failure writes nothing", func(t *testing.T) {
		invalid := DefaultSettings()
		invalid.JPEGQuality = 200
		invalid.TargetModels = []string{"changed"}

		_, err := Save(invalid, 1, persist)
		require.Error(t, err)
		var issuesErr *ValidationIssuesError
		require.ErrorAs(t, err, &issuesErr)
		assert.True(t, errors.Is(err, ErrValidationFailed))
		assert.NotEmpty(t, issuesErr.Issues)

		assert.Equal(t, before, GetSnapshot())
		assert.Equal(t, encodeSettings(t, before.Settings, 1), stored)
	})

	t.Run("persistence failure writes nothing", func(t *testing.T) {
		failing := func(string) error { return errors.New("disk unavailable") }
		_, err := Save(DefaultSettings(), 1, failing)
		require.ErrorContains(t, err, "disk unavailable")

		assert.Equal(t, before, GetSnapshot())
		assert.Equal(t, encodeSettings(t, before.Settings, 1), stored)
	})
}

// TestVersionConflict covers optimistic concurrency: a stale version is
// refused, and the refusal changes nothing.
func TestVersionConflict(t *testing.T) {
	resetStore(t)

	var stored string
	persist := recordingPersist(&stored)
	_, err := Save(DefaultSettings(), 0, persist)
	require.NoError(t, err)

	// Two administrators both read version 1; the first save wins.
	_, err = Save(DefaultSettings(), 1, persist)
	require.NoError(t, err)

	// The second is refused and must not have advanced anything.
	current := GetSnapshot()
	storedBefore := stored
	_, err = Save(DefaultSettings(), 1, persist)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrVersionConflict))
	assert.Equal(t, current, GetSnapshot())
	assert.Equal(t, storedBefore, stored)

	// Re-reading and retrying with the current version succeeds.
	_, err = Save(DefaultSettings(), current.Version, persist)
	require.NoError(t, err)
}

// TestConcurrentSavesOnlyOneWinsPerVersion proves the version check is
// race-free: of many saves quoting the same version, exactly one commits.
func TestConcurrentSavesOnlyOneWinsPerVersion(t *testing.T) {
	resetStore(t)

	persist := func(string) error { return nil }
	_, err := Save(DefaultSettings(), 0, persist)
	require.NoError(t, err)

	const attempts = 16
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
		conflicts int
	)
	for range attempts {
		wg.Go(func() {
			_, err := Save(DefaultSettings(), 1, persist)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
				return
			}
			if errors.Is(err, ErrVersionConflict) {
				conflicts++
			}
		})
	}
	wg.Wait()

	assert.Equal(t, 1, successes)
	assert.Equal(t, attempts-1, conflicts)
	assert.Equal(t, int64(2), GetSnapshot().Version)
}

// TestSnapshotIsolation covers the immutable-snapshot guarantee: a caller that
// mutates what it received cannot affect the store or another caller.
func TestSnapshotIsolation(t *testing.T) {
	resetStore(t)

	var stored string
	persist := recordingPersist(&stored)
	settings := settingsWithModel("first")
	settings.RemoteURLAllowedHosts = []string{"media.example.com"}
	_, err := Save(settings, 0, persist)
	require.NoError(t, err)

	first := GetSettings()
	require.Equal(t, []string{"first"}, first.TargetModels)

	// Mutating slices and scalars on a handed-out value is harmless.
	first.TargetModels[0] = "mutated"
	first.TargetModels = append(first.TargetModels, "extra")
	first.Enabled = true
	first.MaxRunningJobs = 1
	first.RemoteURLAllowedHosts[0] = "changed.example.com"

	assert.Equal(t, []string{"first"}, GetSettings().TargetModels)
	assert.False(t, GetSettings().Enabled)
	assert.Equal(t, DefaultSettings().MaxRunningJobs, GetSettings().MaxRunningJobs)
	assert.Equal(t, []string{"media.example.com"}, GetSettings().RemoteURLAllowedHosts)

	// A later save does not reach into a snapshot a caller already holds.
	held := GetSettings()
	_, err = Save(settingsWithModel("second"), GetSnapshot().Version, persist)
	require.NoError(t, err)
	assert.Equal(t, []string{"first"}, held.TargetModels)
	assert.Equal(t, []string{"second"}, GetSettings().TargetModels)

	// The same holds for a snapshot read before any save at all.
	snapshot := GetSnapshot()
	snapshot.Settings.AllowedCodecs[0] = "tampered"
	assert.Equal(t, "h264", GetSettings().AllowedCodecs[0])
}

// TestPublishFromOption covers the startup and periodic-reload path, including
// a stored document this build can no longer accept.
func TestPublishFromOption(t *testing.T) {
	resetStore(t)

	t.Run("installs a stored document", func(t *testing.T) {
		stored := DefaultSettings()
		stored.Enabled = true
		stored.RetentionDays = 14
		require.NoError(t, PublishFromOption(encodeSettings(t, stored, 7)))

		snapshot := GetSnapshot()
		assert.Equal(t, int64(7), snapshot.Version)
		assert.True(t, snapshot.Settings.Enabled)
		assert.Equal(t, 14, snapshot.Settings.RetentionDays)
	})

	t.Run("a reload of the live document is a no-op", func(t *testing.T) {
		live := GetSnapshot()
		require.NoError(t, PublishFromOption(encodeSettings(t, live.Settings, live.Version)))
		assert.Equal(t, live, GetSnapshot())
	})

	t.Run("a generation cannot be replaced with different settings", func(t *testing.T) {
		live := GetSnapshot()
		conflicting := live.Settings
		conflicting.Enabled = !conflicting.Enabled
		require.NoError(t, PublishFromOption(encodeSettings(t, conflicting, live.Version)))
		assert.Equal(t, live, GetSnapshot())
	})

	t.Run("an unacceptable document leaves the snapshot untouched", func(t *testing.T) {
		before := GetSnapshot()
		invalid := DefaultSettings()
		invalid.MaxRunningPerUser = 99

		err := PublishFromOption(encodeSettings(t, invalid, before.Version+1))
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrValidationFailed))
		assert.Equal(t, before, GetSnapshot())
	})

	t.Run("a corrupt document leaves the snapshot untouched", func(t *testing.T) {
		before := GetSnapshot()
		require.Error(t, PublishFromOption("{not json"))
		require.Error(t, PublishFromOption(`{"version":1}`))
		assert.Equal(t, before, GetSnapshot())
	})
}

// TestDecodeRejectsUnknownAndMissingFields covers the document contract: a
// stored document may carry a subset of the schema, a candidate may not, and
// neither may carry a key the schema does not define.
func TestDecodeRejectsUnknownAndMissingFields(t *testing.T) {
	t.Run("a complete document decodes", func(t *testing.T) {
		snapshot, err := Decode(encodeSettings(t, DefaultSettings(), 3))
		require.NoError(t, err)
		assert.Equal(t, int64(3), snapshot.Version)
		assert.Equal(t, DefaultSettings(), snapshot.Settings)
	})

	t.Run("a stored subset falls back to defaults", func(t *testing.T) {
		snapshot, err := Decode(`{"version":2,"settings":{"enabled":true,"max_job_seconds":300}}`)
		require.NoError(t, err)
		assert.True(t, snapshot.Settings.Enabled)
		assert.Equal(t, 300, snapshot.Settings.MaxJobSeconds)
		// Fields absent from the document keep their default rather than
		// collapsing to a zero value.
		assert.Equal(t, DefaultSettings().MaxImagesPerRequest, snapshot.Settings.MaxImagesPerRequest)
		assert.Equal(t, DefaultSettings().TargetModels, snapshot.Settings.TargetModels)
		assert.False(t, snapshot.Settings.RemoteURLEnabled)
		assert.False(t, snapshot.Settings.RemoteURLAllowlistEnabled)
		assert.Equal(t, []string{}, snapshot.Settings.RemoteURLAllowedHosts)
		assert.True(t, snapshot.Settings.RemoteURLAllowUnknownSize)
		assert.Equal(t, 120, snapshot.Settings.RemoteURLDownloadTimeoutSeconds)
		assert.Equal(t, 8, snapshot.Settings.RemoteURLMaxDownloads)
	})

	t.Run("explicit remote settings survive storage", func(t *testing.T) {
		settings := DefaultSettings()
		settings.RemoteURLEnabled = true
		settings.RemoteURLAllowlistEnabled = true
		settings.RemoteURLAllowedHosts = []string{"media.example.com"}
		settings.RemoteURLAllowUnknownSize = false
		settings.RemoteURLDownloadTimeoutSeconds = 300
		settings.RemoteURLMaxDownloads = 32
		snapshot, err := Decode(encodeSettings(t, settings, 11))
		require.NoError(t, err)
		assert.Equal(t, settings, snapshot.Settings)
	})

	t.Run("candidate must include new remote fields", func(t *testing.T) {
		var fields map[string]common.RawMessage
		require.NoError(t, common.Unmarshal(encodeSettingsObject(t, DefaultSettings()), &fields))
		delete(fields, "remote_url_enabled")
		raw, err := common.Marshal(fields)
		require.NoError(t, err)
		_, err = DecodeSettings(raw)
		require.ErrorIs(t, err, ErrMissingField)
		assert.Contains(t, err.Error(), "remote_url_enabled")
	})

	t.Run("an unknown top-level key is rejected", func(t *testing.T) {
		_, err := Decode(`{"version":1,"settings":{},"extra":1}`)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrUnknownField))
	})

	t.Run("an unknown settings key is rejected", func(t *testing.T) {
		_, err := Decode(`{"version":1,"settings":{"unknown_key":1}}`)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrUnknownField))
	})

	t.Run("a candidate missing a field is rejected", func(t *testing.T) {
		_, err := DecodeSettings(common.RawMessage(`{"enabled":true}`))
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrMissingField))
		assert.Contains(t, err.Error(), "settings.")
	})

	t.Run("a candidate with an unknown field is rejected", func(t *testing.T) {
		_, err := DecodeSettings(common.RawMessage(`{"enabled":true,"jpeg_qualty":82}`))
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrUnknownField))
	})

	t.Run("a complete candidate decodes to the same value", func(t *testing.T) {
		settings, err := DecodeSettings(encodeSettingsObject(t, DefaultSettings()))
		require.NoError(t, err)
		assert.Equal(t, DefaultSettings(), settings)
	})

	// A malformed member must be rejected rather than read as "nothing set":
	// decoding null or an array into a map succeeds with an empty result.
	for _, malformed := range []struct {
		name  string
		value string
	}{
		{name: "document is not an object", value: `[1,2,3]`},
		{name: "document is null", value: `null`},
		{name: "settings is null", value: `{"version":1,"settings":null}`},
		{name: "settings is an array", value: `{"version":1,"settings":[]}`},
		{name: "settings is a string", value: `{"version":1,"settings":"nope"}`},
		{name: "version is missing", value: `{"settings":{}}`},
		{name: "version is a string", value: `{"version":"1","settings":{}}`},
	} {
		t.Run(malformed.name, func(t *testing.T) {
			_, err := Decode(malformed.value)
			require.Error(t, err)
			// A malformed document must never be reported as a syntactically
			// fine candidate that merely failed validation.
			assert.NotErrorIs(t, err, ErrValidationFailed)
		})
	}

	for _, malformed := range []string{`null`, `[]`, `"nope"`, `1`, ``, `{}`} {
		t.Run("candidate "+malformed, func(t *testing.T) {
			_, err := DecodeSettings(common.RawMessage(malformed))
			require.Error(t, err)
		})
	}
}

// TestValidateOptionOnlyClaimsItsOwnNamespace covers the Option-table
// integration: keys belonging to other modules are accepted untouched.
func TestValidateOptionOnlyClaimsItsOwnNamespace(t *testing.T) {
	assert.NoError(t, ValidateOption("ai_router.max_attempts", "3"))
	assert.NoError(t, ValidateOption("MaxTokenAutoGroups", "not-a-number"))
	assert.NoError(t, ValidateOption(SettingsOptionKey, encodeSettings(t, DefaultSettings(), 1)))

	invalid := DefaultSettings()
	invalid.MaxLongSide = 1
	err := ValidateOption(SettingsOptionKey, encodeSettings(t, invalid, 1))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrValidationFailed))
}

// TestSettingsJSONIsStable pins the field names the Web client and the stored
// document depend on, so a rename cannot pass unnoticed. Every field is
// checked in both directions, which also catches a field added without a JSON
// tag (it would be dropped from both decoding paths).
func TestSettingsJSONIsStable(t *testing.T) {
	encoded, err := common.Marshal(DefaultSettings())
	require.NoError(t, err)

	var fields map[string]common.RawMessage
	require.NoError(t, common.Unmarshal(encoded, &fields))

	for _, key := range []string{
		"enabled", "mode", "target_models", "allowed_mime_types", "allowed_codecs",
		"max_videos_per_request", "max_decoded_video_mb", "max_duration_seconds",
		"max_width", "max_height", "max_pixels", "max_fps", "probe_timeout_seconds",
		"max_images_per_request", "default_max_frames", "high_detail_max_frames",
		"min_frames_per_video", "reserve_image_slots", "analysis_width",
		"candidate_limit", "short_scan_fps", "medium_scan_fps", "long_scan_fps",
		"min_selected_gap_ms", "scene_weight", "motion_weight", "sharpness_weight",
		"temporal_weight", "phash_duplicate_threshold", "black_frame_threshold",
		"jpeg_quality", "max_long_side", "max_total_image_mb",
		"max_rewritten_body_mb", "include_timestamps",
		"max_running_jobs", "ffmpeg_threads_per_job", "max_waiting_jobs",
		"max_waiting_bytes_mb", "max_wait_seconds", "max_job_seconds",
		"max_running_per_user", "max_waiting_per_user", "max_estimated_wait_seconds",
		"max_temp_storage_gb", "stale_file_ttl_minutes", "telemetry_enabled",
		"retention_days", "record_per_video_details",
	} {
		assert.Contains(t, fields, key)
		assert.Contains(t, settingsKeys, key)
	}
	assert.Len(t, fields, len(settingsKeys))
}

// TestIsEnabledForModel covers the hot-path predicate the relay middleware
// calls: it must require both the master switch and a target-model match.
func TestIsEnabledForModel(t *testing.T) {
	resetStore(t)
	assert.False(t, IsEnabledForModel("kimi-k3"), "disabled by default")

	resetStore(t)
	var stored string
	enabled := DefaultSettings()
	enabled.Enabled = true
	enabled.TargetModels = []string{"kimi-k3"}
	_, err := Save(enabled, 0, recordingPersist(&stored))
	require.NoError(t, err)
	assert.True(t, IsEnabledForModel("kimi-k3"))
	assert.False(t, IsEnabledForModel("gpt-4o"))

	resetStore(t)
	disabled := settingsWithModel("kimi-k3")
	_, err = Save(disabled, 0, recordingPersist(&stored))
	require.NoError(t, err)
	assert.False(t, IsEnabledForModel("kimi-k3"))
}

// TestAllowedMediaLists covers the allowlist lookups the extractor will use.
func TestAllowedMediaLists(t *testing.T) {
	settings := DefaultSettings()
	assert.True(t, settings.AllowsMimeType("video/mp4"))
	assert.True(t, settings.AllowsCodec("h264"))
	assert.False(t, settings.AllowsMimeType("video/avi"))
	assert.False(t, settings.AllowsCodec("mpeg4"))

	settings.AllowedMimeTypes = []string{"video/quicktime"}
	settings.AllowedCodecs = []string{"av1"}
	assert.False(t, settings.AllowsMimeType("video/mp4"))
	assert.True(t, settings.AllowsMimeType("video/quicktime"))
	assert.False(t, settings.AllowsCodec("h264"))
	assert.True(t, settings.AllowsCodec("av1"))
}

// TestIssueCodesAreStable pins the machine-readable codes and levels the Web
// client dispatches on. A code that changes shape silently breaks the form.
func TestIssueCodesAreStable(t *testing.T) {
	settings := DefaultSettings()
	settings.Mode = "bogus"
	settings.MaxRunningPerUser = 999
	settings.JPEGQuality = 1
	settings.SceneWeight = math.NaN()

	issues := settings.Validate()
	require.NotEmpty(t, issues)
	for _, issue := range issues {
		assert.NotEmpty(t, issue.Code)
		assert.Equal(t, strings.ToLower(issue.Code), issue.Code)
		assert.NotContains(t, issue.Code, " ")
		assert.Contains(t, []string{"error", "warning"}, issue.Level)
		assert.NotEmpty(t, issue.Field)
		assert.NotEmpty(t, issue.Message)
	}
	assert.True(t, HasBlockingIssue(issues))
	assert.NotEmpty(t, BlockingIssues(issues))
	assert.LessOrEqual(t, len(BlockingIssues(issues)), len(issues))
}

// TestSaveAndReloadDoNotDeadlock is a lock-order regression test.
//
// A save takes the settings save lock and, only after releasing it, the option
// map lock (via cacheDocument). The option reload path takes those two in the
// opposite order: updateOptionMap holds the option map lock while it calls into
// PublishFromOption, which takes the save lock. If a save ever touched the
// option map while still holding the save lock the two paths would deadlock.
//
// This reproduces both paths concurrently against the real locks, so an
// inversion hangs here instead of in production.
func TestSaveAndReloadDoNotDeadlock(t *testing.T) {
	resetStore(t)

	// Built before the goroutine starts: assertion helpers must not run off the
	// test goroutine.
	reloadDocument := encodeSettings(t, DefaultSettings(), 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			// The reload path's lock order: the option map is held while the
			// store is entered, exactly as model.updateOptionMap does.
			common.OptionMapRWMutex.Lock()
			_ = PublishFromOption(reloadDocument)
			common.OptionMapRWMutex.Unlock()
		}
	}()

	for range 100 {
		settings := DefaultSettings()
		settings.RetentionDays = 21
		// The save path must take the option map lock only after releasing the
		// save lock.
		if _, err := Save(settings, GetSnapshot().Version, func(string) error { return nil }); err != nil {
			t.Error(err)
			break
		}
	}

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("save and reload paths deadlocked on the option-map and save locks")
	}
}

// resetStore restores the package snapshot between tests: the snapshot is
// process-wide, so each test starts from the defaults.
func resetStore(t *testing.T) {
	t.Helper()
	restore := func() {
		settings := DefaultSettings()
		settings.Normalize()
		publish(settings, 0)
	}
	restore()
	t.Cleanup(restore)
}
