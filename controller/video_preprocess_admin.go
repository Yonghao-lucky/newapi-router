package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/video_preprocess"
	"github.com/QuantumNous/new-api/setting/video_preprocess_setting"

	"github.com/gin-gonic/gin"
)

// videoSettingsResponse is the payload shared by GET and PUT: the effective
// settings, the version an update must quote, and the deployment-level values
// that are shown read-only (plan §8.3).
type videoSettingsResponse struct {
	Settings video_preprocess_setting.Settings `json:"settings"`
	Defaults video_preprocess_setting.Settings `json:"defaults"`
	// Version is the currently published generation. A PUT must echo it back in
	// expected_version; a mismatch means another admin saved first.
	Version int64 `json:"version"`
	// ConfigVersion is the schema version of the document format.
	ConfigVersion int `json:"config_version"`
	// Deployment is informational: FFmpeg paths, temp directory and hardware
	// decoding cannot be changed through this API.
	Deployment video_preprocess_setting.DeploymentConfig `json:"deployment"`
}

// videoSettingsUpdateRequest is the PUT body. A full candidate document is
// required: settings is validated as a whole and written as a whole, so there
// is no representation of a partial update. Settings stays raw until it has
// passed the strict decode, which rejects unknown and missing fields.
type videoSettingsUpdateRequest struct {
	ExpectedVersion *int64            `json:"expected_version"`
	Settings        common.RawMessage `json:"settings"`
}

// videoSettingsValidationView is the validate response. Issues carries warnings
// as well as errors; blocking is true when at least one error is present.
type videoSettingsValidationView struct {
	Issues   []video_preprocess_setting.ValidationIssue `json:"issues"`
	Blocking bool                                       `json:"blocking"`
}

// writeVideoSettingsDecodeError reports a body the client sent wrong. Every
// decode failure is a 400: the schema is fixed and the Web form always sends a
// complete candidate, so a parse or shape error is a client bug. The code is
// narrowed for the two cases the form handles specially.
func writeVideoSettingsDecodeError(c *gin.Context, err error) {
	code := "video_settings_invalid_request"
	switch {
	case errors.Is(err, video_preprocess_setting.ErrUnknownField):
		code = "video_settings_unknown_field"
	case errors.Is(err, video_preprocess_setting.ErrMissingField):
		code = "video_settings_missing_field"
	}
	c.JSON(http.StatusBadRequest, gin.H{
		"success": false,
		"code":    code,
		"message": err.Error(),
	})
}

// writeVideoSettingsError maps a Save failure onto the HTTP status and
// machine-readable code the Web client dispatches on.
func writeVideoSettingsError(c *gin.Context, err error) {
	var issuesErr *video_preprocess_setting.ValidationIssuesError
	switch {
	case errors.Is(err, video_preprocess_setting.ErrVersionConflict):
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"code":    "video_settings_version_conflict",
			"message": err.Error(),
		})
	case errors.As(err, &issuesErr):
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"code":    "video_settings_invalid",
			"message": err.Error(),
			"issues":  issuesErr.Issues,
		})
	default:
		// A persistence failure, not a client error: the store reported it and
		// nothing was written, so the generic shape is the right one.
		common.ApiError(c, err)
	}
}

// currentVideoSettingsResponse reads the published snapshot and the read-only
// deployment values. The snapshot is a deep copy, so serializing it cannot race
// with a concurrent save.
func currentVideoSettingsResponse() videoSettingsResponse {
	snapshot := video_preprocess_setting.GetSnapshot()
	return videoSettingsResponse{
		Settings:      snapshot.Settings,
		Defaults:      video_preprocess_setting.DefaultSettings(),
		Version:       snapshot.Version,
		ConfigVersion: video_preprocess_setting.ConfigVersion,
		Deployment:    video_preprocess_setting.GetDeploymentConfig(),
	}
}

// GetVideoPreprocessSettings returns the effective Video Preprocessing
// settings, the current version and the deployment constraints.
func GetVideoPreprocessSettings(c *gin.Context) {
	common.ApiSuccess(c, currentVideoSettingsResponse())
}

// UpdateVideoPreprocessSettings replaces the whole settings document.
//
// The candidate is validated in full before anything is written, and the write
// is a single Option row, so a rejected candidate leaves both storage and the
// runtime snapshot untouched.
func UpdateVideoPreprocessSettings(c *gin.Context) {
	var req videoSettingsUpdateRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"code":    "video_settings_invalid_request",
			"message": "invalid request body",
		})
		return
	}
	if req.ExpectedVersion == nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"code":    "video_settings_version_required",
			"message": "expected_version is required",
		})
		return
	}
	candidate, err := video_preprocess_setting.DecodeSettings(req.Settings)
	if err != nil {
		writeVideoSettingsDecodeError(c, err)
		return
	}

	snapshot, err := video_preprocess_setting.Save(candidate, *req.ExpectedVersion,
		model.SaveVideoPreprocessSettingsDocument)
	if err != nil {
		writeVideoSettingsError(c, err)
		return
	}

	// The save published a new generation directly, without passing the option
	// reload path where the node hooks pick it up, so push the new contract to
	// this node's processor here. A nil processor (build failure) is a no-op
	// that the runtime API already reports.
	video_preprocess.ApplyPublishedSettings()

	common.ApiSuccess(c, videoSettingsResponse{
		Settings:      snapshot.Settings,
		Defaults:      video_preprocess_setting.DefaultSettings(),
		Version:       snapshot.Version,
		ConfigVersion: video_preprocess_setting.ConfigVersion,
		Deployment:    video_preprocess_setting.GetDeploymentConfig(),
	})
}

