package model

import (
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func insertAIRouterLog(t *testing.T, log *AIRouterLog) {
	t.Helper()
	require.NoError(t, DB.Create(log).Error)
}

// The fallback filter separates single-channel chains from multi-channel
// (fallback) chains; the channel filter matches any position of the attempt
// chain including single-element arrays.
func TestGetAIRouterLogsFallbackAndChannelFilters(t *testing.T) {
	truncateTables(t)
	base := int64(1758000000)
	insertAIRouterLog(t, &AIRouterLog{RequestId: "r1", Model: "m", Attempts: 1, SelectedChannels: "[4]", FinalChannelId: 4, Success: true, CreatedAt: base})
	insertAIRouterLog(t, &AIRouterLog{RequestId: "r2", Model: "m", Attempts: 3, SelectedChannels: "[4,9,10]", FinalChannelId: 10, Success: true, CreatedAt: base + 1})
	insertAIRouterLog(t, &AIRouterLog{RequestId: "r3", Model: "m", Attempts: 3, SelectedChannels: "[14,15]", FinalChannelId: 15, Success: false, CreatedAt: base + 2})
	insertAIRouterLog(t, &AIRouterLog{RequestId: "r4", Model: "m", Attempts: 0, SelectedChannels: "", FinalChannelId: 0, Success: true, CreatedAt: base + 3})

	_, fallbackTrue, err := GetAIRouterLogs(0, 0, "m", 0, "", nil, "", "true", 0, 0, "", nil, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), fallbackTrue, "attempts>1 rows are fallback")

	_, fallbackFalse, err := GetAIRouterLogs(0, 0, "m", 0, "", nil, "", "false", 0, 0, "", nil, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), fallbackFalse, "attempts<=1 rows are non-fallback")

	_, ch4, err := GetAIRouterLogs(0, 0, "m", 0, "", nil, "", "", 4, 0, "", nil, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), ch4, "channel 4 appears in two chains")

	_, ch14, err := GetAIRouterLogs(0, 0, "m", 0, "", nil, "", "", 14, 0, "", nil, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(1), ch14, "channel 14 must not match channel 4 chains")
	_, ch1, err := GetAIRouterLogs(0, 0, "m", 0, "", nil, "", "", 1, 0, "", nil, 1, 20)
	require.NoError(t, err)
	assert.Zero(t, ch1, "channel 1 must not match channel 14 via substring")

	_, final10, err := GetAIRouterLogs(0, 0, "m", 0, "", nil, "", "", 10, 0, "", nil, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(1), final10)
}

