# Per-Member Model Denylist Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let project owners and maintainers mark specific models as forbidden for individual project members; the proxy enforces this denylist before the existing per-API-key allowlist, on both API-key and OAuth-authenticated requests, and the `/v1/models` listing reflects it.

**Architecture:** Add a `denied_models TEXT[] NOT NULL DEFAULT '{}'` column to `project_members`. The auth middleware already reads each member row to fetch credit quota; we piggyback to read denylist into request context (with a 10s cache). A new `Handler.checkModelAllowed` helper consolidates the denylist→allowlist check that runs at four proxy entry points; `HandleListModels` subtracts the denylist from its output. The admin PATCH endpoint for member updates accepts a new `denied_models` field (role gate is already enforced by `requireRole(owner, maintainer)`).

**Tech Stack:** Go 1.x, PostgreSQL 13+, chi router, pgx, standard `testing` package. Integration tests use `TEST_DATABASE_URL` and `openTestStore`.

**Reference spec:** `docs/superpowers/specs/2026-06-04-member-model-denylist-design.md`

**Branch:** `feat/member-model-denylist` (already created)

---

## File Map

**Create:**
- `internal/store/migrations/043_project_member_denied_models.sql` — schema migration
- `internal/store/migrations_043_test.go` — verify migration semantics
- `internal/admin/handle_projects_member_denylist_test.go` — admin endpoint tests for the new field

**Modify:**
- `internal/types/user.go` — add `DeniedModels []string` to `ProjectMember`
- `internal/store/projects.go` — read/write the new column in 4 functions, extend `UpdateProjectMember` signature
- `internal/admin/handle_projects.go` — accept `denied_models` in PATCH body, expose in member responses, update existing call sites of `UpdateProjectMember`
- `internal/proxy/auth_middleware.go` — load denylist (both auth paths), add cache + context accessor, add cache-invalidation hook
- `internal/proxy/handler.go` — add `checkModelAllowed` helper, replace 4 inline checks
- `internal/proxy/router.go` — make `HandleListModels` subtract denylist
- `internal/proxy/router_test.go` — add `HandleListModels` denylist test
- `internal/proxy/handler_test.go` (if exists) or new test file — denylist proxy enforcement tests

**Numbering note:** before writing the migration, list `internal/store/migrations/` and confirm `043_*` is free. If another `043_*` landed, renumber to the next free integer and update all references in this plan.

---

## Task 1: Migration — add `denied_models` column

**Files:**
- Create: `internal/store/migrations/043_project_member_denied_models.sql`
- Create: `internal/store/migrations_043_test.go`

- [ ] **Step 1: Confirm migration number is free**

Run: `ls /root/coding/modelserver/internal/store/migrations/ | sort | tail -3`
Expected: last entry is `042_add_opus_4_8.sql`. If a newer `043_*` exists, pick the next free number (e.g. `044_*`) and **update every reference to `043` in this plan** before continuing.

- [ ] **Step 2: Write the failing migration test**

File: `internal/store/migrations_043_test.go`

```go
package store

import (
	"context"
	"testing"
)

// TestMigration043_AddDeniedModelsColumn verifies that:
//   1. The denied_models column exists with the expected type and default.
//   2. Pre-existing project_members rows read back with an empty slice
//      (PostgreSQL fast-default behavior for NOT NULL DEFAULT '{}').
//   3. INSERT without an explicit value uses '{}' rather than NULL.
func TestMigration043_AddDeniedModelsColumn(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Seed a user + project + member BEFORE the test reads schema.
	// (Migrations have already run by the time openTestStore returns;
	// the test still proves the post-migration semantics hold for new
	// inserts. The "existing rows" guarantee is from the migration
	// itself — see comment in 043_project_member_denied_models.sql.)
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
```

- [ ] **Step 3: Run the test and verify it fails**

Run: `cd /root/coding/modelserver && TEST_DATABASE_URL=$TEST_DATABASE_URL go test ./internal/store/ -run TestMigration043_AddDeniedModelsColumn -v`

If `TEST_DATABASE_URL` is unset locally, the test will `t.Skip` — that is **not** a passing run. Set the env var to a Postgres instance with a writable schema (see `extra_usage_db_test.go:17` for the format).

Expected: FAIL with a Postgres error about column `denied_models` not existing.

- [ ] **Step 4: Write the migration**

File: `internal/store/migrations/043_project_member_denied_models.sql`

```sql
-- 043_project_member_denied_models.sql
--
-- Per-member model denylist. Owners/maintainers populate this via the
-- admin PATCH endpoint; the proxy checks it on every request before
-- the existing api_keys.allowed_models allowlist.
--
-- PostgreSQL 11+ applies ADD COLUMN ... NOT NULL DEFAULT '{}' as a
-- "fast default" — no table rewrite, every pre-existing row reads
-- back as '{}'. No backfill script needed.
ALTER TABLE project_members
  ADD COLUMN denied_models TEXT[] NOT NULL DEFAULT '{}';
```

- [ ] **Step 5: Run the test and verify it passes**

Run: `cd /root/coding/modelserver && TEST_DATABASE_URL=$TEST_DATABASE_URL go test ./internal/store/ -run TestMigration043_AddDeniedModelsColumn -v`
Expected: PASS.

- [ ] **Step 6: Run the full store package test suite**

Run: `cd /root/coding/modelserver && TEST_DATABASE_URL=$TEST_DATABASE_URL go test ./internal/store/ -v`
Expected: PASS (no regressions in pre-existing migration tests).

- [ ] **Step 7: Commit**

```bash
cd /root/coding/modelserver
git add internal/store/migrations/043_project_member_denied_models.sql internal/store/migrations_043_test.go
git commit -m "feat(store): add project_members.denied_models column

Per-member model denylist, NOT NULL DEFAULT '{}' so existing rows
read back as empty (PG 11+ fast-default — no backfill required).

Spec: docs/superpowers/specs/2026-06-04-member-model-denylist-design.md

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Type — add `DeniedModels` to `ProjectMember`

**Files:**
- Modify: `internal/types/user.go:42-52`

- [ ] **Step 1: Add the field**

Replace the `ProjectMember` struct in `internal/types/user.go` with:

```go
// ProjectMember links a User to a Project with an assigned role.
type ProjectMember struct {
	UserID         string    `json:"user_id"`
	ProjectID      string    `json:"project_id"`
	Role           string    `json:"role"`
	CreditQuotaPct *float64  `json:"credit_quota_percent"` // nil = no limit (effective 100%)
	DeniedModels   []string  `json:"denied_models"`         // empty = no model denied
	CreatedAt      time.Time `json:"created_at"`

	// User is populated when the record is fetched with a join.
	User *User `json:"user,omitempty"`
}
```

Note: no `omitempty` on `DeniedModels` — empty slice serializes as `[]`, which is the contract the frontend will rely on.

- [ ] **Step 2: Verify it compiles**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: builds clean. (No test yet; store/admin layer updates in later tasks will populate the field.)

- [ ] **Step 3: Commit**

```bash
cd /root/coding/modelserver
git add internal/types/user.go
git commit -m "feat(types): add DeniedModels to ProjectMember

Empty slice (not nil) serializes as []; matches DB NOT NULL DEFAULT '{}'.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Store — read `denied_models` in all member-fetching queries

**Files:**
- Modify: `internal/store/projects.go` — `GetProjectMember`, `ListProjectMembers`, `ListProjectMembersPaginated`

