package ai_router

import (
	"encoding/json"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"
)

// RecordTelemetry asynchronously persists the routing summary and its
// per-attempt rows together, in one write goroutine. It never blocks the
// request: write failures only log.
func RecordTelemetry(rc *RouteContext, userID int, group string, success bool, errMsg string) {
	settings := ai_router_setting.GetSettings()
	if !settings.TelemetryEnabled {
		return
	}

	log := buildLogEntry(rc, userID, group, success, errMsg)
	attempts := buildAttemptLogEntries(rc, log.Id)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				common.SysError(fmt.Sprintf("ai_router telemetry panic: %v", r))
			}
		}()
		if err := model.DB.Create(log).Error; err != nil {
			common.SysError(fmt.Sprintf("ai_router telemetry write failed: %v", err))
			return
		}
		// Attempt rows reference the summary by its id; set it only after
		// the summary insert assigned one.
		for _, attempt := range attempts {
			attempt.RequestLogId = log.Id
		}
		if err := model.InsertAIRouterAttemptLogs(attempts); err != nil {
			common.SysError(fmt.Sprintf("ai_router attempt telemetry write failed: %v", err))
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
		Modality:         string(rc.LogModality),
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

// buildAttemptLogEntries converts the settled attempt records into DB rows.
// AttemptSeq/ChannelRetryNo follow the Phase 1 semantics: seq counts distinct
// channels (1-based), retry_no counts same-channel calls (0-based). Rows
// whose upstream never answered keep a NULL http_status_code. Stream
// interruptions surface both on the failed attempt row and on the summary
// (the summary's error message and the request-level stream flags).
func buildAttemptLogEntries(rc *RouteContext, requestLogId int) []*model.AIRouterAttemptLog {
	if len(rc.AttemptRecords) == 0 {
		return nil
	}
	attempts := make([]*model.AIRouterAttemptLog, 0, len(rc.AttemptRecords))
	requestId := rc.RequestID
	createdAt := common.GetTimestamp()
	for _, record := range rc.AttemptRecords {
		attempts = append(attempts, &model.AIRouterAttemptLog{
			RequestLogId:   requestLogId,
			RequestId:      requestId,
			AttemptSeq:     record.AttemptSeq,
			ChannelRetryNo: record.ChannelRetryNo,
			ChannelId:      record.ChannelId,
			ChannelName:    record.ChannelName,
			Modality:       record.Modality,
			HttpStatusCode: record.HttpStatusCode,
			ErrorCategory:  record.ErrorCategory,
			Retryable:      record.Retryable,
			DurationMs:     record.DurationMs,
			TtftMs:         record.TtftMs,
			Success:        record.Success,
			CreatedAt:      createdAt,
		})
	}
	return attempts
}
