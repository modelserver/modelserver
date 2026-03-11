package compensate

import (
	"testing"
	"time"
)

func TestBackoffDuration(t *testing.T) {
	tests := []struct {
		retries int
		minWait time.Duration
	}{
		{0, 30 * time.Second},
		{1, 60 * time.Second},
		{2, 120 * time.Second},
		{5, 960 * time.Second},
	}
	for _, tt := range tests {
		got := backoffDuration(tt.retries)
		if got < tt.minWait {
			t.Errorf("backoffDuration(%d) = %v, want >= %v", tt.retries, got, tt.minWait)
		}
	}
}
