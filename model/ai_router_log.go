package model

// AIRouterLog records one AI Router routing decision for observability.
type AIRouterLog struct {
	Id               int    `json:"id" gorm:"primaryKey"`
	RequestId        string `json:"request_id" gorm:"type:varchar(64);index"`
	UserId           int    `json:"user_id" gorm:"index"`
	GroupId          string `json:"group_id" gorm:"type:varchar(64)"`
	Model            string `json:"model" gorm:"type:varchar(255)"`
	Modality         string `json:"modality" gorm:"type:varchar(16)"`
	HasTools         bool   `json:"has_tools"`
	UnknownTypes     string `json:"unknown_types" gorm:"type:text"`
	Attempts         int    `json:"attempts"`
	MaxAttempts      int    `json:"max_attempts"`
	SelectedChannels string `json:"selected_channels" gorm:"type:text"`
	FinalChannelId   int    `json:"final_channel_id"`
	Success          bool   `json:"success"`
	TTFTMs           int64  `json:"ttft_ms"`
	TotalDurationMs  int64  `json:"total_duration_ms"`
	ErrorMessage     string `json:"error_message" gorm:"type:text"`
	CreatedAt        int64  `json:"created_at" gorm:"bigint;index"`
}

func (AIRouterLog) TableName() string {
	return "ai_router_logs"
}

// GetAIRouterLogs returns paginated, filtered AI Router log entries.
func GetAIRouterLogs(
	startTime, endTime int64,
	modelName string,
	userID int,
	groupID string,
	success *bool,
	modality string,
	page, pageSize int,
) (logs []*AIRouterLog, total int64, err error) {
	tx := DB.Model(&AIRouterLog{})

	if startTime > 0 {
		tx = tx.Where("created_at >= ?", startTime)
	}
	if endTime > 0 {
		tx = tx.Where("created_at <= ?", endTime)
	}
	if modelName != "" {
		tx = tx.Where("model = ?", modelName)
	}
	if userID > 0 {
		tx = tx.Where("user_id = ?", userID)
	}
	if groupID != "" {
		tx = tx.Where("group_id = ?", groupID)
	}
	if success != nil {
		tx = tx.Where("success = ?", *success)
	}
	if modality != "" {
		tx = tx.Where("modality = ?", modality)
	}

	if err = tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	offset := (page - 1) * pageSize
	err = tx.Order("created_at DESC, id DESC").Offset(offset).Limit(pageSize).Find(&logs).Error
	return logs, total, err
}

// AIRouterStats holds basic aggregate statistics.
type AIRouterStats struct {
	TotalRequests int64            `json:"total_requests"`
	SuccessCount  int64            `json:"success_count"`
	FailureCount  int64            `json:"failure_count"`
	SuccessRate   float64          `json:"success_rate"`
	AvgDurationMs float64          `json:"avg_duration_ms"`
	AvgTTFTMs     float64          `json:"avg_ttft_ms"`
	AvgAttempts   float64          `json:"avg_attempts"`
	ByModality    map[string]int64 `json:"by_modality"`
}

// GetAIRouterStats computes basic aggregate statistics for a time range.
func GetAIRouterStats(startTime, endTime int64, modelName, groupID string) (*AIRouterStats, error) {
	tx := DB.Model(&AIRouterLog{})
	if startTime > 0 {
		tx = tx.Where("created_at >= ?", startTime)
	}
	if endTime > 0 {
		tx = tx.Where("created_at <= ?", endTime)
	}
	if modelName != "" {
		tx = tx.Where("model = ?", modelName)
	}
	if groupID != "" {
		tx = tx.Where("group_id = ?", groupID)
	}

	var stats AIRouterStats
	stats.ByModality = make(map[string]int64)

	var result struct {
		Total         int64   `json:"total"`
		SuccessCount  int64   `json:"success_count"`
		AvgDurationMs float64 `json:"avg_duration_ms"`
		AvgTTFTMs     float64 `json:"avg_ttft_ms"`
		AvgAttempts   float64 `json:"avg_attempts"`
	}
	if err := tx.Select(
		"COUNT(*) as total, SUM(CASE WHEN success THEN 1 ELSE 0 END) as success_count, " +
			"COALESCE(AVG(total_duration_ms), 0) as avg_duration_ms, " +
			"COALESCE(AVG(CASE WHEN ttft_ms > 0 THEN ttft_ms END), 0) as avg_ttft_ms, " +
			"COALESCE(AVG(attempts), 0) as avg_attempts",
	).Scan(&result).Error; err != nil {
		return nil, err
	}

	stats.TotalRequests = result.Total
	stats.SuccessCount = result.SuccessCount
	stats.FailureCount = result.Total - result.SuccessCount
	if result.Total > 0 {
		stats.SuccessRate = float64(result.SuccessCount) / float64(result.Total)
	}
	stats.AvgDurationMs = result.AvgDurationMs
	stats.AvgTTFTMs = result.AvgTTFTMs
	stats.AvgAttempts = result.AvgAttempts

	// By modality — reuse the same tx; the previous aggregate Select has
	// already executed, so re-binding conditions here is safe.
	var modalityResults []struct {
		Modality string `json:"modality"`
		Count    int64  `json:"count"`
	}
	if err := tx.Select("modality, COUNT(*) as count").Group("modality").Scan(&modalityResults).Error; err != nil {
		return nil, err
	}
	for _, r := range modalityResults {
		stats.ByModality[r.Modality] = r.Count
	}

	return &stats, nil
}
