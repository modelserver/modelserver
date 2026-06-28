# Revoke API Keys on Member Removal — Design

## Problem

A user reported that ex-members' API keys still work. Confirmed: removing a
member from a project leaves their API keys fully usable.

Root cause:

1. `handleRemoveMember` (`internal/admin/handle_projects.go:605-617`) only
   calls `st.RemoveProjectMember`.
2. `RemoveProjectMember` (`internal/store/projects.go:328-333`) runs one
   statement: `DELETE FROM project_members WHERE project_id=$1 AND
   user_id=$2`. The `api_keys` table is never touched.
3. `api_keys` foreign keys are `project_id REFERENCES projects ON DELETE
   CASCADE` and `created_by REFERENCES users ON DELETE RESTRICT` — neither
   triggers anything on a `project_members` delete.
4. `AuthMiddleware` (`internal/proxy/auth_middleware.go:263-385`) checks
   `apiKey.Status == active`, `apiKey.ExpiresAt`, and `project.Status ==
   active`. It already calls `GetProjectMember(project.ID,
   apiKey.CreatedBy)` to hydrate per-member quota and denylist — but the
   `member == nil` branch (line ~357) deliberately sets sentinels and
   continues serving the request. Comment in the file: *"member == nil
   (API key outlived membership)"*. Known path, no enforcement.

Result: ex-members continue to consume project credits via any key they
had (or copied) at removal time. Per-member quota silently degrades to
"unlimited," per-member denylist silently degrades to empty.

## Goal

Two layers of defense, deployed together:

- **Layer A — Proactive revocation at the removal site.** Removing a
  member also revokes (status `active → revoked`) every API key that
  member created in the project, atomically.
- **Layer B — Fail-closed membership check at every request.**
  `AuthMiddleware` rejects requests whose `apiKey.CreatedBy` is no longer
  a project member, even if Layer A was bypassed (direct SQL, future
  ownership-transfer flow, race conditions across nodes).
- **One-shot backfill.** A migration revokes existing zombie keys in
  production at deploy time.
- **Dashboard feedback.** The Members page shows a pre-removal
  confirmation dialog stating how many keys will be revoked, and the
  success toast reports the actual count.

## Non-goals

- **Owner-removal protection.** `handleRemoveMember` currently lets an
  owner remove themselves or be removed by another owner. That is a
  separate bug. This spec applies the revocation rule uniformly regardless
  of role — a removed owner's keys are revoked just like anyone else's.
- **Re-add auto-reactivation.** Re-adding a previously-removed member
  does NOT resurrect their revoked keys. The user creates new keys, or an
  admin manually flips status back via the existing edit path.
- **Reason field on revocation.** We do not add a `revoked_reason` column
  or audit table entry. The deploy-time migration count plus the
  per-removal response field provide adequate visibility.
- **Hard delete of revoked keys.** Existing behavior (admin must
  explicitly revoke, then delete) is unchanged.

## Closing the superadmin loophole