// GetVideoPreprocessRuntime returns this node's live video preprocessing
// counters: queue occupancy, media subprocess gauge, temp storage, wait age,
// config version, throughput, logging health and binary availability.
func GetVideoPreprocessRuntime(c *gin.Context) {
	common.ApiSuccess(c, video_preprocess.GetRuntimeStatus())
}

// ValidateVideoPreprocessSettings checks a candidate document without writing.
// Unlike PUT it accepts a missing settings member by validating the published
// settings, so the form can also be checked before any edit.
func ValidateVideoPreprocessSettings(c *gin.Context) {
	var req struct {
		Settings common.RawMessage `json:"settings"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"code":    "video_settings_invalid_request",
			"message": "invalid request body",
		})
		return
	}
	// The published settings are the fallback so that an empty body re-checks
	// the current configuration, which is what the page does on load.
	candidate := video_preprocess_setting.GetSettings()
	if len(req.Settings) > 0 {
		decoded, err := video_preprocess_setting.DecodeSettings(req.Settings)
		if err != nil {
			writeVideoSettingsDecodeError(c, err)
			return
		}
		candidate = decoded
	}
	issues := video_preprocess_setting.ValidateCandidate(candidate)

	common.ApiSuccess(c, videoSettingsValidationView{
		Issues:   issues,
		Blocking: video_preprocess_setting.HasBlockingIssue(issues),
	})
}

// The same validated filter is used for logs and every statistics view. Query
// deadlines protect relay capacity from expensive administrator range scans.
func videoPreprocessFilter(c *gin.Context) (model.VideoPreprocessLogFilter, bool) {
	now := time.Now().Unix()
	filter := model.VideoPreprocessLogFilter{StartTime: now - 24*3600, EndTime: now}
	if err := c.ShouldBindQuery(&filter); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid video log filters"})
		return filter, false
	}
	if err := filter.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return filter, false
	}
	return filter, true
}

func GetVideoPreprocessLogs(c *gin.Context) {
	filter, ok := videoPreprocessFilter(c)
	if !ok {
		return
	}
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 || page > 1000000 {
		c.JSON(400, gin.H{"success": false, "message": "invalid page"})
		return
	}
	size, err := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if err != nil || size < 1 {
		c.JSON(400, gin.H{"success": false, "message": "invalid page size"})
		return
	}
	size = min(size, 100)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	rows, total, err := model.GetVideoPreprocessLogs(ctx, filter, page, size)
	if err != nil {
		c.JSON(503, gin.H{"success": false, "message": "video logs are unavailable"})
		return
	}
	common.ApiSuccess(c, gin.H{"items": rows, "total": total, "page": page, "page_size": size})
}

func GetVideoPreprocessLogDetail(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(400, gin.H{"success": false, "message": "invalid video log id"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	row, err := model.GetVideoPreprocessLog(ctx, id)
	if err != nil {
		c.JSON(503, gin.H{"success": false, "message": "video log is unavailable"})
		return
	}
	if row == nil {
		c.JSON(404, gin.H{"success": false, "message": "video log was not found"})
		return
	}
	common.ApiSuccess(c, row)
}

// All statistics routes share the same portable aggregation and filters; the
// route suffix only chooses the projection sent to the dashboard.
func GetVideoPreprocessStats(c *gin.Context) {
	filter, ok := videoPreprocessFilter(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	stats, err := model.GetVideoPreprocessStats(ctx, filter)
	if err != nil {
		c.JSON(503, gin.H{"success": false, "message": "video statistics are unavailable"})
		return
	}
	switch {
	case strings.HasSuffix(c.Request.URL.Path, "/trend"):
		common.ApiSuccess(c, stats.Trend)
	case strings.HasSuffix(c.Request.URL.Path, "/durations"):
		common.ApiSuccess(c, gin.H{"queue": stats.Queue, "total": stats.Total, "stage_ms": stats.StageMS, "by_duration": stats.ByDuration, "by_codec": stats.ByCodec, "by_frames": stats.ByFrames})
	case strings.HasSuffix(c.Request.URL.Path, "/errors"):
		common.ApiSuccess(c, gin.H{"by_error": stats.ByError, "rejected_count": stats.RejectedCount})
	default:
		common.ApiSuccess(c, stats)
	}
}