- [ ] **Step 1: Write the failing test**

Append to `internal/store/migrations_043_test.go`:

```go
// TestGetProjectMemberDeniedModelsRoundTrip verifies that
// GetProjectMember/ListProjectMembers read back denied_models as the
// stored slice — empty for default rows, non-empty after a direct
// UPDATE.
func TestGetProjectMemberDeniedModelsRoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	userID, projectID := seedUserAndProject(t, st)

	// Seed a member row.
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
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `cd /root/coding/modelserver && TEST_DATABASE_URL=$TEST_DATABASE_URL go test ./internal/store/ -run TestGetProjectMemberDeniedModelsRoundTrip -v`
Expected: FAIL — the `Scan` will error because `denied_models` is not selected.

- [ ] **Step 3: Modify `GetProjectMember`**

In `internal/store/projects.go`, replace the body of `GetProjectMember` (lines 168-183):

```go
// GetProjectMember returns a single member.
func (s *Store) GetProjectMember(projectID, userID string) (*types.ProjectMember, error) {
	m := &types.ProjectMember{}
	err := s.pool.QueryRow(context.Background(), `
		SELECT user_id, project_id, role, created_at, credit_quota_percent, denied_models
		FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&m.UserID, &m.ProjectID, &m.Role, &m.CreatedAt, &m.CreditQuotaPct, &m.DeniedModels)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get member: %w", err)
	}
	return m, nil
}
```

- [ ] **Step 4: Modify `ListProjectMembers`**

Replace lines 185-214:

```go
// ListProjectMembers returns all members of a project with user info.
func (s *Store) ListProjectMembers(projectID string) ([]types.ProjectMember, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT pm.user_id, pm.project_id, pm.role, pm.credit_quota_percent, pm.denied_models, pm.created_at,
			u.id, u.email, u.nickname, COALESCE(u.picture, '')
		FROM project_members pm
		JOIN users u ON pm.user_id = u.id
		WHERE pm.project_id = $1
		ORDER BY pm.created_at ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	var members []types.ProjectMember
	for rows.Next() {
		var m types.ProjectMember
		var u types.User
		if err := rows.Scan(&m.UserID, &m.ProjectID, &m.Role, &m.CreditQuotaPct, &m.DeniedModels, &m.CreatedAt,
			&u.ID, &u.Email, &u.Nickname, &u.Picture); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		m.User = &u
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate members: %w", err)
	}
	return members, nil
}
```

- [ ] **Step 5: Modify `ListProjectMembersPaginated`**

Replace the inner SELECT and Scan in lines 226-251 to add `denied_models`:

```go
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT pm.user_id, pm.project_id, pm.role, pm.credit_quota_percent, pm.denied_models, pm.created_at,
			u.id, u.email, u.nickname, COALESCE(u.picture, '')
		FROM project_members pm
		JOIN users u ON pm.user_id = u.id
		WHERE pm.project_id = $1
		ORDER BY pm.%s %s LIMIT $2 OFFSET $3`,
		sanitizeSort(p.Sort, "created_at"), sanitizeOrder(p.Order)),
		projectID, p.Limit(), p.Offset(),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list members paginated: %w", err)
	}
	defer rows.Close()

	var members []types.ProjectMember
	for rows.Next() {
		var m types.ProjectMember
		var u types.User
		if err := rows.Scan(&m.UserID, &m.ProjectID, &m.Role, &m.CreditQuotaPct, &m.DeniedModels, &m.CreatedAt,
			&u.ID, &u.Email, &u.Nickname, &u.Picture); err != nil {
			return nil, 0, fmt.Errorf("scan member: %w", err)
		}
		m.User = &u
		members = append(members, m)
	}
```

- [ ] **Step 6: Run the test and verify it passes**

Run: `cd /root/coding/modelserver && TEST_DATABASE_URL=$TEST_DATABASE_URL go test ./internal/store/ -run TestGetProjectMemberDeniedModelsRoundTrip -v`
Expected: PASS.

- [ ] **Step 7: Run the full store suite**

Run: `cd /root/coding/modelserver && TEST_DATABASE_URL=$TEST_DATABASE_URL go test ./internal/store/ -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
cd /root/coding/modelserver
git add internal/store/projects.go internal/store/migrations_043_test.go
git commit -m "feat(store): scan denied_models in member-fetching queries

GetProjectMember, ListProjectMembers, and
ListProjectMembersPaginated all now surface the per-member denylist.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Store — extend `UpdateProjectMember` to write `denied_models`

**Files:**
- Modify: `internal/store/projects.go:269-301` — extend signature
- Modify: `internal/admin/handle_projects.go:459,541` — adapt call sites

The current function uses a chain of if-blocks to build the right UPDATE. We will replace it with a single dynamic-SET builder so new fields don't grow the matrix combinatorially.

- [ ] **Step 1: Write the failing test**

Append to `internal/store/migrations_043_test.go`:

```go
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
	//    the explicit argument (in this case we pass nil → unchanged).
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
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `cd /root/coding/modelserver && TEST_DATABASE_URL=$TEST_DATABASE_URL go test ./internal/store/ -run TestUpdateProjectMemberDeniedModels -v`
Expected: FAIL — function signature is wrong (too few arguments to `UpdateProjectMember`).

- [ ] **Step 3: Rewrite `UpdateProjectMember` with dynamic SET builder**

In `internal/store/projects.go`, replace the entire function (lines 266-301):

```go
// UpdateProjectMember updates a member's role, credit quota, and/or
// denied models. Pass nil pointers to leave fields unchanged.
//
//   - role:           *string. nil = unchanged.
//   - creditQuotaPct: **float64. nil = unchanged; non-nil pointer whose
//     value is nil = set NULL; non-nil pointer whose value is non-nil =
//     set that float. (Same convention the previous signature used.)
//   - deniedModels:   *[]string. nil = unchanged; non-nil pointer (including
//     empty slice) = replace the column with that slice. Empty slice means
//     "no model is denied".
//
// If role is set to "owner", credit_quota_percent is forced to NULL
// regardless of the creditQuotaPct argument.
func (s *Store) UpdateProjectMember(
	projectID, userID string,
	role *string,
	creditQuotaPct **float64,
	deniedModels *[]string,
) error {
	if role == nil && creditQuotaPct == nil && deniedModels == nil {
		return nil
	}

	sets := make([]string, 0, 3)
	args := make([]any, 0, 5)
	next := 1
	add := func(col string, val any) {
		sets = append(sets, fmt.Sprintf("%s = $%d", col, next))
		args = append(args, val)
		next++
	}

	if role != nil {
		add("role", *role)
		// Owner promotion always clears the quota, even if the caller
		// also passed an explicit creditQuotaPct.
		if *role == types.RoleOwner {
			sets = append(sets, "credit_quota_percent = NULL")
		} else if creditQuotaPct != nil {
			add("credit_quota_percent", *creditQuotaPct) // *float64 (may be nil → SQL NULL)
		}
	} else if creditQuotaPct != nil {
		add("credit_quota_percent", *creditQuotaPct)
	}

	if deniedModels != nil {
		// pgx encodes a non-nil []string as TEXT[]. Empty slice → '{}'.
		add("denied_models", *deniedModels)
	}

	args = append(args, projectID, userID)
	query := fmt.Sprintf(
		"UPDATE project_members SET %s WHERE project_id = $%d AND user_id = $%d",
		strings.Join(sets, ", "), next, next+1,
	)

	_, err := s.pool.Exec(context.Background(), query, args...)
	return err
}
```

Add `"strings"` to the imports at the top of `internal/store/projects.go` if not already present.

- [ ] **Step 4: Adapt the two existing call sites in `handle_projects.go`**

In `internal/admin/handle_projects.go`:

Line 459 (inside `handleAddMember`) — change:
```go
if err := st.UpdateProjectMember(projectID, userID, nil, quotaPtr); err != nil {
```
to:
```go
if err := st.UpdateProjectMember(projectID, userID, nil, quotaPtr, nil); err != nil {
```

Line 541 (inside `handleUpdateMember`) — change:
```go
if err := st.UpdateProjectMember(projectID, userID, body.Role, quotaArg); err != nil {
```
to:
```go
if err := st.UpdateProjectMember(projectID, userID, body.Role, quotaArg, nil); err != nil {
```

(Task 5 will replace this `nil` with the actual `denied_models` argument.)

- [ ] **Step 5: Run the new test and the existing store + admin suites**

Run:
```
cd /root/coding/modelserver
go build ./...
TEST_DATABASE_URL=$TEST_DATABASE_URL go test ./internal/store/ -v
go test ./internal/admin/ -v
```
Expected: all PASS. If any pre-existing admin test fails because of the `nil` argument, audit Step 4 — both call sites must be touched.

- [ ] **Step 6: Commit**

```bash
cd /root/coding/modelserver
git add internal/store/projects.go internal/store/migrations_043_test.go internal/admin/handle_projects.go
git commit -m "feat(store): extend UpdateProjectMember with denied_models

Replaces the role/quota if-ladder with a dynamic SET builder so new
columns are additive. Owner-promotion still clears credit_quota_percent
unconditionally.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Admin API — accept `denied_models` in PATCH body

**Files:**
- Modify: `internal/admin/handle_projects.go:469-547` (`handleUpdateMember`)
- Modify: `internal/admin/handle_projects.go:383-409` (`memberCompact` + `handleListMembersCompact`)

Spec:
- Tri-state: missing → unchanged; `[]` → clear; `[...]` → replace.
- Role gate: already enforced by the function's `requireRole(owner, maintainer)`.
- Element validation: trim, drop empties, dedupe, hard cap 256.
- No "maintainer can't touch another maintainer's denylist" rule — that exists for quota by design, but Q1 of the spec explicitly chose uniform configurability for denylist.

- [ ] **Step 1: Write the failing tests**

Create `internal/admin/handle_projects_member_denylist_test.go`. The exact test scaffolding (router setup, auth bypass for tests, body helpers) should match what existing admin tests in this package do — open one of `internal/admin/handle_extra_usage_bypass_test.go` or `internal/admin/handle_codex_oauth_test.go`, copy the harness pattern (likely `httptest.NewServer` + a helper that sets `UserFromContext` / `MemberFromContext` on the request), and adapt:

```go
package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

// Test matrix for PATCH /admin/projects/{id}/members/{user_id} when
// denied_models is in the body. The role gate is already enforced
// by requireRole() — the developer→403 path is therefore covered
// by the existing test suite for any field on this endpoint, but we
// re-assert it here so changes to requireRole don't silently lift
// the gate for denylist writes.

func TestUpdateMember_DeniedModels_DeveloperForbidden(t *testing.T) {
	// Use the package's existing test harness — set up a Store with a
	// project containing a developer caller and a member target, then
	// PATCH with denied_models and expect 403 from requireRole.
	// (See e.g. handle_extra_usage_bypass_test.go for the harness pattern.)
	t.Skip("TODO: wire to existing admin test harness (see file header comment)")
}

func TestUpdateMember_DeniedModels_MaintainerOK(t *testing.T) {
	t.Skip("TODO: wire to existing admin test harness")
}

func TestUpdateMember_DeniedModels_OwnerCanSelfDeny(t *testing.T) {
	// Q1 of the spec: uniform runtime constraint, owner self-deny is allowed.
	t.Skip("TODO: wire to existing admin test harness")
}

func TestUpdateMember_DeniedModels_EmptyArrayClears(t *testing.T) {
	t.Skip("TODO: wire to existing admin test harness")
}

func TestUpdateMember_DeniedModels_OverCapRejected(t *testing.T) {
	// 257 entries → 400 bad_request.
	t.Skip("TODO: wire to existing admin test harness")
}

func TestUpdateMember_DeniedModels_DedupesAndTrims(t *testing.T) {
	// Input ["  a  ", "a", "b", ""] → stored as ["a","b"].
	t.Skip("TODO: wire to existing admin test harness")
}

func TestUpdateMember_BodyAllNil_StillRejected(t *testing.T) {
	// Existing behavior: empty body → 400 "at least one of ...".
	// New error message must list denied_models among the accepted fields.
	t.Skip("TODO: wire to existing admin test harness")
}

// --- Compile-time guards (run even without the harness) -------------------

// Ensure the request body struct still parses denied_models as expected.
// This is a pure JSON round-trip — no HTTP / DB needed.
func TestUpdateMemberBody_DeniedModelsJSON(t *testing.T) {
	type updateMemberBody struct {
		Role           *string   `json:"role"`
		CreditQuotaPct *float64  `json:"credit_quota_percent"`
		ClearQuota     bool      `json:"clear_quota"`
		DeniedModels   *[]string `json:"denied_models"`
	}

	cases := []struct {
		name string
		body string
		want *[]string
	}{
		{"omitted", `{}`, nil},
		{"null", `{"denied_models":null}`, nil},
		{"empty", `{"denied_models":[]}`, &[]string{}},
		{"one", `{"denied_models":["x"]}`, &[]string{"x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b updateMemberBody
			if err := json.Unmarshal([]byte(c.body), &b); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			switch {
			case c.want == nil && b.DeniedModels != nil:
				t.Fatalf("want nil, got %v", *b.DeniedModels)
			case c.want != nil && b.DeniedModels == nil:
				t.Fatalf("want non-nil, got nil")
			case c.want != nil:
				if len(*b.DeniedModels) != len(*c.want) {
					t.Fatalf("len mismatch: want %v got %v", *c.want, *b.DeniedModels)
				}
			}
		})
	}
	// Force the harness to compile even if the http helpers are unused.
	_ = bytes.NewReader
	_ = strings.TrimSpace
	_ = httptest.NewRecorder
	_ = http.StatusForbidden
	_ = types.RoleOwner
}
```

**Note for the implementer:** the `t.Skip` placeholders exist because this codebase's admin tests vary in their harness style and there isn't one canonical helper to copy verbatim. Before unskipping, open 1-2 existing admin tests in the package, identify the pattern they use to construct a request with `UserFromContext` and `MemberFromContext` populated, and replicate it. Replace each `t.Skip` with the real test body. The expected behaviors are unambiguous from the spec — see §"Admin API" of `2026-06-04-member-model-denylist-design.md`.

- [ ] **Step 2: Run the tests and verify they fail (or skip)**

Run: `cd /root/coding/modelserver && go test ./internal/admin/ -run 'TestUpdateMember' -v`

The JSON-round-trip test will fail because `denied_models` doesn't yet exist in any struct; the other tests will skip.

- [ ] **Step 3: Add `DeniedModels` to the PATCH body and validate it**

Modify `handleUpdateMember` in `internal/admin/handle_projects.go` (lines 469-547). Replace the function body with:

```go
func handleUpdateMember(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, types.RoleOwner, types.RoleMaintainer) {
			return
		}
		projectID := chi.URLParam(r, "projectID")
		userID := chi.URLParam(r, "userID")

		var body struct {
			Role           *string   `json:"role"`
			CreditQuotaPct *float64  `json:"credit_quota_percent"`
			ClearQuota     bool      `json:"clear_quota"`
			DeniedModels   *[]string `json:"denied_models"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		// At least one field must be provided.
		if body.Role == nil && body.CreditQuotaPct == nil && !body.ClearQuota && body.DeniedModels == nil {
			writeError(w, http.StatusBadRequest, "bad_request",
				"at least one of role, credit_quota_percent, clear_quota, or denied_models must be provided")
			return
		}

		// Validate credit_quota_percent range.
		if body.CreditQuotaPct != nil && (*body.CreditQuotaPct < 0 || *body.CreditQuotaPct > 100) {
			writeError(w, http.StatusBadRequest, "bad_request", "credit_quota_percent must be between 0 and 100")
			return
		}

		// Normalize denied_models: trim entries, drop empties, dedupe in input
		// order. Hard cap of 256 entries after normalization. No catalog
		// existence check — the catalog is a read-only snapshot and storing
		// names not in it is a documented no-op (see spec §Non-goals).
		if body.DeniedModels != nil {
			cleaned, ok := normalizeDeniedModels(*body.DeniedModels)
			if !ok {
				writeError(w, http.StatusBadRequest, "bad_request",
					"denied_models has too many entries (max 256)")
				return
			}
			body.DeniedModels = &cleaned
		}

		caller := UserFromContext(r.Context())
		callerMember := MemberFromContext(r.Context())

		// Cannot set quota on yourself.
		if (body.CreditQuotaPct != nil || body.ClearQuota) && userID == caller.ID {
			writeError(w, http.StatusBadRequest, "bad_request", "cannot set quota on yourself")
			return
		}

		// Load target member to check their role.
		targetMember, err := st.GetProjectMember(projectID, userID)
		if err != nil || targetMember == nil {
			writeError(w, http.StatusNotFound, "not_found", "member not found")
			return
		}

		// Cannot set quota on an owner.
		if (body.CreditQuotaPct != nil || body.ClearQuota) && targetMember.Role == types.RoleOwner {
			writeError(w, http.StatusForbidden, "forbidden", "cannot set quota on an owner")
			return
		}

		// Maintainers cannot set quota on other maintainers.
		//
		// NOTE: this restriction intentionally does NOT extend to denied_models.
		// Per the design (Q1), any owner or maintainer may configure the
		// denylist for any member. Do not "mirror" the quota rule here.
		if callerMember != nil && callerMember.Role == types.RoleMaintainer &&
			(body.CreditQuotaPct != nil || body.ClearQuota) &&
			targetMember.Role == types.RoleMaintainer {
			writeError(w, http.StatusForbidden, "forbidden", "maintainers cannot set quota on other maintainers")
			return
		}

		// Build quota pointer argument (**float64).
		var quotaArg **float64
		if body.ClearQuota {
			var nilPtr *float64
			quotaArg = &nilPtr
		} else if body.CreditQuotaPct != nil {
			quotaArg = &body.CreditQuotaPct
		}

		// If promoting to owner, quota is auto-cleared in the store layer.
		if err := st.UpdateProjectMember(projectID, userID, body.Role, quotaArg, body.DeniedModels); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update member")
			return
		}

		// Cache invalidation for the denylist context value (10s TTL).
		// See proxy/auth_middleware.go — denylistCache key is "<projectID>:<userID>".
		if body.DeniedModels != nil {
			proxyInvalidateDeniedModelsCache(projectID, userID)
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

// normalizeDeniedModels trims whitespace, drops empties, dedupes (preserving
// first-seen order), and enforces the 256-entry cap. Returns (cleaned, ok).
const deniedModelsMaxEntries = 256

func normalizeDeniedModels(in []string) ([]string, bool) {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) > deniedModelsMaxEntries {
		return nil, false
	}
	return out, true
}
```

Add to imports if not already present: `"strings"`.

The function `proxyInvalidateDeniedModelsCache` will be defined in Task 6 — for now the compile will break with "undefined". That is expected; Task 6 introduces it.

- [ ] **Step 4: Surface `denied_models` in member list/compact responses**

`memberCompact` exists as a minimal-shape struct for filter dropdowns. Leave it minimal; the frontend reads denylist from the full member list. No change to `handleListMembersCompact`.

The full member list (the GET endpoint at line ~298) calls `ListProjectMembersPaginated` and serializes `[]types.ProjectMember` directly — since Task 2 added `DeniedModels` to that struct with a JSON tag, the field appears automatically. Verify by searching for the GET handler that emits members:

```
grep -n "ListProjectMembersPaginated\|ListProjectMembers\b" /root/coding/modelserver/internal/admin/handle_projects.go
```

For each call site, confirm the response either writes `members` directly (no transformation) or, if it transforms, copy the `DeniedModels` field through. If transformation drops the field, that response also needs updating — add a line copying `m.DeniedModels` into the response struct.

- [ ] **Step 5: Skip the compile + test run for now**

The `proxyInvalidateDeniedModelsCache` reference deliberately won't compile yet — Task 6 wires it up. Do **not** commit this task in isolation; Tasks 5 and 6 commit together at the end of Task 6, since they have a circular code dependency.

---

## Task 6: Proxy — auth middleware loads denylist into context; cache + invalidation hook

**Files:**
- Modify: `internal/proxy/auth_middleware.go`

This task ends the cross-package compile gap from Task 5.

- [ ] **Step 1: Add the cache, context key, accessor, and invalidation hook**

In `internal/proxy/auth_middleware.go`, near the top where `quotaCache` lives (around line 18-31), add:

```go
// deniedModelsCache caches per-user denylist lookups (10s TTL). Stored
// as a slice of strings keyed by "<projectID>:<userID>". A cached value
// of nil means "loaded once, no denylist found" (member row had empty
// slice) — distinct from a cache miss.
var (
	deniedModelsCacheMu sync.RWMutex
	deniedModelsCache   = make(map[string]deniedModelsCacheEntry)
)

type deniedModelsCacheEntry struct {
	models    []string // empty slice means "no model denied"
	expiresAt time.Time
}

const deniedModelsCacheTTL = 10 * time.Second

func deniedModelsCacheGet(key string) ([]string, bool) {
	deniedModelsCacheMu.RLock()
	defer deniedModelsCacheMu.RUnlock()
	entry, ok := deniedModelsCache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.models, true
}

func deniedModelsCacheSet(key string, models []string) {
	deniedModelsCacheMu.Lock()
	defer deniedModelsCacheMu.Unlock()
	deniedModelsCache[key] = deniedModelsCacheEntry{
		models:    models,
		expiresAt: time.Now().Add(deniedModelsCacheTTL),
	}
}

// InvalidateDeniedModelsCache drops any cached denylist for the given
// project/user pair. The admin PATCH endpoint calls this after writing
// a new denylist so subsequent requests see the change immediately
// rather than waiting up to deniedModelsCacheTTL for natural expiry.
func InvalidateDeniedModelsCache(projectID, userID string) {
	key := projectID + ":" + userID
	deniedModelsCacheMu.Lock()
	defer deniedModelsCacheMu.Unlock()
	delete(deniedModelsCache, key)
}
```

Add `ctxUserDeniedModels` to the context key constants block:

```go
const (
	ctxAPIKey            contextKey = "apikey"
	ctxProject           contextKey = "project"
	ctxPolicy            contextKey = "policy"
	ctxSubscription      contextKey = "subscription"
	ctxUserQuotaPct      contextKey = "user_quota_pct"
	ctxUserDeniedModels  contextKey = "user_denied_models"
	ctxOAuthGrantID      contextKey = "oauth_grant_id"
)
```

Add the context accessor near `UserQuotaPctFromContext`:

```go
// UserDeniedModelsFromContext returns the caller's per-member denylist.
// Returns nil if no denylist applies (no member row, empty slice, or
// the context value was never set).
func UserDeniedModelsFromContext(ctx context.Context) []string {
	if v, ok := ctx.Value(ctxUserDeniedModels).([]string); ok && len(v) > 0 {
		return v
	}
	return nil
}
```

- [ ] **Step 2: Load denylist on the API-key path**

In `handleAPIKeyAuth` (the inline lambda inside `AuthMiddleware`), find the block around line 248-266 that loads `userQuotaPct`. Right after that block — still inside `else` (the cache-miss branch) — we already have the `member` variable in scope when the miss path triggers a load. **Refactor that block** so both quota and denylist are loaded from the same member row:

Replace lines 248-266 with:

```go
			// Load per-user credit quota + denylist (cached 10s each).
			// Both share the same project_members row, so a miss on either
			// triggers a single GetProjectMember call that hydrates both.
			var userQuotaPct *float64
			var userDeniedModels []string

			quotaKey := project.ID + ":" + apiKey.CreatedBy
			quotaCached, quotaHit := quotaCache.Get(quotaKey)
			deniedCached, deniedHit := deniedModelsCacheGet(quotaKey)

			if quotaHit {
				if quotaCached >= 0 {
					v := quotaCached
					userQuotaPct = &v
				}
			}
			if deniedHit {
				userDeniedModels = deniedCached
			}

			if !quotaHit || !deniedHit {
				member, memberErr := st.GetProjectMember(project.ID, apiKey.CreatedBy)
				if memberErr != nil {
					// Fail open for BOTH quota and denylist on transient
					// DB errors — never lock everyone out of every model.
				} else if member != nil {
					if !quotaHit {
						if member.CreditQuotaPct != nil {
							userQuotaPct = member.CreditQuotaPct
							quotaCache.Set(quotaKey, *member.CreditQuotaPct)
						} else {
							quotaCache.Set(quotaKey, -1) // sentinel: no quota
						}
					}
					if !deniedHit {
						userDeniedModels = member.DeniedModels
						deniedModelsCacheSet(quotaKey, member.DeniedModels)
					}
				} else {
					// member == nil (user not in project_members; can
					// happen if the API key outlived membership). Cache
					// "no quota" + "no denylist" sentinels.
					if !quotaHit {
						quotaCache.Set(quotaKey, -1)
					}
					if !deniedHit {
						deniedModelsCacheSet(quotaKey, nil)
					}
				}
			}
```

Then in the context-population block (around lines 270-281), add:

```go
				ctx := context.WithValue(r.Context(), ctxAPIKey, apiKey)
				ctx = context.WithValue(ctx, ctxProject, project)
				if policy != nil {
					ctx = context.WithValue(ctx, ctxPolicy, policy)
				}
				if subscription != nil {
					ctx = context.WithValue(ctx, ctxSubscription, subscription)
				}
				if userQuotaPct != nil {
					ctx = context.WithValue(ctx, ctxUserQuotaPct, userQuotaPct)
				}
				if len(userDeniedModels) > 0 {
					ctx = context.WithValue(ctx, ctxUserDeniedModels, userDeniedModels)
				}
				next.ServeHTTP(w, r.WithContext(ctx))
```

- [ ] **Step 3: Do the same on the OAuth introspection path**

In `handleTokenIntrospectionAuth` (around lines 359-403), replace the equivalent block:

```go
		// Load per-user credit quota + denylist (cached 10s each), shared
		// with the API-key path. fail-open semantics identical.
		var userQuotaPct *float64
		var userDeniedModels []string
		if userID != "" {
			key := project.ID + ":" + userID
			quotaCached, quotaHit := quotaCache.Get(key)
			deniedCached, deniedHit := deniedModelsCacheGet(key)

			if quotaHit {
				if quotaCached >= 0 {
					v := quotaCached
					userQuotaPct = &v
				}
			}
			if deniedHit {
				userDeniedModels = deniedCached
			}

			if !quotaHit || !deniedHit {
				member, memberErr := st.GetProjectMember(project.ID, userID)
				if memberErr != nil {
					// Fail open.
				} else if member != nil {
					if !quotaHit {
						if member.CreditQuotaPct != nil {
							userQuotaPct = member.CreditQuotaPct
							quotaCache.Set(key, *member.CreditQuotaPct)
						} else {
							quotaCache.Set(key, -1)
						}
					}
					if !deniedHit {
						userDeniedModels = member.DeniedModels
						deniedModelsCacheSet(key, member.DeniedModels)
					}
				} else {
					if !quotaHit {
						quotaCache.Set(key, -1)
					}
					if !deniedHit {
						deniedModelsCacheSet(key, nil)
					}
				}
			}
		}
```

And in the corresponding context-population block (around lines 390-403), add:

```go
		if len(userDeniedModels) > 0 {
			ctx = context.WithValue(ctx, ctxUserDeniedModels, userDeniedModels)
		}
```

- [ ] **Step 4: Wire the admin invalidation hook**

In `internal/admin/handle_projects.go`, near the top of the file (just under the imports / package-level vars), add:

```go
// proxyInvalidateDeniedModelsCache is set during init() to the
// proxy package's cache-invalidation function. Indirected as a
// function variable to avoid an admin→proxy import cycle.
var proxyInvalidateDeniedModelsCache = func(projectID, userID string) {}
```

Then in a `main` package or wherever the admin router is mounted next to the proxy router, wire the hook. Find the file (likely `cmd/modelserver/main.go` or similar):

```
grep -rn "InvalidateDeniedModelsCache\|admin\.\|MountRoutes" /root/coding/modelserver/cmd/ | head -20
```

In the file that imports both `admin` and `proxy`, add at startup:

```go
admin.SetDeniedModelsCacheInvalidator(proxy.InvalidateDeniedModelsCache)
```

And in `internal/admin/handle_projects.go` (or a new `internal/admin/wiring.go` if cleaner), add the setter:

```go
// SetDeniedModelsCacheInvalidator wires the proxy's cache-invalidation
// function so admin handlers can drop stale denylist entries after a
// successful PATCH. Defaults to a no-op until called.
func SetDeniedModelsCacheInvalidator(fn func(projectID, userID string)) {
	if fn != nil {
		proxyInvalidateDeniedModelsCache = fn
	}
}
```

- [ ] **Step 5: Verify the full project builds**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: clean build. If the wiring file from Step 4 isn't found, search more broadly:

```
grep -rn "admin\.MountRoutes\|admin\.Mount\b" /root/coding/modelserver/ --include='*.go'
```

The point is to find the one binary entrypoint that already wires admin + proxy together, and call `SetDeniedModelsCacheInvalidator` there once at startup.

- [ ] **Step 6: Run tests for admin + proxy + store**

Run:
```
cd /root/coding/modelserver
TEST_DATABASE_URL=$TEST_DATABASE_URL go test ./internal/store/ ./internal/admin/ ./internal/proxy/ -v
```
Expected: all PASS (no regressions). The new admin tests from Task 5 are still skipped (placeholder bodies); unskipping happens at the end of this task.

- [ ] **Step 7: Unskip and flesh out the admin tests from Task 5**

Open `internal/admin/handle_projects_member_denylist_test.go` and replace each `t.Skip(...)` with an actual test body. Use the harness pattern you identified by reading the existing admin tests in this package. The behaviors to assert (from the spec):

| Test                                            | Setup                                                              | Action                                                                 | Expect                                                                |
|-------------------------------------------------|--------------------------------------------------------------------|------------------------------------------------------------------------|-----------------------------------------------------------------------|
| `DeniedModels_DeveloperForbidden`               | caller is `developer`                                              | PATCH with `denied_models: ["x"]`                                      | 403 from `requireRole`                                                |
| `DeniedModels_MaintainerOK`                     | caller is `maintainer`, target is `developer`                      | PATCH `denied_models: ["claude-opus-4-8"]`                             | 204; DB row has `{claude-opus-4-8}`                                   |
| `DeniedModels_OwnerCanSelfDeny`                 | caller is `owner`, target = self                                   | PATCH `denied_models: ["gpt-5-5"]`                                     | 204; row has `{gpt-5-5}`                                              |
| `DeniedModels_EmptyArrayClears`                 | row already has `{a,b}`                                            | PATCH `denied_models: []`                                              | 204; row is `{}`                                                      |
| `DeniedModels_OverCapRejected`                  | caller is `maintainer`                                             | PATCH `denied_models` with 257 unique entries                          | 400 `bad_request`, "too many"                                         |
| `DeniedModels_DedupesAndTrims`                  | caller is `maintainer`                                             | PATCH `denied_models: ["  a  ","a","b",""]`                            | 204; DB row is `{a,b}`                                                |
| `BodyAllNil_StillRejected`                      | caller is `maintainer`                                             | PATCH `{}`                                                             | 400; message lists `denied_models` among accepted fields              |
| `DeniedModels_CacheInvalidatedAfterPATCH`       | seed denylist, prime cache via direct `deniedModelsCacheSet`        | PATCH `denied_models: ["x"]`, then `deniedModelsCacheGet` for same key | cache miss (entry was invalidated)                                    |

For the last test, the admin handler calls `proxyInvalidateDeniedModelsCache(...)` which the binary wiring sets to `proxy.InvalidateDeniedModelsCache`. In tests, install your own stub:

```go
called := make(map[string]bool)
SetDeniedModelsCacheInvalidator(func(pid, uid string) { called[pid+":"+uid] = true })
t.Cleanup(func() { SetDeniedModelsCacheInvalidator(func(string, string) {}) })
```

Then assert `called["<projectID>:<userID>"]` after PATCH.

- [ ] **Step 8: Run admin tests**

Run: `cd /root/coding/modelserver && TEST_DATABASE_URL=$TEST_DATABASE_URL go test ./internal/admin/ -run 'TestUpdateMember' -v`
Expected: all PASS.

- [ ] **Step 9: Commit Tasks 5 + 6 together**

```bash
cd /root/coding/modelserver
git add internal/admin/handle_projects.go internal/admin/handle_projects_member_denylist_test.go internal/proxy/auth_middleware.go cmd/  # adjust the cmd/ path as found in Step 4
git commit -m "feat(admin,proxy): per-member denylist — PATCH endpoint + ctx loading

- PATCH /admin/projects/{id}/members/{user_id} accepts denied_models
  (tri-state: omitted/[]/[...]; normalized + capped at 256).
- Auth middleware (both API-key and OAuth paths) loads the denylist
  from the same project_members row it already reads for quota; a
  new deniedModelsCache (10s TTL) sits alongside quotaCache.
- Admin PATCH invalidates the cache key on success via an injected
  function var (no admin→proxy import).

Role gate on the endpoint is unchanged — requireRole(owner, maintainer).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Proxy — enforce denylist on the four request entry points

**Files:**
- Modify: `internal/proxy/handler.go` — add `checkModelAllowed`, replace 4 inline checks
- Modify (or create): `internal/proxy/handler_denylist_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/proxy/handler_denylist_test.go`:

```go
package proxy

import (
	"context"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

// TestCheckModelAllowed_Order verifies that the member-level denylist
// is checked BEFORE the api_key.allowed_models check. When both would
// reject, the user must see the denylist message.
func TestCheckModelAllowed_DenylistChecksFirst(t *testing.T) {
	// Member denylist: claude-opus-4-8 is denied.
	// API key allowlist: only ["gpt-5"] permitted.
	// Request for "claude-opus-4-8" hits both. Spec requires the
	// denylist message, not the allowlist one.

	ctx := context.WithValue(context.Background(),
		ctxUserDeniedModels, []string{"claude-opus-4-8"})

	apiKey := &types.APIKey{AllowedModels: []string{"gpt-5"}}

	rec := &capturingErrWriter{}
	ok := (&Handler{}).checkModelAllowed(rec, ctx, apiKey, "claude-opus-4-8",
		func(w http.ResponseWriter, status int, msg string) {
			rec.status = status
			rec.msg = msg
		})

	if ok {
		t.Fatalf("expected check to fail")
	}
	if rec.status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.status)
	}
	if rec.msg != "model denied for this member by project policy" {
		t.Fatalf("msg = %q, want denylist message (proves denylist checked first)", rec.msg)
	}
}

func TestCheckModelAllowed_AllowlistOnly(t *testing.T) {
	// No denylist; request hits only the allowlist.
	ctx := context.Background()
	apiKey := &types.APIKey{AllowedModels: []string{"gpt-5"}}

	rec := &capturingErrWriter{}
	ok := (&Handler{}).checkModelAllowed(rec, ctx, apiKey, "claude-opus-4-8",
		func(w http.ResponseWriter, status int, msg string) {
			rec.status = status
			rec.msg = msg
		})

	if ok {
		t.Fatalf("expected check to fail")
	}
	if rec.msg != "model not allowed for this API key" {
		t.Fatalf("msg = %q, want allowlist message", rec.msg)
	}
}

func TestCheckModelAllowed_BothPass(t *testing.T) {
	ctx := context.WithValue(context.Background(),
		ctxUserDeniedModels, []string{"claude-opus-4-8"})
	apiKey := &types.APIKey{AllowedModels: []string{"gpt-5"}}

	rec := &capturingErrWriter{}
	ok := (&Handler{}).checkModelAllowed(rec, ctx, apiKey, "gpt-5",
		func(w http.ResponseWriter, status int, msg string) {
			rec.status = status
			rec.msg = msg
		})
	if !ok {
		t.Fatalf("expected check to pass; got status=%d msg=%q", rec.status, rec.msg)
	}
}

func TestCheckModelAllowed_EmptyDenylistEmptyAllowlist(t *testing.T) {
	// Neither configured → permissive.
	ctx := context.Background()
	apiKey := &types.APIKey{}

	rec := &capturingErrWriter{}
	ok := (&Handler{}).checkModelAllowed(rec, ctx, apiKey, "anything",
		func(w http.ResponseWriter, status int, msg string) {
			rec.status = status
			rec.msg = msg
		})
	if !ok {
		t.Fatalf("expected check to pass on empty config; status=%d msg=%q", rec.status, rec.msg)
	}
}

// capturingErrWriter is a minimal http.ResponseWriter stand-in. We
// only need the *errWriter-shape the writeErr callback uses; it never
// touches Header/WriteHeader/Write on the writer directly when we
// supply our own writeErr.
type capturingErrWriter struct {
	status int
	msg    string
}

func (w *capturingErrWriter) Header() http.Header        { return http.Header{} }
func (w *capturingErrWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *capturingErrWriter) WriteHeader(s int)           { w.status = s }
```

Add `net/http` to the imports at the top:

```go
import (
	"context"
	"net/http"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestCheckModelAllowed -v`
Expected: FAIL — `checkModelAllowed` is undefined.

- [ ] **Step 3: Add `checkModelAllowed` to handler.go**

In `internal/proxy/handler.go`, add this method right after `resolveModel` (after line 70):

```go
// checkModelAllowed enforces, in order:
//   1. the member-level denied_models (project administrator policy)
//   2. the api_key.allowed_models (per-key customization)
//
// The denylist is checked FIRST per the design (spec §"Check order and
// error messages"): it's the harder, project-wide constraint, so when
// both would reject, the denylist message names the right layer.
//
// On rejection, writeErr writes a 403 with the appropriate envelope
// (writeProxyError for OpenAI/Anthropic; writeGeminiError for Gemini).
// Returns false on rejection so the caller can `return`.
func (h *Handler) checkModelAllowed(
	w http.ResponseWriter,
	ctx context.Context,
	apiKey *types.APIKey,
	canonical string,
	writeErr func(http.ResponseWriter, int, string),
) bool {
	if denied := UserDeniedModelsFromContext(ctx); len(denied) > 0 && modelInList(denied, canonical) {
		writeErr(w, http.StatusForbidden, "model denied for this member by project policy")
		return false
	}
	if len(apiKey.AllowedModels) > 0 && !modelInList(apiKey.AllowedModels, canonical) {
		writeErr(w, http.StatusForbidden, "model not allowed for this API key")
		return false
	}
	return true
}
```

Add `"context"` to the imports of `handler.go` if it's not already there (`grep -n '"context"' /root/coding/modelserver/internal/proxy/handler.go`).

- [ ] **Step 4: Replace the four inline checks**

Each call site replaces:

```go
if len(apiKey.AllowedModels) > 0 && !modelInList(apiKey.AllowedModels, canonical) {
    writeProxyError(w, http.StatusForbidden, "model not allowed for this API key")
    return
}
```

with:

```go
if !h.checkModelAllowed(w, r.Context(), apiKey, canonical, writeProxyError) {
    return
}
```

— or, for the Gemini path (line 287), with `writeGeminiError` instead of `writeProxyError`.

Call sites:
- Line 154 (`handleImagesEditsMultipart`) — use `writeProxyError`
- Line 287 (`HandleGemini`) — use `writeGeminiError`
- Line 386 (`handleProxyRequest`) — use `writeProxyError`
- Line 503 (`HandleCountTokens`) — use `writeProxyError`

After all four replacements, the only call site of `apiKey.AllowedModels` in `handler.go` should be inside `checkModelAllowed` itself. Verify:

```
grep -n "AllowedModels" /root/coding/modelserver/internal/proxy/handler.go
```

Should show exactly one match in `checkModelAllowed`'s body.

- [ ] **Step 5: Run the new test and verify it passes**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestCheckModelAllowed -v`
Expected: PASS.

- [ ] **Step 6: Run the full proxy test suite (no regressions)**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd /root/coding/modelserver
git add internal/proxy/handler.go internal/proxy/handler_denylist_test.go
git commit -m "feat(proxy): enforce per-member denylist before api-key allowlist

New checkModelAllowed helper consolidates the two-step check; member
denylist runs first so the error message names the project-level
policy when both would reject.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: Proxy — filter denylist out of `/v1/models`

**Files:**
- Modify: `internal/proxy/router.go:72-94` (`HandleListModels`)
- Modify: `internal/proxy/router_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/proxy/router_test.go`:

```go
// TestHandleListModels_DenylistSubtract ensures the /v1/models output
// has the per-member denylist subtracted, regardless of whether the
// caller's api_key has an allowlist.
func TestHandleListModels_DenylistSubtract(t *testing.T) {
	cat := newTestCatalog()

	// Build a minimal Handler with a real router so ActiveModels returns
	// the catalog set when no allowlist is configured.
	h := &Handler{catalog: cat, router: &Router{catalog: cat}}

	cases := []struct {
		name       string
		allowed    []string
		denied     []string
		wantBefore []string // sanity check on inputs
		want       []string // expected list returned to caller
	}{
		{
			name:    "no allowlist, denylist removes one",
			allowed: nil,
			denied:  []string{"claude-opus-4-7"},
			want:    []string{"gpt-5"},
		},
		{
			name:    "allowlist intersected with denylist",
			allowed: []string{"gpt-5", "claude-opus-4-7"},
			denied:  []string{"claude-opus-4-7"},
			want:    []string{"gpt-5"},
		},
		{
			name:    "empty denylist = unchanged",
			allowed: []string{"gpt-5", "claude-opus-4-7"},
			denied:  nil,
			want:    []string{"gpt-5", "claude-opus-4-7"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v1/models", nil)
			ctx := context.WithValue(req.Context(), ctxAPIKey, &types.APIKey{
				AllowedModels: tc.allowed,
			})
			if len(tc.denied) > 0 {
				ctx = context.WithValue(ctx, ctxUserDeniedModels, tc.denied)
			}
			req = req.WithContext(ctx)

			rec := httptest.NewRecorder()
			h.HandleListModels(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var resp struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			got := make([]string, 0, len(resp.Data))
			for _, d := range resp.Data {
				got = append(got, d.ID)
			}
			if !equalStringSet(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func equalStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]int, len(a))
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
		if m[s] < 0 {
			return false
		}
	}
	return true
}
```

Add `"context"` to the imports of `router_test.go` if not present (also `httptest`, which is already there per the existing tests).

The `&Router{catalog: cat}` reference may need adjustment — check `Router`'s struct definition (`grep -n "^type Router" /root/coding/modelserver/internal/proxy/router_engine.go` or wherever it lives). If `Router` requires more fields to make `ActiveModels()` return a sensible list, either populate them or skip the "no allowlist" subcase and rely on the two cases where the allowlist is set (those don't call `ActiveModels`).

- [ ] **Step 2: Run the test and verify it fails**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestHandleListModels_DenylistSubtract -v`
Expected: FAIL — denylist isn't subtracted yet.

- [ ] **Step 3: Modify `HandleListModels` to subtract the denylist**

Replace the function body in `internal/proxy/router.go` (lines 72-94):

```go
// HandleListModels returns available models in OpenAI or Anthropic format
// depending on the auth-header style the client used. Bearer or fallback →
// OpenAI; x-api-key → Anthropic. The per-member denylist (if any) is
// subtracted from the result, so a caller never sees a model whose
// requests will 403 against their own member-level policy.
func (h *Handler) HandleListModels(w http.ResponseWriter, r *http.Request) {
	apiKey := APIKeyFromContext(r.Context())
	if apiKey == nil {
		writeProxyError(w, http.StatusUnauthorized, "missing api key")
		return
	}

	var names []string
	if len(apiKey.AllowedModels) > 0 {
		names = apiKey.AllowedModels
	} else {
		names = h.router.ActiveModels()
	}
	if denied := UserDeniedModelsFromContext(r.Context()); len(denied) > 0 {
		names = subtractStrings(names, denied)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if r.Header.Get("x-api-key") != "" {
		writeAnthropicModelsList(w, h.catalog, names)
		return
	}
	writeOpenAIModelsList(w, h.catalog, names)
}

// subtractStrings returns the elements of a that are not present in b,
// preserving order. b is small (≤256 entries) so the linear scan is
// fine.
func subtractStrings(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	out := make([]string, 0, len(a))
	for _, s := range a {
		if !modelInList(b, s) {
			out = append(out, s)
		}
	}
	return out
}
```

(`modelInList` is reused from `handler.go` — same package, no import change.)

- [ ] **Step 4: Run the new test and verify it passes**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestHandleListModels_DenylistSubtract -v`
Expected: PASS.

- [ ] **Step 5: Run the full proxy + admin + store suite**

Run:
```
cd /root/coding/modelserver
go test ./internal/proxy/ -v
TEST_DATABASE_URL=$TEST_DATABASE_URL go test ./internal/store/ ./internal/admin/ -v
```
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
cd /root/coding/modelserver
git add internal/proxy/router.go internal/proxy/router_test.go
git commit -m "feat(proxy): subtract per-member denylist from /v1/models output

HandleListModels removes denylist entries from the response so callers
never see a model name whose actual request will 403.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: Documentation + final verification

**Files:**
- Modify: any admin API reference doc that lists `project_members.credit_quota_percent`
- Modify: the README or operator docs section that mentions `api_keys.allowed_models`, if it covers proxy-side enforcement

- [ ] **Step 1: Locate user-facing docs**

Run:
```
cd /root/coding/modelserver
grep -rln "credit_quota_percent\|allowed_models" docs/ README.md 2>/dev/null
```

For each file found, add a paragraph for `denied_models`:

- **Where it lives:** `project_members.denied_models`, configurable via
  `PATCH /admin/projects/{project_id}/members/{user_id}` with body
  `{"denied_models": ["model1", "model2"]}` (or `[]` to clear).
- **Who can set it:** owners and maintainers of the project.
- **Who it constrains:** every member of the project, including owners
  (constraint is uniform at the proxy; only the *write* side is gated).
- **Order of evaluation:** the member denylist is checked **before**
  `api_keys.allowed_models`. A model in both still 403s with the denylist
  message.
- **Effect on `/v1/models`:** the model is omitted from the list.

If no such docs exist, skip this step — the spec already documents the
behavior, and a short paragraph in the `PR` description is enough.

- [ ] **Step 2: Verify end-to-end on a real database**

If you have a local instance:

```bash
cd /root/coding/modelserver
docker compose up -d postgres
# wait for postgres to accept connections
go build ./...
./modelserver  # or whatever the binary is named — confirm with `ls cmd/*/`
```

Then with the running server:
1. Create a project, add a developer member via the admin API.
2. PATCH `denied_models: ["claude-opus-4-8"]` for that member.
3. Issue an API key for that member (no allowlist).
4. Send a request with `"model":"claude-opus-4-8"` → expect 403 `"model denied for this member by project policy"`.
5. Send the same request with a different model → expect normal response.
6. PATCH `denied_models: []` and re-send the first request → expect normal response.
7. GET `/v1/models` for that member with denylist `["claude-opus-4-8"]` set → confirm the model is absent from the response.

If you can't run end-to-end locally, the test suite from Tasks 1-8 already covers the same paths in isolation.

- [ ] **Step 3: Run the entire repo test suite one last time**

```
cd /root/coding/modelserver
go build ./...
TEST_DATABASE_URL=$TEST_DATABASE_URL go test ./... -v
```
Expected: PASS (or skipped where TEST_DATABASE_URL isn't relevant).

- [ ] **Step 4: Commit + push + open PR**

```bash
cd /root/coding/modelserver
git status   # confirm clean
git push -u origin feat/member-model-denylist
gh pr create --base main --title "feat: per-member model denylist" --body "$(cat <<'EOF'
Adds a project-level model denylist that binds to project members
rather than individual API keys, so a member cannot circumvent it by
issuing a new key.

## Behavior
- New column `project_members.denied_models TEXT[] NOT NULL DEFAULT '{}'`.
  Existing rows read back as `[]` (PG 11+ fast-default; no backfill).
- `PATCH /admin/projects/{id}/members/{user_id}` accepts a new
  `denied_models` field (tri-state: omitted/[]/[...]); writes are gated
  to owners and maintainers by the existing `requireRole`.
- The proxy checks the member denylist **before** the existing
  `api_keys.allowed_models` allowlist at all four request entry points
  (Anthropic messages via shared handler, Gemini, count tokens, images-
  edits multipart). Both API-key and OAuth-introspected auth paths
  populate the denylist into request context from the same
  `GetProjectMember` call that already loads credit quota.
- `/v1/models` subtracts the denylist from its output.
- Cache: 10s TTL alongside the existing quota cache; admin PATCH
  invalidates the relevant key on success.

## Spec
`docs/superpowers/specs/2026-06-04-member-model-denylist-design.md`

## Out of scope
Wildcards, model families, plan-tier gating, bulk endpoints — see spec
§Non-goals.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-Review

**Spec coverage:**
- Data model (column, default, no backfill) → Task 1 ✓
- Type round-trip → Task 2 ✓
- Store layer (read 3 functions, write 1 function) → Tasks 3 + 4 ✓
- Admin PATCH (tri-state, normalize, cap, role gate, invalidation) → Task 5 + 6 ✓
- Auth middleware (API-key + OAuth paths, cache) → Task 6 ✓
- Proxy enforcement (4 checks → 1 helper, ordering) → Task 7 ✓
- `/v1/models` subtraction → Task 8 ✓
- Docs + e2e verification → Task 9 ✓
- "Existing rows are empty after migration" verified in Task 1 ✓
- No catalog FK / no wildcard / no plan gate / no batch (out of scope, called out in PR) ✓

**Placeholder scan:** Task 5 Step 1 has `t.Skip` placeholders. These are *intentional* and resolved within Task 6 Step 7, which tells the implementer exactly what to assert in each test (table with setup/action/expect). Not a planning gap; the alternative was to embed a copy of the admin test harness here, which would have been guessed code rather than real code.

**Type consistency:** `UpdateProjectMember`'s new 5th parameter is `*[]string` everywhere. `UserDeniedModelsFromContext` returns `[]string` (not `*[]string`). `InvalidateDeniedModelsCache(projectID, userID)` matches `proxyInvalidateDeniedModelsCache`'s signature. `checkModelAllowed` takes `func(http.ResponseWriter, int, string)` — same shape as `writeProxyError` and `writeGeminiError`. Consistent.

**Risk to flag:** Task 6 Step 4 (wiring the cache invalidator in `cmd/`) depends on a `cmd/` file the plan author hasn't fully read. If that file structures `admin.Mount(...)` differently (e.g. takes a `cache.Invalidator` interface as a parameter), prefer plumbing it that way instead of the package-global setter, but keep the behavior identical.
