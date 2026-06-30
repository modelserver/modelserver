# Single-Owner Invariant for Projects — Design

## Problem

`project_members.role` has no constraint preventing a project from having
zero or multiple owners. Today five write paths can violate this, and none
of them check:

| Write | HTTP route | Store fn | Owner-count check |
|---|---|---|---|
| INSERT (creator) | `POST /projects` | `CreateProject` | ✓ (hard-coded `'owner'`) |
| INSERT/UPSERT | `POST /projects/{id}/members` | `AddProjectMember` | ✗ |
| UPDATE role | `PUT /projects/{id}/members/{uid}` | `UpdateProjectMember` | ✗ |
| UPDATE role (unused) | — | `UpdateProjectMemberRole` | ✗ |
| DELETE | `DELETE /projects/{id}/members/{uid}` | `RemoveProjectMember` | ✗ |

`role` is also not enum-validated — the API accepts arbitrary strings.

Concrete observed consequences:

- A user with `max_projects=5` ended up owning five projects and being
  a `developer` in a sixth — earlier ownership transfers (via
  `PUT /members/{uid}` with `role='owner'`) silently created multi-owner
  states, then someone got demoted, leaving the project's original
  creator without an owner row. `CountUserOwnedProjects` (the basis for
  the per-user quota check) reads `role='owner'`, so multi-owner states
  also double-count against multiple users' `max_projects`.
- Nothing prevents `DELETE /members/{uid}` on the only owner, which
  leaves the project ownerless. Downstream code that assumes "every
  project has an owner" (notification routing, billing attribution) has
  no defined behavior in that state.

## Goal

Enforce the invariant: **every non-archived project has exactly one
`role='owner'` row in `project_members`.**

Two layers of defense:

- **Layer A — handler-level guards.** Reject requests that would
  create/remove owners through the wrong endpoint, with a friendly
  409 message.
- **Layer B — store-level transactional re-check.** Every write that
  could affect owner count re-counts inside the same transaction with
  row locks; rollback + sentinel error if the post-state would be
  `owner count ≠ 1`. Survives concurrent requests across nodes.

A new dedicated endpoint, `POST /projects/{id}/transfer-ownership`,
becomes the **only** way to change who the owner is.

Archived projects (`status='archived'`) are explicitly excluded — they
are read-only by convention and existing tooling does not maintain
membership rows on them.

## Non-goals

- **No database-level UNIQUE constraint.** Historical data may already
  have `owner count ≠ 1` projects; adding `CREATE UNIQUE INDEX ...
  WHERE role='owner'` would fail at migration time. We hold the line
  at the application layer and leave historical bad state in place.
- **No backfill migration.** Per user decision: no automatic repair of
  existing 0-owner or multi-owner projects. Operators clean them up
  manually if desired.
- **No startup self-check.** Same reasoning — don't surface noise from
  legacy data the user has chosen to leave alone.

## API Changes

### New endpoint

```
POST /api/v1/projects/{projectID}/transfer-ownership
Authorization: Bearer <jwt>
Body:
  {
    "to_user_id": "<uuid>",
    "demote_to":  "maintainer" | "developer"   // optional, default "developer"
  }
```

Permission: caller must be the current owner of the project, or a
superadmin. (Maintainers cannot transfer ownership.)

Atomic effect within one transaction:

1. Validate `to_user_id` is a current member of the project; otherwise
   400 `not_a_member`.
2. Validate `to_user_id` is not the current owner; otherwise 400
   `already_owner` (no-op).
3. Validate `demote_to ∈ {maintainer, developer}`; otherwise 400.
4. `UPDATE project_members SET role=$demote_to, credit_quota_percent=NULL
   WHERE project_id=$p AND role='owner'` — demotes the (single) current
   owner.
5. `UPDATE project_members SET role='owner', credit_quota_percent=NULL
   WHERE project_id=$p AND user_id=$to` — promotes target.
6. Re-count owners in the same tx; abort if ≠ 1.

