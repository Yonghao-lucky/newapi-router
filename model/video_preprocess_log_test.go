package model

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func videoLogDatabase(t *testing.T, dsn, path string) *gorm.DB {
	t.Helper()
	previousDB, previousLogDB := DB, LOG_DB
	previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
	previousPath := common.SQLitePath
	common.SQLitePath = path
	t.Setenv("VIDEO_LOG_TEST_DSN", dsn)
	db, kind, err := chooseDB("VIDEO_LOG_TEST_DSN", false)
	require.NoError(t, err)
	DB, LOG_DB = db, db
	common.SetDatabaseTypes(kind, kind)
	initCol()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sqlDB.Close())
		DB, LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMain, previousLog)
		common.SQLitePath = previousPath
		initCol()
	})
	var version string
	query := "SELECT version()"
	if kind == common.DatabaseTypeSQLite {
		query = "SELECT sqlite_version()"
	}
	require.NoError(t, db.Raw(query).Scan(&version).Error)
	t.Logf("database=%s version=%s", kind, version)
	return db
}

func TestVideoPreprocessDatabase(t *testing.T) {
	for _, tc := range []struct{ name, dsn string }{
		{"sqlite", "local"}, {"mysql", os.Getenv("VIDEO_TEST_MYSQL_DSN")}, {"postgres", os.Getenv("VIDEO_TEST_POSTGRES_DSN")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.dsn == "" {
				t.Skip("real database DSN is not configured")
			}
			db := videoLogDatabase(t, tc.dsn, filepath.Join(t.TempDir(), "video.db"))
			require.NoError(t, migrateDB())
			recorder := &migrationSQLRecorder{}
			DB = db.Session(&gorm.Session{Logger: recorder})
			require.NoError(t, migrateDB())
			assert.Empty(t, recorder.schemaMutations(), "repeat startup must not mutate an already migrated schema")
			// Dedicated test databases may be reused by the acceptance command.
			require.NoError(t, DB.Where("request_id LIKE ?", "video-matrix-%").Delete(&VideoPreprocessLog{}).Error)
			now := time.Now().Unix()
			rows := []VideoPreprocessLog{
				{RequestId: "video-matrix-success", UserId: 71, Model: "kimi-k3", Status: "succeeded", VideoCount: 2, ExistingImages: 1, ExtractedFrames: 6, Codec: "mixed", Codecs: ",h264,vp9,", VideoDurationMS: 15000, QueueMS: 10, TotalMS: 100, AnalyzeMS: 20, OriginalBodyBytes: 9000, OutputBodyBytes: 3000, CreatedAt: now - 2, Videos: []VideoPreprocessMedia{{Index: 1, Codec: "h264", Frames: 3, DurationMS: 10000}, {Index: 2, Codec: "vp9", Frames: 3, DurationMS: 5000}}},
				{RequestId: "video-matrix-rejected", UserId: 72, Model: "kimi-k3", Status: "rejected", ErrorCode: "video_queue_full", QueueMS: 20, TotalMS: 200, CreatedAt: now - 1},
				{RequestId: "video-matrix-failed", UserId: 71, Model: "other", Status: "failed", ErrorCode: "video_unsupported_format", QueueMS: 30, TotalMS: 300, CreatedAt: now},
			}
			for i := range rows {
				require.NoError(t, InsertVideoPreprocessLog(context.Background(), &rows[i]))
			}
			filter := VideoPreprocessLogFilter{StartTime: now - 100, EndTime: now}
			page, total, err := GetVideoPreprocessLogs(context.Background(), filter, 1, 2)
			require.NoError(t, err)
			assert.EqualValues(t, 3, total)
			require.Len(t, page, 2)
			assert.Equal(t, rows[2].RequestId, page[0].RequestId)
			assert.Empty(t, page[0].VideosJSON)
			filter.Codec = "vp9"
			page, total, err = GetVideoPreprocessLogs(context.Background(), filter, 1, 1000)
			require.NoError(t, err)
			require.EqualValues(t, 1, total)
			assert.Equal(t, rows[0].Id, page[0].Id)
			filter.Codec = ""
			filter.UserID = 71
			_, total, err = GetVideoPreprocessLogs(context.Background(), filter, 1, 20)
			require.NoError(t, err)
			assert.EqualValues(t, 2, total)
			filter.UserID = 0
			detail, err := GetVideoPreprocessLog(context.Background(), rows[0].Id)
			require.NoError(t, err)
			assert.Equal(t, rows[0].Videos, detail.Videos)
			stats, err := GetVideoPreprocessStats(context.Background(), filter)
			require.NoError(t, err)
			assert.EqualValues(t, 3, stats.TotalRequests)
			assert.EqualValues(t, 1, stats.SuccessCount)
			assert.EqualValues(t, 2, stats.FailureCount)
			assert.Equal(t, VideoPreprocessPercentiles{P50: 20, P95: 30, P99: 30}, stats.Queue)
			assert.Equal(t, VideoPreprocessPercentiles{P50: 200, P95: 300, P99: 300}, stats.Total)
			assert.EqualValues(t, 1, stats.ByCodec["vp9"])
			assert.EqualValues(t, 1, stats.ByFrames[6])
			assert.EqualValues(t, 1, stats.ByError["video_queue_full"])
			assert.InDelta(t, 20.0/3, stats.StageMS["analyze"], 0.00001)
			assert.EqualValues(t, 3000, stats.OutputBodyBytes)
			require.Len(t, stats.Trend, 1)
			assert.EqualValues(t, 3, stats.Trend[0].Total)
			assert.EqualValues(t, 1, stats.Trend[0].Rejected)
			expired := VideoPreprocessLog{RequestId: "video-matrix-expired", Status: "canceled", CreatedAt: now - 8*24*3600}
			require.NoError(t, InsertVideoPreprocessLog(context.Background(), &expired))
			deleted, err := DeleteVideoPreprocessLogsBefore(context.Background(), now-7*24*3600, 1000)
			require.NoError(t, err)
			assert.EqualValues(t, 1, deleted)
			missing, err := GetVideoPreprocessLog(context.Background(), expired.Id)
			require.NoError(t, err)
			assert.Nil(t, missing)
			var remaining int64
			require.NoError(t, DB.Model(&VideoPreprocessLog{}).Where("request_id LIKE ?", "video-matrix-%").Count(&remaining).Error)
			assert.EqualValues(t, 3, remaining)
			require.NoError(t, migrateDB())
			detail, err = GetVideoPreprocessLog(context.Background(), rows[0].Id)
			require.NoError(t, err)
			assert.Equal(t, rows[0].Videos, detail.Videos)
		})
	}
}

