package model

// Error categories for ai_router_attempt_logs. Success rows carry the
// "success" category; the rest classify the failure so the monitoring
// dashboard can break errors down without parsing messages. Classification
// itself lives in service/ai_router/error_category.go.
const (
	AttemptErrorCategory429               = "429"
	AttemptErrorCategory5xx               = "5xx"
	AttemptErrorCategoryTimeout           = "timeout"
	AttemptErrorCategoryConnection        = "connection"
	AttemptErrorCategoryStreamInterrupted = "stream_interrupted"
	AttemptErrorCategoryOther             = "other"
	AttemptErrorCategorySuccess           = "success"
)

// AIRouterAttemptLog records one real upstream call made by the AI Router:
// every channel switch and every same-channel max_retry retry gets its own
// row. It never stores request bodies, response bodies or prompts.
type AIRouterAttemptLog struct {
	Id int `json:"id" gorm:"primaryKey"`
	// References ai_router_logs.id; queries by request fetch all rows of
	// one summary at once.
	RequestLogId int    `json:"request_log_id" gorm:"index"`
	RequestId    string `json:"request_id" gorm:"type:varchar(64)"`
	// AttemptSeq is 1-based over distinct channels; ChannelRetryNo is
	// 0-based over retries of the same channel (Phase 1 semantics).
	AttemptSeq     int `json:"attempt_seq"`
	ChannelRetryNo int `json:"channel_retry_no"`
	ChannelId      int `json:"channel_id" gorm:"index:idx_ai_router_attempt_channel_time,priority:1"`
	// ChannelName is snapshotted at write time so deleted channels still
	// render in the attempt detail; never JOIN channel at query time.
	ChannelName string `json:"channel_name" gorm:"type:varchar(191)"`
	Modality    string `json:"modality" gorm:"type:varchar(16)"`
	// HTTP status of the upstream response; nil when the call failed before
	// a response (connection refused, dial timeout, ...).
	HttpStatusCode *int   `json:"http_status_code" gorm:"index"`
	ErrorCategory  string `json:"error_category" gorm:"type:varchar(32);index:idx_ai_router_attempt_category_time,priority:1"`
	// Retryable mirrors the ShouldRetryRelayError decision made at relay
	// time for this call.
	Retryable  bool  `json:"retryable"`
	DurationMs int64 `json:"duration_ms"`
	// Time to first event; nil for non-streaming calls or streams that
	// never produced a first event.
	TtftMs    *int64 `json:"ttft_ms"`
	Success   bool   `json:"success"`
	CreatedAt int64  `json:"created_at" gorm:"bigint;index;index:idx_ai_router_attempt_channel_time,priority:2;index:idx_ai_router_attempt_category_time,priority:2"`
}

func (AIRouterAttemptLog) TableName() string {
	return "ai_router_attempt_logs"
}

// InsertAIRouterAttemptLogs persists a batch of attempt rows. Callers write
// all attempts of one request together, right after the summary log.
func InsertAIRouterAttemptLogs(logs []*AIRouterAttemptLog) error {
	if len(logs) == 0 {
		return nil
	}
	return DB.Create(&logs).Error
}

// GetAIRouterAttemptLogsByRequestLogId returns the attempt rows of one
// request summary, ordered by attempt_seq then channel_retry_no. Requests
// recorded before V1.5 Phase 3 have no rows; the empty result is not an error.
func GetAIRouterAttemptLogsByRequestLogId(requestLogId int) ([]*AIRouterAttemptLog, error) {
	if requestLogId <= 0 {
		return []*AIRouterAttemptLog{}, nil
	}
	var logs []*AIRouterAttemptLog
	err := DB.Where("request_log_id = ?", requestLogId).
		Order("attempt_seq ASC, channel_retry_no ASC").
		Find(&logs).Error
	return logs, err
}

// AIRouterAttemptChannelStat aggregates attempt rows of one channel for the
// monitoring channel table: total attempts, successes and per-category
// failure counts.
type AIRouterAttemptChannelStat struct {
	ChannelId        int
	Attempts         int64
	SuccessCount     int64
	Count429         int64
	Count5xx         int64
	CountTimeout     int64
	CountConnection  int64
	CountInterrupted int64
	CountOther       int64
	TotalDurationMs  int64
}

// GetAIRouterAttemptChannelStats aggregates attempt rows per channel between
// start and end (unix seconds, inclusive bounds, zero = unbounded). The
// per-category counts come from a GROUP BY on (channel_id, error_category);
// the (channel_id, created_at) index bounds the scan by time range.
func GetAIRouterAttemptChannelStats(startTime, endTime int64) (map[int]*AIRouterAttemptChannelStat, error) {
	var rows []struct {
		ChannelId     int    `json:"channel_id"`
		ErrorCategory string `json:"error_category"`
		Count         int64  `json:"count"`
		TotalDuration int64  `json:"total_duration"`
	}
	tx := DB.Model(&AIRouterAttemptLog{}).
		Select("channel_id, error_category, COUNT(*) as count, COALESCE(SUM(duration_ms), 0) as total_duration")
	if startTime > 0 {
		tx = tx.Where("created_at >= ?", startTime)
	}
	if endTime > 0 {
		tx = tx.Where("created_at <= ?", endTime)
	}
	if err := tx.Group("channel_id, error_category").Scan(&rows).Error; err != nil {
		return nil, err
	}

	stats := make(map[int]*AIRouterAttemptChannelStat)
	for _, row := range rows {
		s, ok := stats[row.ChannelId]
		if !ok {
			s = &AIRouterAttemptChannelStat{ChannelId: row.ChannelId}
			stats[row.ChannelId] = s
		}
		s.Attempts += row.Count
		s.TotalDurationMs += row.TotalDuration
		switch row.ErrorCategory {
		case AttemptErrorCategorySuccess:
			s.SuccessCount += row.Count
		case AttemptErrorCategory429:
			s.Count429 += row.Count
		case AttemptErrorCategory5xx:
			s.Count5xx += row.Count
		case AttemptErrorCategoryTimeout:
			s.CountTimeout += row.Count
		case AttemptErrorCategoryConnection:
			s.CountConnection += row.Count
		case AttemptErrorCategoryStreamInterrupted:
			s.CountInterrupted += row.Count
		default:
			s.CountOther += row.Count
		}
	}
	return stats, nil
}

// CountAIRouterAttemptStats returns global attempt totals for consistency
// checks between the summary and attempt tables.
func CountAIRouterAttemptStats(startTime, endTime int64) (total int64, success int64, err error) {
	tx := DB.Model(&AIRouterAttemptLog{})
	if startTime > 0 {
		tx = tx.Where("created_at >= ?", startTime)
	}
	if endTime > 0 {
		tx = tx.Where("created_at <= ?", endTime)
	}
	var result struct {
		Total   int64 `json:"total"`
		Success int64 `json:"success"`
	}
	if err = tx.Select(
		"COUNT(*) as total, SUM(CASE WHEN success THEN 1 ELSE 0 END) as success",
	).Scan(&result).Error; err != nil {
		return 0, 0, err
	}
	return result.Total, result.Success, nil
}
