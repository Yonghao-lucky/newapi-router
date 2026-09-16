package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The target policy tables back the V1.5 Router scheduling contract:
// (model, group) is unique, and (policy, channel) is unique per candidate.

func TestTargetPolicyUniqueModelGroup(t *testing.T) {
	t.Cleanup(func() {
		DB.Exec("DELETE FROM ai_router_target_policies")
		DB.Exec("DELETE FROM ai_router_target_candidates")
	})
	DB.Exec("DELETE FROM ai_router_target_policies")
	DB.Exec("DELETE FROM ai_router_target_candidates")

	first := &AIRouterTargetPolicy{Model: "kimi-k3", GroupId: "default", Enabled: true, MaxAttempts: 3}
	require.NoError(t, DB.Create(first).Error)

	duplicate := &AIRouterTargetPolicy{Model: "kimi-k3", GroupId: "default", Enabled: true, MaxAttempts: 3}
	require.Error(t, DB.Create(duplicate).Error)

	otherGroup := &AIRouterTargetPolicy{Model: "kimi-k3", GroupId: "vip", Enabled: true, MaxAttempts: 3}
	require.NoError(t, DB.Create(otherGroup).Error)
}

func TestTargetCandidateUniquePolicyChannel(t *testing.T) {
	t.Cleanup(func() {
		DB.Exec("DELETE FROM ai_router_target_policies")
		DB.Exec("DELETE FROM ai_router_target_candidates")
	})
	DB.Exec("DELETE FROM ai_router_target_policies")
	DB.Exec("DELETE FROM ai_router_target_candidates")

	policy := &AIRouterTargetPolicy{Model: "kimi-k3", GroupId: "default", Enabled: true, MaxAttempts: 3}
	require.NoError(t, DB.Create(policy).Error)

	first := &AIRouterTargetCandidate{PolicyId: policy.Id, ChannelId: 42, Enabled: true}
	require.NoError(t, DB.Create(first).Error)

	duplicate := &AIRouterTargetCandidate{PolicyId: policy.Id, ChannelId: 42, Enabled: true}
	require.Error(t, DB.Create(duplicate).Error)

	// The same channel may appear under a different policy.
	other := &AIRouterTargetCandidate{PolicyId: policy.Id + 1, ChannelId: 42, Enabled: true}
	require.NoError(t, DB.Create(other).Error)
}

func TestChannelRoutingProfileMaxRetryDefaultsAndBounds(t *testing.T) {
	// Absent max_retry defaults to zero: one call per channel.
	var absent dto.ChannelOtherSettings
	require.NoError(t, common.UnmarshalJsonStr(`{"router":{"enabled":true}}`, &absent))
	assert.Equal(t, 0, absent.Router.MaxRetry)

	inRange := dto.ChannelOtherSettings{}
	require.NoError(t, common.UnmarshalJsonStr(`{"router":{"enabled":true,"max_retry":3}}`, &inRange))
	assert.Equal(t, 3, inRange.Router.MaxRetry)
	require.NoError(t, inRange.ValidateRouter())

	overLimit := dto.ChannelOtherSettings{}
	require.NoError(t, common.UnmarshalJsonStr(`{"router":{"enabled":true,"max_retry":6}}`, &overLimit))
	require.Error(t, overLimit.ValidateRouter())

	negative := dto.ChannelOtherSettings{}
	require.NoError(t, common.UnmarshalJsonStr(`{"router":{"enabled":true,"max_retry":-1}}`, &negative))
	require.Error(t, negative.ValidateRouter())
}
