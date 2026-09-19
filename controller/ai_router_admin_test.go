package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service/video_preprocess"
	"github.com/QuantumNous/new-api/setting/video_preprocess_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUpdateAIRouterSettingsValidation(t *testing.T) {
	boolPtr := func(v bool) *bool { return &v }
	// Verify that model.UpdateOption rejects invalid ai_router config values.
	// This tests the existing Phase 1 validation pipeline, not the HTTP layer.
	validProfiles := []dto.ChannelRoutingProfile{
		{Enabled: true, SupportsText: boolPtr(true)},
		{Enabled: true, SupportsVideo: boolPtr(true)},
		{Enabled: true, SupportsText: boolPtr(false), SupportsVideo: boolPtr(true)},
	}
	for _, p := range validProfiles {
		if err := p.Validate(); err != nil {
			t.Errorf("expected valid profile %+v, got %v", p, err)
		}
	}
}

// setupVideoPreprocessControllerTestDB gives the settings endpoints a real
// SQLite database with the Option table, so the whole loop the admin API
// exercises — decode, validate, persist, publish — runs against real storage
// rather than a stand-in.
func setupVideoPreprocessControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	previousMode := gin.Mode()
	previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedisEnabled := common.RedisEnabled
	gin.SetMode(gin.TestMode)
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))

	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousSettings := video_preprocess_setting.GetSnapshot().Settings
	previousHook := model.OnVideoPreprocessPublished
	model.OnVideoPreprocessPublished = video_preprocess.ApplyPublishedSettings
	model.DB, model.LOG_DB = db, db
	common.OptionMapRWMutex.Lock()
	previousOptionMap := common.OptionMap
	common.OptionMap = map[string]string{}
	common.OptionMapRWMutex.Unlock()

	t.Cleanup(func() {
		defer func() { model.OnVideoPreprocessPublished = previousHook }()
		model.DB, model.LOG_DB = previousDB, previousLogDB
		gin.SetMode(previousMode)
		common.SetDatabaseTypes(previousMainType, previousLogType)
		common.RedisEnabled = previousRedisEnabled
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
		// Restore settings at a new version after restoring the cache map, so
		// the next test observes matching cache, snapshot and runtime generations.
		_, err := video_preprocess_setting.Save(previousSettings, video_preprocess_setting.GetSnapshot().Version, func(string) error { return nil })
		require.NoError(t, err)
		video_preprocess.ApplyPublishedSettings()
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})

	return db
}

// videoSettingsRequest issues one admin request against the settings handler.
func videoSettingsRequest(t *testing.T, handler gin.HandlerFunc, method string, body string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(method, "/api/ai-router/video/settings", strings.NewReader(body))
	context.Request.Header.Set("Content-Type", "application/json")
	handler(context)
	return recorder
}

