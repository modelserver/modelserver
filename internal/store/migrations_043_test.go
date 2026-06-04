package store

import (
	"context"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

// TestMigration043_AddDeniedModelsColumn verifies that an INSERT into
// project_members that omits denied_models reads back as a non-nil empty
// slice — the runtime contract relied on by the Go layer. (PG 11+'s
// fast-default also guarantees the same for rows that pre-existed the
// migration; that guarantee is documented in the migration file's
// header comment but is not directly exercised here, because
// openTestStore runs all migrations on connect.)
func TestMigration043_AddDeniedModelsColumn(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	userID, projectID := seedUserAndProject(t, st)

	// Add a second user as a project member without specifying denied_models.
	var memberID string
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO users (email) VALUES ('member-' || gen_random_uuid()::text || '@test.local')
		RETURNING id`).Scan(&memberID); err != nil {
		t.Fatalf("seed second user: %v", err)
	}
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO project_members (user_id, project_id, role)
		VALUES ($1, $2, 'developer')`, memberID, projectID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	// Read back denied_models without a column-level COALESCE — must be []string{}.
	var denied []string
	if err := st.pool.QueryRow(ctx, `
		SELECT denied_models FROM project_members
		WHERE project_id = $1 AND user_id = $2`, projectID, memberID).Scan(&denied); err != nil {
		t.Fatalf("read denied_models: %v", err)
	}
	if denied == nil {
		t.Fatalf("denied_models was nil; expected empty slice (NOT NULL DEFAULT '{}')")
	}
	if len(denied) != 0 {
		t.Fatalf("denied_models = %v; expected empty slice", denied)
	}

	// Suppress unused-var warning on userID (owner of the seeded project).
	_ = userID
}

// TestGetProjectMemberDeniedModelsRoundTrip verifies that
// GetProjectMember/ListProjectMembers read back denied_models as the
// stored slice — empty for default rows, non-empty after a direct
// UPDATE.
func TestGetProjectMemberDeniedModelsRoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	userID, projectID := seedUserAndProject(t, st)

	// Seed a second user as a project member.
	var memberID string
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO users (email) VALUES ('m-' || gen_random_uuid()::text || '@test.local')
		RETURNING id`).Scan(&memberID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := st.AddProjectMember(projectID, memberID, "developer"); err != nil {
		t.Fatalf("add member: %v", err)
	}

	// Default: empty slice.
	got, err := st.GetProjectMember(projectID, memberID)
	if err != nil || got == nil {
		t.Fatalf("get member: err=%v got=%v", err, got)
	}
	if got.DeniedModels == nil || len(got.DeniedModels) != 0 {
		t.Fatalf("default DeniedModels = %v; want []", got.DeniedModels)
	}

	// Set via raw UPDATE, then re-read.
	if _, err := st.pool.Exec(ctx, `
		UPDATE project_members SET denied_models = $1
		WHERE project_id = $2 AND user_id = $3`,
		[]string{"claude-opus-4-8", "gpt-5-5"}, projectID, memberID); err != nil {
		t.Fatalf("update denied_models: %v", err)
	}
	got, err = st.GetProjectMember(projectID, memberID)
	if err != nil || got == nil {
		t.Fatalf("re-get member: err=%v got=%v", err, got)
	}
	if len(got.DeniedModels) != 2 || got.DeniedModels[0] != "claude-opus-4-8" || got.DeniedModels[1] != "gpt-5-5" {
		t.Fatalf("DeniedModels = %v; want [claude-opus-4-8 gpt-5-5]", got.DeniedModels)
	}

	// ListProjectMembers also surfaces it.
	members, err := st.ListProjectMembers(projectID)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	var found bool
	for _, m := range members {
		if m.UserID == memberID {
			found = true
			if len(m.DeniedModels) != 2 {
				t.Fatalf("ListProjectMembers DeniedModels = %v; want 2 entries", m.DeniedModels)
			}
		}
	}
	if !found {
		t.Fatalf("seeded member not in ListProjectMembers result")
	}
	_ = userID
}

// TestUpdateProjectMemberDeniedModels exercises the new
// UpdateProjectMember signature with denied_models in every
// combination with role and creditQuotaPct.
func TestUpdateProjectMemberDeniedModels(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, projectID := seedUserAndProject(t, st)
	var memberID string
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO users (email) VALUES ('u-' || gen_random_uuid()::text || '@test.local')
		RETURNING id`).Scan(&memberID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := st.AddProjectMember(projectID, memberID, "developer"); err != nil {
		t.Fatalf("add member: %v", err)
	}

	// Helper to re-read the row.
	read := func() *types.ProjectMember {
		t.Helper()
		m, err := st.GetProjectMember(projectID, memberID)
		if err != nil || m == nil {
			t.Fatalf("get member: err=%v m=%v", err, m)
		}
		return m
	}

	// 1. denylist-only update.
	denied := []string{"claude-opus-4-8"}
	if err := st.UpdateProjectMember(projectID, memberID, nil, nil, &denied); err != nil {
		t.Fatalf("update denylist only: %v", err)
	}
	if got := read().DeniedModels; len(got) != 1 || got[0] != "claude-opus-4-8" {
		t.Fatalf("denylist after solo update = %v", got)
	}

	// 2. Clear denylist.
	empty := []string{}
	if err := st.UpdateProjectMember(projectID, memberID, nil, nil, &empty); err != nil {
		t.Fatalf("clear denylist: %v", err)
	}
	if got := read().DeniedModels; len(got) != 0 {
		t.Fatalf("denylist after clear = %v; want []", got)
	}

	// 3. Combined role + quota + denylist in one call.
	role := "maintainer"
	q := 50.0
	qPtr := &q
	denied = []string{"a", "b"}
	if err := st.UpdateProjectMember(projectID, memberID, &role, &qPtr, &denied); err != nil {
		t.Fatalf("combined update: %v", err)
	}
	m := read()
	if m.Role != "maintainer" {
		t.Fatalf("Role = %q", m.Role)
	}
	if m.CreditQuotaPct == nil || *m.CreditQuotaPct != 50.0 {
		t.Fatalf("CreditQuotaPct = %v", m.CreditQuotaPct)
	}
	if len(m.DeniedModels) != 2 {
		t.Fatalf("DeniedModels = %v", m.DeniedModels)
	}

	// 4. All nil → no-op, no error.
	if err := st.UpdateProjectMember(projectID, memberID, nil, nil, nil); err != nil {
		t.Fatalf("no-op update: %v", err)
	}

	// 5. Promote to owner with denied_models present — owner promotion
	//    still clears credit_quota_percent, and denied_models follows
	//    the explicit argument (nil → unchanged).
	owner := "owner"
	if err := st.UpdateProjectMember(projectID, memberID, &owner, nil, nil); err != nil {
		t.Fatalf("promote owner: %v", err)
	}
	m = read()
	if m.Role != "owner" {
		t.Fatalf("Role after promote = %q", m.Role)
	}
	if m.CreditQuotaPct != nil {
		t.Fatalf("CreditQuotaPct should clear on owner promote; got %v", *m.CreditQuotaPct)
	}
	// denied_models was unchanged (we passed nil) — still 2 entries.
	if len(m.DeniedModels) != 2 {
		t.Fatalf("DeniedModels post-promote = %v; want 2", m.DeniedModels)
	}
}