// The acceptance script creates these databases using the actual v1.5 source
// archive, including a serialized manifest of its data and indexes.
func TestVideoPreprocessReleasedUpgrade(t *testing.T) {
	for _, tc := range []struct{ name, dsn, path string }{
		{"sqlite", "local", os.Getenv("VIDEO_UPGRADE_SQLITE_PATH")},
		{"mysql", os.Getenv("VIDEO_UPGRADE_MYSQL_DSN"), ""},
		{"postgres", os.Getenv("VIDEO_UPGRADE_POSTGRES_DSN"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.dsn == "" || tc.name == "sqlite" && tc.path == "" {
				t.Skip("released-version database fixture is not configured")
			}
			db := videoLogDatabase(t, tc.dsn, tc.path)
			var manifest Option
			require.NoError(t, db.Where(map[string]any{"key": "video_acceptance_manifest"}).First(&manifest).Error)
			var expected map[string]struct {
				Rows    string   `json:"rows"`
				Indexes []string `json:"indexes"`
			}
			require.NoError(t, common.UnmarshalJsonStr(manifest.Value, &expected))
			for range 2 {
				require.NoError(t, migrateDB())
			}
			for table, want := range expected {
				var data []map[string]any
				tx := db.Table(table)
				if table == "options" {
					tx = tx.Where(map[string]any{"key": "video_acceptance_preserved"})
				} else {
					tx = tx.Where("id = ?", 700001)
				}
				require.NoError(t, tx.Find(&data).Error)
				encoded, err := common.Marshal(data)
				require.NoError(t, err)
				assert.JSONEq(t, want.Rows, string(encoded), "released data in %s must survive", table)
				for _, index := range want.Indexes {
					assert.True(t, db.Migrator().HasIndex(table, index), "released index %s must survive", index)
				}
			}
			require.Error(t, db.Create(&Token{Key: "videoacceptancepreservedkey"}).Error, "released token uniqueness must survive")
			require.Error(t, db.Create(&Option{Key: "video_acceptance_preserved", Value: "duplicate"}).Error, "released option primary key must survive")
			assert.True(t, db.Migrator().HasTable(&VideoPreprocessLog{}))
			entry := VideoPreprocessLog{RequestId: "video-upgrade-new-row", Model: "kimi-k3", Status: "succeeded", CreatedAt: time.Now().Unix()}
			require.NoError(t, InsertVideoPreprocessLog(context.Background(), &entry))
			detail, err := GetVideoPreprocessLog(context.Background(), entry.Id)
			require.NoError(t, err)
			assert.Equal(t, entry.RequestId, detail.RequestId)
		})
	}
}
