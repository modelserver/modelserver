package proxy

import (
	"testing"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

func TestMatchChannels_ProjectSpecificRoute(t *testing.T) {
	channels := []types.Channel{
		{ID: "ch1", Status: "active", SupportedModels: []string{"claude-sonnet-4"}, SelectionPriority: 0},
		{ID: "ch2", Status: "active", SupportedModels: []string{"claude-sonnet-4"}, SelectionPriority: 1},
		{ID: "ch3", Status: "active", SupportedModels: []string{"claude-opus-4"}, SelectionPriority: 0},
	}
	routes := []types.ChannelRoute{
		{ProjectID: "proj1", ModelPattern: "claude-sonnet-4", ChannelIDs: []string{"ch2"}, MatchPriority: 10, Status: "active"},
	}

	router := NewChannelRouter(channels, routes, nil, nil, 0)
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
		{ProjectID: "", ModelPattern: "*", ChannelIDs: []string{"ch1"}, MatchPriority: 0, Status: "active"},
	}

	router := NewChannelRouter(channels, routes, nil, nil, 0)
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

	router := NewChannelRouter(channels, nil, nil, nil, 0)
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
		{ProjectID: "proj1", ModelPattern: "claude-sonnet-*", ChannelIDs: []string{"ch1"}, MatchPriority: 5, Status: "active"},
	}

	router := NewChannelRouter(channels, routes, nil, nil, 0)
	selected := router.MatchChannels("proj1", "claude-sonnet-4-20250514")

	if len(selected) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(selected))
	}
}

func TestSelectChannelForSession_StoresBinding(t *testing.T) {
	candidates := []types.Channel{
		{ID: "ch1", Weight: 1, SelectionPriority: 0},
	}
	router := NewChannelRouter(nil, nil, nil, nil, time.Hour)

	ch := router.SelectChannelForSession(candidates, "session-1")
	if ch == nil || ch.ID != "ch1" {
		t.Fatalf("expected ch1, got %v", ch)
	}

	// Second call with same session should return same channel.
	ch2 := router.SelectChannelForSession(candidates, "session-1")
	if ch2 == nil || ch2.ID != "ch1" {
		t.Fatalf("expected ch1 on repeat, got %v", ch2)
	}
}

func TestSelectChannelForSession_StickyAcrossCalls(t *testing.T) {
	candidates := []types.Channel{
		{ID: "ch1", Weight: 1, SelectionPriority: 10},
		{ID: "ch2", Weight: 1, SelectionPriority: 0},
	}
	router := NewChannelRouter(nil, nil, nil, nil, time.Hour)

	// First call will select ch1 (highest priority).
	ch := router.SelectChannelForSession(candidates, "session-sticky")
	if ch == nil || ch.ID != "ch1" {
		t.Fatalf("expected ch1, got %v", ch)
	}

	// Even with both candidates, session should stick to ch1.
	for i := 0; i < 10; i++ {
		ch = router.SelectChannelForSession(candidates, "session-sticky")
		if ch.ID != "ch1" {
			t.Fatalf("iteration %d: expected ch1, got %s", i, ch.ID)
		}
	}
}

func TestSelectChannelForSession_ExpiredBinding(t *testing.T) {
	candidates := []types.Channel{
		{ID: "ch1", Weight: 1, SelectionPriority: 0},
	}
	router := NewChannelRouter(nil, nil, nil, nil, 1*time.Millisecond)

	router.SelectChannelForSession(candidates, "session-expire")
	time.Sleep(5 * time.Millisecond)

	// After TTL, binding should be gone and a new selection made.
	ch := router.SelectChannelForSession(candidates, "session-expire")
	if ch == nil || ch.ID != "ch1" {
		t.Fatalf("expected ch1 after expiry, got %v", ch)
	}
}

func TestSelectChannelForSession_BoundChannelNotInCandidates(t *testing.T) {
	router := NewChannelRouter(nil, nil, nil, nil, time.Hour)

	// Bind session to ch1.
	initial := []types.Channel{
		{ID: "ch1", Weight: 1, SelectionPriority: 0},
	}
	router.SelectChannelForSession(initial, "session-removed")

	// Now ch1 is gone, only ch2 available.
	newCandidates := []types.Channel{
		{ID: "ch2", Weight: 1, SelectionPriority: 0},
	}
	ch := router.SelectChannelForSession(newCandidates, "session-removed")
	if ch == nil || ch.ID != "ch2" {
		t.Fatalf("expected ch2 after ch1 removed, got %v", ch)
	}
}

func TestSelectChannelForSession_EmptySessionFallback(t *testing.T) {
	candidates := []types.Channel{
		{ID: "ch1", Weight: 1, SelectionPriority: 0},
	}
	router := NewChannelRouter(nil, nil, nil, nil, time.Hour)

	ch := router.SelectChannelForSession(candidates, "")
	if ch == nil || ch.ID != "ch1" {
		t.Fatalf("expected ch1 for empty session, got %v", ch)
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