// decodeVideoSettingsResponse reads the success payload of a settings call.
func decodeVideoSettingsResponse(t *testing.T, recorder *httptest.ResponseRecorder) struct {
	Success bool                  `json:"success"`
	Message string                `json:"message"`
	Data    videoSettingsResponse `json:"data"`
} {
	t.Helper()

	var payload struct {
		Success bool                  `json:"success"`
		Message string                `json:"message"`
		Data    videoSettingsResponse `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	return payload
}

// settingsJSONBody renders a full settings document for a PUT, quoting the
// expected version.
func settingsJSONBody(t *testing.T, settings video_preprocess_setting.Settings, expectedVersion int64) string {
	t.Helper()

	encoded, err := common.Marshal(map[string]any{
		"expected_version": expectedVersion,
		"settings":         settings,
	})
	require.NoError(t, err)
	return string(encoded)
}

// TestVideoSettingsGetReturnsDefaultsAndDeployment checks the read endpoint:
// it returns the effective settings, the version an update must quote, and the
// read-only deployment values.
func TestVideoSettingsGetReturnsDefaultsAndDeployment(t *testing.T) {
	setupVideoPreprocessControllerTestDB(t)
	initial := resetVideoPreprocessSettings(t)

	recorder := videoSettingsRequest(t, GetVideoPreprocessSettings, http.MethodGet, "")
	require.Equal(t, http.StatusOK, recorder.Code)

	payload := decodeVideoSettingsResponse(t, recorder)
	require.True(t, payload.Success)
	require.Empty(t, payload.Message)
	assert.Equal(t, video_preprocess_setting.DefaultSettings(), payload.Data.Settings)
	assert.Equal(t, initial.Version, payload.Data.Version)
	assert.Equal(t, video_preprocess_setting.ConfigVersion, payload.Data.ConfigVersion)
	// Deployment values are present but never writable through this API.
	assert.NotEmpty(t, payload.Data.Deployment.TempDir)
	assert.Contains(t, payload.Data.Deployment.FFmpegPath, "ffmpeg")
}

// TestVideoSettingsPutPersistsWholeDocument covers the happy path and proves
// the version advances and the persisted document matches what was published.
func TestVideoSettingsPutPersistsWholeDocument(t *testing.T) {
	db := setupVideoPreprocessControllerTestDB(t)
	initial := resetVideoPreprocessSettings(t)

	candidate := video_preprocess_setting.DefaultSettings()
	candidate.Enabled = true
	candidate.TargetModels = []string{"kimi-k3", "gpt-4o"}
	candidate.MaxRunningJobs = 8
	candidate.MaxRunningPerUser = 2
	candidate.RetentionDays = 30

	recorder := videoSettingsRequest(t, UpdateVideoPreprocessSettings, http.MethodPut,
		settingsJSONBody(t, candidate, initial.Version))
	require.Equal(t, http.StatusOK, recorder.Code)

	payload := decodeVideoSettingsResponse(t, recorder)
	require.True(t, payload.Success)
	assert.Equal(t, initial.Version+1, payload.Data.Version)
	assert.True(t, payload.Data.Settings.Enabled)
	assert.Equal(t, []string{"kimi-k3", "gpt-4o"}, payload.Data.Settings.TargetModels)

	// The published snapshot and storage agree, and a GET sees the same thing.
	assert.Equal(t, payload.Data.Settings, video_preprocess_setting.GetSettings())
	assert.Equal(t, payload.Data.Settings.MaxRunningJobs, video_preprocess_setting.GetSettings().MaxRunningJobs)

	var stored model.Option
	require.NoError(t, db.First(&stored, "key = ?", video_preprocess_setting.SettingsOptionKey).Error)
	decoded, err := video_preprocess_setting.Decode(stored.Value)
	require.NoError(t, err)
	assert.Equal(t, initial.Version+1, decoded.Version)
	assert.Equal(t, payload.Data.Settings, decoded.Settings)

	getRecorder := videoSettingsRequest(t, GetVideoPreprocessSettings, http.MethodGet, "")
	getPayload := decodeVideoSettingsResponse(t, getRecorder)
	assert.Equal(t, payload.Data.Version, getPayload.Data.Version)
	assert.Equal(t, payload.Data.Settings, getPayload.Data.Settings)
}

// TestVideoSettingsPutRejectsInvalidCandidateWithoutWriting covers the
// all-or-nothing requirement: a candidate with one bad field must leave both
// storage and the published snapshot exactly as they were.
func TestVideoSettingsPutRejectsInvalidCandidateWithoutWriting(t *testing.T) {
	db := setupVideoPreprocessControllerTestDB(t)
	initial := resetVideoPreprocessSettings(t)

	valid := video_preprocess_setting.DefaultSettings()
	valid.TargetModels = []string{"kimi-k3"}
	require.Equal(t, http.StatusOK,
		videoSettingsRequest(t, UpdateVideoPreprocessSettings, http.MethodPut, settingsJSONBody(t, valid, initial.Version)).Code)

	before := video_preprocess_setting.GetSnapshot()
	var storedBefore model.Option
	require.NoError(t, db.First(&storedBefore, "key = ?", video_preprocess_setting.SettingsOptionKey).Error)

	// One field out of range, plus a second change that would otherwise be
	// acceptable: neither may be written.
	invalid := valid
	invalid.Enabled = true
	invalid.JPEGQuality = 99

	recorder := videoSettingsRequest(t, UpdateVideoPreprocessSettings, http.MethodPut,
		settingsJSONBody(t, invalid, before.Version))
	require.Equal(t, http.StatusBadRequest, recorder.Code)

	var payload struct {
		Success bool                                       `json:"success"`
		Code    string                                     `json:"code"`
		Message string                                     `json:"message"`
		Issues  []video_preprocess_setting.ValidationIssue `json:"issues"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	assert.False(t, payload.Success)
	assert.Equal(t, "video_settings_invalid", payload.Code)
	require.NotEmpty(t, payload.Issues)
	assert.True(t, video_preprocess_setting.HasBlockingIssue(payload.Issues))

	assert.Equal(t, before, video_preprocess_setting.GetSnapshot())
	assert.False(t, video_preprocess_setting.GetSettings().Enabled)

	var storedAfter model.Option
	require.NoError(t, db.First(&storedAfter, "key = ?", video_preprocess_setting.SettingsOptionKey).Error)
	assert.Equal(t, storedBefore.Value, storedAfter.Value)
	assert.Equal(t, initial.Version+1, before.Version)
}

// TestVideoSettingsPutVersionConflict covers the optimistic concurrency
// contract at the HTTP layer: a stale expected_version is a 409, and the
// winning save is the one that quoted the current version.
func TestVideoSettingsPutVersionConflict(t *testing.T) {
	db := setupVideoPreprocessControllerTestDB(t)
	initial := resetVideoPreprocessSettings(t)

	candidate := video_preprocess_setting.DefaultSettings()
	candidate.RetentionDays = 14
	require.Equal(t, http.StatusOK,
		videoSettingsRequest(t, UpdateVideoPreprocessSettings, http.MethodPut, settingsJSONBody(t, candidate, initial.Version)).Code)
	before := video_preprocess_setting.GetSnapshot()
	var storedBefore model.Option
	require.NoError(t, db.First(&storedBefore, "key = ?", video_preprocess_setting.SettingsOptionKey).Error)

	// A second page still holding the initial version is refused.
	candidate.RetentionDays = 21
	recorder := videoSettingsRequest(t, UpdateVideoPreprocessSettings, http.MethodPut,
		settingsJSONBody(t, candidate, initial.Version))
	require.Equal(t, http.StatusConflict, recorder.Code)

	var payload struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	assert.False(t, payload.Success)
	assert.Equal(t, "video_settings_version_conflict", payload.Code)
	assert.Equal(t, before, video_preprocess_setting.GetSnapshot())
	var storedAfter model.Option
	require.NoError(t, db.First(&storedAfter, "key = ?", video_preprocess_setting.SettingsOptionKey).Error)
	assert.Equal(t, storedBefore, storedAfter)

	// Re-reading and retrying with the current version succeeds.
	require.Equal(t, http.StatusOK,
		videoSettingsRequest(t, UpdateVideoPreprocessSettings, http.MethodPut, settingsJSONBody(t, candidate, before.Version)).Code)
	assert.Equal(t, initial.Version+2, video_preprocess_setting.GetSnapshot().Version)
}

// TestVideoSettingsPutRejectsMalformedBodies covers the request contract: a
// complete candidate and an explicit version are both required.
func TestVideoSettingsPutRejectsMalformedBodies(t *testing.T) {
	setupVideoPreprocessControllerTestDB(t)
	resetVideoPreprocessSettings(t)

	before := video_preprocess_setting.GetSnapshot()
	complete, err := common.Marshal(video_preprocess_setting.DefaultSettings())
	require.NoError(t, err)

	tests := []struct {
		name     string
		body     string
		wantCode string
	}{
		{name: "not json", body: "{not json", wantCode: "video_settings_invalid_request"},
		{name: "missing settings", body: `{"expected_version":0}`, wantCode: "video_settings_missing_field"},
		{name: "settings is null", body: `{"expected_version":0,"settings":null}`, wantCode: "video_settings_invalid_request"},
		{name: "missing version", body: fmt.Sprintf(`{"settings":%s}`, complete), wantCode: "video_settings_version_required"},
		{name: "unknown field", body: `{"expected_version":0,"settings":{"jpeg_qualty":82}}`, wantCode: "video_settings_unknown_field"},
		{
			name:     "incomplete settings",
			body:     `{"expected_version":0,"settings":{"enabled":true}}`,
			wantCode: "video_settings_missing_field",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := videoSettingsRequest(t, UpdateVideoPreprocessSettings, http.MethodPut, test.body)
			require.Equal(t, http.StatusBadRequest, recorder.Code)

			var payload struct {
				Success bool   `json:"success"`
				Code    string `json:"code"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			assert.False(t, payload.Success)
			assert.Equal(t, test.wantCode, payload.Code)
			assert.Equal(t, before, video_preprocess_setting.GetSnapshot())
		})
	}
}

// TestVideoSettingsValidateHasNoSideEffects covers the dry-run endpoint: it
// reports findings and warnings without writing or advancing the version.
func TestVideoSettingsValidateHasNoSideEffects(t *testing.T) {
	db := setupVideoPreprocessControllerTestDB(t)
	resetVideoPreprocessSettings(t)

	before := video_preprocess_setting.GetSnapshot()

	t.Run("a valid candidate reports no blocking findings", func(t *testing.T) {
		candidate := video_preprocess_setting.DefaultSettings()
		candidate.MaxRunningJobs = 24
		candidate.MaxRunningPerUser = 4

		encoded, err := common.Marshal(candidate)
		require.NoError(t, err)
		recorder := videoSettingsRequest(t, ValidateVideoPreprocessSettings, http.MethodPost,
			fmt.Sprintf(`{"settings":%s}`, encoded))
		require.Equal(t, http.StatusOK, recorder.Code)

		var payload struct {
			Success bool `json:"success"`
			Data    struct {
				Issues   []video_preprocess_setting.ValidationIssue `json:"issues"`
				Blocking bool                                       `json:"blocking"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
		require.True(t, payload.Success)
		assert.False(t, payload.Data.Blocking)
	})

	t.Run("an invalid candidate is reported but not applied", func(t *testing.T) {
		candidate := video_preprocess_setting.DefaultSettings()
		candidate.Enabled = true
		candidate.MaxRunningPerUser = 64

		encoded, err := common.Marshal(candidate)
		require.NoError(t, err)
		recorder := videoSettingsRequest(t, ValidateVideoPreprocessSettings, http.MethodPost,
			fmt.Sprintf(`{"settings":%s}`, encoded))
		require.Equal(t, http.StatusOK, recorder.Code)

		var payload struct {
			Success bool `json:"success"`
			Data    struct {
				Issues   []video_preprocess_setting.ValidationIssue `json:"issues"`
				Blocking bool                                       `json:"blocking"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
		require.True(t, payload.Success)
		assert.True(t, payload.Data.Blocking)
		assert.NotEmpty(t, payload.Data.Issues)
		// The refusal is a finding, not a zero value: the enabled flag the
		// candidate carried was never published.
		assert.False(t, video_preprocess_setting.GetSettings().Enabled)
	})

	t.Run("an empty body re-checks the published settings", func(t *testing.T) {
		recorder := videoSettingsRequest(t, ValidateVideoPreprocessSettings, http.MethodPost, `{}`)
		require.Equal(t, http.StatusOK, recorder.Code)

		var payload struct {
			Success bool `json:"success"`
			Data    struct {
				Issues   []video_preprocess_setting.ValidationIssue `json:"issues"`
				Blocking bool                                       `json:"blocking"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
		require.True(t, payload.Success)
		assert.False(t, payload.Data.Blocking)
	})

	// No call above may have written anything.
	assert.Equal(t, before, video_preprocess_setting.GetSnapshot())
	var count int64
	require.NoError(t, db.Model(&model.Option{}).Where("key = ?", video_preprocess_setting.SettingsOptionKey).Count(&count).Error)
	assert.Zero(t, count)
}

// TestVideoSettingsPersistThroughOptionReload covers the startup and periodic
// reload path: what the settings API stored is what the Option pipeline
// republishes, so a restarted or long-running node converges on it.
func TestVideoSettingsPersistThroughOptionReload(t *testing.T) {
	db := setupVideoPreprocessControllerTestDB(t)
	initial := resetVideoPreprocessSettings(t)

	candidate := video_preprocess_setting.DefaultSettings()
	candidate.Enabled = true
	candidate.MaxJobSeconds = 240
	require.Equal(t, http.StatusOK,
		videoSettingsRequest(t, UpdateVideoPreprocessSettings, http.MethodPut, settingsJSONBody(t, candidate, initial.Version)).Code)
	published := video_preprocess_setting.GetSnapshot()

	// Simulate another writer storing a newer generation without publishing it
	// in this process. Reload must actually advance the current snapshot.
	candidate.Enabled = false
	candidate.TargetModels = []string{"new-model"}
	candidate.MaxRunningJobs = 6
	candidate.MaxWaitingJobs = 30
	candidate.MaxJobSeconds = 300
	nextDocument, err := video_preprocess_setting.Encode(candidate, published.Version+1)
	require.NoError(t, err)
	require.NoError(t, model.SaveVideoPreprocessSettingsDocument(nextDocument))
	require.Equal(t, published, video_preprocess_setting.GetSnapshot())

	var stored model.Option
	require.NoError(t, db.First(&stored, "key = ?", video_preprocess_setting.SettingsOptionKey).Error)
	require.NoError(t, model.UpdateOption(stored.Key, stored.Value))

	want := video_preprocess_setting.Snapshot{Version: published.Version + 1, Settings: candidate}
	assert.Equal(t, want, video_preprocess_setting.GetSnapshot())

	// UpdateOption enters the same updateOptionMap publication/cache path as a
	// background reload. An older document must not roll back the generic GET
	// response, even though the legacy option writer accepts it into storage.
	delayedDocument, err := video_preprocess_setting.Encode(published.Settings, published.Version)
	require.NoError(t, err)
	require.NoError(t, model.UpdateOption(stored.Key, delayedDocument))
	assert.Equal(t, want, video_preprocess_setting.GetSnapshot())
	options := videoSettingsRequest(t, GetOptions, http.MethodGet, "")
	require.Equal(t, http.StatusOK, options.Code)
	var response struct {
		Success bool           `json:"success"`
		Data    []model.Option `json:"data"`
	}
	require.NoError(t, common.Unmarshal(options.Body.Bytes(), &response))
	require.True(t, response.Success)
	var cached string
	for _, option := range response.Data {
		if option.Key == stored.Key {
			cached = option.Value
		}
	}
	decoded, err := video_preprocess_setting.Decode(cached)
	require.NoError(t, err)
	assert.Equal(t, want, decoded)
	runtime := video_preprocess.GetRuntimeStatus()
	require.True(t, runtime.Available)
	assert.Equal(t, want.Version, runtime.Version)
	assert.Equal(t, want.Settings.Enabled, runtime.Enabled)
	assert.Equal(t, want.Settings.MaxRunningJobs, runtime.Queue.MaxRunningJobs)
	assert.Equal(t, want.Settings.MaxWaitingJobs, runtime.Queue.MaxWaitingJobs)
}

// TestVideoSettingsOptionUpdateRejectsInvalidDocument covers the Option-table
// guard directly: a document that would not pass the settings API cannot be
// smuggled in through model.UpdateOption either.
func TestVideoSettingsOptionUpdateRejectsInvalidDocument(t *testing.T) {
	db := setupVideoPreprocessControllerTestDB(t)
	initial := resetVideoPreprocessSettings(t)

	invalid := video_preprocess_setting.DefaultSettings()
	invalid.MaxLongSide = 1
	encoded, err := video_preprocess_setting.Encode(invalid, initial.Version+1)
	require.NoError(t, err)

	require.Error(t, model.UpdateOption(video_preprocess_setting.SettingsOptionKey, encoded))
	assert.Equal(t, initial, video_preprocess_setting.GetSnapshot())
	var count int64
	require.NoError(t, db.Model(&model.Option{}).Count(&count).Error)
	assert.Zero(t, count)
}

// resetVideoPreprocessSettings starts a fresh generation with defaults. The DB
// fixture restores the previous settings on cleanup. Versions remain monotonic
// across tests; the no-op writer leaves their DB fixtures empty.
func resetVideoPreprocessSettings(t *testing.T) video_preprocess_setting.Snapshot {
	t.Helper()

	previous := video_preprocess_setting.GetSnapshot()
	snapshot, err := video_preprocess_setting.Save(video_preprocess_setting.DefaultSettings(), previous.Version, func(string) error { return nil })
	require.NoError(t, err)
	video_preprocess.ApplyPublishedSettings()
	return snapshot
}
