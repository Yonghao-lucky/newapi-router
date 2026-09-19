package model

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// VideoPreprocessLog is one completed preprocessing request, stored in the
// primary database alongside AI Router telemetry. Only structural metadata is
// persisted; the schema has no prompt, URL, path, credential or media field.
type VideoPreprocessLog struct {
	Id                int                    `json:"id" gorm:"primaryKey"`
	RequestId         string                 `json:"request_id" gorm:"type:varchar(64);index"`
	UserId            int                    `json:"user_id" gorm:"index"`
	Model             string                 `json:"model" gorm:"type:varchar(255);index"`
	Status            string                 `json:"status" gorm:"type:varchar(16);index"`
	ErrorCode         string                 `json:"error_code" gorm:"type:varchar(64);index"`
	VideoCount        int                    `json:"video_count"`
	ExistingImages    int                    `json:"existing_images"`
	ExtractedFrames   int                    `json:"extracted_frames"`
	VideoDurationMS   int64                  `json:"video_duration_ms"`
	Width             int                    `json:"width"`
	Height            int                    `json:"height"`
	Codec             string                 `json:"codec" gorm:"type:varchar(32);index"`
	Codecs            string                 `json:"-" gorm:"type:varchar(128)"`
	OriginalBodyBytes int64                  `json:"original_body_bytes"`
	DecodedVideoBytes int64                  `json:"decoded_video_bytes"`
	OutputImageBytes  int64                  `json:"output_image_bytes"`
	OutputBodyBytes   int64                  `json:"output_body_bytes"`
	QueueMS           int64                  `json:"queue_ms"`
	DecodeMS          int64                  `json:"decode_ms"`
	ProbeMS           int64                  `json:"probe_ms"`
	AnalyzeMS         int64                  `json:"analyze_ms"`
	SelectMS          int64                  `json:"select_ms"`
	ExportMS          int64                  `json:"export_ms"`
	RewriteMS         int64                  `json:"rewrite_ms"`
	TotalMS           int64                  `json:"total_ms"`
	SamplerVersion    string                 `json:"sampler_version" gorm:"type:varchar(64)"`
	ConfigVersion     int64                  `json:"config_version"`
	CreatedAt         int64                  `json:"created_at" gorm:"bigint;index"`
	VideosJSON        string                 `json:"-" gorm:"type:text"`
	Videos            []VideoPreprocessMedia `json:"videos,omitempty" gorm:"-"`
}

