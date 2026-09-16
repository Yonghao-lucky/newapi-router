package ai_router

import (
	"fmt"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupPolicyServiceTest(t *testing.T) {
	t.Helper()

	originalDB := model.DB
	originalSettings := ai_router_setting.GetSettings()
	originalMemoryCache := common.MemoryCacheEnabled
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Channel{},
		&model.Ability{},
		&model.AIRouterTargetPolicy{},
		&model.AIRouterTargetCandidate{},
	))
	model.DB = db
	common.MemoryCacheEnabled = true
	model.InitChannelCache()

	// Start from a clean published snapshot; it is package-level state.
	PublishTargetPolicySnapshot(BuildTargetPolicySnapshot(nil))

	settings := ai_router_setting.DefaultSettings()
	settings.TargetModels = []string{"kimi-k3"}
	settings.MaxAttempts = 3
	require.NoError(t, ai_router_setting.Configure(map[string]string{
		"enabled":       "true",
		"target_models": `["kimi-k3"]`,
		"max_attempts":  "3",
	}))

	t.Cleanup(func() {
		model.DB = originalDB
		common.MemoryCacheEnabled = originalMemoryCache
		require.NoError(t, ai_router_setting.Configure(map[string]string{
			"enabled":       fmt.Sprintf("%t", originalSettings.Enabled),
			"target_models": `["kimi-k3"]`,
			"max_attempts":  fmt.Sprintf("%d", originalSettings.MaxAttempts),
		}))
	})
}

func policyTestChannel(t *testing.T, id int, name, group, models string, routerEnabled bool) *model.Channel {
	t.Helper()
	channel := &model.Channel{
		Id:     id,
		Type:   constant.ChannelTypeOpenAI,
		Status: common.ChannelStatusEnabled,
		Name:   name,
		Group:  group,
		Models: models,
	}
	profile := dto.DefaultChannelRoutingProfile()
	profile.Enabled = routerEnabled
	channel.SetOtherSettings(dto.ChannelOtherSettings{Router: &profile})
	require.NoError(t, channel.Insert())
	require.NoError(t, channel.AddAbilities(nil))
	model.InitChannelCache()
	return channel
}

func TestSaveTargetPolicyPublishesSnapshot(t *testing.T) {
	setupPolicyServiceTest(t)
	policyTestChannel(t, 940001, "text-a", "default", "kimi-k3", true)
	policyTestChannel(t, 940002, "text-b", "default", "kimi-k3", true)

	input := TargetPolicyInput{
		Model:       "kimi-k3",
		GroupId:     "default",
		Enabled:     true,
		MaxAttempts: 3,
		Candidates: []TargetPolicyCandidateInput{
			{ChannelID: 940001, Enabled: true},
			{ChannelID: 940002, Enabled: true},
		},
	}
	policy, issues, err := SaveTargetPolicy(input, 0)
	require.NoError(t, err)
	for _, issue := range issues {
		assert.NotEqual(t, "error", issue.Level, "unexpected error issue: %s", issue.Code)
	}
	require.NotZero(t, policy.Id)

	snapshot := GetTargetPolicySnapshot()
	stored := snapshot.GetPolicy("kimi-k3", "default")
	require.NotNil(t, stored)
	assert.Equal(t, policy.Id, stored.Id)
	assert.Len(t, stored.Candidates, 2)
	assert.Equal(t, 3, stored.MaxAttempts)
}