A background security review flagged that today `projectAccessMiddleware`
(`internal/admin/routes.go:321-324`) lets superadmins bypass the
project-membership check entirely — they can call
`POST /projects/{projectID}/keys` without ever appearing in
`project_members`. Combined with this spec's `NOT EXISTS (project_members
…)` predicate, those superadmin-created keys would be revoked by
migration 055 (and by any future `RemoveProjectMember` call that happens
to share the `created_by` user).

Rather than carve a `WHERE NOT EXISTS (… is_superadmin)` exemption into
both the migration and `RemoveProjectMember` — which would weaken the
invariant "every active key has a member" — we tighten key creation
instead:

**`handleCreateKey` requires the caller to have a `project_members`
row in the target project, even when the caller is a superadmin.**
Superadmins who want to create a key in a project they don't yet belong
to must first add themselves as a member (any role) via the existing
`POST /projects/{projectID}/members` endpoint.

This restores the invariant the revocation logic relies on. The
migration's `NOT EXISTS` predicate becomes precise: every active key
has a corresponding member row, by construction.

Failure mode: a superadmin who calls `POST /keys` without first joining
gets a 403 with body
`{"error":{"code":"forbidden","message":"superadmins must join the
project as a member before creating API keys"}}`. The dashboard already
exposes the Add Member flow on the project's Members tab.

## Scope decisions (from brainstorming)

| Decision | Choice |
|---|---|
| Post-removal API-key status | `revoked` — reuses the existing `APIKeyStatus` enum value, which already semantically means "scheduled for delete by an admin" |
| Re-add of removed member | Keys stay revoked |
| Backfill of pre-existing zombie keys | One-shot migration revokes them at deploy time |
| Auth check site | Reuse the existing `GetProjectMember` call in `AuthMiddleware`; 401 when `member == nil`; cache the "missing member" state alongside the existing quota / denylist caches so the per-request DB-call budget is unchanged |
| Dashboard UI | Confirmation dialog shows pre-count; success toast shows post-count |
| Owner-removal protection | Out of scope; revocation applies uniformly |

## Backend

### Layer A — Transactional revocation

`internal/store/projects.go`:

```go
// RemoveProjectMember atomically revokes every active API key the member
// created in this project, then removes the member. Returns the number of
// keys flipped from active to revoked.
//
// The revocation runs FIRST so a concurrent in-flight request can never
// observe the (member-deleted, key-still-active) state during the tx
// window. Postgres rolls back both statements on any failure.
func (s *Store) RemoveProjectMember(projectID, userID string) (revokedCount int, err error) {
    tx, err := s.pool.Begin(context.Background())
    if err != nil {
        return 0, fmt.Errorf("begin: %w", err)
    }
    defer tx.Rollback(context.Background()) // no-op after Commit

    tag, err := tx.Exec(context.Background(), `
        UPDATE api_keys
           SET status = 'revoked', updated_at = NOW()
         WHERE project_id = $1 AND created_by = $2 AND status = 'active'`,
        projectID, userID)
    if err != nil {
        return 0, fmt.Errorf("revoke keys: %w", err)
    }
    revokedCount = int(tag.RowsAffected())

    if _, err := tx.Exec(context.Background(),
        `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`,
        projectID, userID); err != nil {
        return 0, fmt.Errorf("delete member: %w", err)
    }

    if err := tx.Commit(context.Background()); err != nil {
        return 0, fmt.Errorf("commit: %w", err)
    }
    return revokedCount, nil
}

// CountActiveKeysForMember returns the number of active API keys the
// given user has in the given project. Used by the dashboard's
// pre-removal confirmation dialog.
func (s *Store) CountActiveKeysForMember(projectID, userID string) (int, error) {
    var n int
    err := s.pool.QueryRow(context.Background(),
        `SELECT COUNT(*) FROM api_keys
           WHERE project_id = $1 AND created_by = $2 AND status = 'active'`,
        projectID, userID).Scan(&n)
    return n, err
}
```

Signature change is backward-incompatible — the only in-tree caller is
`handleRemoveMember`. There are no other callers.

### Handler change

`internal/admin/handle_projects.go`:

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

Response shape changes from `204 No Content` to `200 OK` with body:

```json
{ "data": { "revoked_api_keys": 3 } }
```

The dashboard's existing call only checks the status code for success;
adding a body is backward-compatible.

A new sibling endpoint feeds the pre-removal dialog:

```go
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

Registered in `internal/admin/routes.go` inside the existing project
members subtree, beside the existing `DELETE /members/{userID}`:

```go
r.Get("/members/{userID}/affected-keys", handleCountAffectedKeysOnRemove(st))
```

### Layer B — Fail-closed membership check in `AuthMiddleware`

`internal/proxy/auth_middleware.go`, around line 339.

Today the middleware loads `member` via `GetProjectMember` and uses it
only to hydrate quota and denylist:

```go
if !quotaHit || !deniedHit {
    member, memberErr := st.GetProjectMember(project.ID, apiKey.CreatedBy)
    if memberErr != nil {
        // Fail open for BOTH quota and denylist on transient DB errors.
    } else if member != nil {
        // hydrate quota + denylist caches
    } else {
        // member == nil (API key outlived membership) — set sentinels.
    }
}
```

Replace with a three-cache hydration that also caches *membership
presence*:

```go
memberCacheKey := project.ID + ":" + apiKey.CreatedBy

memberPresent, presenceHit := memberPresentCache.Get(memberCacheKey)
quotaCached, quotaHit       := quotaCache.Get(memberCacheKey)
deniedCached, deniedHit     := deniedModelsCacheGet(memberCacheKey)

if !presenceHit || !quotaHit || !deniedHit {
    member, memberErr := st.GetProjectMember(project.ID, apiKey.CreatedBy)
    if memberErr != nil {
        // SECURITY: this branch is fail-CLOSED for the membership check
        // even though the quota / denylist hydration historically fails
        // open. A transient DB error must not turn into "any caller can
        // use a key whose membership we can't verify."
        writeProxyError(w, http.StatusServiceUnavailable,
            "membership check unavailable, retry")
        return
    }
    memberPresent = member != nil
    memberPresentCache.Set(memberCacheKey, memberPresent)
    if memberPresent {
        // hydrate quota + denylist as today
    } else {
        quotaCache.Set(memberCacheKey, -1)         // sentinel: no quota
        deniedModelsCacheSet(memberCacheKey, nil)
    }
}

if !memberPresent {
    writeProxyError(w, http.StatusUnauthorized,
        "api key creator is no longer a project member")
    return
}

// continue with hydrated quota / denylist as today
```

The `memberPresentCache` is a new sync.Map-backed cache (10 s TTL,
matching the existing two). All three caches share a key
(`project.ID + ":" + apiKey.CreatedBy`). A single cache miss triggers a
single DB query that hydrates all three caches at once.

**Why fail-closed here when quota/denylist fail-open.** Quota and
denylist are usage-shaping controls — failing open briefly degrades
metering accuracy. Membership is the *authorization* gate. Failing open
on it turns any transient DB error into an authorization bypass. 503 +
retry is the safe answer; quotas survive a short period of staleness,
authorization must not.

### Backfill migration

`internal/store/migrations/055_revoke_orphaned_api_keys.sql`:

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

The migration framework logs `UPDATE n` so the operator sees the cleanup
magnitude in deploy logs. The migration does not filter by project status
— a revoked key on an archived project stays revoked; unarchiving the
project does not reactivate it.

## Frontend

`dashboard/src/api/members.ts`:

```ts
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

// Existing useRemoveMember updates its return type:
//   useMutation<DataResponse<{ revoked_api_keys: number }>, Error, string>
```

`dashboard/src/pages/members/MembersPage.tsx`:

- Replace the bare `removeMember.mutate(m.user_id)` `DropdownMenuItem`
  click with state-setting that opens a confirmation `Dialog` (matching
  the patterns already used by `UpstreamGroupsPage` etc.):
  ```
  ┌─ Remove member ──────────────────────────┐
  │                                          │
  │  Remove {display_name} from this         │
  │  project?                                │
  │                                          │
  │  This will revoke {N} API keys they      │
  │  created. The keys cannot be reactivated │
  │  by re-adding this member.               │
  │                                          │
  │           [Cancel]   [Remove member]     │
  └──────────────────────────────────────────┘
  ```
- When the dialog opens, fire `useMemberAffectedKeys`. While the query is
  loading, render the line as "Counting affected API keys…"; on error
  render "Could not count affected keys — proceed with caution." Either
  state still allows confirmation (the failure does not block removal).
- On confirm: `await removeMember.mutateAsync(userId)` → response carries
  `{ revoked_api_keys: N }` → `toast.success(\`Removed ${name}; revoked
  ${N} API keys\`)`. On error: existing toast.error path.

No new test framework. Verification = `pnpm build` + manual smoke.

## Data flow

```
Admin clicks "Remove" on a member row
  └─► confirmation Dialog opens
       └─► useMemberAffectedKeys(projectId, userId)
            └─► GET /projects/{p}/members/{u}/affected-keys
                 └─► CountActiveKeysForMember → COUNT(*)
       Dialog body shows count
  Admin confirms
  └─► useRemoveMember.mutateAsync(userId)
       └─► DELETE /projects/{p}/members/{u}
            └─► handleRemoveMember
                 └─► RemoveProjectMember tx:
                       UPDATE api_keys SET status='revoked'
                         WHERE ... AND status='active'                  (rows = N)
                       DELETE FROM project_members WHERE ...
                     COMMIT
            response: { "data": { "revoked_api_keys": N } }
       toast.success("Removed X; revoked N API keys")

Steady state — request bearing a revoked key:
  └─► AuthMiddleware loads api_key (status=='revoked')
       existing check `apiKey.Status != active` 401s
       (Layer A made the key inert; Layer B never even fires here.)

Defense-in-depth — key still 'active' but membership is gone:
  └─► AuthMiddleware loads api_key (status=='active')
       project-status check passes
       triple-cache miss → GetProjectMember → nil
       memberPresentCache.Set(key, false)        (caches the miss)
       401 "api key creator is no longer a project member"
```

## Error handling

| Failure | Behavior |
|---|---|
| `RemoveProjectMember` tx fails mid-revoke | Postgres rolls back; 500; member intact; keys intact. Operator retries. |
| `affected-keys` endpoint fails | Dialog body says "Could not count affected keys"; confirm still allowed. Failure does not block removal. |
| `AuthMiddleware`'s `GetProjectMember` returns a transient DB error | **Fail-closed**: 503 "membership check unavailable, retry" (divergence from quota's fail-open posture — security-critical path). |
| Cache race: member removed between request A (sets `memberPresent=true`) and request B within the 10 s TTL | Request B may briefly succeed. Acceptable: Layer A already revoked the key, so the `apiKey.Status != active` check catches it before the cache matters. The membership cache is the safety net, not the primary defense. |
| Migration 055 runs on a fresh DB with no zombie keys | `UPDATE 0`. No-op. |
| Re-running migration 055 after deploy | `UPDATE 0`. The post-fix `handleRemoveMember` keeps the invariant; nothing to clean up. |

## Testing

### Backend

`internal/store/projects_test.go` — extend:

- `TestRemoveProjectMember_RevokesActiveKeys`: member + 3 active keys + 1
  already-revoked key. Call `RemoveProjectMember`. Assert returned count
  = 3; all 4 keys end up `revoked`; member row gone; *other projects'*
  keys for the same user are untouched.
- `TestRemoveProjectMember_AtomicOnFailure`: simulate a tx error (e.g.
  use a closed pool or invalid UUID) and assert no partial state.
- `TestRemoveProjectMember_NoMemberStillRevokesKeys`: keys exist, no
  membership row (pre-migration edge case). Count returned, keys
  revoked, no error from the DELETE-zero-rows.
- `TestCountActiveKeysForMember`: zero / one / several; only counts
  `status='active'`.

`internal/proxy/auth_middleware_test.go` — extend:

- `TestAuthMiddleware_RejectsRemovedMember`: active key whose
  `created_by` has no `project_members` row. Assert 401 + body contains
  `"api key creator is no longer a project member"`.
- `TestAuthMiddleware_RejectsRemovedMember_Cached`: same scenario, two
  back-to-back requests; assert only one `GetProjectMember` DB call
  (verify via a wrapping spy store).
- `TestAuthMiddleware_MembershipCheck_DBError_FailsClosed`: store
  returns a transient error from `GetProjectMember`; assert 503 (not
  401, not 200).

`internal/store/migrations_055_test.go` (new, mirroring the existing
migration-test pattern, e.g. `migrations_053_test.go`):

- Seed: 2 zombie active keys + 2 normal active keys + 1 already-revoked
  key. Run migration 055. Assert zombies become revoked; normal stays
  active; already-revoked stays revoked. Re-run; assert idempotent
  (no further changes).

### Frontend

No test framework. Verification:

- `cd dashboard && pnpm exec tsc -b && pnpm build` green.
- Manual smoke:
  1. Open Members page, click Remove on a member with >0 API keys;
     confirm the dialog shows the correct count; confirm; verify the
     success toast says the same count; verify the keys list page shows
     them as `revoked`; verify the affected user's other unrelated
     project keys are untouched.
  2. Cancel path — leaves member intact, no DELETE call fires.
  3. Remove a member with zero keys — dialog shows "0 API keys"; toast
     reports "revoked 0 API keys".

## Build / deploy order

1. **Backend deploy** brings migration 055 + Layer A + Layer B in one
   release. The migration runs at process start; the middleware change
   begins enforcing on the first request; subsequent removals go
   through the new transactional path.
2. **Dashboard deploy** updates the toast / dialog. Old dashboard
   continues to work — it just ignores the new response body and shows
   its existing generic success toast (no breakage).

No rollback hazard. Migration 055 has no down step: revoke is the safe
direction. Un-revoking a once-zombie key is an explicit admin action
through the existing edit endpoint.
