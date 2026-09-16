package model

import (
	"fmt"
	"sort"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

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
// fallback: "", "true", "false". channelID > 0 matches any channel on the
// attempt chain.
func GetAIRouterLogs(
	startTime, endTime int64,
	modelName string,
	userID int,
	groupID string,
	success *bool,
	modality string,
	fallback string,
	channelID int,
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
	// fallback = the request tried more than one channel.
	if fallback != "" {
		switch fallback {
		case "true":
			tx = tx.Where("attempts > 1")
		case "false":
			tx = tx.Where("attempts <= 1")
		}
	}
	if channelID > 0 {
		// channel_id matches any channel on the attempt chain. Attempt chains
		// are JSON arrays like "[4,9,10]"; a JSON_CONTAINS equivalent does not
		// exist on SQLite, so matching uses delimiter-anchored LIKE — "[id,"
		// at the head, ",id," inside, ",id]" at the tail, and "[id]" for a
		// single-element chain.
		tx = tx.Where(
			"selected_channels LIKE ? OR selected_channels LIKE ? OR selected_channels LIKE ? OR selected_channels LIKE ?",
			fmt.Sprintf("%%[%d,%%", channelID),
			fmt.Sprintf("%%,%d,%%", channelID),
			fmt.Sprintf("%%,%d]%%", channelID),
			fmt.Sprintf("%%[%d]%%", channelID),
		)
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
	FallbackCount int64            `json:"fallback_count"`
	FallbackRate  float64          `json:"fallback_rate"`
	AvgDurationMs float64          `json:"avg_duration_ms"`
	AvgTTFTMs     float64          `json:"avg_ttft_ms"`
	AvgAttempts   float64          `json:"avg_attempts"`
	ByModality    map[string]int64 `json:"by_modality"`
}

// AIRouterTrendPoint is one time bucket of the request-trend chart.
type AIRouterTrendPoint struct {
	Time    int64 `json:"time"`
	Total   int64 `json:"total"`
	Success int64 `json:"success"`
	Failure int64 `json:"failure"`
}

// AIRouterChannelStat aggregates one channel's role across requests. Attempts
// counts every chain that includes the channel; finals counts chains where the
// channel was last; success finals are requests it delivered.
type AIRouterChannelStat struct {
	ChannelId      int     `json:"channel_id"`
	Attempts       int64   `json:"attempts"`
	Finals         int64   `json:"finals"`
	SuccessFinals  int64   `json:"success_finals"`
	FailureFinals  int64   `json:"failure_finals"`
	SuccessRate    float64 `json:"success_rate"`
	AvgDurationMs  float64 `json:"avg_duration_ms"`
	AvgTTFTMs      float64 `json:"avg_ttft_ms"`
	TotalDurationS int64   `json:"-"`
	TTFTSamples    int64   `json:"-"`
}

// airRouterLogScope builds a fresh query with the shared time/model/group
// filters applied.
func airRouterLogScope(startTime, endTime int64, modelName, groupID string) *gorm.DB {
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
	return tx
}

// timeBucketSeconds picks a bucket size that keeps the trend chart at a few
// dozen points for the requested span.
func timeBucketSeconds(startTime, endTime int64) int64 {
	span := endTime - startTime
	if span <= 0 {
		span = 3600
	}
	switch {
	case span <= 6*3600:
		return 300 // 5 min
	case span <= 48*3600:
		return 3600 // 1 h
	case span <= 45*24*3600:
		return 6 * 3600 // 6 h
	default:
		return 24 * 3600 // 1 day
	}
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
		FallbackCount int64   `json:"fallback_count"`
		AvgDurationMs float64 `json:"avg_duration_ms"`
		AvgTTFTMs     float64 `json:"avg_ttft_ms"`
		AvgAttempts   float64 `json:"avg_attempts"`
	}
	if err := tx.Select(
		"COUNT(*) as total, SUM(CASE WHEN success THEN 1 ELSE 0 END) as success_count, " +
			"SUM(CASE WHEN attempts > 1 THEN 1 ELSE 0 END) as fallback_count, " +
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
		stats.FallbackRate = float64(result.FallbackCount) / float64(result.Total)
	}
	stats.FallbackCount = result.FallbackCount
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

// GetAIRouterTrend buckets requests into fixed-size time buckets for the
// success/failure trend chart. Bucketing happens in Go: integer division on
// created_at is portable, but MySQL's `/` yields a DECIMAL while SQLite's is
// an integer, so SQL-side bucketing diverges between dialects.
func GetAIRouterTrend(startTime, endTime int64, modelName, groupID string) ([]AIRouterTrendPoint, error) {
	bucket := timeBucketSeconds(startTime, endTime)
	var rows []struct {
		CreatedAt int64 `json:"created_at"`
		Success   bool  `json:"success"`
	}
	if err := airRouterLogScope(startTime, endTime, modelName, groupID).
		Select("created_at, success").
		Order("created_at ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	index := make(map[int64]int)
	points := make([]AIRouterTrendPoint, 0)
	for _, row := range rows {
		bucketStart := startTime + ((row.CreatedAt-startTime)/bucket)*bucket
		i, ok := index[bucketStart]
		if !ok {
			i = len(points)
			index[bucketStart] = i
			points = append(points, AIRouterTrendPoint{Time: bucketStart})
		}
		points[i].Total++
		if row.Success {
			points[i].Success++
		} else {
			points[i].Failure++
		}
	}
	return points, nil
}

// GetAIRouterChannelStats aggregates per-channel involvement across attempt
// chains. Channels are read from the selected_channels JSON arrays; final
// attribution comes from final_channel_id. The per-channel aggregates are
// computed in Go because portable SQL cannot parse the JSON array portably.
func GetAIRouterChannelStats(startTime, endTime int64, modelName, groupID string) ([]AIRouterChannelStat, error) {
	var rows []AIRouterLog
	if err := airRouterLogScope(startTime, endTime, modelName, groupID).
		Select("selected_channels, final_channel_id, success, total_duration_ms, ttft_ms").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	stats := make(map[int]*AIRouterChannelStat)
	statFor := func(channelID int) *AIRouterChannelStat {
		if channelID <= 0 {
			return nil
		}
		s, ok := stats[channelID]
		if !ok {
			s = &AIRouterChannelStat{ChannelId: channelID}
			stats[channelID] = s
		}
		return s
	}
	for i := range rows {
		row := &rows[i]
		var chain []int
		if row.SelectedChannels != "" && row.SelectedChannels != "null" {
			_ = common.Unmarshal([]byte(row.SelectedChannels), &chain)
		}
		for _, channelID := range chain {
			if s := statFor(channelID); s != nil {
				s.Attempts++
			}
		}
		if s := statFor(row.FinalChannelId); s != nil && row.FinalChannelId > 0 {
			s.Finals++
			if row.Success {
				s.SuccessFinals++
			} else {
				s.FailureFinals++
			}
			s.TotalDurationS += row.TotalDurationMs
			if row.TTFTMs > 0 {
				s.AvgTTFTMs += float64(row.TTFTMs)
				s.TTFTSamples++
			}
		}
	}

	result := make([]AIRouterChannelStat, 0, len(stats))
	for _, s := range stats {
		if s.Finals > 0 {
			s.SuccessRate = float64(s.SuccessFinals) / float64(s.Finals)
			s.AvgDurationMs = float64(s.TotalDurationS) / float64(s.Finals)
		}
		if s.TTFTSamples > 0 {
			s.AvgTTFTMs = s.AvgTTFTMs / float64(s.TTFTSamples)
		}
		result = append(result, *s)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Attempts > result[j].Attempts })
	return result, nil
}
