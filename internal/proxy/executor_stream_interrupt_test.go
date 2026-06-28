package proxy

import (
	"errors"
	"strings"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

// TestCompleteStreamingRequest_InterruptSetsError verifies that when the
// metrics callback fires with a non-nil InterruptErr, the synthesised
// types.Request has Status=error and ErrorMessage prefixed with
// "stream_interrupted:". Drives the building branch of
// completeStreamingRequest in isolation by constructing the same fields
// the function does and asserting on them.
//
// We exercise the production code path indirectly through the helper
// requestStatusFromMetrics, avoiding a fake store roundtrip.
func TestCompleteStreamingRequest_InterruptSetsError(t *testing.T) {
	metrics := StreamMetrics{
		Model:        "claude-opus-4-8",
		InterruptErr: errors.New("write tcp 1.2.3.4: broken pipe"),
	}
	status, msg := requestStatusFromMetrics(metrics)
	if status != types.RequestStatusError {
		t.Errorf("status = %q, want %q", status, types.RequestStatusError)
	}
	if !strings.HasPrefix(msg, "stream_interrupted: ") {
		t.Errorf("error_message = %q, want prefix %q", msg, "stream_interrupted: ")
	}
	if !strings.Contains(msg, "broken pipe") {
		t.Errorf("error_message = %q, want it to include the underlying error", msg)
	}
}

// TestCompleteStreamingRequest_CleanStreamRecordsSuccess is the regression
// guard for normal completions — must remain Status=success with no
// error_message.
func TestCompleteStreamingRequest_CleanStreamRecordsSuccess(t *testing.T) {
	metrics := StreamMetrics{
		Model:        "claude-opus-4-8",
		OutputTokens: 100,
		TTFTMs:       42,
	}
	status, msg := requestStatusFromMetrics(metrics)
	if status != types.RequestStatusSuccess {
		t.Errorf("status = %q, want %q", status, types.RequestStatusSuccess)
	}
	if msg != "" {
		t.Errorf("error_message = %q, want empty", msg)
	}
}
