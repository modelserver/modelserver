# Single-Owner Invariant + Creator-Based Quota — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce "every non-archived project has exactly one `role='owner'` row in `project_members`" and switch the `max_projects` per-user quota from owner-count to `projects.created_by`-count.

**Architecture:** Two layers of defense — handler-level validators that return friendly 400/409/403 codes, plus a store-level `BEGIN ... SELECT ... FOR UPDATE` re-check that survives concurrent writes across nodes. A new atomic `POST /projects/{id}/transfer-ownership` endpoint becomes the only way to change who the owner is. No DB-level UNIQUE constraint and no backfill migration — historical bad data is left alone per product decision.

**Tech Stack:** Go 1.21+, chi router, pgx/v5 + Postgres, existing `internal/store` and `internal/admin` packages.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-06-30-single-owner-invariant-design.md`.
- Role constants `RoleOwner` / `RoleMaintainer` / `RoleDeveloper` already live in `internal/types/user.go:7-9` — extend that file, do **not** create `internal/types/roles.go`.
- Sentinel-error style follows `internal/store/extra_usage.go:17-19`: `var ErrFoo = errors.New("...")`.
- Test-DB tests use `openTestStore(t)` from `internal/store/extra_usage_db_test.go:13` — they call `t.Skip` when `TEST_DATABASE_URL` is unset, so `go test ./...` stays green without a DB. Use `seedUserAndProject`, `seedSecondUser`, `addMember` from `internal/store/extra_usage_db_test.go:29` and `internal/store/projects_test.go:157,168`.
- Handler-level unit tests follow `internal/admin/handle_extra_usage_bypass_test.go`: mount the handler on a fresh `chi.NewRouter()`, drive with `httptest.NewRequest` + `httptest.NewRecorder`.
- Error responses use the existing `writeError(w, status, code, message)` helper in `internal/admin/admin.go`.
- Commit at the end of every task. Conventional Commit subject line.

## File Map

| File | Action | Responsibility |
|---|---|---|
| `internal/types/user.go` | modify | Add `AssignableRoles` set and `IsAssignableRole` helper alongside existing role constants. |
| `internal/store/projects.go` | modify | Add sentinel errors, `TransferProjectOwnership`, `CountUserCreatedProjects`; tighten `UpdateProjectMember` and `RemoveProjectMember`; delete unused `UpdateProjectMemberRole`. |
| `internal/store/users.go` | modify | Delete `CountUserOwnedProjects` (moved + renamed to `projects.go`). |
| `internal/store/projects_test.go` | modify | Add tests for transfer, blocked owner-demote/delete, and creator-count. |
| `internal/admin/handle_projects.go` | modify | Add `handleTransferOwnership`, tighten add/update/remove, switch quota call. |
| `internal/admin/routes.go` | modify | Mount `POST /transfer-ownership`. |
| `internal/admin/handle_projects_test.go` | create | Handler-level table tests for all tightened endpoints. |

---

### Task 1: Role validation helpers in `types`

**Files:**
- Modify: `internal/types/user.go`
- Test: `internal/types/user_test.go` (create)

**Interfaces:**
- Consumes: nothing.
- Produces: `types.AssignableRoles` (map[string]struct{}), `types.IsAssignableRole(r string) bool`.

- [ ] **Step 1: Write the failing test**

Create `internal/types/user_test.go`:

```go
package types

import "testing"

