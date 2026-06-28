# Revoke API Keys on Member Removal — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the security gap where ex-members' API keys remain usable: (A) `RemoveProjectMember` becomes transactional and revokes the member's active keys; (B) `AuthMiddleware` fail-closes when `apiKey.CreatedBy` is no longer a project member; (M) a one-shot migration revokes pre-existing zombie keys.

**Architecture:** Two redundant layers + a backfill. Layer A is the canonical fix (atomic UPDATE-then-DELETE inside a tx; new endpoint returns the revoked count). Layer B is the safety net (reuse the existing per-request `GetProjectMember` call; cache the "present/absent" bit alongside quota & denylist so the per-request DB-call budget is unchanged; fail-closed on transient DB errors). The migration runs at process start. The dashboard's Remove action gains a confirmation dialog that shows the pre-count and a success toast that shows the post-count.

**Tech Stack:** Go 1.x, `pgx` (`pool.Begin` / `tx.Exec` / `tx.Commit`), stdlib `testing`. React 19, `@tanstack/react-query` v5, `@base-ui/react` Dialog primitive, Tailwind v4. No new dependencies.

## Global Constraints

- **Spec:** `docs/superpowers/specs/2026-06-28-revoke-keys-on-member-removal-design.md` — re-read before each task.
- **Revocation method:** flip `api_keys.status` from `'active'` to `'revoked'` (the existing `types.APIKeyStatusRevoked` constant — DO NOT introduce a new enum value). Always stamp `updated_at = NOW()` on the same UPDATE.
- **Transactionality:** `RemoveProjectMember` runs **revoke UPDATE first, then DELETE membership row**, inside a single tx, so a concurrent in-flight request can never observe the (member-deleted, key-still-active) intermediate state during the tx window.
- **Layer B fail-CLOSED on transient DB errors.** The membership check at every request must return `503 Service Unavailable` (NOT 401, NOT 200) on `GetProjectMember` errors. This is a conscious divergence from quota/denylist's fail-open posture — document with a comment in the code.
- **Cache TTL:** the new `memberPresentCache` matches the existing `quotaCache` and `deniedModelsCache` TTL (10s). One shared key `<projectID>:<userID>` across all three caches.
- **Response shape:** `handleRemoveMember` returns `200 OK` with body `{"data": {"revoked_api_keys": N}}` (was `204 No Content`). The new endpoint `GET /projects/{projectID}/members/{userID}/affected-keys` returns `{"data": {"active_api_keys": N}}`.
- **Auth:** all new admin endpoints inherit `requireRole(types.RoleOwner, types.RoleMaintainer)` matching the existing remove handler.
- **No frontend test framework** exists. Frontend tasks verify with `pnpm exec tsc -b && pnpm build` + manual smoke listed in the task.
- **Migration naming:** numbered `055_revoke_orphaned_api_keys.sql` (the previously-merged max is `054_add_request_indexes.sql`). No down migration.
- **Idempotence:** migration 055 must be safe to re-run after the fix lands (the post-fix code keeps the invariant; the migration becomes a no-op on second run).
- **Commit message footer:** every commit ends with
  ```
  Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
  ```

## File Structure

**Backend — create:**
- `internal/store/migrations/055_revoke_orphaned_api_keys.sql` — one-shot backfill.
- `internal/store/migrations_055_test.go` — backfill correctness + idempotence.
- `internal/proxy/auth_middleware_test.go` — first test file for `AuthMiddleware`; defines a small `memberStore` interface + fake.

**Backend — modify:**
- `internal/store/projects.go` — `RemoveProjectMember` signature gains `(int, error)`; runs in a tx; add `CountActiveKeysForMember`.
- `internal/store/projects_test.go` *(currently absent — create as new file)* — new tests for `RemoveProjectMember` + `CountActiveKeysForMember`.
- `internal/admin/handle_keys.go` — `handleCreateKey` rejects callers without a `project_members` row (closes the superadmin loophole the spec calls out).
- `internal/admin/handle_projects.go` — `handleRemoveMember` returns 200 with count; add `handleCountAffectedKeysOnRemove`.
- `internal/admin/routes.go` — register `GET /members/{userID}/affected-keys`.
- `internal/proxy/auth_middleware.go` — extract `GetProjectMember` behind a small interface used by AuthMiddleware (for testability); add `memberPresentCache`; rewrite the L319-365 cache-hydration block to a triple-cache pattern with fail-closed semantics.

**Frontend — modify:**
- `dashboard/src/api/members.ts` — add `useMemberAffectedKeys` hook; update `useRemoveMember`'s response type.
- `dashboard/src/pages/members/MembersPage.tsx` — replace bare Remove click with a confirmation Dialog; update success toast.

---

### Task 1: Backfill migration + test

**Files:**
- Create: `internal/store/migrations/055_revoke_orphaned_api_keys.sql`
- Create: `internal/store/migrations_055_test.go`

**Interfaces:**
- Consumes: existing migration framework (auto-runs `*.sql` files in order at store startup).
- Produces: an SQL migration; one Go test that exercises it against a real DB.

This task ships in isolation — the migration is idempotent and safe to deploy even before Tasks 2–4 land, because it only revokes already-orphaned keys.

- [ ] **Step 1: Write the migration SQL**

Create `internal/store/migrations/055_revoke_orphaned_api_keys.sql`:

```sql
-- 055_revoke_orphaned_api_keys.sql
--
-- One-shot cleanup of API keys belonging to users who have already been
-- removed from their project. Pre-fix, RemoveProjectMember only deleted
-- the project_members row; the API keys stayed active. Flip those zombie
-- keys to status='revoked' so AuthMiddleware's existing
-- `apiKey.Status != "active"` check immediately starts rejecting them.
--
-- Idempotent: re-running this migration after the fix lands is a no-op,
-- because handleRemoveMember will keep this invariant going forward.

UPDATE api_keys
   SET status = 'revoked',
       updated_at = NOW()
 WHERE status = 'active'
   AND NOT EXISTS (
       SELECT 1 FROM project_members
        WHERE project_members.project_id = api_keys.project_id
          AND project_members.user_id    = api_keys.created_by
   );
```

- [ ] **Step 2: Write the migration test**

Create `internal/store/migrations_055_test.go`:

```go
package store

import (
	"context"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

// TestMigration055_RevokesOrphanedKeys asserts the migration only flips
// active keys whose creator has no project_members row in the same project,
// and leaves all other keys alone. Idempotent on re-run.
func TestMigration055_RevokesOrphanedKeys(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Two users + one project.
	member, projectID := seedUserAndProject(t, st)

	// Second user: never a member of projectID (the "orphan" creator).
	var orphanUserID string
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO users (email) VALUES ('orphan-' || gen_random_uuid()::text || '@test.local')
		RETURNING id`).Scan(&orphanUserID); err != nil {
		t.Fatalf("seed orphan user: %v", err)
	}

	// Seed four api_keys (raw SQL — bypass any creator-must-be-member checks):
	//   k1: created_by = member,       status='active'   -> stays active
	//   k2: created_by = orphan,       status='active'   -> revoked by migration
	//   k3: created_by = orphan,       status='active'   -> revoked by migration
	//   k4: created_by = orphan,       status='revoked'  -> stays revoked
	insert := func(createdBy, status string) string {
		var id string
		if err := st.pool.QueryRow(ctx, `
			INSERT INTO api_keys (project_id, created_by, key_hash, key_suffix, name, status)
			VALUES ($1, $2, gen_random_uuid()::text, '', 'test-key', $3)
			RETURNING id`, projectID, createdBy, status).Scan(&id); err != nil {
			t.Fatalf("seed key: %v", err)
		}
		return id
	}
	k1 := insert(member, types.APIKeyStatusActive)
	k2 := insert(orphanUserID, types.APIKeyStatusActive)
	k3 := insert(orphanUserID, types.APIKeyStatusActive)
	k4 := insert(orphanUserID, types.APIKeyStatusRevoked)

	// Re-run migration 055 manually (it already ran at openTestStore;
	// re-running tests idempotence directly).
	if _, err := st.pool.Exec(ctx, `
		UPDATE api_keys
		   SET status = 'revoked', updated_at = NOW()
		 WHERE status = 'active'
		   AND NOT EXISTS (
		     SELECT 1 FROM project_members
		      WHERE project_members.project_id = api_keys.project_id
		        AND project_members.user_id    = api_keys.created_by
		   )`); err != nil {
		t.Fatalf("re-run migration: %v", err)
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
	check(k1, types.APIKeyStatusActive)   // member -> untouched
	check(k2, types.APIKeyStatusRevoked)  // orphan -> revoked
	check(k3, types.APIKeyStatusRevoked)  // orphan -> revoked
	check(k4, types.APIKeyStatusRevoked)  // already revoked -> unchanged
}
```

- [ ] **Step 3: Run the test**

Run: `cd /root/coding/modelserver && TEST_DATABASE_URL=$TEST_DATABASE_URL go test ./internal/store/ -run TestMigration055_RevokesOrphanedKeys -v`

If `TEST_DATABASE_URL` is not exported, the test prints SKIP — acceptable for a local quick check, but the CI run will exercise it.

Expected (with TEST_DATABASE_URL set): PASS.

- [ ] **Step 4: Run the full store package once**

Run: `cd /root/coding/modelserver && go test ./internal/store/...`

Confirms no regressions in other migration tests or in the broader package.
Expected: PASS (or skips only — no failures).

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add internal/store/migrations/055_revoke_orphaned_api_keys.sql internal/store/migrations_055_test.go
git commit -m "feat(store): revoke orphaned api keys on member removal — backfill

Migration 055 closes the existing exposure: api_keys where
(project_id, created_by) has no row in project_members get
status flipped to 'revoked'. Idempotent.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Transactional `RemoveProjectMember` + `CountActiveKeysForMember`

**Files:**
- Modify: `internal/store/projects.go` (replace `RemoveProjectMember`; add `CountActiveKeysForMember`)
- Create: `internal/store/projects_test.go`

**Interfaces:**
- Consumes: existing `s.pool` (pgxpool), `types.APIKeyStatusActive`, `types.APIKeyStatusRevoked`.
- Produces:
  ```go
  func (s *Store) RemoveProjectMember(projectID, userID string) (revokedCount int, err error)
  func (s *Store) CountActiveKeysForMember(projectID, userID string) (int, error)
  ```
  Signature on `RemoveProjectMember` is intentionally backward-incompatible — Task 4 updates the one caller (`handleRemoveMember`).

- [ ] **Step 1: Write the failing tests**

Create `internal/store/projects_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /root/coding/modelserver && go test ./internal/store/ -run 'TestRemoveProjectMember|TestCountActiveKeysForMember' -v`

Expected: build error — current `RemoveProjectMember` returns only `error`, and `CountActiveKeysForMember` doesn't exist. The compiler errors are the RED state.

- [ ] **Step 3: Update `RemoveProjectMember` and add `CountActiveKeysForMember`**

In `internal/store/projects.go`, locate the existing `RemoveProjectMember` (around line 328) and replace it with:

```go
// RemoveProjectMember atomically revokes every active API key the member
// created in this project, then removes the member. Returns the number
// of keys flipped from 'active' to 'revoked'.
//
// The revocation UPDATE runs FIRST so a concurrent in-flight request can
// never observe (member-deleted, key-still-active) during the tx window.
// Postgres rolls both statements back on any failure.
//
// If the user has no membership row (pre-migration zombie state), the
// keys are still revoked and the DELETE of zero rows is not an error.
func (s *Store) RemoveProjectMember(projectID, userID string) (int, error) {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) // no-op after Commit

	tag, err := tx.Exec(ctx, `
		UPDATE api_keys
		   SET status = 'revoked', updated_at = NOW()
		 WHERE project_id = $1 AND created_by = $2 AND status = 'active'`,
		projectID, userID)
	if err != nil {
		return 0, fmt.Errorf("revoke keys: %w", err)
	}
	revokedCount := int(tag.RowsAffected())

	if _, err := tx.Exec(ctx,
		`DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`,
		projectID, userID); err != nil {
		return 0, fmt.Errorf("delete member: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return revokedCount, nil
}

// CountActiveKeysForMember returns the number of api_keys with
// status='active' that the user created in the project. Used by the
// dashboard's pre-removal confirmation dialog.
func (s *Store) CountActiveKeysForMember(projectID, userID string) (int, error) {
	var n int
	err := s.pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM api_keys
		 WHERE project_id = $1 AND created_by = $2 AND status = 'active'`,
		projectID, userID).Scan(&n)
	return n, err
}
```

If `fmt` is not yet imported in this file, add it to the import block (the package likely already imports it elsewhere — check before adding a duplicate).

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /root/coding/modelserver && go test ./internal/store/ -run 'TestRemoveProjectMember|TestCountActiveKeysForMember' -v`

Expected: all four tests PASS (assuming `TEST_DATABASE_URL` is set; otherwise they SKIP).

- [ ] **Step 5: Compile check across all callers**

Run: `cd /root/coding/modelserver && go build ./...`

Expected: build failure in `internal/admin/handle_projects.go` because `handleRemoveMember` still calls `RemoveProjectMember` expecting a single `error` return. This is the bridge to Task 4 — leave the build broken and commit the store changes; Task 4 fixes the caller. (Task 3, between, also lands inside this red-build window.)

(If you prefer to commit only on green: skip this step and proceed to Step 6, treating the build failure as expected scaffolding for the Task 3 + Task 4 commits.)

- [ ] **Step 6: Commit**

```bash
cd /root/coding/modelserver
git add internal/store/projects.go internal/store/projects_test.go
git commit -m "feat(store): RemoveProjectMember revokes member's active api keys atomically

UPDATE api_keys SET status='revoked' runs FIRST inside the same tx,
then DELETE project_members. Returns the revoked count. Adds
CountActiveKeysForMember for the dashboard's pre-removal dialog.

Signature change intentional — admin/handle_projects.go updated in the
next commit. Build is red between these two commits.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Close superadmin loophole in `handleCreateKey`

**Files:**
- Modify: `internal/admin/handle_keys.go` (`handleCreateKey`, currently around line 44)

**Interfaces:**
- Consumes: existing `MemberFromContext`, `UserFromContext` from `internal/admin`; existing `writeError` helper.
- Produces: a new pre-flight check inside `handleCreateKey` that 403s any caller without a `project_members` row for the target project. No new exported symbols.

**Why this task exists:** `projectAccessMiddleware` (`internal/admin/routes.go:321-324`) lets superadmins through without populating a member context. As a result, `handleCreateKey` happily creates API keys for users who are NOT in `project_members`. That violates the invariant the rest of this PR depends on ("every active key has a corresponding member row"). Without this task, migration 055 and `RemoveProjectMember` would revoke superadmin-created keys on deploy. The spec's "Closing the superadmin loophole" section authorizes this tightening.

- [ ] **Step 1: Inspect the current handler and confirm the failure mode**

Run: `grep -n "MemberFromContext\|handleCreateKey" /root/coding/modelserver/internal/admin/handle_keys.go | head -5`

Confirm `handleCreateKey` does NOT currently call `MemberFromContext`. Read the function body (around lines 44-105) — it pulls `user := UserFromContext(...)` and assigns `key.CreatedBy = user.ID`, with no membership check. That's the exact gap.

- [ ] **Step 2: Add the membership check**

In `internal/admin/handle_keys.go`, locate the existing `handleCreateKey` function. Immediately after the `projectID := chi.URLParam(r, "projectID")` line and the `user := UserFromContext(...)` line (and before the body-decode block), insert a membership pre-flight check:

```go
		// Enforce: every API key creator MUST be a project member, even
		// superadmins. projectAccessMiddleware bypasses the membership
		// load for superadmins (member-in-context is nil), so we check
		// here explicitly. Without this, superadmin-created keys would
		// violate the invariant relied on by migration 055 and
		// RemoveProjectMember: "every active api_key has a row in
		// project_members for (project_id, created_by)".
		if member := MemberFromContext(r.Context()); member == nil {
			m, err := st.GetProjectMember(projectID, user.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal",
					"failed to verify project membership")
				return
			}
			if m == nil {
				writeError(w, http.StatusForbidden, "forbidden",
					"superadmins must join the project as a member before creating API keys")
				return
			}
		}
```

The check runs ONLY when `MemberFromContext` returns nil (superadmin path). Non-superadmin callers always have a member context populated by `projectAccessMiddleware`, so they skip this DB call entirely — no regression in the hot path.

- [ ] **Step 3: Build**

Run: `cd /root/coding/modelserver && go build ./...`

Expected: this is still failing in `handle_projects.go` from Task 2's signature change. That's fine — the build green-bridges in Task 4 (next task). Verify the failure is ONLY the `RemoveProjectMember` arity error and not something this task introduced.

- [ ] **Step 4: Run admin tests (skip-permissive)**

Run: `cd /root/coding/modelserver && go test ./internal/admin/... 2>&1 | tail -30`

Expected: compile error from the in-progress `handle_projects.go` change (Task 2). Verify nothing else in `internal/admin/` started failing because of this task's change. If `handle_keys_test.go` or similar starts failing, debug — your change should be purely additive on a path no current test exercises.

(If you prefer green-build commits: skip Step 3-4 and rely on the Task 4 commit to restore the green state. Either path is acceptable; the plan already authorizes a red-build window across Tasks 2-4.)

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add internal/admin/handle_keys.go
git commit -m "feat(admin): handleCreateKey requires project membership for superadmins

projectAccessMiddleware bypasses membership for superadmins, but
handleCreateKey was happily creating api_keys whose created_by user
had no project_members row. That violates the invariant migration 055
and RemoveProjectMember rely on.

Adds an explicit GetProjectMember check on the superadmin path only;
returns 403 'superadmins must join the project as a member before
creating API keys' on miss. Non-superadmins are unaffected (they
already have a member in context).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: `handleRemoveMember` returns count; add `handleCountAffectedKeysOnRemove`

**Files:**
- Modify: `internal/admin/handle_projects.go` (around line 605)
- Modify: `internal/admin/routes.go` (project members subtree)

**Interfaces:**
- Consumes: `st.RemoveProjectMember(projectID, userID) (int, error)` and `st.CountActiveKeysForMember(projectID, userID) (int, error)` (Task 2).
- Produces:
  - `DELETE /api/v1/projects/{projectID}/members/{userID}` now returns `200 OK` with body `{"data": {"revoked_api_keys": N}}` (was `204 No Content`).
  - `GET /api/v1/projects/{projectID}/members/{userID}/affected-keys` returns `200 OK` with body `{"data": {"active_api_keys": N}}`.

- [ ] **Step 1: Update `handleRemoveMember`**

In `internal/admin/handle_projects.go`, replace the body of `handleRemoveMember` (currently L605-617) with:

```go
func handleRemoveMember(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, types.RoleOwner, types.RoleMaintainer) {
			return
		}
		projectID := chi.URLParam(r, "projectID")
		userID := chi.URLParam(r, "userID")

		revokedCount, err := st.RemoveProjectMember(projectID, userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal",
				"failed to remove member")
			return
		}
		writeData(w, http.StatusOK, map[string]int{
			"revoked_api_keys": revokedCount,
		})
	}
}
```

- [ ] **Step 2: Add `handleCountAffectedKeysOnRemove`**

Below `handleRemoveMember` in the same file, add:

```go
// handleCountAffectedKeysOnRemove returns how many active API keys the
// given user has in the project. Used by the dashboard's pre-removal
// confirmation dialog so the operator sees the blast radius before
// clicking Confirm.
func handleCountAffectedKeysOnRemove(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, types.RoleOwner, types.RoleMaintainer) {
			return
		}
		projectID := chi.URLParam(r, "projectID")
		userID := chi.URLParam(r, "userID")
		n, err := st.CountActiveKeysForMember(projectID, userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal",
				"failed to count affected keys")
			return
		}
		writeData(w, http.StatusOK, map[string]int{
			"active_api_keys": n,
		})
	}
}
```

- [ ] **Step 3: Register the new GET endpoint**

In `internal/admin/routes.go`, locate the project-members subtree (look for the existing `r.Delete("/members/{userID}", handleRemoveMember(st))` — around line 175). Add the GET on the line above it (or below — order doesn't matter for chi routing):

```go
r.Get("/members/{userID}/affected-keys", handleCountAffectedKeysOnRemove(st))
r.Delete("/members/{userID}", handleRemoveMember(st))
```

Confirm the surrounding `r.Route(...)` block already applies the appropriate auth middleware (it should — the existing `DELETE /members/{userID}` works without per-route auth wrappers because the parent block has them).

- [ ] **Step 4: Build + run admin tests**

Run: `cd /root/coding/modelserver && go build ./... && go test ./internal/admin/...`

Expected: build green; admin tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add internal/admin/handle_projects.go internal/admin/routes.go
git commit -m "feat(admin): handleRemoveMember returns revoked-key count

DELETE /projects/{p}/members/{u} now returns 200 + {revoked_api_keys: N}
(was 204). Adds GET /projects/{p}/members/{u}/affected-keys for the
dashboard's pre-removal confirmation dialog.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: AuthMiddleware fail-closed membership check

**Files:**
- Modify: `internal/proxy/auth_middleware.go`
- Create: `internal/proxy/auth_middleware_test.go`

**Interfaces:**
- Consumes: existing `st.GetProjectMember(projectID, userID) (*types.ProjectMember, error)`, existing `quotaCache` and `deniedModelsCache*` helpers, existing `writeProxyError(w, status, msg)`.
- Produces:
  - A new in-file `memberPresentCache` with `Get(key string) (bool, bool)` and `Set(key string, present bool)` (mirroring `deniedModelsCache*` shape) and a public `InvalidateMemberPresentCache(projectID, userID string)` for future-proofing.
  - `AuthMiddleware` behavior: triple-cache hydration via a single `GetProjectMember` call on a cache miss; 401 `"api key creator is no longer a project member"` when present==false; 503 `"membership check unavailable, retry"` when the DB call errors.
  - A small `memberStore` interface used by the rewritten code path so tests can inject a fake without a real DB:
    ```go
    type memberStore interface {
        GetProjectMember(projectID, userID string) (*types.ProjectMember, error)
    }
    ```

- [ ] **Step 1: Write the failing tests**

Create `internal/proxy/auth_middleware_test.go`:

```go
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
	calls   atomic.Int32
	result  *types.ProjectMember
	err     error
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestCheckMembership -v`

Expected: build errors — `memberStore`, `checkMembership`, and `clearMemberPresentCache` are all undefined.

- [ ] **Step 3: Add `memberPresentCache` and `memberStore` to `auth_middleware.go`**

In `internal/proxy/auth_middleware.go`, near the existing `deniedModelsCache` block (around line 27-65), add:

```go
// memberPresentCache caches the "is (project, user) currently a member?"
// answer with the same 10s TTL as quotaCache and deniedModelsCache.
// Caching the negative answer too means a removed member's continued
// requests don't pound the DB while the cache window lapses naturally.
//
// Transient DB errors are NEVER cached — the next request must retry,
// because caching an error would convert a momentary blip into 10s of
// authorization failures.
var (
	memberPresentCacheMu sync.RWMutex
	memberPresentCache   = make(map[string]memberPresentCacheEntry)
)

type memberPresentCacheEntry struct {
	present   bool
	expiresAt time.Time
}

const memberPresentCacheTTL = 10 * time.Second

func memberPresentCacheGet(key string) (bool, bool) {
	memberPresentCacheMu.RLock()
	defer memberPresentCacheMu.RUnlock()
	entry, ok := memberPresentCache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return false, false
	}
	return entry.present, true
}

func memberPresentCacheSet(key string, present bool) {
	memberPresentCacheMu.Lock()
	defer memberPresentCacheMu.Unlock()
	memberPresentCache[key] = memberPresentCacheEntry{
		present:   present,
		expiresAt: time.Now().Add(memberPresentCacheTTL),
	}
}

// InvalidateMemberPresentCache drops any cached membership answer for the
// given (project, user). Safe to call without a corresponding entry.
// Exposed so future code paths that change membership outside
// RemoveProjectMember can invalidate immediately rather than waiting up
// to memberPresentCacheTTL.
func InvalidateMemberPresentCache(projectID, userID string) {
	key := projectID + ":" + userID
	memberPresentCacheMu.Lock()
	defer memberPresentCacheMu.Unlock()
	delete(memberPresentCache, key)
}

// clearMemberPresentCache resets the cache. Test-only helper.
func clearMemberPresentCache() {
	memberPresentCacheMu.Lock()
	defer memberPresentCacheMu.Unlock()
	memberPresentCache = make(map[string]memberPresentCacheEntry)
}

// memberStore is the narrow subset of *store.Store that the membership
// check uses. Defined as an interface so tests can inject a fake without
// a real DB.
type memberStore interface {
	GetProjectMember(projectID, userID string) (*types.ProjectMember, error)
}

// checkMembership returns true iff the (projectID, userID) pair is
// currently a project member, OR a cached answer says so. On a cache
// miss it queries `ms` and caches the result.
//
// On a transient DB error from `ms`, it writes 503 to `w` and returns
// false (the request must NOT proceed). The error is NOT cached.
//
// On a definitively-absent member, it writes 401 to `w` and returns
// false; the "absent" answer IS cached for the TTL window.
//
// On success, it writes nothing to `w` and returns true.
//
// SECURITY: this is the only fail-CLOSED path in AuthMiddleware. Quota
// and denylist hydration deliberately fail open on transient DB errors
// (a brief metering glitch is preferable to a global outage). Membership
// is the authorization gate — failing open here is an authorization
// bypass, so it returns 503 instead.
func checkMembership(w http.ResponseWriter, ms memberStore, projectID, userID string) bool {
	key := projectID + ":" + userID
	if present, ok := memberPresentCacheGet(key); ok {
		if !present {
			writeProxyError(w, http.StatusUnauthorized,
				"api key creator is no longer a project member")
			return false
		}
		return true
	}

	member, err := ms.GetProjectMember(projectID, userID)
	if err != nil {
		writeProxyError(w, http.StatusServiceUnavailable,
			"membership check unavailable, retry")
		return false
	}
	present := member != nil
	memberPresentCacheSet(key, present)
	if !present {
		writeProxyError(w, http.StatusUnauthorized,
			"api key creator is no longer a project member")
		return false
	}
	return true
}
```

If `sync` and `time` are already imported (they are — `deniedModelsCacheMu` already uses both), you don't need to add them.

- [ ] **Step 4: Run the membership tests to confirm GREEN**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestCheckMembership -v`

Expected: all five tests PASS.

- [ ] **Step 5: Wire `checkMembership` into AuthMiddleware**

In `internal/proxy/auth_middleware.go`, locate the block beginning around line 319 (`// Load per-user credit quota + denylist (cached 10s each).`). Find the existing structure that ends roughly:

```go
if !quotaHit || !deniedHit {
    member, memberErr := st.GetProjectMember(project.ID, apiKey.CreatedBy)
    if memberErr != nil {
        // Fail open for BOTH quota and denylist on transient
        // DB errors — never lock everyone out of every model.
    } else if member != nil {
        if !quotaHit {
            ...
        }
        if !deniedHit {
            ...
        }
    } else {
        // member == nil (API key outlived membership).
        if !quotaHit {
            quotaCache.Set(memberCacheKey, -1)
        }
        if !deniedHit {
            deniedModelsCacheSet(memberCacheKey, nil)
        }
    }
}
```

Replace it with the triple-cache pattern. Insert immediately before the `quotaCached, quotaHit := quotaCache.Get(memberCacheKey)` line (so the membership check happens first) — but reuse the SAME `GetProjectMember` call to hydrate everything:

```go
memberCacheKey := project.ID + ":" + apiKey.CreatedBy

// Triple-cache hydration: membership presence, quota %, denylist.
// All three share the cache key and TTL (10s). One GetProjectMember
// call hydrates all three on a miss.
memberPresent, presenceHit := memberPresentCacheGet(memberCacheKey)
quotaCached, quotaHit := quotaCache.Get(memberCacheKey)
deniedCached, deniedHit := deniedModelsCacheGet(memberCacheKey)

if quotaHit && quotaCached >= 0 {
    v := quotaCached
    userQuotaPct = &v
}
if deniedHit {
    userDeniedModels = deniedCached
}

if !presenceHit || !quotaHit || !deniedHit {
    member, memberErr := st.GetProjectMember(project.ID, apiKey.CreatedBy)
    if memberErr != nil {
        // SECURITY: membership check is fail-CLOSED. Quota and denylist
        // historically fail open (a brief metering glitch is preferable
        // to global outage), but failing open on the authorization gate
        // would let any caller use a key whose membership we can't
        // verify — that's an authorization bypass.
        //
        // Error is NOT cached: the next attempt must retry.
        writeProxyError(w, http.StatusServiceUnavailable,
            "membership check unavailable, retry")
        return
    }
    memberPresent = member != nil
    memberPresentCacheSet(memberCacheKey, memberPresent)

    if memberPresent {
        if !quotaHit {
            if member.CreditQuotaPct != nil {
                userQuotaPct = member.CreditQuotaPct
                quotaCache.Set(memberCacheKey, *member.CreditQuotaPct)
            } else {
                quotaCache.Set(memberCacheKey, -1) // sentinel: no quota
            }
        }
        if !deniedHit {
            userDeniedModels = member.DeniedModels
            deniedModelsCacheSet(memberCacheKey, member.DeniedModels)
        }
    } else {
        // Member is gone — quota / denylist defaults are irrelevant
        // because the request will be rejected below, but cache the
        // sentinels so we don't redundantly hit the DB if the same
        // (project, user) appears again within the TTL window.
        if !quotaHit {
            quotaCache.Set(memberCacheKey, -1)
        }
        if !deniedHit {
            deniedModelsCacheSet(memberCacheKey, nil)
        }
    }
}

if !memberPresent {
    writeProxyError(w, http.StatusUnauthorized,
        "api key creator is no longer a project member")
    return
}
```

The two `writeProxyError(...); return` paths replace the prior fall-through "set sentinels and proceed" behavior. The variable `memberPresent` is declared by either the cache get OR the freshly-loaded GetProjectMember branch (Go's "declared at the outer scope, assigned in either path" pattern).

Note: this rewrite is NOT a call to `checkMembership` because `checkMembership` would do its OWN GetProjectMember call, defeating the point of the shared three-cache hydration. Keep `checkMembership` for its unit tests (it documents the contract precisely), but inline the equivalent logic here for the production hot path.

- [ ] **Step 6: Run the full proxy package**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/...`

Expected: all green. The existing tests that pre-date this change continue to pass because they either don't use AuthMiddleware or set up a complete `project_members` row.

- [ ] **Step 7: Build the whole repo**

Run: `cd /root/coding/modelserver && go build ./...`

Expected: green.

- [ ] **Step 8: Commit**

```bash
cd /root/coding/modelserver
git add internal/proxy/auth_middleware.go internal/proxy/auth_middleware_test.go
git commit -m "feat(proxy): AuthMiddleware fail-closes when api key creator is no longer a project member

Adds memberPresentCache (10s TTL, mirroring quota/denylist). Triple-cache
hydration: one GetProjectMember call on a miss feeds all three caches.

On member==nil: 401 'api key creator is no longer a project member'.
On transient DB error: 503 'membership check unavailable, retry' — a
deliberate fail-CLOSED divergence from quota/denylist's fail-open
posture (membership is the authorization gate; failing open would
bypass it). Errors are never cached.

Adds checkMembership() with unit tests via a fakeMemberStore covering
positive cache hits, negative cache hits, and the no-cache-on-error
contract.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Dashboard — confirmation dialog + post-action count

**Files:**
- Modify: `dashboard/src/api/members.ts`
- Modify: `dashboard/src/pages/members/MembersPage.tsx`

**Interfaces:**
- Consumes: backend endpoints from Task 4 (`GET /members/{userID}/affected-keys`, updated `DELETE /members/{userID}` response).
- Produces:
  - `useMemberAffectedKeys(projectId, userId | null): UseQueryResult<DataResponse<{ active_api_keys: number }>>`.
  - `useRemoveMember`'s mutation now returns `DataResponse<{ revoked_api_keys: number }>`.
  - `MembersPage` opens a confirmation Dialog when Remove is clicked; success toast reports the revoked count.

- [ ] **Step 1: Update `useRemoveMember` + add `useMemberAffectedKeys`**

In `dashboard/src/api/members.ts`, replace the existing `useRemoveMember` and add a new hook. Locate the existing definition (around line 93) and change:

```ts
export function useRemoveMember(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) =>
      api.delete<DataResponse<{ revoked_api_keys: number }>>(
        `/api/v1/projects/${projectId}/members/${userId}`,
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["members", projectId] });
      qc.invalidateQueries({ queryKey: ["members-compact", projectId] });
    },
  });
}

// useMemberAffectedKeys returns the count of active API keys the member
// has in the project. Used by the Remove-member confirmation dialog so
// the operator sees the blast radius before clicking Confirm. Pass null
// for userId to disable the query.
export function useMemberAffectedKeys(projectId: string, userId: string | null) {
  return useQuery({
    queryKey: ["member-affected-keys", projectId, userId],
    queryFn: () =>
      api.get<DataResponse<{ active_api_keys: number }>>(
        `/api/v1/projects/${projectId}/members/${userId}/affected-keys`,
      ),
    enabled: !!userId,
  });
}
```

Confirm `useQuery` and `DataResponse` are imported at the top of the file; the existing `useQuotaUsage` hook in the same file already uses both, so the imports should be in place.

- [ ] **Step 2: Add confirmation dialog state and JSX to `MembersPage`**

In `dashboard/src/pages/members/MembersPage.tsx`:

1. **Read the file end-to-end first.** The plan below describes the target shape; you must align with what's actually there (existing dropdown, dialog placement conventions, toast wiring).

2. **Imports.** Add at the top:
   ```ts
   import {
     Dialog,
     DialogContent,
     DialogDescription,
     DialogFooter,
     DialogHeader,
     DialogTitle,
   } from "@/components/ui/dialog";
   import { useMemberAffectedKeys } from "@/api/members";  // already exported from members.ts above
   ```
   Confirm `toast` from `sonner` is already imported (it should be — the page already uses toasts for other actions). If not, add `import { toast } from "sonner";`.

3. **State.** Inside the `MembersPage` component body, add:
   ```ts
   const [removeTarget, setRemoveTarget] = useState<ProjectMember | null>(null);
   const removeKeysQuery = useMemberAffectedKeys(
     projectId,
     removeTarget?.user_id ?? null,
   );
   ```
   (`ProjectMember` is the existing type used elsewhere on the page — confirm the import.)

4. **Replace the Remove menu item.** Find:
   ```tsx
   <DropdownMenuItem
     className="text-destructive-foreground"
     onClick={() => removeMember.mutate(m.user_id)}
   >
     Remove
   </DropdownMenuItem>
   ```
   Change to:
   ```tsx
   <DropdownMenuItem
     className="text-destructive-foreground"
     onClick={() => setRemoveTarget(m)}
   >
     Remove
   </DropdownMenuItem>
   ```

5. **Add the confirmation Dialog.** Place it alongside any existing dialogs on the page (typically at the bottom of the return tree, just before the outermost `</div>`). Use the exact pattern matching the project's other destructive-confirmation dialogs (e.g. RoutesPage's Delete Route dialog, around RoutesPage.tsx:530):

   ```tsx
   <Dialog
     open={!!removeTarget}
     onOpenChange={(open) => !open && setRemoveTarget(null)}
   >
     <DialogContent>
       <DialogHeader>
         <DialogTitle>Remove member</DialogTitle>
         <DialogDescription>
           Remove{" "}
           <strong>
             {removeTarget?.user?.display_name ?? removeTarget?.user?.email ?? "this member"}
           </strong>{" "}
           from this project?
         </DialogDescription>
       </DialogHeader>
       <div className="text-sm text-muted-foreground">
         {removeKeysQuery.isLoading && "Counting affected API keys…"}
         {removeKeysQuery.error && (
           <>Could not count affected API keys. Proceed with caution — any keys this member created will be revoked.</>
         )}
         {removeKeysQuery.data && (
           <>
             This will revoke{" "}
             <strong>{removeKeysQuery.data.data.active_api_keys}</strong>{" "}
             active API key
             {removeKeysQuery.data.data.active_api_keys === 1 ? "" : "s"}{" "}
             they created. The keys cannot be reactivated by re-adding this member.
           </>
         )}
       </div>
       <DialogFooter>
         <Button variant="outline" onClick={() => setRemoveTarget(null)}>
           Cancel
         </Button>
         <Button
           variant="destructive"
           disabled={removeMember.isPending}
           onClick={async () => {
             if (!removeTarget) return;
             try {
               const res = await removeMember.mutateAsync(removeTarget.user_id);
               const n = res?.data?.revoked_api_keys ?? 0;
               const who =
                 removeTarget.user?.display_name ??
                 removeTarget.user?.email ??
                 "member";
               toast.success(
                 `Removed ${who}; revoked ${n} API key${n === 1 ? "" : "s"}`,
               );
             } catch (e) {
               toast.error("Failed to remove member");
             } finally {
               setRemoveTarget(null);
             }
           }}
         >
           {removeMember.isPending ? "Removing…" : "Remove member"}
         </Button>
       </DialogFooter>
     </DialogContent>
   </Dialog>
   ```

   If the page does NOT have a `Button` with `variant="destructive"` precedent, use `variant="outline"` for both buttons and rely on the `text-destructive-foreground` className on the confirm button instead (match whatever destructive-style precedent the page already uses).

- [ ] **Step 3: Type-check + build**

Run: `cd /root/coding/modelserver/dashboard && pnpm exec tsc -b && pnpm build`

Expected: both green. No new errors.

- [ ] **Step 4: Manual smoke checklist (engineer does this once)**

Start the dev server and verify:

```bash
cd /root/coding/modelserver/dashboard && pnpm dev
```

Then in the browser:

1. Open a project's Members page. Pick a member with at least one active API key (create one first if needed via the Keys page).
2. Click **Remove** on that member. Confirm:
   - Dialog opens with the member's display name.
   - The "This will revoke N API key(s)…" line shows the correct count after a brief loading state.
3. Click **Cancel**. Confirm:
   - Dialog closes.
   - No DELETE call fires (check Network tab).
   - Member is still listed.
4. Repeat: click **Remove** on the same member; click **Remove member** in the dialog. Confirm:
   - Success toast reads `"Removed <name>; revoked N API key(s)"` with the same N.
   - Member is gone from the list.
   - On the Keys page, those keys now show status `revoked`.
5. Test the zero-key case: remove a member with no API keys. Confirm:
   - Dialog body reads "This will revoke 0 active API keys they created."
   - Success toast reads "revoked 0 API keys".

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add dashboard/src/api/members.ts dashboard/src/pages/members/MembersPage.tsx
git commit -m "feat(dashboard): confirmation dialog + revoked-key toast on member removal

Adds a confirmation Dialog before Remove member. Pre-count comes from
GET /members/{u}/affected-keys; the success toast reports the actual
revoked count returned by the updated DELETE handler.

Replaces the prior bare 'click to remove' menu item (which had no
confirmation at all).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**1. Spec coverage**

| Spec section / requirement | Task(s) |
|---|---|
| Layer A — transactional `RemoveProjectMember` | Task 2 |
| `CountActiveKeysForMember` for pre-removal dialog | Task 2 |
| Close superadmin loophole — `handleCreateKey` requires membership | Task 3 |
| `handleRemoveMember` returns `{revoked_api_keys: N}` | Task 4 |
| `GET /members/{userID}/affected-keys` endpoint | Task 4 |
| Layer B — fail-closed membership check in AuthMiddleware | Task 5 |
| `memberPresentCache` (10s, shared key) | Task 5 |
| Triple-cache hydration via one DB call | Task 5 |
| Backfill migration 055 | Task 1 |
| Frontend confirmation dialog | Task 6 |
| Frontend success toast w/ count | Task 6 |
| Status flip uses existing `revoked` enum value | Task 2 (SQL literal `'revoked'`), Task 1 (SQL literal `'revoked'`) |
| Re-add does NOT auto-reactivate | Implicit — nothing in any task reactivates revoked keys; documented in spec |
| Owner-removal protection | Out of scope (spec) — no task |
| Tests cover: tx atomicity, scope by (project, user), pre-migration zombie state, count-only-active | Task 2 (4 tests) |
| Tests cover: 401 on removed member, 503 on DB error (fail-closed), positive and negative cache hits, error not cached | Task 5 (5 tests) |
| Tests cover: migration revokes only orphans, leaves member's keys alone, idempotent on re-run | Task 1 (1 test, multiple assertions) |
| Frontend tests | N/A — no FE framework (called out in Global Constraints + Task 6 Step 4 manual smoke) |

No spec requirement is unimplemented.

**2. Placeholder scan**

- No TBD / TODO / "implement later" / "appropriate error handling" / "similar to Task N" / vague refs.
- Task 6 Step 2 instructs the engineer to read `MembersPage.tsx` end-to-end before editing — necessary because the page already has its own state and the dialog JSX must align with existing patterns. The example JSX block is the target shape; the surrounding scaffold (imports, useState placement, dialog footer convention) is described concretely.
- Task 5 Step 5 inlines the triple-cache logic instead of calling `checkMembership` directly — this is a deliberate decoupling explained in the step text (the inline version uses the shared `GetProjectMember` call to hydrate three caches in one DB round-trip; `checkMembership` exists purely for its unit-test contract).
- Task 2 Step 5 explicitly authorizes leaving the build red between commits (Task 2's signature change vs Task 4's caller update; Task 3 also lands inside this window). This is honest scaffolding, not a defect.

**3. Type consistency**

- `RemoveProjectMember(projectID, userID string) (int, error)` — declared in Task 2, consumed in Task 4 (`revokedCount, err := st.RemoveProjectMember(...)`).
- `CountActiveKeysForMember(projectID, userID string) (int, error)` — declared in Task 2, consumed in Task 4.
- Response JSON tags: `{"revoked_api_keys": N}` (Task 4 handler) ↔ TS type `{ revoked_api_keys: number }` (Task 6 mutation). Match.
- Response JSON tags: `{"active_api_keys": N}` (Task 4 handler) ↔ TS type `{ active_api_keys: number }` (Task 6 hook). Match.
- `memberStore` interface (Task 5) consumed only inside the proxy package; the production code path doesn't go through it, only the test does. Consistent.
- `memberPresentCacheGet/Set` and `InvalidateMemberPresentCache` — Task 5 declares all three; production code uses Get+Set, exported Invalidate name is reserved for future callers.
- `checkMembership(w, ms, projectID, userID) bool` — only consumed by tests in this plan; production inlines the equivalent logic. Documented as such.
- `useMemberAffectedKeys(projectId, userId | null)` — declared in Task 6 Step 1, consumed in Task 6 Step 2.
- `removeMember.mutateAsync(userId)` returning a typed body — established via the updated mutation generic in Task 6 Step 1, consumed in the dialog's confirm button (Task 6 Step 2).
- Task 3's `handleCreateKey` membership check uses existing `MemberFromContext` and `st.GetProjectMember` — no new symbols introduced; no downstream consumers.

No naming drift detected.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-06-28-revoke-keys-on-member-removal.md`.