// Stats expose the fallback count/rate next to success metrics; trend buckets
// split success/failure per bucket; empty ranges return no points.
func TestGetAIRouterStatsFallbackAndTrend(t *testing.T) {
	truncateTables(t)
	start := int64(1758000000) // aligned to an hour
	insertAIRouterLog(t, &AIRouterLog{RequestId: "a", Model: "m", Attempts: 1, SelectedChannels: "[4]", FinalChannelId: 4, Success: true, CreatedAt: start + 10})
	insertAIRouterLog(t, &AIRouterLog{RequestId: "b", Model: "m", Attempts: 2, SelectedChannels: "[4,9]", FinalChannelId: 9, Success: true, CreatedAt: start + 20})
	insertAIRouterLog(t, &AIRouterLog{RequestId: "c", Model: "m", Attempts: 2, SelectedChannels: "[4,9]", FinalChannelId: 9, Success: false, CreatedAt: start + 3600 + 30})

	stats, err := GetAIRouterStats(start, start+7200, "m", "", AIRouterRequestCategoryAll)
	require.NoError(t, err)
	assert.Equal(t, int64(3), stats.TotalRequests)
	assert.Equal(t, int64(2), stats.FallbackCount)
	assert.InDelta(t, 2.0/3.0, stats.FallbackRate, 0.0001)
	assert.InDelta(t, 2.0/3.0, stats.SuccessRate, 0.0001)

	trend, err := GetAIRouterTrend(start, start+7200, "m", "", AIRouterRequestCategoryAll)
	require.NoError(t, err)
	require.Len(t, trend, 2)
	assert.Equal(t, int64(2), trend[0].Total)
	assert.Equal(t, int64(2), trend[0].Success)
	assert.Equal(t, int64(1), trend[1].Total)
	assert.Zero(t, trend[1].Success)
	assert.Equal(t, start, trend[0].Time)

	empty, err := GetAIRouterTrend(start+7200, start+14400, "m", "", AIRouterRequestCategoryAll)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// Per-channel aggregates count every chain that includes the channel, with
// final/success attribution from final_channel_id.
func TestGetAIRouterChannelStatsAggregates(t *testing.T) {
	truncateTables(t)
	start := int64(1758000000)
	mk := func(id string, chain string, final int, success bool, duration int64, ttft int64) *AIRouterLog {
		return &AIRouterLog{RequestId: id, Model: "m", Attempts: 1, SelectedChannels: chain,
			FinalChannelId: final, Success: success, TotalDurationMs: duration, TTFTMs: ttft, CreatedAt: start}
	}
	insertAIRouterLog(t, mk("a", "[4,9]", 9, true, 1000, 200))
	insertAIRouterLog(t, mk("b", "[4,9]", 9, false, 2000, 0))
	insertAIRouterLog(t, mk("c", "[4]", 4, true, 300, 100))
	insertAIRouterLog(t, mk("d", "[8]", 8, true, 500, 0))

	stats, err := GetAIRouterChannelStats(start, start+3600, "m", "", AIRouterRequestCategoryAll)
	require.NoError(t, err)
	byID := map[int]AIRouterChannelStat{}
	for _, s := range stats {
		byID[s.ChannelId] = s
	}

	ch4 := byID[4]
	assert.Equal(t, int64(3), ch4.Attempts, "channel 4 is on three chains")
	assert.Equal(t, int64(1), ch4.Finals)
	assert.Equal(t, int64(1), ch4.SuccessFinals)
	assert.InDelta(t, 300.0, ch4.AvgDurationMs, 0.001)

	ch9 := byID[9]
	assert.Equal(t, int64(2), ch9.Attempts)
	assert.Equal(t, int64(2), ch9.Finals)
	assert.Equal(t, int64(1), ch9.SuccessFinals)
	assert.InDelta(t, 0.5, ch9.SuccessRate, 0.0001)
	assert.InDelta(t, 1500.0, ch9.AvgDurationMs, 0.001)
	assert.InDelta(t, 200.0, ch9.AvgTTFTMs, 0.001, "zero TTFT rows are excluded from the TTFT average")

	ch8 := byID[8]
	assert.Equal(t, int64(1), ch8.Attempts)

	// Model scoping excludes other models' chains.
	other := &AIRouterLog{RequestId: "x", Model: "other", SelectedChannels: "[4]", FinalChannelId: 4, Success: true, CreatedAt: start}
	insertAIRouterLog(t, other)
	scoped, err := GetAIRouterChannelStats(start, start+3600, "m", "", AIRouterRequestCategoryAll)
	require.NoError(t, err)
	scopedByID := map[int]AIRouterChannelStat{}
	for _, s := range scoped {
		scopedByID[s.ChannelId] = s
	}
	assert.Equal(t, int64(3), scopedByID[4].Attempts, "the other-model chain must not be counted under model=m")
}

func TestAIRouterMonitoringRequestCategory(t *testing.T) {
	truncateTables(t)
	verifyAIRouterMonitoringRequestCategory(t)
}

func verifyAIRouterMonitoringRequestCategory(t *testing.T) {
	t.Helper()
	start := int64(1758000000)
	logs := []*AIRouterLog{
		{RequestId: "video-ok", Model: "m", GroupId: "g", Modality: "video", Attempts: 1, SelectedChannels: "[1]", FinalChannelId: 1, Success: true, TotalDurationMs: 100, TTFTMs: 50, CreatedAt: start + 1},
		{RequestId: "video-fail", Model: "m", GroupId: "g", Modality: "video", Attempts: 3, SelectedChannels: "[1,2]", FinalChannelId: 2, Success: false, TotalDurationMs: 300, CreatedAt: start + 2},
		{RequestId: "text-ok", Model: "m", GroupId: "g", Modality: "text", Attempts: 1, SelectedChannels: "[3]", FinalChannelId: 3, Success: true, TotalDurationMs: 500, TTFTMs: 150, CreatedAt: start + 3},
		{RequestId: "image-ok", Model: "m", GroupId: "g", Modality: "image", Attempts: 1, SelectedChannels: "[4]", FinalChannelId: 4, Success: true, TotalDurationMs: 700, CreatedAt: start + 4},
		{RequestId: "unknown", Model: "m", GroupId: "g", Modality: "unknown", Attempts: 1, SelectedChannels: "[5]", FinalChannelId: 5, Success: false, TotalDurationMs: 900, CreatedAt: start + 5},
	}
	for _, log := range logs {
		insertAIRouterLog(t, log)
	}
	require.NoError(t, InsertAIRouterAttemptLogs([]*AIRouterAttemptLog{
		{RequestLogId: logs[0].Id, ChannelId: 1, Modality: "video", ErrorCategory: AttemptErrorCategorySuccess, Success: true, DurationMs: 100, CreatedAt: start + 1},
		{RequestLogId: logs[1].Id, ChannelId: 1, Modality: "video", ErrorCategory: AttemptErrorCategory429, HttpStatusCode: intPtrM(429), DurationMs: 100, CreatedAt: start + 2},
		{RequestLogId: logs[1].Id, ChannelId: 1, Modality: "video", ErrorCategory: AttemptErrorCategory429, HttpStatusCode: intPtrM(429), DurationMs: 100, CreatedAt: start + 2},
		{RequestLogId: logs[1].Id, ChannelId: 2, Modality: "video", ErrorCategory: AttemptErrorCategory5xx, HttpStatusCode: intPtrM(503), DurationMs: 200, CreatedAt: start + 2},
		{RequestLogId: logs[2].Id, ChannelId: 3, Modality: "text", ErrorCategory: AttemptErrorCategorySuccess, Success: true, DurationMs: 500, CreatedAt: start + 3},
		{RequestLogId: logs[3].Id, ChannelId: 4, Modality: "image", ErrorCategory: AttemptErrorCategoryOther, DurationMs: 700, CreatedAt: start + 4},
		{RequestLogId: logs[4].Id, ChannelId: 5, Modality: "unknown", ErrorCategory: AttemptErrorCategoryConnection, DurationMs: 900, CreatedAt: start + 5},
	}))

	stats, err := GetAIRouterStats(start, start+10, "m", "g", AIRouterRequestCategoryVideo)
	require.NoError(t, err)
	assert.Equal(t, int64(2), stats.TotalRequests)
	assert.Equal(t, int64(1), stats.SuccessCount)
	assert.InDelta(t, 0.5, stats.SuccessRate, 0.0001)
	assert.InDelta(t, 200, stats.AvgDurationMs, 0.0001)
	assert.InDelta(t, 50, stats.AvgTTFTMs, 0.0001)
	assert.InDelta(t, 2, stats.AvgAttempts, 0.0001)

	trend, err := GetAIRouterTrend(start, start+10, "m", "g", AIRouterRequestCategoryVideo)
	require.NoError(t, err)
	require.Len(t, trend, 1)
	assert.Equal(t, int64(2), trend[0].Total)
	assert.Equal(t, int64(1), trend[0].Success)

	channels, err := GetAIRouterChannelStats(start, start+10, "m", "g", AIRouterRequestCategoryVideo)
	require.NoError(t, err)
	require.Len(t, channels, 2)
	attempts, err := GetAIRouterAttemptChannelStats(start, start+10, "m", "g", AIRouterRequestCategoryVideo)
	require.NoError(t, err)
	require.Len(t, attempts, 2)
	assert.Nil(t, attempts[3], "text attempts must not enter the video aggregate")

	errors, err := GetAIRouterErrorDistribution(start, start+10, "m", "g", AIRouterRequestCategoryVideo)
	require.NoError(t, err)
	assert.Equal(t, []AIRouterErrorCategoryStat{{ErrorCategory: AttemptErrorCategory429, Count: 2}, {ErrorCategory: AttemptErrorCategory5xx, Count: 1}}, errors.ByCategory)

	textImage, err := GetAIRouterStats(start, start+10, "m", "g", AIRouterRequestCategoryTextImage)
	require.NoError(t, err)
	assert.Equal(t, int64(2), textImage.TotalRequests)
	assert.Equal(t, int64(2), textImage.SuccessCount)
	assert.NotContains(t, textImage.ByModality, "unknown")
}

func TestAIRouterMonitoringRequestCategoryDatabaseMatrix(t *testing.T) {
	for _, test := range []struct {
		name      string
		dsn       string
		dbType    common.DatabaseType
		dialector func(string) gorm.Dialector
	}{
		{name: "mysql", dsn: os.Getenv("AI_ROUTER_TEST_MYSQL_DSN"), dbType: common.DatabaseTypeMySQL, dialector: func(dsn string) gorm.Dialector { return mysql.Open(dsn) }},
		{name: "postgres", dsn: os.Getenv("AI_ROUTER_TEST_POSTGRES_DSN"), dbType: common.DatabaseTypePostgreSQL, dialector: func(dsn string) gorm.Dialector { return postgres.Open(dsn) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			dsn := strings.TrimSpace(test.dsn)
			if dsn == "" {
				t.Skipf("AI Router %s test DSN is not configured", test.name)
			}
			db, err := gorm.Open(test.dialector(dsn), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(&AIRouterLog{}, &AIRouterAttemptLog{}))

			previousDB, previousLogDB := DB, LOG_DB
			previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
			DB, LOG_DB = db, db
			common.SetDatabaseTypes(test.dbType, test.dbType)
			defer func() {
				DB, LOG_DB = previousDB, previousLogDB
				common.SetDatabaseTypes(previousMainType, previousLogType)
				sqlDB, sqlErr := db.DB()
				if sqlErr == nil {
					require.NoError(t, sqlDB.Close())
				}
			}()

			verifyAIRouterMonitoringRequestCategory(t)
		})
	}
}
