package ratelimit

import (
	"testing"
	"time"
)

func TestWindowStartTimeAt_Fixed_Normal(t *testing.T) {
	anchor := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 12, 14, 0, 0, 0, time.UTC)
	got := WindowStartTimeAt(now, "7d", "fixed", &anchor)
	want := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("WindowStartTimeAt = %v, want %v", got, want)
	}
}

func TestWindowStartTimeAt_Fixed_SecondWindow(t *testing.T) {
	anchor := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)
	got := WindowStartTimeAt(now, "7d", "fixed", &anchor)
	want := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("WindowStartTimeAt = %v, want %v", got, want)
	}
}

func TestWindowStartTimeAt_Fixed_ExactBoundary(t *testing.T) {
	anchor := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	got := WindowStartTimeAt(now, "7d", "fixed", &anchor)
	want := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("WindowStartTimeAt = %v, want %v", got, want)
	}
}

func TestWindowStartTimeAt_Fixed_AnchorInFuture(t *testing.T) {
	anchor := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC)
	got := WindowStartTimeAt(now, "7d", "fixed", &anchor)
	if !got.Equal(anchor) {
		t.Errorf("WindowStartTimeAt = %v, want %v (anchor)", got, anchor)
	}
}

func TestWindowStartTimeAt_Fixed_NilAnchor(t *testing.T) {
	now := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)
	got := WindowStartTimeAt(now, "7d", "fixed", nil)
	want := now.Add(-7 * 24 * time.Hour)
	if !got.Equal(want) {
		t.Errorf("WindowStartTimeAt nil anchor = %v, want %v", got, want)
	}
}

func TestWindowStartTimeAt_Fixed_HourInterval(t *testing.T) {
	anchor := time.Date(2026, 3, 10, 14, 30, 0, 0, time.UTC)
	now := time.Date(2026, 3, 10, 19, 15, 0, 0, time.UTC)
	got := WindowStartTimeAt(now, "5h", "fixed", &anchor)
	want := time.Date(2026, 3, 10, 14, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("WindowStartTimeAt = %v, want %v", got, want)
	}
}

func TestWindowStartTimeAt_Sliding_Unchanged(t *testing.T) {
	now := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)
	got := WindowStartTimeAt(now, "5h", "sliding", nil)
	want := now.Add(-5 * time.Hour)
	if !got.Equal(want) {
		t.Errorf("sliding unchanged: got %v, want %v", got, want)
	}
}

func TestWindowStartTimeAt_Calendar_Weekly(t *testing.T) {
	now := time.Date(2026, 3, 18, 15, 0, 0, 0, time.UTC) // Wednesday
	got := WindowStartTimeAt(now, "1w", "calendar", nil)
	want := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC) // Monday
	if !got.Equal(want) {
		t.Errorf("calendar 1w: got %v, want %v", got, want)
	}
}

func TestWindowStartTimeAt_Calendar_Monthly(t *testing.T) {
	now := time.Date(2026, 3, 18, 15, 0, 0, 0, time.UTC)
	got := WindowStartTimeAt(now, "1M", "calendar", nil)
	want := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("calendar 1M: got %v, want %v", got, want)
	}
}

func TestWindowResetDurationAt_Fixed(t *testing.T) {
	anchor := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC)
	got := WindowResetDurationAt(now, "7d", "fixed", &anchor)
	want := 5 * 24 * time.Hour
	if got != want {
		t.Errorf("WindowResetDurationAt = %v, want %v", got, want)
	}
}

func TestWindowResetDurationAt_Fixed_AtBoundary(t *testing.T) {
	anchor := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	got := WindowResetDurationAt(now, "7d", "fixed", &anchor)
	want := 7 * 24 * time.Hour
	if got != want {
		t.Errorf("WindowResetDurationAt at boundary = %v, want %v", got, want)
	}
}

func TestWindowResetDurationAt_Sliding_Unchanged(t *testing.T) {
	now := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)
	got := WindowResetDurationAt(now, "5h", "sliding", nil)
	want := 5 * time.Hour
	if got != want {
		t.Errorf("sliding unchanged: got %v, want %v", got, want)
	}
}

func TestParseDurationStr(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"5h", 5 * time.Hour},
		{"1d", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"2w", 2 * 7 * 24 * time.Hour},
		{"30m", 30 * time.Minute},
		{"1M", time.Hour}, // month strings fallback to 1h
	}
	for _, tt := range tests {
		got := ParseDurationStr(tt.input)
		if got != tt.want {
			t.Errorf("ParseDurationStr(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
