package ai_router

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ai_router_setting"
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
