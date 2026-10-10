package ai_router

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr(b bool) *bool { return &b }

func fixtureChannels() []RouterChannel {
	return []RouterChannel{
		{ID: 1, Priority: 100, Weight: 1, Group: "default", RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsText: ptr(true)}},
		{ID: 2, Priority: 100, Weight: 1, Group: "default", RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsText: ptr(true), SupportsImage: ptr(true)}},
		{ID: 3, Priority: 50, Weight: 1, Group: "default", RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsText: ptr(true), SupportsImage: ptr(true), SupportsVideo: ptr(true)}},
		{ID: 4, Priority: 100, Weight: 1, Group: "default", RoutingProfile: dto.ChannelRoutingProfile{Enabled: false, SupportsText: ptr(true)}},
	}
}

func TestFilterChannels(t *testing.T) {
	chs := fixtureChannels()
	settings := ai_router_setting.DefaultSettings()
	group := "default"

	tests := []struct {
		name     string
		modality Modality
		wantIDs  []int
	}{
		{"text can pick A/B/C", ModalityText, []int{1, 2, 3}},
		{"image can pick B/C", ModalityImage, []int{2, 3}},
		{"video can pick C only", ModalityVideo, []int{3}},
		{"unknown gets empty", ModalityUnknown, []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			constraints := BuildConstraints(RouteDemand{Modality: tt.modality}, settings)
			got := FilterChannels(chs, constraints, group)
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("got %d channels, want %d", len(got), len(tt.wantIDs))
			}
			for i, c := range got {
				if c.ID != tt.wantIDs[i] {
					t.Errorf("got ID %d at index %d, want %d", c.ID, i, tt.wantIDs[i])
				}
			}
		})
	}
}

func TestThinkingAndMixedContentNeverSelectIncompatibleFallback(t *testing.T) {
	channels := fixtureChannels()
	channels[0].AllowsThinkingDisabled = true
	constraints := BuildConstraints(RouteDemand{Modality: ModalityText, RequireThinkingDisabled: true}, ai_router_setting.DefaultSettings())
	selected := FilterChannels(channels, constraints, "default")
	require.Len(t, selected, 1)
	assert.Equal(t, channels[0].ID, selected[0].ID)
	assert.Empty(t, FilterChannelsWithExclusions(channels, constraints, "default", map[int]bool{channels[0].ID: true}))
	channels[0].AllowsReasoningNone = true
	noneConstraints := BuildConstraints(RouteDemand{Modality: ModalityText, RequireReasoningNone: true}, ai_router_setting.DefaultSettings())
	noneCandidates := FilterChannels(channels, noneConstraints, "default")
	require.Len(t, noneCandidates, 1)
	assert.Equal(t, channels[0].ID, noneCandidates[0].ID)
	assert.Empty(t, FilterChannelsWithExclusions(channels, noneConstraints, "default", map[int]bool{channels[0].ID: true}))
	constraints = BuildConstraints(RouteDemand{Modality: ModalityVideo, UnknownContentTypes: []string{"document"}}, ai_router_setting.DefaultSettings())
	assert.Empty(t, FilterChannels(channels, constraints, "default"))
	channels[2].RoutingProfile.UnknownFallback = true
	selected = FilterChannels(channels, constraints, "default")
	require.Len(t, selected, 1)
	assert.Equal(t, channels[2].ID, selected[0].ID)
}

func TestSelectChannelHighestPriority(t *testing.T) {
	constraints := BuildConstraints(RouteDemand{Modality: ModalityText}, ai_router_setting.DefaultSettings())
	filtered := FilterChannels(fixtureChannels(), constraints, "default")
	decision, err := SelectChannel(filtered, constraints, 0)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Priority != 100 {
		t.Errorf("got priority %d, want 100", decision.Priority)
	}
	if decision.ChannelID != 1 && decision.ChannelID != 2 {
		t.Errorf("got channel %d, want 1 or 2", decision.ChannelID)
	}
}

func TestRouteCandidatesUnknownNoMatch(t *testing.T) {
	_, _, err := RouteCandidates(RouteDemand{Modality: ModalityUnknown}, fixtureChannels(), "default", ai_router_setting.DefaultSettings())
	if err == nil {
		t.Fatal("expected error for unknown modality with no fallback channels")
	}
}

func TestRouterDisabledChannelsExcluded(t *testing.T) {
	chs := fixtureChannels()
	constraints := BuildConstraints(RouteDemand{Modality: ModalityText}, ai_router_setting.DefaultSettings())
	got := FilterChannels(chs, constraints, "default")
	for _, c := range got {
		if c.ID == 4 {
			t.Error("router-disabled channel 4 should be excluded")
		}
	}
}

func TestGroupFilter(t *testing.T) {
	chs := []RouterChannel{
		{ID: 1, Priority: 100, Weight: 1, Group: "default", RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsText: ptr(true)}},
		{ID: 2, Priority: 100, Weight: 1, Group: "vip", RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsText: ptr(true)}},
	}
	constraints := BuildConstraints(RouteDemand{Modality: ModalityText}, ai_router_setting.DefaultSettings())
	got := FilterChannels(chs, constraints, "vip")
	if len(got) != 1 || got[0].ID != 2 {
		t.Errorf("expected only channel 2 in vip group, got %v", got)
	}
}

func TestVideoPriorityRespectsEligibility(t *testing.T) {
	high, low := int64(100), int64(90)
	channels := []RouterChannel{
		{ID: 1, Priority: 100, Group: "default", Weight: 1, RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsText: ptr(true), VideoPriority: &high}},
		{ID: 2, Priority: 90, Group: "vip", Weight: 1, RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsImage: ptr(true), VideoPriority: &high}},
		{ID: 3, Priority: 80, Group: "default", Weight: 1, RoutingProfile: dto.ChannelRoutingProfile{Enabled: false, SupportsImage: ptr(true), VideoPriority: &high}},
		{ID: 4, Priority: 70, Group: "default", Weight: 7, RoutingProfile: dto.ChannelRoutingProfile{Enabled: true, SupportsImage: ptr(true), SupportsVideo: ptr(false), VideoPriority: &low}},
	}
	demand := RouteDemand{Modality: ModalityImage, OriginalModality: ModalityVideo}
	decision, eligible, err := RouteCandidates(demand, channels, "default", ai_router_setting.DefaultSettings())
	require.NoError(t, err)
	require.Len(t, eligible, 1)
	assert.Equal(t, 4, decision.ChannelID)
	assert.Equal(t, 7, decision.Weight)
	assert.Equal(t, int64(70), decision.Priority)
	assert.Equal(t, "video_priority", decision.SelectionReason)
	demand.Modality = ModalityVideo
	_, _, err = RouteCandidates(demand, channels, "default", ai_router_setting.DefaultSettings())
	require.Error(t, err, "native video still requires video capability")
}