type VideoPreprocessMedia struct {
	Index          int    `json:"index"`
	DurationMS     int64  `json:"duration_ms"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	Codec          string `json:"codec"`
	Frames         int    `json:"frames"`
	DecodedBytes   int64  `json:"decoded_bytes"`
	ImageBytes     int64  `json:"image_bytes"`
	SamplerVersion string `json:"sampler_version"`
}

func (VideoPreprocessLog) TableName() string { return "video_preprocess_logs" }

func InsertVideoPreprocessLog(ctx context.Context, entry *VideoPreprocessLog) error {
	if len(entry.Videos) > 0 {
		data, err := common.Marshal(entry.Videos)
		if err != nil {
			return err
		}
		entry.VideosJSON = string(data)
	}
	return DB.WithContext(ctx).Create(entry).Error
}

type VideoPreprocessLogFilter struct {
	StartTime     int64  `form:"start_time"`
	EndTime       int64  `form:"end_time"`
	RequestID     string `form:"request_id"`
	UserID        int    `form:"user_id"`
	Model         string `form:"model"`
	Status        string `form:"status"`
	ErrorCode     string `form:"error_code"`
	Codec         string `form:"codec"`
	MinDurationMS int64  `form:"min_duration_ms"`
	MaxDurationMS *int64 `form:"max_duration_ms"`
	MinFrames     int    `form:"min_frames"`
	MaxFrames     *int   `form:"max_frames"`
}

func (f VideoPreprocessLogFilter) Validate() error {
	if f.StartTime < 1 || f.EndTime < f.StartTime || f.EndTime-f.StartTime > 90*24*3600 {
		return errors.New("time range must be ordered and no longer than 90 days")
	}
	if f.UserID < 0 || f.MinDurationMS < 0 || (f.MaxDurationMS != nil && *f.MaxDurationMS < 0) || f.MinFrames < 0 || (f.MaxFrames != nil && *f.MaxFrames < 0) {
		return errors.New("filter quantities must not be negative")
	}
	if f.MaxDurationMS != nil && f.MinDurationMS > *f.MaxDurationMS || f.MaxFrames != nil && f.MinFrames > *f.MaxFrames {
		return errors.New("filter minimum must not exceed maximum")
	}
	if f.Status != "" && !slices.Contains([]string{"succeeded", "failed", "timed_out", "canceled", "rejected"}, f.Status) {
		return errors.New("invalid status filter")
	}
	if f.Codec != "" && !slices.Contains([]string{"h264", "hevc", "vp9", "av1", "mixed"}, f.Codec) {
		return errors.New("invalid codec filter")
	}
	if len(f.Model) > 255 || len(f.RequestID) > 64 || len(f.ErrorCode) > 64 {
		return errors.New("filter value is too long")
	}
	return nil
}

func videoPreprocessScope(ctx context.Context, f VideoPreprocessLogFilter) *gorm.DB {
	tx := DB.WithContext(ctx).Model(&VideoPreprocessLog{}).Where("created_at >= ? AND created_at <= ?", f.StartTime, f.EndTime)
	if f.RequestID != "" {
		tx = tx.Where("request_id = ?", f.RequestID)
	}
	if f.UserID > 0 {
		tx = tx.Where("user_id = ?", f.UserID)
	}
	if f.Model != "" {
		tx = tx.Where("model = ?", f.Model)
	}
	if f.Status != "" {
		tx = tx.Where("status = ?", f.Status)
	}
	if f.ErrorCode != "" {
		tx = tx.Where("error_code = ?", f.ErrorCode)
	}
	if f.Codec != "" {
		tx = tx.Where("codec = ? OR codecs LIKE ?", f.Codec, "%,"+f.Codec+",%")
	}
	if f.MinDurationMS > 0 {
		tx = tx.Where("video_duration_ms >= ?", f.MinDurationMS)
	}
	if f.MaxDurationMS != nil {
		tx = tx.Where("video_duration_ms <= ?", f.MaxDurationMS)
	}
	if f.MinFrames > 0 {
		tx = tx.Where("extracted_frames >= ?", f.MinFrames)
	}
	if f.MaxFrames != nil {
		tx = tx.Where("extracted_frames <= ?", f.MaxFrames)
	}
	return tx
}

func GetVideoPreprocessLogs(ctx context.Context, f VideoPreprocessLogFilter, page, size int) ([]VideoPreprocessLog, int64, error) {
	if err := f.Validate(); err != nil {
		return nil, 0, err
	}
	page, size = max(1, page), min(100, max(1, size))
	if page > 1000000 {
		return nil, 0, errors.New("page exceeds the supported range")
	}
	var total int64
	if err := videoPreprocessScope(ctx, f).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	entries := []VideoPreprocessLog{}
	err := videoPreprocessScope(ctx, f).Omit("videos_json").Order("created_at DESC, id DESC").Offset((page - 1) * size).Limit(size).Find(&entries).Error
	return entries, total, err
}

func GetVideoPreprocessLog(ctx context.Context, id int) (*VideoPreprocessLog, error) {
	var entry VideoPreprocessLog
	err := DB.WithContext(ctx).First(&entry, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if entry.VideosJSON != "" {
		if err := common.UnmarshalJsonStr(entry.VideosJSON, &entry.Videos); err != nil {
			return nil, err
		}
	}
	return &entry, nil
}

type VideoPreprocessPercentiles struct {
	P50 int64 `json:"p50"`
	P95 int64 `json:"p95"`
	P99 int64 `json:"p99"`
}
type VideoPreprocessTrend struct {
	Time     int64 `json:"time"`
	Total    int64 `json:"total"`
	Success  int64 `json:"success"`
	Failure  int64 `json:"failure"`
	Rejected int64 `json:"rejected"`
	QueueMS  int64 `json:"queue_ms"`
	TotalMS  int64 `json:"total_ms"`
}
type VideoPreprocessStats struct {
	TotalRequests     int64                      `json:"total_requests"`
	SuccessCount      int64                      `json:"success_count"`
	FailureCount      int64                      `json:"failure_count"`
	RejectedCount     int64                      `json:"rejected_count"`
	Queue             VideoPreprocessPercentiles `json:"queue"`
	Total             VideoPreprocessPercentiles `json:"total"`
	StageMS           map[string]float64         `json:"stage_ms"`
	ByCodec           map[string]int64           `json:"by_codec"`
	ByDuration        map[string]int64           `json:"by_duration"`
	ByFrames          map[int]int64              `json:"by_frames"`
	ByError           map[string]int64           `json:"by_error"`
	OriginalBodyBytes int64                      `json:"original_body_bytes"`
	OutputBodyBytes   int64                      `json:"output_body_bytes"`
	OutputImageBytes  int64                      `json:"output_image_bytes"`
	Trend             []VideoPreprocessTrend     `json:"trend"`
}

// GetVideoPreprocessStats streams a narrow projection in bounded memory. Time
// buckets and histograms are formed in Go, avoiding dialect-specific date/JSON
// expressions. Exact percentiles use six ordered single-row queries.
func GetVideoPreprocessStats(ctx context.Context, f VideoPreprocessLogFilter) (*VideoPreprocessStats, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	stats := &VideoPreprocessStats{StageMS: map[string]float64{}, ByCodec: map[string]int64{}, ByDuration: map[string]int64{}, ByFrames: map[int]int64{}, ByError: map[string]int64{}, Trend: []VideoPreprocessTrend{}}
	bucketSeconds := max(int64(60), (f.EndTime-f.StartTime+119)/120)
	buckets := map[int64]*VideoPreprocessTrend{}
	rows, err := videoPreprocessScope(ctx, f).Select("status,error_code,codec,codecs,extracted_frames,video_duration_ms,original_body_bytes,output_body_bytes,output_image_bytes,queue_ms,decode_ms,probe_ms,analyze_ms,select_ms,export_ms,rewrite_ms,total_ms,created_at").Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var row VideoPreprocessLog
		if err := DB.ScanRows(rows, &row); err != nil {
			return nil, err
		}
		stats.TotalRequests++
		bucket := (row.CreatedAt-f.StartTime)/bucketSeconds*bucketSeconds + f.StartTime
		point := buckets[bucket]
		if point == nil {
			point = &VideoPreprocessTrend{Time: bucket}
			buckets[bucket] = point
		}
		point.Total++
		point.QueueMS += row.QueueMS
		point.TotalMS += row.TotalMS
		if row.Status == "succeeded" {
			stats.SuccessCount++
			point.Success++
		} else {
			stats.FailureCount++
			point.Failure++
		}
		if row.Status == "rejected" {
			stats.RejectedCount++
			point.Rejected++
		}
		if row.ErrorCode != "" {
			stats.ByError[row.ErrorCode]++
		}
		for codec := range strings.SplitSeq(strings.Trim(row.Codecs, ","), ",") {
			if codec != "" {
				stats.ByCodec[codec]++
			}
		}
		duration := "over_1800"
		for _, bound := range []struct {
			ms   int64
			name string
		}{{10000, "0_10"}, {30000, "10_30"}, {120000, "30_120"}, {300000, "120_300"}, {600000, "300_600"}, {1800000, "600_1800"}} {
			if row.VideoDurationMS <= bound.ms {
				duration = bound.name
				break
			}
		}
		stats.ByDuration[duration]++
		stats.ByFrames[row.ExtractedFrames]++
		if row.Status == "succeeded" {
			stats.OriginalBodyBytes += row.OriginalBodyBytes
			stats.OutputBodyBytes += row.OutputBodyBytes
			stats.OutputImageBytes += row.OutputImageBytes
		}
		stats.StageMS["queue"] += float64(row.QueueMS)
		stats.StageMS["decode"] += float64(row.DecodeMS)
		stats.StageMS["probe"] += float64(row.ProbeMS)
		stats.StageMS["analyze"] += float64(row.AnalyzeMS)
		stats.StageMS["select"] += float64(row.SelectMS)
		stats.StageMS["export"] += float64(row.ExportMS)
		stats.StageMS["rewrite"] += float64(row.RewriteMS)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, point := range buckets {
		stats.Trend = append(stats.Trend, *point)
	}
	slices.SortFunc(stats.Trend, func(a, b VideoPreprocessTrend) int {
		if a.Time < b.Time {
			return -1
		}
		if a.Time > b.Time {
			return 1
		}
		return 0
	})
	if stats.TotalRequests == 0 {
		return stats, nil
	}
	for stage := range stats.StageMS {
		stats.StageMS[stage] /= float64(stats.TotalRequests)
	}
	for _, column := range []string{"queue_ms", "total_ms"} {
		var values [3]int64
		for i, fraction := range []float64{0.5, 0.95, 0.99} {
			offset := int(math.Ceil(float64(stats.TotalRequests)*fraction)) - 1
			if err := videoPreprocessScope(ctx, f).Select(column).Order(column + " ASC, id ASC").Offset(offset).Limit(1).Scan(&values[i]).Error; err != nil {
				return nil, err
			}
		}
		quantiles := VideoPreprocessPercentiles{P50: values[0], P95: values[1], P99: values[2]}
		if column == "queue_ms" {
			stats.Queue = quantiles
		} else {
			stats.Total = quantiles
		}
	}
	return stats, nil
}

func DeleteVideoPreprocessLogsBefore(ctx context.Context, cutoff int64, limit int) (int64, error) {
	if cutoff <= 0 || cutoff > time.Now().Unix() {
		return 0, errors.New("invalid retention cutoff")
	}
	var ids []int
	if err := DB.WithContext(ctx).Model(&VideoPreprocessLog{}).Where("created_at < ?", cutoff).Order("id ASC").Limit(min(1000, max(1, limit))).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := DB.WithContext(ctx).Where("id IN ?", ids).Delete(&VideoPreprocessLog{})
	return result.RowsAffected, result.Error
}
