package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The error charts are built from failed attempts only, and must obey the
// same model/group filter as the summary-level channel stats — otherwise a
// model-filtered dashboard shows other models' errors.
func TestGetAIRouterErrorDistribution(t *testing.T) {
	truncateTables(t)

	// Model m1: 429 + 503 + connection (no status) failures.
	summary1 := &AIRouterLog{RequestId: "d1", Model: "m1", GroupId: "g1", Attempts: 3, SelectedChannels: "[4,5,9]", FinalChannelId: 9, Success: false, CreatedAt: 1726500000}
	require.NoError(t, DB.Create(summary1).Error)
	// Model m2: one 5xx failure.
	summary2 := &AIRouterLog{RequestId: "d2", Model: "m2", GroupId: "g2", Attempts: 1, SelectedChannels: "[7]", FinalChannelId: 7, Success: false, CreatedAt: 1726500001}
	require.NoError(t, DB.Create(summary2).Error)

	require.NoError(t, InsertAIRouterAttemptLogs([]*AIRouterAttemptLog{
		{RequestLogId: summary1.Id, ChannelId: 4, ErrorCategory: AttemptErrorCategory429, HttpStatusCode: intPtrM(429), DurationMs: 10, CreatedAt: 1726500000},
		{RequestLogId: summary1.Id, ChannelId: 5, ErrorCategory: AttemptErrorCategory5xx, HttpStatusCode: intPtrM(503), DurationMs: 20, CreatedAt: 1726500001},
		{RequestLogId: summary1.Id, ChannelId: 9, ErrorCategory: AttemptErrorCategoryConnection, DurationMs: 30, CreatedAt: 1726500002},
		{RequestLogId: summary1.Id, ChannelId: 4, ErrorCategory: AttemptErrorCategorySuccess, HttpStatusCode: intPtrM(200), Success: true, DurationMs: 40, CreatedAt: 1726500003},
		{RequestLogId: summary2.Id, ChannelId: 7, ErrorCategory: AttemptErrorCategory5xx, HttpStatusCode: intPtrM(500), DurationMs: 50, CreatedAt: 1726500004},
	}))

	all, err := GetAIRouterErrorDistribution(0, 0, "", "", AIRouterRequestCategoryAll)
	require.NoError(t, err)
	require.Equal(t, []AIRouterErrorCategoryStat{
		{ErrorCategory: AttemptErrorCategory5xx, Count: 2},
		{ErrorCategory: AttemptErrorCategory429, Count: 1},
		{ErrorCategory: AttemptErrorCategoryConnection, Count: 1},
	}, all.ByCategory, "success rows are excluded, ordered by count desc")
	require.Equal(t, []AIRouterStatusCodeStat{
		{HttpStatusCode: 429, Count: 1},
		{HttpStatusCode: 500, Count: 1},
		{HttpStatusCode: 503, Count: 1},
	}, all.ByStatusCode, "status chart orders ascending and skips NULL statuses")

	scoped, err := GetAIRouterErrorDistribution(0, 0, "m1", "", AIRouterRequestCategoryAll)
	require.NoError(t, err)
	require.Equal(t, []AIRouterErrorCategoryStat{
		{ErrorCategory: AttemptErrorCategory429, Count: 1},
		{ErrorCategory: AttemptErrorCategory5xx, Count: 1},
		{ErrorCategory: AttemptErrorCategoryConnection, Count: 1},
	}, scoped.ByCategory)
	require.Equal(t, []AIRouterStatusCodeStat{
		{HttpStatusCode: 429, Count: 1},
		{HttpStatusCode: 503, Count: 1},
	}, scoped.ByStatusCode)

	byGroup, err := GetAIRouterErrorDistribution(0, 0, "", "g2", AIRouterRequestCategoryAll)
	require.NoError(t, err)
	require.Equal(t, []AIRouterErrorCategoryStat{{ErrorCategory: AttemptErrorCategory5xx, Count: 1}}, byGroup.ByCategory)

	// Unknown model: no bars, no error.
	none, err := GetAIRouterErrorDistribution(0, 0, "nope", "", AIRouterRequestCategoryAll)
	require.NoError(t, err)
	require.Empty(t, none.ByCategory)
	require.Empty(t, none.ByStatusCode)

	// The same scope must apply to the channel table: channel 7 only exists
	// under m2, so an m1-filtered aggregate cannot see it.
	m1Channels, err := GetAIRouterAttemptChannelStats(0, 0, "m1", "", AIRouterRequestCategoryAll)
	require.NoError(t, err)
	require.Len(t, m1Channels, 3)
	require.Nil(t, m1Channels[7])
	require.Equal(t, int64(1), m1Channels[4].SuccessCount)
	require.Equal(t, int64(1), m1Channels[4].Count429)
}