The `credit_quota_percent=NULL` on the new owner mirrors existing
`UpdateProjectMember` behavior (owners never carry a per-member quota).
Demoting the old owner from `NULL` to a finite quota is the
caller's responsibility via a subsequent `PUT /members/{old_owner_uid}`
if they want one; we default to no quota.

Response: `200 OK` with the updated member list (same shape as
`GET /projects/{id}/members`).

### Tightened existing endpoints

| Endpoint | New behavior | HTTP code |
|---|---|---|
| `POST /projects/{id}/members` | Body `role` must be in `{maintainer, developer}`. `role='owner'` rejected. | 400 `invalid_role` |
| `PUT /projects/{id}/members/{uid}` | If body sets `role='owner'`, reject. If `uid` is the current owner and body sets a different `role`, reject. | 400 `invalid_role` / 409 `owner_must_transfer` |
| `DELETE /projects/{id}/members/{uid}` | If `uid` is the current owner, reject. | 409 `owner_must_transfer` |
| `POST /projects` | unchanged — still creates exactly one owner row for the creator. | — |

Error body follows the existing `writeError` shape:
`{"error":"conflict","message":"owner cannot be removed; use POST /projects/{id}/transfer-ownership first"}`.

## Implementation Outline

### New file: `internal/types/roles.go`

```go
package types

const (
    RoleOwner      = "owner"
    RoleMaintainer = "maintainer"
    RoleDeveloper  = "developer"
)

// AssignableRoles is the set of roles that can be set via the
// add-member and update-member endpoints. Owner is intentionally
// excluded: it is only ever set by CreateProject or by the
// transfer-ownership endpoint.
var AssignableRoles = map[string]struct{}{
    RoleMaintainer: {},
    RoleDeveloper:  {},
}

func IsAssignableRole(r string) bool {
    _, ok := AssignableRoles[r]
    return ok
}
```

(`types.RoleOwner` etc. already exist informally — consolidate them here
if scattered.)

### `internal/store/projects.go`

- **New:** `TransferProjectOwnership(projectID, fromUID, toUID, demoteTo string) error`
  - `BEGIN`
  - `SELECT user_id, role FROM project_members WHERE project_id=$1 FOR UPDATE` — locks all member rows, including the current owner and the target.
  - Validate: exactly one row with `role='owner'`, its `user_id == fromUID`; target row exists; target is not the current owner.
  - Two UPDATEs as above.
  - Re-count owners; assert == 1.
  - `COMMIT`.
  - Returns sentinel errors (`ErrNotAMember`, `ErrAlreadyOwner`,
    `ErrInvariantViolated`) so the handler can map to HTTP codes.

