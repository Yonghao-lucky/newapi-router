package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The AI Router must enumerate the same candidates New API would use for a
// model: a group-wide scan hands it channels that do not serve the requested
// model at all, which is how a real site ends up routing every request to a
// channel that cannot answer it.

func TestGetEnabledChannelsForGroupModelFiltersByModelFromCache(t *testing.T) {
	originalMemoryCache := common.MemoryCacheEnabled
	originalChannels := channelsIDM
	originalGroups := group2model2channels
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		common.MemoryCacheEnabled = originalMemoryCache
		channelsIDM = originalChannels
		group2model2channels = originalGroups
	})

	channelsIDM = map[int]*Channel{
		930001: {Id: 930001, Status: common.ChannelStatusEnabled, Models: "gpt-4o", Group: "default"},
		930002: {Id: 930002, Status: common.ChannelStatusEnabled, Models: "kimi-k3", Group: "default"},
	}
	group2model2channels = map[string]map[string][]int{
		"default": {
			"gpt-4o":  {930001},
			"kimi-k3": {930002},
		},
	}

	got := GetEnabledChannelsForGroupModel("default", "kimi-k3", nil)
	require.Len(t, got, 1)
	assert.Equal(t, 930002, got[0].Id)
}

func TestGetEnabledChannelsForGroupModelFiltersByModelFromDB(t *testing.T) {
	originalMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() {
		common.MemoryCacheEnabled = originalMemoryCache
		DB.Exec("DELETE FROM abilities")
		DB.Exec("DELETE FROM channels")
	})
	DB.Exec("DELETE FROM abilities")
	DB.Exec("DELETE FROM channels")

	other := &Channel{Id: 930101, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "other", Models: "gpt-4o", Group: "default"}
	target := &Channel{Id: 930102, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Name: "target", Models: "kimi-k3", Group: "default"}
	require.NoError(t, other.Insert())
	require.NoError(t, target.Insert())

	got := GetEnabledChannelsForGroupModel("default", "kimi-k3", nil)
	require.Len(t, got, 1)
	assert.Equal(t, 930102, got[0].Id)

	// A model nobody serves must yield no candidates rather than the group.
	assert.Empty(t, GetEnabledChannelsForGroupModel("default", "kimi-k3-vision", nil))
}
