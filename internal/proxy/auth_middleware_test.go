package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

// fakeMemberStore is a test double for the memberStore interface used by
// the membership check. It records call count for cache-hit assertions
// and supports both "not a member" and "error" outcomes.
type fakeMemberStore struct {
	calls  atomic.Int32
	result *types.ProjectMember
	err    error
}

func (f *fakeMemberStore) GetProjectMember(projectID, userID string) (*types.ProjectMember, error) {
	f.calls.Add(1)
	return f.result, f.err
}

// callMembershipCheck invokes the membership-check helper exposed by
// AuthMiddleware. Returns the HTTP status code written and the response
// body. The helper itself is package-private; the test lives in package
// proxy so it can call it directly.
func callMembershipCheck(t *testing.T, ms memberStore, projectID, userID string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	ok := checkMembership(w, ms, projectID, userID)
	if ok {
		return http.StatusOK, ""
	}
	return w.Code, w.Body.String()
}

func TestCheckMembership_PassesForActiveMember(t *testing.T) {
	clearMemberPresentCache()
	ms := &fakeMemberStore{result: &types.ProjectMember{UserID: "u", ProjectID: "p", Role: types.RoleDeveloper}}
	status, _ := callMembershipCheck(t, ms, "p", "u")
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if ms.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", ms.calls.Load())
	}
}

func TestCheckMembership_RejectsRemovedMember(t *testing.T) {
	clearMemberPresentCache()
	ms := &fakeMemberStore{result: nil} // not a member
	status, body := callMembershipCheck(t, ms, "p2", "u2")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", status)
	}
	if !strings.Contains(body, "api key creator is no longer a project member") {
		t.Errorf("body = %q, missing expected message", body)
	}
}

// TestCheckMembership_CachesPositive verifies that two back-to-back
// successful checks hit the DB only once (10s TTL).
func TestCheckMembership_CachesPositive(t *testing.T) {
	clearMemberPresentCache()
	ms := &fakeMemberStore{result: &types.ProjectMember{UserID: "u3", ProjectID: "p3", Role: types.RoleDeveloper}}
	_, _ = callMembershipCheck(t, ms, "p3", "u3")
	_, _ = callMembershipCheck(t, ms, "p3", "u3")
	if ms.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (cache hit)", ms.calls.Load())
	}
}

// TestCheckMembership_CachesNegative verifies the "not a member" answer
// is also cached so we don't pound the DB on every request from an
// already-removed member.
func TestCheckMembership_CachesNegative(t *testing.T) {
	clearMemberPresentCache()
	ms := &fakeMemberStore{result: nil}
	_, _ = callMembershipCheck(t, ms, "p4", "u4")
	_, _ = callMembershipCheck(t, ms, "p4", "u4")
	if ms.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (cache hit on negative)", ms.calls.Load())
	}
}

// TestCheckMembership_FailsClosedOnDBError verifies the security-critical
// divergence from quota/denylist's fail-open posture: transient DB errors
// on the membership check return 503, not 200 or 401.
func TestCheckMembership_FailsClosedOnDBError(t *testing.T) {
	clearMemberPresentCache()
	ms := &fakeMemberStore{err: errors.New("connection reset")}
	status, body := callMembershipCheck(t, ms, "p5", "u5")
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", status)
	}
	if !strings.Contains(body, "membership check unavailable") {
		t.Errorf("body = %q, missing expected message", body)
	}
	// Error must NOT be cached — the next attempt should retry.
	ms.calls.Store(0)
	ms.err = nil
	ms.result = &types.ProjectMember{UserID: "u5", ProjectID: "p5"}
	_, _ = callMembershipCheck(t, ms, "p5", "u5")
	if ms.calls.Load() != 1 {
		t.Errorf("expected retry after error; calls = %d", ms.calls.Load())
	}
}

// silence unused-import linter when context isn't otherwise referenced.
var _ = context.Background
