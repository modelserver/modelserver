package proxy

import (
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func TestMatchChannels_ProjectSpecificRoute(t *testing.T) {
	channels := []types.Channel{
		{ID: "ch1", Status: "active", SupportedModels: []string{"claude-sonnet-4"}, SelectionPriority: 0},
		{ID: "ch2", Status: "active", SupportedModels: []string{"claude-sonnet-4"}, SelectionPriority: 1},
		{ID: "ch3", Status: "active", SupportedModels: []string{"claude-opus-4"}, SelectionPriority: 0},
	}
	routes := []types.ChannelRoute{
		{ProjectID: "proj1", ModelPattern: "claude-sonnet-4", ChannelIDs: []string{"ch2"}, MatchPriority: 10, Enabled: true},
	}

	router := NewChannelRouter(channels, routes, nil, nil)
	selected := router.MatchChannels("proj1", "claude-sonnet-4")

	if len(selected) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(selected))
	}
	if selected[0].ID != "ch2" {
		t.Errorf("expected ch2, got %s", selected[0].ID)
	}
}

func TestMatchChannels_GlobalRoute(t *testing.T) {
	channels := []types.Channel{
		{ID: "ch1", Status: "active", SupportedModels: []string{"claude-sonnet-4"}, SelectionPriority: 0},
		{ID: "ch2", Status: "active", SupportedModels: []string{"claude-sonnet-4"}, SelectionPriority: 0},
	}
	routes := []types.ChannelRoute{
		{ProjectID: "", ModelPattern: "*", ChannelIDs: []string{"ch1"}, MatchPriority: 0, Enabled: true},
	}

	router := NewChannelRouter(channels, routes, nil, nil)
	selected := router.MatchChannels("any-project", "claude-sonnet-4")

	if len(selected) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(selected))
	}
	if selected[0].ID != "ch1" {
		t.Errorf("expected ch1, got %s", selected[0].ID)
	}
}

func TestMatchChannels_FallbackToAllActive(t *testing.T) {
	channels := []types.Channel{
		{ID: "ch1", Status: "active", SupportedModels: []string{"claude-sonnet-4"}, SelectionPriority: 0},
		{ID: "ch2", Status: "disabled", SupportedModels: []string{"claude-sonnet-4"}, SelectionPriority: 0},
	}

	router := NewChannelRouter(channels, nil, nil, nil)
	selected := router.MatchChannels("proj1", "claude-sonnet-4")

	if len(selected) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(selected))
	}
	if selected[0].ID != "ch1" {
		t.Errorf("expected ch1, got %s", selected[0].ID)
	}
}

func TestMatchChannels_WildcardPattern(t *testing.T) {
	channels := []types.Channel{
		{ID: "ch1", Status: "active", SupportedModels: []string{"claude-sonnet-4-20250514"}, SelectionPriority: 0},
	}
	routes := []types.ChannelRoute{
		{ProjectID: "proj1", ModelPattern: "claude-sonnet-*", ChannelIDs: []string{"ch1"}, MatchPriority: 5, Enabled: true},
	}

	router := NewChannelRouter(channels, routes, nil, nil)
	selected := router.MatchChannels("proj1", "claude-sonnet-4-20250514")

	if len(selected) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(selected))
	}
}

func TestSelectChannel_WeightedPriority(t *testing.T) {
	channels := []types.Channel{
		{ID: "low", Weight: 1, SelectionPriority: 0},
		{ID: "high", Weight: 1, SelectionPriority: 10},
	}

	selected := SelectChannel(channels)
	if selected == nil {
		t.Fatal("expected a channel")
	}
	if selected.ID != "high" {
		t.Errorf("expected 'high' (priority 10), got %q", selected.ID)
	}
}
