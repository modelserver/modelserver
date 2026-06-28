package store

import (
	"context"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

// seedAPIKey inserts an api_key row with the given (project, creator, status)
// and returns its ID. Bypasses any handler-level checks — for store tests only.
func seedAPIKey(t *testing.T, st *Store, projectID, createdBy, status string) string {
	t.Helper()
	var id string
	if err := st.pool.QueryRow(context.Background(), `
		INSERT INTO api_keys (project_id, created_by, key_hash, key_suffix, name, status)
		VALUES ($1, $2, gen_random_uuid()::text, '', 'test-key', $3)
		RETURNING id`, projectID, createdBy, status).Scan(&id); err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	return id
}

func seedSecondUser(t *testing.T, st *Store, label string) string {
	t.Helper()
	var id string
	if err := st.pool.QueryRow(context.Background(), `
		INSERT INTO users (email) VALUES ($1 || '-' || gen_random_uuid()::text || '@test.local')
		RETURNING id`, label).Scan(&id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

func addMember(t *testing.T, st *Store, projectID, userID, role string) {
	t.Helper()
	if _, err := st.pool.Exec(context.Background(), `
		INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)`,
		projectID, userID, role); err != nil {
		t.Fatalf("add member: %v", err)
	}
}

// TestRemoveProjectMember_RevokesActiveKeys covers the happy path: member +
// 3 active keys + 1 already-revoked key, in the same project. Removal
// must (a) return count == 3, (b) flip all 4 keys to revoked, (c) delete
// the member row.
func TestRemoveProjectMember_RevokesActiveKeys(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	owner, projectID := seedUserAndProject(t, st)
	addMember(t, st, projectID, owner, types.RoleOwner)

	member := seedSecondUser(t, st, "tomove")
	addMember(t, st, projectID, member, types.RoleDeveloper)

	k1 := seedAPIKey(t, st, projectID, member, types.APIKeyStatusActive)
	k2 := seedAPIKey(t, st, projectID, member, types.APIKeyStatusActive)
	k3 := seedAPIKey(t, st, projectID, member, types.APIKeyStatusActive)
	k4 := seedAPIKey(t, st, projectID, member, types.APIKeyStatusRevoked)

	revoked, err := st.RemoveProjectMember(projectID, member)
	if err != nil {
		t.Fatalf("RemoveProjectMember: %v", err)
	}
	if revoked != 3 {
		t.Errorf("revoked count = %d, want 3", revoked)
	}

	check := func(id, want string) {
		t.Helper()
		var got string
		if err := st.pool.QueryRow(ctx, `SELECT status FROM api_keys WHERE id=$1`, id).Scan(&got); err != nil {
			t.Fatalf("query key %s: %v", id, err)
		}
		if got != want {
			t.Errorf("key %s status = %q, want %q", id, got, want)
		}
	}
	check(k1, types.APIKeyStatusRevoked)
	check(k2, types.APIKeyStatusRevoked)
	check(k3, types.APIKeyStatusRevoked)
	check(k4, types.APIKeyStatusRevoked)

	// Member row must be gone.
	var n int
	if err := st.pool.QueryRow(ctx, `SELECT COUNT(*) FROM project_members WHERE project_id=$1 AND user_id=$2`, projectID, member).Scan(&n); err != nil {
		t.Fatalf("count members: %v", err)
	}
	if n != 0 {
		t.Errorf("member rows after removal = %d, want 0", n)
	}
}

// TestRemoveProjectMember_DoesNotTouchOtherProjects ensures the revoke
// UPDATE is scoped by project_id, not just by created_by.
func TestRemoveProjectMember_DoesNotTouchOtherProjects(t *testing.T) {
	st := openTestStore(t)
	owner, projectA := seedUserAndProject(t, st)
	_ = owner
	_, projectB := seedUserAndProject(t, st)

	member := seedSecondUser(t, st, "twoprojects")
	addMember(t, st, projectA, member, types.RoleDeveloper)
	addMember(t, st, projectB, member, types.RoleDeveloper)

	kA := seedAPIKey(t, st, projectA, member, types.APIKeyStatusActive)
	kB := seedAPIKey(t, st, projectB, member, types.APIKeyStatusActive)

	if _, err := st.RemoveProjectMember(projectA, member); err != nil {
		t.Fatalf("RemoveProjectMember: %v", err)
	}

	check := func(id, want string) {
		t.Helper()
		var got string
		if err := st.pool.QueryRow(context.Background(), `SELECT status FROM api_keys WHERE id=$1`, id).Scan(&got); err != nil {
			t.Fatalf("query key %s: %v", id, err)
		}
		if got != want {
			t.Errorf("key %s status = %q, want %q", id, got, want)
		}
	}
	check(kA, types.APIKeyStatusRevoked) // projectA's key revoked
	check(kB, types.APIKeyStatusActive)  // projectB's key untouched
}

// TestRemoveProjectMember_NoMemberStillRevokesKeys covers the
// pre-migration edge case: keys exist for a user who is NOT in
// project_members (zombie state). The function must still flip those
// keys and return their count; the DELETE of zero membership rows is
// not an error.
func TestRemoveProjectMember_NoMemberStillRevokesKeys(t *testing.T) {
	st := openTestStore(t)
	_, projectID := seedUserAndProject(t, st)
	orphan := seedSecondUser(t, st, "orphan")

	k1 := seedAPIKey(t, st, projectID, orphan, types.APIKeyStatusActive)

	revoked, err := st.RemoveProjectMember(projectID, orphan)
	if err != nil {
		t.Fatalf("RemoveProjectMember: %v", err)
	}
	if revoked != 1 {
		t.Errorf("revoked = %d, want 1", revoked)
	}
	var got string
	if err := st.pool.QueryRow(context.Background(), `SELECT status FROM api_keys WHERE id=$1`, k1).Scan(&got); err != nil {
		t.Fatalf("query key: %v", err)
	}
	if got != types.APIKeyStatusRevoked {
		t.Errorf("status = %q, want %q", got, types.APIKeyStatusRevoked)
	}
}

// TestCountActiveKeysForMember asserts the count is by (project, user)
// and only counts status='active'.
func TestCountActiveKeysForMember(t *testing.T) {
	st := openTestStore(t)
	_, projectID := seedUserAndProject(t, st)
	u := seedSecondUser(t, st, "counted")

	if n, err := st.CountActiveKeysForMember(projectID, u); err != nil || n != 0 {
		t.Fatalf("initial: n=%d err=%v, want n=0 err=nil", n, err)
	}

	seedAPIKey(t, st, projectID, u, types.APIKeyStatusActive)
	seedAPIKey(t, st, projectID, u, types.APIKeyStatusActive)
	seedAPIKey(t, st, projectID, u, types.APIKeyStatusRevoked)
	seedAPIKey(t, st, projectID, u, types.APIKeyStatusDisabled)

	n, err := st.CountActiveKeysForMember(projectID, u)
	if err != nil {
		t.Fatalf("CountActiveKeysForMember: %v", err)
	}
	if n != 2 {
		t.Errorf("n = %d, want 2 (only active)", n)
	}
}
