package controller

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"

	"github.com/gin-gonic/gin"
)

// GetAIRouterSettings returns the current global AI Router configuration.
func GetAIRouterSettings(c *gin.Context) {
	settings := ai_router_setting.GetSettings()
	common.ApiSuccess(c, settings)
}

// UpdateAIRouterSettings accepts partial config updates and persists
// each changed field through model.UpdateOption (which validates and
// publishes the snapshot atomically).
func UpdateAIRouterSettings(c *gin.Context) {
	var req map[string]any
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		common.ApiErrorMsg(c, "无效的参数")
		return
	}

	// Convert each field to a string value for model.UpdateOption.
	for key, raw := range req {
		optionKey := "ai_router." + key
		var value string
		switch v := raw.(type) {
		case bool:
			if v {
				value = "true"
			} else {
				value = "false"
			}
		case float64:
			value = strconv.FormatFloat(v, 'f', -1, 64)
		case string:
			value = v
		case []any:
			// For target_models (JSON array of strings).
			jsonBytes, err := json.Marshal(raw)
			if err != nil {
				common.ApiErrorMsg(c, fmt.Sprintf("failed to serialize %s: %v", key, err))
				return
			}
			value = string(jsonBytes)
		default:
			common.ApiErrorMsg(c, fmt.Sprintf("unsupported type for key %s", key))
			return
		}

		if err := model.UpdateOption(optionKey, value); err != nil {
			common.ApiErrorMsg(c, err.Error())
			return
		}
	}

	common.ApiSuccess(c, ai_router_setting.GetSettings())
}

// GetAIRouterLogs returns paginated Router log entries.
func GetAIRouterLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	startTime, _ := strconv.ParseInt(c.Query("start_time"), 10, 64)
	endTime, _ := strconv.ParseInt(c.Query("end_time"), 10, 64)
	modelName := c.Query("model")
	userID, _ := strconv.Atoi(c.Query("user_id"))
	groupID := c.Query("group_id")
	successStr := c.Query("success")
	modality := c.Query("modality")

	var success *bool
	if successStr == "true" {
		v := true
		success = &v
	} else if successStr == "false" {
		v := false
		success = &v
	}

	logs, total, err := model.GetAIRouterLogs(
		startTime, endTime, modelName, userID, groupID, success, modality,
		page, pageSize,
	)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	common.ApiSuccess(c, gin.H{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"items":     logs,
	})
}

// GetAIRouterStats returns basic aggregate statistics for a time range.
func GetAIRouterStats(c *gin.Context) {
	startTime, err := strconv.ParseInt(c.Query("start_time"), 10, 64)
	if err != nil || startTime == 0 {
		common.ApiErrorMsg(c, "start_time is required")
		return
	}
	endTime, err := strconv.ParseInt(c.Query("end_time"), 10, 64)
	if err != nil || endTime == 0 {
		common.ApiErrorMsg(c, "end_time is required")
		return
	}
	modelName := c.Query("model")
	groupID := c.Query("group_id")

	stats, err := model.GetAIRouterStats(startTime, endTime, modelName, groupID)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	common.ApiSuccess(c, stats)
}
