package ai_router

import (
	"encoding/json"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"
)

// RecordTelemetry asynchronously persists a routing log entry.
// It never blocks the request: writes happen in a goroutine with panic recovery.
func RecordTelemetry(rc *RouteContext, userID int, group string, success bool, errMsg string) {
	settings := ai_router_setting.GetSettings()
	if !settings.TelemetryEnabled {
		return
	}

	log := buildLogEntry(rc, userID, group, success, errMsg)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				common.SysError(fmt.Sprintf("ai_router telemetry panic: %v", r))
			}
		}()
		if err := model.DB.Create(log).Error; err != nil {
			common.SysError(fmt.Sprintf("ai_router telemetry write failed: %v", err))
		}
	}()
}

func buildLogEntry(rc *RouteContext, userID int, group string, success bool, errMsg string) *model.AIRouterLog {
	channelsJSON, _ := json.Marshal(rc.AttemptOrder)

	unknownTypesJSON, _ := json.Marshal(rc.Demand.UnknownContentTypes)

	finalChannel := 0
	if rc.Decision.ChannelID != 0 {
		finalChannel = rc.Decision.ChannelID
	}

	return &model.AIRouterLog{
		RequestId:        rc.RequestID,
		UserId:           userID,
		GroupId:          group,
		Model:            rc.Demand.Model,
		Modality:         string(rc.Demand.Modality),
		HasTools:         rc.Demand.HasTools,
		UnknownTypes:     string(unknownTypesJSON),
		Attempts:         rc.CurrentAttempt,
		MaxAttempts:      rc.MaxAttempts,
		SelectedChannels: string(channelsJSON),
		FinalChannelId:   finalChannel,
		Success:          success,
		TTFTMs:           rc.TTFT,
		TotalDurationMs:  rc.ElapsedMS(),
		ErrorMessage:     errMsg,
		CreatedAt:        common.GetTimestamp(),
	}
}
