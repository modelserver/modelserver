package store

import (
	"context"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

// seedProjectOwnedBy inserts a project with the given created_by user.
// Uses the existing seed helpers from extra_usage_db_test.go.
func seedProjectOwnedBy(t *testing.T, st *Store, name, createdBy string) string {
	t.Helper()
	var id string
	if err := st.pool.QueryRow(context.Background(), `
		INSERT INTO projects (name, created_by, status)
		VALUES ($1, $2, 'active')
		RETURNING id`, name, createdBy).Scan(&id); err != nil {
		t.Fatalf("seed project %s: %v", name, err)
	}
	return id
}

func TestListAllProjects_NoFilters_ReturnsAll(t *testing.T) {
	st := openTestStore(t)
	ownerA, _ := seedUserAndProject(t, st) // creates a project too — that one will be counted
	pid1 := seedProjectOwnedBy(t, st, "list-all-1", ownerA)
	pid2 := seedProjectOwnedBy(t, st, "list-all-2", ownerA)

	got, total, err := st.ListAllProjects(types.PaginationParams{Page: 1, PerPage: 100}, ProjectListFilters{})
	if err != nil {
		t.Fatalf("ListAllProjects: %v", err)
	}
	if total < 2 {
		t.Errorf("total = %d, want >= 2 (we seeded at least 2 plus the auto-created one)", total)
	}
	ids := map[string]bool{}
	for _, p := range got {
		ids[p.ID] = true
	}
	if !ids[pid1] || !ids[pid2] {
		t.Errorf("seeded projects missing from list: pid1=%v, pid2=%v in %v", ids[pid1], ids[pid2], ids)
	}
}

func TestListAllProjects_FilterByProjectID(t *testing.T) {
	st := openTestStore(t)
	ownerA, _ := seedUserAndProject(t, st)
	target := seedProjectOwnedBy(t, st, "filter-by-id-target", ownerA)
	_ = seedProjectOwnedBy(t, st, "filter-by-id-other", ownerA)

	got, total, err := st.ListAllProjects(
		types.PaginationParams{Page: 1, PerPage: 100},
		ProjectListFilters{ProjectID: target},
	)
	if err != nil {
		t.Fatalf("ListAllProjects: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1 (single ID match)", total)
	}
	if len(got) != 1 || got[0].ID != target {
		t.Errorf("got = %v, want exactly [%s]", got, target)
	}
}

func TestListAllProjects_FilterByCreatedBy(t *testing.T) {
	st := openTestStore(t)
	ownerA, _ := seedUserAndProject(t, st)
	ownerB := seedSecondUser(t, st, "owner-b")
	a1 := seedProjectOwnedBy(t, st, "owner-a-proj-1", ownerA)
	a2 := seedProjectOwnedBy(t, st, "owner-a-proj-2", ownerA)
	b1 := seedProjectOwnedBy(t, st, "owner-b-proj-1", ownerB)

	got, total, err := st.ListAllProjects(
		types.PaginationParams{Page: 1, PerPage: 100},
		ProjectListFilters{CreatedBy: ownerB},
	)
	if err != nil {
		t.Fatalf("ListAllProjects: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1 (one project for ownerB)", total)
	}
	if len(got) != 1 || got[0].ID != b1 {
		t.Errorf("got = %v, want [%s]", got, b1)
	}
	// Confirm we did NOT see ownerA's projects.
	for _, p := range got {
		if p.ID == a1 || p.ID == a2 {
			t.Errorf("ownerA project %s leaked into ownerB filter", p.ID)
		}
	}
}

func TestListAllProjects_FilterByBoth_IntersectsAND(t *testing.T) {
	st := openTestStore(t)
	ownerA, _ := seedUserAndProject(t, st)
	ownerB := seedSecondUser(t, st, "owner-b-both")
	a1 := seedProjectOwnedBy(t, st, "both-a-1", ownerA)
	_ = seedProjectOwnedBy(t, st, "both-b-1", ownerB)

	// Filter by a1's ID AND ownerB → should be empty (mismatch).
	_, total, err := st.ListAllProjects(
		types.PaginationParams{Page: 1, PerPage: 100},
		ProjectListFilters{ProjectID: a1, CreatedBy: ownerB},
	)
	if err != nil {
		t.Fatalf("ListAllProjects: %v", err)
	}
	if total != 0 {
		t.Errorf("total = %d, want 0 (id and owner don't match same row)", total)
	}

	// Filter by a1's ID AND ownerA → should match.
	got, total, err := st.ListAllProjects(
		types.PaginationParams{Page: 1, PerPage: 100},
		ProjectListFilters{ProjectID: a1, CreatedBy: ownerA},
	)
	if err != nil {
		t.Fatalf("ListAllProjects: %v", err)
	}
	if total != 1 || len(got) != 1 || got[0].ID != a1 {
		t.Errorf("both-match: total=%d got=%v want=[%s]", total, got, a1)
	}
}

func TestListAllProjects_FilterEmptyMatchReturnsZero(t *testing.T) {
	st := openTestStore(t)
	// Filter by a UUID that doesn't exist in the table.
	_, total, err := st.ListAllProjects(
		types.PaginationParams{Page: 1, PerPage: 100},
		ProjectListFilters{ProjectID: "00000000-0000-0000-0000-000000000000"},
	)
	if err != nil {
		t.Fatalf("ListAllProjects: %v", err)
	}
	if total != 0 {
		t.Errorf("total = %d, want 0 (unmatched filter)", total)
	}
}

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

	revoked, _, err := st.RemoveProjectMember(projectID, member)
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

	if _, _, err := st.RemoveProjectMember(projectA, member); err != nil {
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

	revoked, _, err := st.RemoveProjectMember(projectID, orphan)
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

// TestRemoveProjectMember_DeletesOAuthGrants verifies that RemoveProjectMember
// deletes the member's oauth_grants in the same tx and returns them.
// Grants for other projects must not be touched.
func TestRemoveProjectMember_DeletesOAuthGrants(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	owner, projectID := seedUserAndProject(t, st)
	addMember(t, st, projectID, owner, types.RoleOwner)

	member := seedSecondUser(t, st, "withgrants")
	addMember(t, st, projectID, member, types.RoleDeveloper)

	// Seed two grants for the member + one grant in a different project
	// (must NOT be touched).
	seedGrant := func(t *testing.T, st *Store, projectID, userID, clientID string) string {
		t.Helper()
		var id string
		if err := st.pool.QueryRow(ctx, `
			INSERT INTO oauth_grants (project_id, user_id, client_id, client_name, scopes)
			VALUES ($1, $2, $3, 'test-client', ARRAY['openid','offline'])
			RETURNING id`, projectID, userID, clientID).Scan(&id); err != nil {
			t.Fatalf("seed grant: %v", err)
		}
		return id
	}
	g1 := seedGrant(t, st, projectID, member, "client-A")
	g2 := seedGrant(t, st, projectID, member, "client-B")

	// Different project — keep it untouched.
	_, otherProject := seedUserAndProject(t, st)
	addMember(t, st, otherProject, member, types.RoleDeveloper)
	g3 := seedGrant(t, st, otherProject, member, "client-A")

	_, deleted, err := st.RemoveProjectMember(projectID, member)
	if err != nil {
		t.Fatalf("RemoveProjectMember: %v", err)
	}
	if len(deleted) != 2 {
		t.Errorf("deleted = %d grants, want 2", len(deleted))
	}
	deletedIDs := map[string]bool{}
	for _, g := range deleted {
		deletedIDs[g.ID] = true
		if g.ProjectID != projectID || g.UserID != member {
			t.Errorf("deleted grant scope wrong: %+v", g)
		}
	}
	if !deletedIDs[g1] || !deletedIDs[g2] {
		t.Errorf("expected g1=%s and g2=%s in deleted list, got %v", g1, g2, deletedIDs)
	}

	// g1 and g2 should be gone from DB; g3 must remain.
	var stillThere int
	if err := st.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM oauth_grants WHERE id = ANY($1)`,
		[]string{g1, g2}).Scan(&stillThere); err != nil {
		t.Fatalf("count deleted: %v", err)
	}
	if stillThere != 0 {
		t.Errorf("deleted grants still present: %d", stillThere)
	}
	var otherKeep int
	if err := st.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM oauth_grants WHERE id = $1`, g3).Scan(&otherKeep); err != nil {
		t.Fatalf("count other: %v", err)
	}
	if otherKeep != 1 {
		t.Errorf("other-project grant got touched: count=%d", otherKeep)
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

func TestCountUserCreatedProjects_CountsByCreatedByOnly(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	a, _ := seedUserAndProject(t, st) // a creates one project via the helper
	b := seedSecondUser(t, st, "creator-b")

	// a creates two more projects (active + archived); b creates one.
	seedProjectOwnedBy(t, st, "a-active", a)
	aArchived := seedProjectOwnedBy(t, st, "a-archived", a)
	if _, err := st.pool.Exec(ctx, `UPDATE projects SET status='archived' WHERE id=$1`, aArchived); err != nil {
		t.Fatalf("archive: %v", err)
	}
	seedProjectOwnedBy(t, st, "b-active", b)

	// Now hand off ownership of one of a's projects to b inside project_members
	// directly. This must NOT change either count — quota is by created_by.
	otherA := seedProjectOwnedBy(t, st, "a-transferred", a)
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'owner')`,
		otherA, b); err != nil {
		t.Fatalf("add owner: %v", err)
	}

	got, err := st.CountUserCreatedProjects(a)
	if err != nil {
		t.Fatalf("CountUserCreatedProjects(a): %v", err)
	}
	// a created: helper one + a-active + a-archived + a-transferred = 4
	if got != 4 {
		t.Errorf("CountUserCreatedProjects(a) = %d, want 4 (includes archived and transferred-out)", got)
	}

	got, err = st.CountUserCreatedProjects(b)
	if err != nil {
		t.Fatalf("CountUserCreatedProjects(b): %v", err)
	}
	// b created: b-active = 1 (NOT counting the project where b is owner-by-transfer)
	if got != 1 {
		t.Errorf("CountUserCreatedProjects(b) = %d, want 1 (ownership transfer doesn't move quota)", got)
	}
}