- **Modified:** `UpdateProjectMember`
  - When `role != nil` and target is currently `owner`, return
    `ErrOwnerMustTransfer` without UPDATE-ing.
  - When `role != nil` and the new role is `'owner'`, return
    `ErrInvalidRole`.
  - (Quota / denied_models updates on the owner row continue to work
    as today — they don't change role.)

- **Modified:** `RemoveProjectMember`
  - Begin the existing transaction.
  - Before the final `DELETE FROM project_members`, run `SELECT role FROM
    project_members WHERE project_id=$1 AND user_id=$2 FOR UPDATE`. If
    the row's role is `owner`, rollback and return
    `ErrOwnerMustTransfer`.
  - Keep the existing key-revoke + grant-delete steps.

- **Modified:** `AddProjectMember` — leave as-is; handler now rejects
  invalid roles before reaching the store, but the store remains
  permissive so internal code (`CreateProject`) can still set `'owner'`.

- **Deleted:** `UpdateProjectMemberRole` (orphaned, no callers).

### `internal/admin/handle_projects.go`

- **New:** `handleTransferOwnership(st)` — parses body, performs
  authorization:
  - If caller `IsSuperadmin`: allowed unconditionally (matches the
    `handleListMembers` precedent where superadmins can act on
    projects without being members).
  - Otherwise: look up caller's membership row; require
    `member.Role == RoleOwner`. Maintainers and developers → 403.

  Then calls `st.TransferProjectOwnership(projectID, callerUID, body.ToUserID, demoteTo)`.
  When the caller is a superadmin who is not a member, pass the
  current owner's UID as `fromUID` (resolved by a single
  `SELECT user_id FROM project_members WHERE project_id=$1 AND role='owner'`)
  so the store's `from == current owner` assertion holds.

  Maps sentinel errors to HTTP codes:
  - `ErrNotAMember` → 400 `not_a_member`
  - `ErrAlreadyOwner` → 400 `already_owner`
  - `ErrOwnerMustTransfer` → 409 (defensive; shouldn't fire here)
  - `ErrInvariantViolated` → 500 + log (defensive)
- **Modified:** `handleAddMember` — reject `body.Role == "owner"` or
  `!IsAssignableRole(body.Role)` with 400.
- **Modified:** `handleUpdateMember` — same role validation as add when
  role is being changed; map store sentinel errors to HTTP codes.
- **Modified:** `handleRemoveMember` — map `ErrOwnerMustTransfer` to
  409.

### `internal/admin/routes.go`

Add one line under the existing `/projects/{projectID}` route group:

```go
r.Post("/transfer-ownership", handleTransferOwnership(st))
```

(Path-level auth + project-membership check are already provided by
the surrounding middleware.)

### Tests

`internal/admin/handle_projects_test.go` — handler-level table tests:

| Scenario | Expected |
|---|---|
| `POST /members` with `role='owner'` | 400 `invalid_role` |
| `POST /members` with `role='maintainer'` | 201 |
| `POST /members` with `role='janitor'` | 400 `invalid_role` |
| `PUT /members/{owner_uid}` with `role='developer'` | 409 `owner_must_transfer` |
| `PUT /members/{maintainer_uid}` with `role='owner'` | 400 `invalid_role` |
| `PUT /members/{maintainer_uid}` with `role='developer'` | 200 |
| `DELETE /members/{owner_uid}` | 409 `owner_must_transfer` |
| `DELETE /members/{maintainer_uid}` | 204 |
| `POST /transfer-ownership` to a member | 200; owner count remains 1 |
| `POST /transfer-ownership` to non-member | 400 `not_a_member` |
| `POST /transfer-ownership` to current owner | 400 `already_owner` |
| `POST /transfer-ownership` by maintainer | 403 |
| `POST /transfer-ownership` by superadmin (not a member) | 200 |

`internal/store/projects_test.go` — store-level integration tests with a
real Postgres (matches existing test setup):

- Concurrent transfer: two goroutines simultaneously transfer to two
  different targets; exactly one COMMIT succeeds, the other rolls back
  (verified by post-state having one owner equal to one of the two
  targets).
- Concurrent demote-via-`PUT` + `DELETE` of the owner: both must
  return `ErrOwnerMustTransfer`; owner row unchanged.

## Files touched

```
internal/types/roles.go                   # NEW
internal/store/projects.go                # +Transfer, modify Update/Remove, delete unused UpdateProjectMemberRole
internal/store/projects_test.go           # +concurrency tests
internal/admin/handle_projects.go         # +handleTransferOwnership, tighten add/update/remove
internal/admin/handle_projects_test.go    # +table tests
internal/admin/routes.go                  # +1 route
```

Not touched: migrations, `users` table, `CreateProject`, frontend
(separate spec if/when UI gains a "transfer ownership" button).

## Rollout

Single deploy. No data migration. Existing 0-owner or multi-owner
projects keep working under all existing endpoints (their `PUT`/`DELETE`
on the owner row will start returning 409 — which is the correct new
behavior; operators can resolve manually via direct SQL or by adding a
new owner first via `transfer-ownership`, which itself requires exactly
one current owner so multi-owner projects need DB cleanup before
transfer works).

A follow-up may add a one-off admin tool to repair `owner count ≠ 1`
projects, but that is out of scope here.