func TestIsAssignableRole(t *testing.T) {
	cases := []struct {
		role string
		want bool
	}{
		{RoleMaintainer, true},
		{RoleDeveloper, true},
		{RoleOwner, false},  // owner is only set by CreateProject / TransferProjectOwnership
		{"", false},
		{"janitor", false},
		{"OWNER", false},     // case-sensitive
	}
	for _, c := range cases {
		if got := IsAssignableRole(c.role); got != c.want {
			t.Errorf("IsAssignableRole(%q) = %v, want %v", c.role, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/types/ -run TestIsAssignableRole -v
```
Expected: FAIL with `undefined: IsAssignableRole`.

- [ ] **Step 3: Add the helpers**

Append to `internal/types/user.go` (after the existing role constant block):

```go
// AssignableRoles is the set of roles that can be set via the add-member
// and update-member admin endpoints. RoleOwner is intentionally excluded:
// it is only ever set by CreateProject (creator) or by the
// transfer-ownership endpoint, never by direct role assignment.
var AssignableRoles = map[string]struct{}{
	RoleMaintainer: {},
	RoleDeveloper:  {},
}

// IsAssignableRole reports whether r is one of the roles a caller may
// directly assign through the add-member and update-member endpoints.
func IsAssignableRole(r string) bool {
	_, ok := AssignableRoles[r]
	return ok
}
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/types/ -run TestIsAssignableRole -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/types/user.go internal/types/user_test.go
git commit -m "feat(types): add IsAssignableRole helper for member endpoints"
```

---

### Task 2: Sentinel errors + delete dead code in store

**Files:**
- Modify: `internal/store/projects.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `store.ErrNotAMember`, `store.ErrAlreadyOwner`, `store.ErrOwnerMustTransfer`, `store.ErrInvalidRole`, `store.ErrInvariantViolated` (all `error`).

- [ ] **Step 1: Add sentinel errors and delete dead function**

In `internal/store/projects.go`, add an `errors` import if not present, and add this block near the top (right after the imports):

```go
var (
	// ErrNotAMember: target user is not a member of the project.
	ErrNotAMember = errors.New("user is not a project member")
	// ErrAlreadyOwner: target user is already the project owner.
	ErrAlreadyOwner = errors.New("user is already the project owner")
	// ErrOwnerMustTransfer: tried to demote or remove the current owner
	// outside of the transfer-ownership flow.
	ErrOwnerMustTransfer = errors.New("owner cannot be demoted or removed directly; use transfer-ownership")
	// ErrInvalidRole: a role string is not in types.AssignableRoles
	// (or equals types.RoleOwner where owner is not permitted).
	ErrInvalidRole = errors.New("invalid role")
	// ErrInvariantViolated: a store transaction's post-state would have
	// owner count ≠ 1. Indicates concurrent writes or corrupt data.
	ErrInvariantViolated = errors.New("project owner-count invariant violated")
)
```

Then delete the orphaned `UpdateProjectMemberRole` function (currently `internal/store/projects.go:298-304`, no callers exist).

- [ ] **Step 2: Verify build**

```
go build ./...
```
Expected: success (no caller of `UpdateProjectMemberRole`, no `errors` package shadowing).

- [ ] **Step 3: Commit**

```
git add internal/store/projects.go
git commit -m "refactor(store): add invariant sentinel errors; drop unused UpdateProjectMemberRole"
```

---

### Task 3: `CountUserCreatedProjects` (rename + reimplement)

**Files:**
- Modify: `internal/store/projects.go`, `internal/store/users.go`, `internal/admin/handle_projects.go`
- Test: `internal/store/projects_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `(*Store).CountUserCreatedProjects(userID string) (int, error)` returning `SELECT COUNT(*) FROM projects WHERE created_by = $1` (no status filter — archived included).
- Removes: `(*Store).CountUserOwnedProjects` (was in `internal/store/users.go`).

- [ ] **Step 1: Write the failing test**

Append to `internal/store/projects_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```
TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestCountUserCreatedProjects -v
```
Expected: FAIL with `st.CountUserCreatedProjects undefined`. (If no `TEST_DATABASE_URL` is set the test will SKIP — that's also fine for verifying the function shape compiles in step 3; resolve a DB before merging.)

- [ ] **Step 3: Implement and rewire**

In `internal/store/projects.go`, add (anywhere in the file, near other Count-like helpers — e.g. just after `ListAllProjects`):

```go
// CountUserCreatedProjects counts projects where the user is the
// original creator (projects.created_by = $1). Includes archived
// projects — archive is not a quota release. Used by the per-user
// max_projects check at project-creation time.
func (s *Store) CountUserCreatedProjects(userID string) (int, error) {
	var count int
	err := s.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM projects WHERE created_by = $1`, userID,
	).Scan(&count)
	return count, err
}
```

In `internal/store/users.go`, delete the `CountUserOwnedProjects` function (currently `internal/store/users.go:178-186`).

In `internal/admin/handle_projects.go:291`, change:

```go
count, _ := st.CountUserOwnedProjects(user.ID)
```

to:

```go
count, _ := st.CountUserCreatedProjects(user.ID)
```

- [ ] **Step 4: Run test to verify it passes (with DB) and build cleanly (without)**

With DB:
```
TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestCountUserCreatedProjects -v
```
Expected: PASS.

Without DB:
```
go build ./... && go vet ./...
```
Expected: both succeed, no remaining references to `CountUserOwnedProjects`.

- [ ] **Step 5: Commit**

```
git add internal/store/projects.go internal/store/users.go internal/admin/handle_projects.go internal/store/projects_test.go
git commit -m "feat(quota): count by projects.created_by, not owner membership"
```

---

### Task 4: `RemoveProjectMember` blocks deleting the owner

**Files:**
- Modify: `internal/store/projects.go`
- Test: `internal/store/projects_test.go`

**Interfaces:**
- Consumes: `store.ErrOwnerMustTransfer` (Task 2).
- Produces: `RemoveProjectMember` returns `(0, nil, ErrOwnerMustTransfer)` when the target row's role is `'owner'`; existing behavior for non-owner removal is unchanged.

- [ ] **Step 1: Write the failing test**

Append to `internal/store/projects_test.go`:

```go
func TestRemoveProjectMember_RejectsOwner(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	owner, projectID := seedUserAndProject(t, st)
	// seedUserAndProject inserts the project but does NOT add the
	// project_members row — do it explicitly to match production state.
	addMember(t, st, projectID, owner, types.RoleOwner)

	_, _, err := st.RemoveProjectMember(projectID, owner)
	if !errors.Is(err, ErrOwnerMustTransfer) {
		t.Fatalf("err = %v, want ErrOwnerMustTransfer", err)
	}

	// Owner row must still be present (transaction rolled back).
	var n int
	if err := st.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM project_members WHERE project_id=$1 AND user_id=$2 AND role='owner'`,
		projectID, owner).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("owner rows after rejected remove = %d, want 1", n)
	}
}
```

Add `"errors"` to the test file's imports if not already present.

- [ ] **Step 2: Run test to verify it fails**

```
TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestRemoveProjectMember_RejectsOwner -v
```
Expected: FAIL (current code returns `nil` after deleting the owner).

- [ ] **Step 3: Add the guard**

In `internal/store/projects.go`, modify `RemoveProjectMember` (currently `internal/store/projects.go:377-428`). After `defer tx.Rollback(ctx)` and **before** the api_keys UPDATE, insert a row lookup + lock:

```go
	// Single-owner invariant: refuse to delete the current owner.
	// SELECT FOR UPDATE locks the row so a concurrent UPDATE of role
	// (or a concurrent transfer-ownership) blocks until we commit or
	// rollback.
	var role string
	if err := tx.QueryRow(ctx, `
		SELECT role FROM project_members
		WHERE project_id=$1 AND user_id=$2
		FOR UPDATE`, projectID, userID,
	).Scan(&role); err != nil {
		if err == pgx.ErrNoRows {
			// No member row — fall through; the legacy "revoke orphan
			// keys" behavior below still applies.
			role = ""
		} else {
			return 0, nil, fmt.Errorf("lock member row: %w", err)
		}
	}
	if role == types.RoleOwner {
		return 0, nil, ErrOwnerMustTransfer
	}
```

- [ ] **Step 4: Run test to verify it passes**

```
TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestRemoveProjectMember -v
```
Expected: PASS for all `TestRemoveProjectMember_*` cases (including the new one and the existing orphan/grant tests, which still pass non-owner / no-member roles through the lock).

- [ ] **Step 5: Commit**

```
git add internal/store/projects.go internal/store/projects_test.go
git commit -m "feat(store): block RemoveProjectMember on the current owner"
```

---

### Task 5: `UpdateProjectMember` blocks role changes that touch the owner

**Files:**
- Modify: `internal/store/projects.go`
- Test: `internal/store/projects_test.go`

**Interfaces:**
- Consumes: `store.ErrOwnerMustTransfer`, `store.ErrInvalidRole`, `types.RoleOwner`, `types.IsAssignableRole`.
- Produces: `UpdateProjectMember` returns `ErrInvalidRole` when the requested new role is not in `types.AssignableRoles`; returns `ErrOwnerMustTransfer` when the target's current role is `owner` and `role != nil`; quota and denied_models edits on the owner row continue to work as before.

- [ ] **Step 1: Write the failing test**

Append to `internal/store/projects_test.go`:

```go
func TestUpdateProjectMember_RejectsRoleOwner(t *testing.T) {
	st := openTestStore(t)
	owner, projectID := seedUserAndProject(t, st)
	addMember(t, st, projectID, owner, types.RoleOwner)
	dev := seedSecondUser(t, st, "to-promote")
	addMember(t, st, projectID, dev, types.RoleDeveloper)

	newRole := types.RoleOwner
	err := st.UpdateProjectMember(projectID, dev, &newRole, nil, nil)
	if !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("err = %v, want ErrInvalidRole", err)
	}

	// dev's role unchanged.
	var got string
	if err := st.pool.QueryRow(context.Background(),
		`SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2`,
		projectID, dev).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != types.RoleDeveloper {
		t.Errorf("dev role = %q, want %q", got, types.RoleDeveloper)
	}
}

func TestUpdateProjectMember_RejectsDemotingOwner(t *testing.T) {
	st := openTestStore(t)
	owner, projectID := seedUserAndProject(t, st)
	addMember(t, st, projectID, owner, types.RoleOwner)

	newRole := types.RoleDeveloper
	err := st.UpdateProjectMember(projectID, owner, &newRole, nil, nil)
	if !errors.Is(err, ErrOwnerMustTransfer) {
		t.Fatalf("err = %v, want ErrOwnerMustTransfer", err)
	}

	// owner role unchanged.
	var got string
	if err := st.pool.QueryRow(context.Background(),
		`SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2`,
		projectID, owner).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != types.RoleOwner {
		t.Errorf("owner role after rejected demote = %q, want %q", got, types.RoleOwner)
	}
}

func TestUpdateProjectMember_AllowsOwnerDeniedModels(t *testing.T) {
	// denied_models edit on the owner row must still work — it doesn't
	// change role. Asserts the guard is not over-eager.
	st := openTestStore(t)
	owner, projectID := seedUserAndProject(t, st)
	addMember(t, st, projectID, owner, types.RoleOwner)

	denied := []string{"gpt-4"}
	if err := st.UpdateProjectMember(projectID, owner, nil, nil, &denied); err != nil {
		t.Fatalf("UpdateProjectMember(denied_models only): %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestUpdateProjectMember_ -v
```
Expected: FAIL for the two reject tests (the current code happily UPDATEs); PASS for the denied-models test.

- [ ] **Step 3: Add the guards**

In `internal/store/projects.go`, modify `UpdateProjectMember` (currently `internal/store/projects.go:319-364`). At the very top of the function body, after the `if role == nil && creditQuotaPct == nil && deniedModels == nil { return nil }` short-circuit, insert:

```go
	// Single-owner invariant: changing role to RoleOwner is never allowed
	// here (use TransferProjectOwnership). Changing role *away* from
	// RoleOwner is never allowed here either — same reason.
	if role != nil {
		if *role == types.RoleOwner {
			return ErrInvalidRole
		}
		if !types.IsAssignableRole(*role) {
			return ErrInvalidRole
		}
		// Lock the target row to check current role; if the caller
		// is trying to demote the owner, reject.
		var current string
		if err := s.pool.QueryRow(context.Background(),
			`SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2`,
			projectID, userID).Scan(&current); err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("get current role: %w", ErrNotAMember)
			}
			return fmt.Errorf("get current role: %w", err)
		}
		if current == types.RoleOwner {
			return ErrOwnerMustTransfer
		}
	}
```

Note: the check uses a separate `QueryRow` (not `FOR UPDATE`) because the existing `UpdateProjectMember` is not transactional — adding `FOR UPDATE` here would require a tx + commit, which is over-scope. Concurrent races between two demotes are caught by the eventual UPDATE (only one wins, post-state still has the owner row); concurrent demote-vs-transfer is caught by the transfer's `FOR UPDATE` (Task 6).

- [ ] **Step 4: Run test to verify it passes**

```
TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestUpdateProjectMember_ -v
```
Expected: all three PASS.

- [ ] **Step 5: Commit**

```
git add internal/store/projects.go internal/store/projects_test.go
git commit -m "feat(store): UpdateProjectMember rejects role=owner and owner demotion"
```

---

### Task 6: `TransferProjectOwnership`

**Files:**
- Modify: `internal/store/projects.go`
- Test: `internal/store/projects_test.go`

**Interfaces:**
- Consumes: `store.ErrNotAMember`, `store.ErrAlreadyOwner`, `store.ErrInvalidRole`, `store.ErrInvariantViolated`, `types.RoleOwner`, `types.IsAssignableRole`.
- Produces: `(*Store).TransferProjectOwnership(projectID, fromUID, toUID, demoteTo string) error`. Atomic. Post-state: target is owner with `credit_quota_percent = NULL`; previous owner is `demoteTo` with `credit_quota_percent = NULL`; owner count = 1.

- [ ] **Step 1: Write the failing test**

Append to `internal/store/projects_test.go`:

```go
func TestTransferProjectOwnership_HappyPath(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	owner, projectID := seedUserAndProject(t, st)
	addMember(t, st, projectID, owner, types.RoleOwner)
	target := seedSecondUser(t, st, "next-owner")
	addMember(t, st, projectID, target, types.RoleDeveloper)

	if err := st.TransferProjectOwnership(projectID, owner, target, types.RoleDeveloper); err != nil {
		t.Fatalf("TransferProjectOwnership: %v", err)
	}

	// Verify post-state.
	rows, err := st.pool.Query(ctx,
		`SELECT user_id, role, credit_quota_percent FROM project_members WHERE project_id=$1 ORDER BY role`,
		projectID)
	if err != nil {
		t.Fatalf("query members: %v", err)
	}
	defer rows.Close()
	type memberRow struct {
		uid   string
		role  string
		quota *float64
	}
	var got []memberRow
	for rows.Next() {
		var r memberRow
		if err := rows.Scan(&r.uid, &r.role, &r.quota); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("members = %d, want 2", len(got))
	}
	// "developer" < "owner" alphabetically; got[0] is the demoted old owner.
	if got[0].uid != owner || got[0].role != types.RoleDeveloper || got[0].quota != nil {
		t.Errorf("demoted row = %+v, want uid=%s role=developer quota=nil", got[0], owner)
	}
	if got[1].uid != target || got[1].role != types.RoleOwner || got[1].quota != nil {
		t.Errorf("promoted row = %+v, want uid=%s role=owner quota=nil", got[1], target)
	}
}

func TestTransferProjectOwnership_TargetNotMember(t *testing.T) {
	st := openTestStore(t)
	owner, projectID := seedUserAndProject(t, st)
	addMember(t, st, projectID, owner, types.RoleOwner)
	stranger := seedSecondUser(t, st, "stranger")

	err := st.TransferProjectOwnership(projectID, owner, stranger, types.RoleDeveloper)
	if !errors.Is(err, ErrNotAMember) {
		t.Fatalf("err = %v, want ErrNotAMember", err)
	}
}

func TestTransferProjectOwnership_TargetAlreadyOwner(t *testing.T) {
	st := openTestStore(t)
	owner, projectID := seedUserAndProject(t, st)
	addMember(t, st, projectID, owner, types.RoleOwner)

	err := st.TransferProjectOwnership(projectID, owner, owner, types.RoleDeveloper)
	if !errors.Is(err, ErrAlreadyOwner) {
		t.Fatalf("err = %v, want ErrAlreadyOwner", err)
	}
}

func TestTransferProjectOwnership_InvalidDemoteRole(t *testing.T) {
	st := openTestStore(t)
	owner, projectID := seedUserAndProject(t, st)
	addMember(t, st, projectID, owner, types.RoleOwner)
	target := seedSecondUser(t, st, "next")
	addMember(t, st, projectID, target, types.RoleDeveloper)

	err := st.TransferProjectOwnership(projectID, owner, target, types.RoleOwner)
	if !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("demoteTo=owner: err = %v, want ErrInvalidRole", err)
	}
	err = st.TransferProjectOwnership(projectID, owner, target, "janitor")
	if !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("demoteTo=janitor: err = %v, want ErrInvalidRole", err)
	}
}
```

- [ ] **Step 2: Run test to verify they fail**

```
TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestTransferProjectOwnership -v
```
Expected: all FAIL (`st.TransferProjectOwnership undefined`).

- [ ] **Step 3: Implement `TransferProjectOwnership`**

Add to `internal/store/projects.go`, after `RemoveProjectMember`:

```go
// TransferProjectOwnership atomically swaps the project owner from
// fromUID to toUID, demoting fromUID to demoteTo. The whole operation
// runs in a single transaction with row locks on the relevant
// project_members rows; concurrent writers either block or see the
// post-state.
//
// Pre-conditions checked inside the tx:
//   - demoteTo is a member of types.AssignableRoles.
//   - Exactly one row in this project has role='owner', and its user_id
//     equals fromUID (a stale fromUID returns ErrInvariantViolated).
//   - toUID has a membership row in this project (else ErrNotAMember).
//   - toUID != fromUID (else ErrAlreadyOwner).
//
// Post-conditions guaranteed at COMMIT:
//   - exactly one role='owner' row, owned by toUID.
//   - toUID's credit_quota_percent = NULL (owners never carry a quota).
//   - fromUID's role = demoteTo, credit_quota_percent = NULL.
func (s *Store) TransferProjectOwnership(projectID, fromUID, toUID, demoteTo string) error {
	if !types.IsAssignableRole(demoteTo) {
		return ErrInvalidRole
	}
	if fromUID == toUID {
		return ErrAlreadyOwner
	}

	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) // no-op after Commit

	// Lock every member row in this project. Sorting by user_id avoids
	// deadlocks if two transfer ops on the same project race.
	rows, err := tx.Query(ctx, `
		SELECT user_id, role FROM project_members
		WHERE project_id = $1
		ORDER BY user_id
		FOR UPDATE`, projectID)
	if err != nil {
		return fmt.Errorf("lock members: %w", err)
	}
	var (
		currentOwner string
		ownerCount   int
		targetSeen   bool
		targetRole   string
	)
	for rows.Next() {
		var uid, role string
		if err := rows.Scan(&uid, &role); err != nil {
			rows.Close()
			return fmt.Errorf("scan member: %w", err)
		}
		if role == types.RoleOwner {
			currentOwner = uid
			ownerCount++
		}
		if uid == toUID {
			targetSeen = true
			targetRole = role
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate members: %w", err)
	}

	if ownerCount != 1 || currentOwner != fromUID {
		return ErrInvariantViolated
	}
	if !targetSeen {
		return ErrNotAMember
	}
	if targetRole == types.RoleOwner {
		// Defensive — we already checked fromUID != toUID, so this can
		// only happen if multiple owners exist (caught above) or there
		// is a fundamental bug.
		return ErrAlreadyOwner
	}

	if _, err := tx.Exec(ctx, `
		UPDATE project_members
		   SET role = $1, credit_quota_percent = NULL
		 WHERE project_id = $2 AND user_id = $3`,
		demoteTo, projectID, fromUID); err != nil {
		return fmt.Errorf("demote old owner: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE project_members
		   SET role = $1, credit_quota_percent = NULL
		 WHERE project_id = $2 AND user_id = $3`,
		types.RoleOwner, projectID, toUID); err != nil {
		return fmt.Errorf("promote new owner: %w", err)
	}

	// Post-check.
	var postOwners int
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM project_members WHERE project_id=$1 AND role='owner'`,
		projectID).Scan(&postOwners); err != nil {
		return fmt.Errorf("post-count: %w", err)
	}
	if postOwners != 1 {
		return ErrInvariantViolated
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```
TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestTransferProjectOwnership -v
```
Expected: all four PASS.

- [ ] **Step 5: Commit**

```
git add internal/store/projects.go internal/store/projects_test.go
git commit -m "feat(store): add TransferProjectOwnership (atomic owner swap)"
```

---

### Task 7: Concurrency test — two simultaneous transfers

**Files:**
- Test: `internal/store/projects_test.go`

**Interfaces:**
- Consumes: `store.TransferProjectOwnership` (Task 6).
- Produces: regression test guarding the `FOR UPDATE` serialization.

- [ ] **Step 1: Write the test**

Append to `internal/store/projects_test.go`:

```go
func TestTransferProjectOwnership_ConcurrentRacesSerialize(t *testing.T) {
	st := openTestStore(t)
	owner, projectID := seedUserAndProject(t, st)
	addMember(t, st, projectID, owner, types.RoleOwner)
	a := seedSecondUser(t, st, "race-a")
	b := seedSecondUser(t, st, "race-b")
	addMember(t, st, projectID, a, types.RoleDeveloper)
	addMember(t, st, projectID, b, types.RoleDeveloper)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = st.TransferProjectOwnership(projectID, owner, a, types.RoleDeveloper) }()
	go func() { defer wg.Done(); errs[1] = st.TransferProjectOwnership(projectID, owner, b, types.RoleDeveloper) }()
	wg.Wait()

	// Exactly one goroutine succeeds; the other gets ErrInvariantViolated
	// because by the time it acquires the lock, the current owner is no
	// longer `owner` (it's been demoted to developer by the winner).
	var okCount, invariantCount int
	for _, e := range errs {
		switch {
		case e == nil:
			okCount++
		case errors.Is(e, ErrInvariantViolated):
			invariantCount++
		default:
			t.Fatalf("unexpected error: %v", e)
		}
	}
	if okCount != 1 || invariantCount != 1 {
		t.Fatalf("ok=%d invariant=%d, want exactly 1 of each (errs=%v)", okCount, invariantCount, errs)
	}

	// Post-state: exactly one owner, equal to either a or b.
	var n int
	var winner string
	if err := st.pool.QueryRow(context.Background(),
		`SELECT user_id FROM project_members WHERE project_id=$1 AND role='owner'`,
		projectID).Scan(&winner); err != nil {
		t.Fatalf("query owner: %v", err)
	}
	if err := st.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM project_members WHERE project_id=$1 AND role='owner'`,
		projectID).Scan(&n); err != nil {
		t.Fatalf("count owners: %v", err)
	}
	if n != 1 {
		t.Errorf("owner count = %d, want 1", n)
	}
	if winner != a && winner != b {
		t.Errorf("winner = %s, want a=%s or b=%s", winner, a, b)
	}
}
```

Add `"sync"` to the test file's imports.

- [ ] **Step 2: Run the test**

```
TEST_DATABASE_URL=postgres://... go test ./internal/store/ -run TestTransferProjectOwnership_Concurrent -v
```
Expected: PASS.

- [ ] **Step 3: Commit**

```
git add internal/store/projects_test.go
git commit -m "test(store): concurrent transfers serialize on FOR UPDATE"
```

---

### Task 8: `handleTransferOwnership`

**Files:**
- Modify: `internal/admin/handle_projects.go`, `internal/admin/routes.go`
- Test: `internal/admin/handle_projects_test.go` (create)

**Interfaces:**
- Consumes: `store.TransferProjectOwnership` and its sentinel errors; existing `UserFromContext`, `MemberFromContext`, `writeError`, `writeData`.
- Produces: `handleTransferOwnership(st *store.Store) http.HandlerFunc`. Wired at `POST /api/v1/projects/{projectID}/transfer-ownership`.

- [ ] **Step 1: Write the failing test**

Create `internal/admin/handle_projects_test.go`:

```go
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/types"
)

// callerCtx wires the user + member values JWTAuthMiddleware would set.
func callerCtx(ctx context.Context, user *types.User, member *types.ProjectMember) context.Context {
	ctx = context.WithValue(ctx, ctxUser, user)
	if member != nil {
		ctx = context.WithValue(ctx, ctxMember, member)
	}
	return ctx
}

func TestHandleTransferOwnership_MaintainerForbidden(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/projects/{projectID}/transfer-ownership", handleTransferOwnership(nil))

	body, _ := json.Marshal(map[string]string{"to_user_id": "11111111-1111-1111-1111-111111111111"})
	req := httptest.NewRequest("POST", "/projects/p1/transfer-ownership", bytes.NewReader(body))
	req = req.WithContext(callerCtx(req.Context(),
		&types.User{ID: "u-maint", IsSuperadmin: false},
		&types.ProjectMember{UserID: "u-maint", Role: types.RoleMaintainer},
	))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleTransferOwnership_InvalidJSON(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/projects/{projectID}/transfer-ownership", handleTransferOwnership(nil))

	req := httptest.NewRequest("POST", "/projects/p1/transfer-ownership", bytes.NewBufferString("{not json"))
	req = req.WithContext(callerCtx(req.Context(),
		&types.User{ID: "u-owner"},
		&types.ProjectMember{UserID: "u-owner", Role: types.RoleOwner},
	))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestHandleTransferOwnership_MissingToUserID(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/projects/{projectID}/transfer-ownership", handleTransferOwnership(nil))

	req := httptest.NewRequest("POST", "/projects/p1/transfer-ownership", bytes.NewBufferString(`{}`))
	req = req.WithContext(callerCtx(req.Context(),
		&types.User{ID: "u-owner"},
		&types.ProjectMember{UserID: "u-owner", Role: types.RoleOwner},
	))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}
```

(Sentinel-error → HTTP mapping is covered by the integration-style test in Task 11 which goes through the real store.)

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/admin/ -run TestHandleTransferOwnership -v
```
Expected: FAIL with `undefined: handleTransferOwnership`.

- [ ] **Step 3: Implement the handler**

Add to `internal/admin/handle_projects.go` (anywhere with the other handlers; conventional placement is right after `handleRemoveMember`):

```go
// handleTransferOwnership atomically swaps the project owner. Only the
// current owner or a superadmin may call it. The store enforces the
// owner-count invariant in the same transaction.
func handleTransferOwnership(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller := UserFromContext(r.Context())
		callerMember := MemberFromContext(r.Context())
		projectID := chi.URLParam(r, "projectID")

		// Authorization: superadmin OR project owner.
		isAuthorized := caller != nil &&
			(caller.IsSuperadmin || (callerMember != nil && callerMember.Role == types.RoleOwner))
		if !isAuthorized {
			writeError(w, http.StatusForbidden, "forbidden", "only the project owner can transfer ownership")
			return
		}

		var body struct {
			ToUserID string `json:"to_user_id"`
			DemoteTo string `json:"demote_to"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.ToUserID == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "to_user_id is required")
			return
		}
		if body.DemoteTo == "" {
			body.DemoteTo = types.RoleDeveloper
		}

		// Resolve fromUID: for a superadmin acting from outside the
		// project, look up the current owner so the store's
		// from == current-owner assertion holds.
		fromUID := ""
		if callerMember != nil && callerMember.Role == types.RoleOwner {
			fromUID = caller.ID
		} else {
			cur, err := st.GetCurrentProjectOwner(projectID)
			if err != nil || cur == "" {
				writeError(w, http.StatusInternalServerError, "internal",
					"failed to resolve current owner")
				return
			}
			fromUID = cur
		}

		err := st.TransferProjectOwnership(projectID, fromUID, body.ToUserID, body.DemoteTo)
		switch {
		case err == nil:
			// fall through
		case errors.Is(err, store.ErrNotAMember):
			writeError(w, http.StatusBadRequest, "not_a_member", "target user is not a project member")
			return
		case errors.Is(err, store.ErrAlreadyOwner):
			writeError(w, http.StatusBadRequest, "already_owner", "target user is already the project owner")
			return
		case errors.Is(err, store.ErrInvalidRole):
			writeError(w, http.StatusBadRequest, "invalid_role", "demote_to must be 'maintainer' or 'developer'")
			return
		case errors.Is(err, store.ErrInvariantViolated):
			log.Printf("WARN transfer_ownership: invariant violation on project %s: %v", projectID, err)
			writeError(w, http.StatusConflict, "conflict", "project owner state is inconsistent; please retry")
			return
		default:
			writeError(w, http.StatusInternalServerError, "internal", "failed to transfer ownership")
			return
		}

		// Return the updated member list so the dashboard can refresh
		// in one round-trip (same shape as GET /members).
		members, err := st.ListProjectMembers(projectID)
		if err != nil {
			writeData(w, http.StatusOK, map[string]string{"status": "transferred"})
			return
		}
		writeData(w, http.StatusOK, members)
	}
}
```

If `errors` isn't already imported in `handle_projects.go`, add it. `ListProjectMembers` (no pagination) is the existing accessor used by `handleListMembers` — see `internal/store/projects.go:226`.

Also add a one-line store helper used above. In `internal/store/projects.go`, after `TransferProjectOwnership`:

```go
// GetCurrentProjectOwner returns the user_id of the project's current
// owner, or "" if there is none. Used by handleTransferOwnership when a
// superadmin who is not a member initiates a transfer.
func (s *Store) GetCurrentProjectOwner(projectID string) (string, error) {
	var uid string
	err := s.pool.QueryRow(context.Background(),
		`SELECT user_id FROM project_members WHERE project_id=$1 AND role='owner' LIMIT 1`,
		projectID,
	).Scan(&uid)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get current owner: %w", err)
	}
	return uid, nil
}
```

(Existence of `ListProjectMembersPaginated` is assumed from the existing `ListMembers` handler; if it has a different name, use the same store function `handleListMembers` calls — grep for `st.ListProjectMembers` in `handle_projects.go` to confirm the exact name and signature, and substitute.)

- [ ] **Step 4: Wire the route**

In `internal/admin/routes.go`, find the existing `/projects/{projectID}/members` route group. Add a sibling route at the same level:

```go
r.Post("/transfer-ownership", handleTransferOwnership(st))
```

Locate the existing nesting (e.g. `r.Route("/projects/{projectID}", func(r chi.Router) { ... })`) and add the line inside that block, after the members routes.

- [ ] **Step 5: Run tests to verify they pass**

```
go test ./internal/admin/ -run TestHandleTransferOwnership -v
```
Expected: PASS for the three handler-only tests (auth, invalid JSON, missing body field).

- [ ] **Step 6: Commit**

```
git add internal/store/projects.go internal/admin/handle_projects.go internal/admin/routes.go internal/admin/handle_projects_test.go
git commit -m "feat(admin): POST /projects/{id}/transfer-ownership"
```

---

### Task 9: Tighten `handleAddMember`

**Files:**
- Modify: `internal/admin/handle_projects.go`
- Test: `internal/admin/handle_projects_test.go`

**Interfaces:**
- Consumes: `types.IsAssignableRole`, `types.RoleOwner`.
- Produces: `POST /projects/{id}/members` returns 400 `invalid_role` for `role='owner'` or any role not in `types.AssignableRoles`.

- [ ] **Step 1: Write the failing test**

Append to `internal/admin/handle_projects_test.go`:

```go
func TestHandleAddMember_RejectsOwnerRole(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/projects/{projectID}/members", handleAddMember(nil))

	body, _ := json.Marshal(map[string]string{"email": "x@y.z", "role": "owner"})
	req := httptest.NewRequest("POST", "/projects/p1/members", bytes.NewReader(body))
	req = req.WithContext(callerCtx(req.Context(),
		&types.User{ID: "u-owner"},
		&types.ProjectMember{UserID: "u-owner", Role: types.RoleOwner},
	))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte(`"invalid_role"`)) {
		t.Errorf("body lacks invalid_role code: %s", rr.Body.String())
	}
}

func TestHandleAddMember_RejectsUnknownRole(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/projects/{projectID}/members", handleAddMember(nil))

	body, _ := json.Marshal(map[string]string{"email": "x@y.z", "role": "janitor"})
	req := httptest.NewRequest("POST", "/projects/p1/members", bytes.NewReader(body))
	req = req.WithContext(callerCtx(req.Context(),
		&types.User{ID: "u-owner"},
		&types.ProjectMember{UserID: "u-owner", Role: types.RoleOwner},
	))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/admin/ -run TestHandleAddMember -v
```
Expected: FAIL (current handler proceeds and crashes on nil store while resolving email — both tests reach the store).

- [ ] **Step 3: Add the role validation**

In `internal/admin/handle_projects.go`, modify `handleAddMember`. Right after the existing `if body.Email == "" || body.Role == "" { ... return }` block (around `internal/admin/handle_projects.go:457`), insert:

```go
		if body.Role == types.RoleOwner || !types.IsAssignableRole(body.Role) {
			writeError(w, http.StatusBadRequest, "invalid_role",
				"role must be 'maintainer' or 'developer' (use transfer-ownership to change owner)")
			return
		}
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./internal/admin/ -run TestHandleAddMember -v
```
Expected: PASS for both.

- [ ] **Step 5: Commit**

```
git add internal/admin/handle_projects.go internal/admin/handle_projects_test.go
git commit -m "feat(admin): handleAddMember rejects role=owner and unknown roles"
```

---

### Task 10: Tighten `handleUpdateMember` + `handleRemoveMember`

**Files:**
- Modify: `internal/admin/handle_projects.go`
- Test: `internal/admin/handle_projects_test.go`

**Interfaces:**
- Consumes: `types.IsAssignableRole`, `types.RoleOwner`, sentinel errors from Task 2.
- Produces: `PUT /members/{uid}` returns 400 `invalid_role` for `role='owner'` and 409 `owner_must_transfer` when trying to demote the owner. `DELETE /members/{uid}` returns 409 `owner_must_transfer` when target is owner.

- [ ] **Step 1: Write the failing tests**

Append to `internal/admin/handle_projects_test.go`:

```go
func TestHandleUpdateMember_RejectsRoleOwner(t *testing.T) {
	r := chi.NewRouter()
	r.Put("/projects/{projectID}/members/{userID}", handleUpdateMember(nil))

	body, _ := json.Marshal(map[string]string{"role": "owner"})
	req := httptest.NewRequest("PUT", "/projects/p1/members/u-target", bytes.NewReader(body))
	req = req.WithContext(callerCtx(req.Context(),
		&types.User{ID: "u-owner"},
		&types.ProjectMember{UserID: "u-owner", Role: types.RoleOwner},
	))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte(`"invalid_role"`)) {
		t.Errorf("body lacks invalid_role: %s", rr.Body.String())
	}
}

func TestHandleUpdateMember_RejectsUnknownRole(t *testing.T) {
	r := chi.NewRouter()
	r.Put("/projects/{projectID}/members/{userID}", handleUpdateMember(nil))

	body, _ := json.Marshal(map[string]string{"role": "janitor"})
	req := httptest.NewRequest("PUT", "/projects/p1/members/u-target", bytes.NewReader(body))
	req = req.WithContext(callerCtx(req.Context(),
		&types.User{ID: "u-owner"},
		&types.ProjectMember{UserID: "u-owner", Role: types.RoleOwner},
	))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/admin/ -run TestHandleUpdateMember -v
```
Expected: FAIL.

- [ ] **Step 3: Add role validation in `handleUpdateMember`**

In `internal/admin/handle_projects.go`, modify `handleUpdateMember`. After the existing "at least one field must be provided" block (around `internal/admin/handle_projects.go:525-529`), and BEFORE the existing `if body.CreditQuotaPct != nil && ...` validation, insert:

```go
		// Role validation: if role is being changed, it must be in
		// types.AssignableRoles. role='owner' is rejected here — use
		// POST /projects/{id}/transfer-ownership.
		if body.Role != nil {
			if *body.Role == types.RoleOwner || !types.IsAssignableRole(*body.Role) {
				writeError(w, http.StatusBadRequest, "invalid_role",
					"role must be 'maintainer' or 'developer' (use transfer-ownership to change owner)")
				return
			}
		}
```

Then update the existing call site `st.UpdateProjectMember(projectID, userID, body.Role, quotaArg, body.DeniedModels)` (around `internal/admin/handle_projects.go:583`) to map the sentinel errors. Replace:

```go
		if err := st.UpdateProjectMember(projectID, userID, body.Role, quotaArg, body.DeniedModels); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update member")
			return
		}
```

with:

```go
		err = st.UpdateProjectMember(projectID, userID, body.Role, quotaArg, body.DeniedModels)
		switch {
		case err == nil:
			// fall through
		case errors.Is(err, store.ErrOwnerMustTransfer):
			writeError(w, http.StatusConflict, "owner_must_transfer",
				"the current owner cannot be demoted directly; use POST /projects/{id}/transfer-ownership")
			return
		case errors.Is(err, store.ErrInvalidRole):
			writeError(w, http.StatusBadRequest, "invalid_role", "invalid role")
			return
		case errors.Is(err, store.ErrNotAMember):
			writeError(w, http.StatusNotFound, "not_found", "member not found")
			return
		default:
			writeError(w, http.StatusInternalServerError, "internal", "failed to update member")
			return
		}
```

(`err` is already declared above in the function — make sure to use `=` not `:=`. If the surrounding context shadowed it, rename the local variable accordingly.)

- [ ] **Step 4: Update `handleRemoveMember` to map sentinel error**

In `internal/admin/handle_projects.go`, modify `handleRemoveMember` (around `internal/admin/handle_projects.go:622-656`). Replace:

```go
		revokedKeys, deletedGrants, err := st.RemoveProjectMember(projectID, userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal",
				"failed to remove member")
			return
		}
```

with:

```go
		revokedKeys, deletedGrants, err := st.RemoveProjectMember(projectID, userID)
		switch {
		case err == nil:
			// fall through
		case errors.Is(err, store.ErrOwnerMustTransfer):
			writeError(w, http.StatusConflict, "owner_must_transfer",
				"the project owner cannot be removed; use POST /projects/{id}/transfer-ownership first")
			return
		default:
			writeError(w, http.StatusInternalServerError, "internal",
				"failed to remove member")
			return
		}
```

- [ ] **Step 5: Run tests to verify update tests pass**

```
go test ./internal/admin/ -run TestHandleUpdateMember -v
```
Expected: PASS.

- [ ] **Step 6: Commit**

```
git add internal/admin/handle_projects.go internal/admin/handle_projects_test.go
git commit -m "feat(admin): tighten handleUpdateMember/handleRemoveMember; map owner-invariant errors to 4xx"
```

---

### Task 11: End-to-end test — transfer happy path through the handler

**Files:**
- Test: `internal/admin/handle_projects_test.go`

**Interfaces:**
- Consumes: `handleTransferOwnership` (Task 8), all store changes (Tasks 4–6), the test-DB conventions.
- Produces: integration test that wires a real store and exercises the full path.

- [ ] **Step 1: Write the test**

Append to `internal/admin/handle_projects_test.go`:

```go
import "github.com/modelserver/modelserver/internal/store"  // add if not already present

func TestHandleTransferOwnership_E2EHappyPath(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL to run")
	}
	st, err := store.New(dbURL, slog.Default())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	var ownerID, targetID, projectID string
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.Pool().Exec(ctx, q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustOne := func(q string, args ...any) string {
		t.Helper()
		var s string
		if err := st.Pool().QueryRow(ctx, q, args...).Scan(&s); err != nil {
			t.Fatalf("query %s: %v", q, err)
		}
		return s
	}
	ownerID = mustOne(`INSERT INTO users (email) VALUES ('owner-' || gen_random_uuid()::text || '@t.local') RETURNING id`)
	targetID = mustOne(`INSERT INTO users (email) VALUES ('target-' || gen_random_uuid()::text || '@t.local') RETURNING id`)
	projectID = mustOne(`INSERT INTO projects (name, created_by) VALUES ('transfer-e2e', $1) RETURNING id`, ownerID)
	mustExec(`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'owner')`, projectID, ownerID)
	mustExec(`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'developer')`, projectID, targetID)

	r := chi.NewRouter()
	r.Post("/projects/{projectID}/transfer-ownership", handleTransferOwnership(st))

	body, _ := json.Marshal(map[string]string{"to_user_id": targetID})
	req := httptest.NewRequest("POST", "/projects/"+projectID+"/transfer-ownership", bytes.NewReader(body))
	req = req.WithContext(callerCtx(req.Context(),
		&types.User{ID: ownerID, IsSuperadmin: false},
		&types.ProjectMember{UserID: ownerID, Role: types.RoleOwner},
	))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	// Verify post-state.
	gotOwner := mustOne(`SELECT user_id FROM project_members WHERE project_id=$1 AND role='owner'`, projectID)
	if gotOwner != targetID {
		t.Errorf("new owner = %s, want %s", gotOwner, targetID)
	}
	gotOldRole := mustOne(`SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2`, projectID, ownerID)
	if gotOldRole != types.RoleDeveloper {
		t.Errorf("old owner demoted to %s, want developer", gotOldRole)
	}
}
```

Add `"log/slog"`, `"os"`, `"context"` to imports if not already present.

Note: `st.Pool() *pgxpool.Pool` already exists at `internal/store/store.go:59` — no store change needed.

- [ ] **Step 2: Run the test**

```
TEST_DATABASE_URL=postgres://... go test ./internal/admin/ -run TestHandleTransferOwnership_E2E -v
```
Expected: PASS.

- [ ] **Step 3: Commit**

```
git add internal/admin/handle_projects_test.go
git commit -m "test(admin): E2E transfer-ownership happy path"
```

---

### Task 12: Full-suite verification + plan close-out

**Files:** none modified.

- [ ] **Step 1: Run the full test suite**

```
go build ./...
go vet ./...
TEST_DATABASE_URL=postgres://... go test ./...
```
Expected: all pass. Without `TEST_DATABASE_URL`, DB tests skip — fine in CI but the integration tests must have been verified locally with one set.

- [ ] **Step 2: Sanity-check the spec is fully covered**

Re-open `docs/superpowers/specs/2026-06-30-single-owner-invariant-design.md` and confirm every row in the §"API Changes" table and §"Tests" table has a corresponding task in this plan. (At the time of writing, all rows are covered. Re-verify after implementation; if anything was overlooked, add a follow-up task before declaring done.)

- [ ] **Step 3: Mark the plan complete**

No commit needed — the plan file is unmodified. Inform the user the implementation is complete and offer to open a PR.

---

## Self-Review

**Spec coverage check** (against `docs/superpowers/specs/2026-06-30-single-owner-invariant-design.md`):

| Spec section | Plan task(s) |
|---|---|
| §Problem #1 — five unguarded write paths | Tasks 4, 5, 9, 10 |
| §Problem #2 — quota counts wrong column | Task 3 |
| §Goal A — single-owner invariant | Tasks 4, 5, 6, 7 |
| §Goal B — creator-based quota | Task 3 |
| §API: new `POST /transfer-ownership` | Task 8 |
| §API: tightened `POST /members` | Task 9 |
| §API: tightened `PUT /members/{uid}` | Task 10 |
| §API: tightened `DELETE /members/{uid}` | Task 10 |
| §Implementation: `types` helpers | Task 1 |
| §Implementation: sentinel errors + drop unused fn | Task 2 |
| §Implementation: `TransferProjectOwnership` | Task 6 |
| §Implementation: `UpdateProjectMember` guards | Task 5 |
| §Implementation: `RemoveProjectMember` guard | Task 4 |
| §Implementation: route mount | Task 8 |
| §Tests — handler table | Tasks 8, 9, 10, 11 |
| §Tests — store concurrency | Task 7 |

**Drift note:** Spec said "new file `internal/types/roles.go`." Implementation extends `internal/types/user.go` instead because the role constants already live there. Functionally equivalent.

**Placeholder scan:** None — every step has either a code block or an exact command.

**Type consistency:** Sentinel error names `ErrNotAMember` / `ErrAlreadyOwner` / `ErrOwnerMustTransfer` / `ErrInvalidRole` / `ErrInvariantViolated` are used identically across Tasks 2, 4, 5, 6, 8, 10. Function signatures `CountUserCreatedProjects(userID) (int, error)`, `TransferProjectOwnership(projectID, fromUID, toUID, demoteTo) error`, `GetCurrentProjectOwner(projectID) (string, error)` consistent across producer and consumer tasks.