func TestSaveTargetPolicyValidationErrors(t *testing.T) {
	setupPolicyServiceTest(t)
	policyTestChannel(t, 941001, "text-a", "default", "kimi-k3", true)

	// Disabled candidate channel is an error.
	input := TargetPolicyInput{
		Model: "kimi-k3", GroupId: "default", Enabled: true, MaxAttempts: 3,
		Candidates: []TargetPolicyCandidateInput{{ChannelID: 941001, Enabled: true}},
	}
	channel := &model.Channel{}
	require.NoError(t, model.DB.First(channel, 941001).Error)
	channel.Status = common.ChannelStatusManuallyDisabled
	require.NoError(t, model.DB.Model(channel).Update("status", channel.Status).Error)

	_, issues, err := SaveTargetPolicy(input, 0)
	require.Error(t, err)
	foundUnavailable := false
	for _, issue := range issues {
		if issue.Code == "candidate_channel_unavailable" {
			foundUnavailable = true
		}
	}
	assert.True(t, foundUnavailable)

	// The failed save must not have published anything for this policy.
	assert.Nil(t, GetTargetPolicySnapshot().GetPolicy("kimi-k3", "default"))

	// Duplicate (model, group) is rejected.
	policyTestChannel(t, 941002, "text-b", "default", "kimi-k3", true)
	good := TargetPolicyInput{
		Model: "kimi-k3", GroupId: "default", Enabled: true, MaxAttempts: 3,
		Candidates: []TargetPolicyCandidateInput{{ChannelID: 941002, Enabled: true}},
	}
	_, _, err = SaveTargetPolicy(good, 0)
	require.NoError(t, err)
	_, _, err = SaveTargetPolicy(good, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestSaveTargetPolicyWarningsDoNotBlock(t *testing.T) {
	setupPolicyServiceTest(t)
	// Only text channel: missing video and unknown fallback produce warnings,
	// not save failures.
	policyTestChannel(t, 942001, "text-a", "default", "kimi-k3", true)

	input := TargetPolicyInput{
		Model: "kimi-k3", GroupId: "default", Enabled: true, MaxAttempts: 5,
		Candidates: []TargetPolicyCandidateInput{{ChannelID: 942001, Enabled: true}},
	}
	_, issues, err := SaveTargetPolicy(input, 0)
	require.NoError(t, err)
	codes := map[string]bool{}
	for _, issue := range issues {
		codes[issue.Code] = true
		assert.Equal(t, "warning", issue.Level)
	}
	assert.True(t, codes["no_video_channel"])
	assert.True(t, codes["no_unknown_fallback"])
	assert.True(t, codes["max_attempts_exceeds_candidates"])
}

func TestDeleteTargetPolicyRemovesCandidates(t *testing.T) {
	setupPolicyServiceTest(t)
	policyTestChannel(t, 943001, "text-a", "default", "kimi-k3", true)

	input := TargetPolicyInput{
		Model: "kimi-k3", GroupId: "default", Enabled: true, MaxAttempts: 3,
		Candidates: []TargetPolicyCandidateInput{{ChannelID: 943001, Enabled: true}},
	}
	policy, _, err := SaveTargetPolicy(input, 0)
	require.NoError(t, err)
	require.NoError(t, DeleteTargetPolicy(policy.Id))

	var candidateCount int64
	require.NoError(t, model.DB.Model(&model.AIRouterTargetCandidate{}).
		Where("policy_id = ?", policy.Id).Count(&candidateCount).Error)
	assert.Zero(t, candidateCount)
	assert.Nil(t, GetTargetPolicySnapshot().GetPolicy("kimi-k3", "default"))
}

func TestSnapshotImmutability(t *testing.T) {
	policies := []TargetPolicy{
		{Id: 1, Model: "m", GroupId: "default", Enabled: true, MaxAttempts: 3,
			Candidates: []TargetPolicyCandidate{{ChannelID: 10, Enabled: true}}},
	}
	snapshot := BuildTargetPolicySnapshot(policies)

	// Mutating the source slice afterwards must not affect the snapshot.
	policies[0].Candidates[0] = TargetPolicyCandidate{ChannelID: 99, Enabled: false}
	stored := snapshot.GetPolicy("m", "default")
	require.NotNil(t, stored)
	assert.Equal(t, 10, stored.Candidates[0].ChannelID)
	assert.True(t, stored.Candidates[0].Enabled)
}

// The policy candidate set is the Router's admission list: an enabled policy
// with a subset of channels must reduce the pool to exactly those channels,
// and disabled candidate rows must drop their channel even though the
// channel itself is up.
func TestPolicyCandidatesRestrictPool(t *testing.T) {
	setupPolicyServiceTest(t)
	policyTestChannel(t, 947001, "in-a", "default", "kimi-k3", true)
	policyTestChannel(t, 947002, "in-b", "default", "kimi-k3", true)
	policyTestChannel(t, 947003, "outside", "default", "kimi-k3", true)

	input := TargetPolicyInput{
		Model: "kimi-k3", GroupId: "default", Enabled: true, MaxAttempts: 3,
		Candidates: []TargetPolicyCandidateInput{
			{ChannelID: 947001, Enabled: true},
			{ChannelID: 947002, Enabled: false},
		},
	}
	_, _, err := SaveTargetPolicy(input, 0)
	require.NoError(t, err)

	channels := model.GetEnabledChannelsForGroupModel("default", "kimi-k3", nil)
	require.Len(t, channels, 3)

	// Mirror the controller's intersection: only enabled candidates survive.
	policy := GetTargetPolicySnapshot().GetPolicy("kimi-k3", "default")
	require.NotNil(t, policy)
	allowed := map[int]bool{}
	for _, candidate := range policy.Candidates {
		if candidate.Enabled {
			allowed[candidate.ChannelID] = true
		}
	}
	kept := 0
	for _, ch := range channels {
		if allowed[ch.Id] {
			kept++
		}
	}
	assert.Equal(t, 1, kept)
	assert.True(t, allowed[947001])
	assert.False(t, allowed[947002])
	assert.False(t, allowed[947003], "channel outside the policy must not be admitted")
}

func TestMigrateV1TargetPoliciesIdempotent(t *testing.T) {
	setupPolicyServiceTest(t)
	policyTestChannel(t, 944001, "text-a", "default", "kimi-k3", true)
	// A router-disabled channel must not become a candidate.
	policyTestChannel(t, 944002, "text-b", "default", "kimi-k3", false)
	// A channel outside the default group must not become a candidate.
	policyTestChannel(t, 944003, "vip", "vip", "kimi-k3", true)

	require.NoError(t, MigrateV1TargetPolicies())

	stored := GetTargetPolicySnapshot().GetPolicy("kimi-k3", "default")
	require.NotNil(t, stored)
	assert.Equal(t, 3, stored.MaxAttempts)
	require.Len(t, stored.Candidates, 1)
	assert.Equal(t, 944001, stored.Candidates[0].ChannelID)

	// Manual policies survive a re-run, and no duplicates appear.
	input := TargetPolicyInput{
		Model: "gpt-4o", GroupId: "default", Enabled: true, MaxAttempts: 2,
		Candidates: []TargetPolicyCandidateInput{{ChannelID: 944001, Enabled: true}},
	}
	_, _, err := SaveTargetPolicy(input, 0)
	require.NoError(t, err)

	require.NoError(t, MigrateV1TargetPolicies())

	var policyCount int64
	require.NoError(t, model.DB.Model(&model.AIRouterTargetPolicy{}).Count(&policyCount).Error)
	assert.Equal(t, int64(2), policyCount)
}
