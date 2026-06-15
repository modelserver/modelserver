# Simplify Per-Member Credit Quota Permissions

**Date:** 2026-06-15
**Status:** Approved (brainstorming)

## Problem

Owners and maintainers cannot set credit quotas on themselves, and the existing
permission matrix has three overlapping restrictions that produce surprising
behavior (self-block, owner-target block, maintainer-on-maintainer block). The
"Set Quota" button is also hidden from the caller's own row and from any owner
row in the dashboard. Users have asked for a much simpler rule.

## New Rule

There is exactly one restriction left:

> **Only an owner may set a quota on an owner.**

All other combinations are allowed, including setting a quota on yourself and
setting a quota on a same-level maintainer. Developers still have no quota
management permissions. Superadmin still bypasses all checks.

### Resulting matrix

| Caller        | Owner target | Maintainer target | Developer target | Self |
|---------------|--------------|-------------------|------------------|------|
| Owner         | ✅           | ✅                | ✅               | ✅   |
| Maintainer    | ❌ (403)     | ✅                | ✅               | ✅   |
| Developer     | ❌           | ❌                | ❌               | ❌   |
| Superadmin    | ✅           | ✅                | ✅               | ✅   |

## Backend Changes

File: `internal/admin/handle_projects.go`

### `handleUpdateMember` (≈ L483–585)

1. **Remove** the self-quota block at L532–536
   (`"cannot set quota on yourself"`).
2. **Remove** the maintainer-on-maintainer block at L551–561
   (`"maintainers cannot set quota on other maintainers"`), including the
   adjacent comment that explains why the rule was *not* mirrored to
   `denied_models` — the comment becomes obsolete once the rule itself is
   gone.
3. **Narrow** the owner-target block at L545–549 so it only fires when the
   caller is a maintainer:

   ```go
   if (body.CreditQuotaPct != nil || body.ClearQuota) &&
       targetMember.Role == types.RoleOwner &&
       callerMember != nil && callerMember.Role == types.RoleMaintainer {
       writeError(w, http.StatusForbidden, "forbidden",
           "maintainers cannot set quota on an owner")
       return
   }
   ```

   Superadmin path is unaffected (it does not reach this block).

### `handleAddMember` (≈ L425–481)

The existing block at L459–461 forbids setting a quota when the new member's
role is `owner`. Narrow it the same way: only reject when the **caller is a
maintainer** adding an owner with a quota. (In practice maintainers cannot
create owners, so this is mostly defensive; owners adding owners with a quota
is now allowed for consistency with `handleUpdateMember`.)

### Store layer

No change. `store.UpdateProjectMember` already auto-clears the quota when a
member is promoted to owner (`internal/store/projects.go:303-304`); this is an
orthogonal rule and stays as-is.

### Runtime enforcement

No change. The proxy / rate-limit pipeline reads the per-member quota the same
way regardless of who set it.

## Frontend Changes

File: `dashboard/src/pages/members/MembersPage.tsx`

### Quota column editability (≈ L160–182)

Replace:

```ts
const editable =
  canManageQuota &&
  m.role !== "owner" &&
  m.user_id !== currentUser?.id;
```

with:

```ts
const editable =
  canManageQuota &&
  !(currentRole === "maintainer" && m.role === "owner");
```

This makes the "Set Quota" button appear on the caller's own row and on owner
rows when the caller is also an owner.

### Add Member dialog (≈ L344–400)

The quota input currently hides whenever the chosen role is `owner`
(≈ L376). Change the condition so it only hides when the caller is a
maintainer choosing the owner role:

```ts
const showQuotaInput = !(currentRole === "maintainer" && selectedRole === "owner");
```

## Testing

### Backend (Go)

Add cases to the existing admin handler tests (or create a focused new test
file under `internal/admin/`):

1. owner sets quota on self → 200, quota persisted
2. owner sets quota on another owner → 200, quota persisted
3. maintainer sets quota on self → 200
4. maintainer sets quota on another maintainer → 200
5. maintainer sets quota on owner → 403
6. developer attempts any quota change → 403 (unchanged)
7. `handleAddMember`: owner adds a new owner with `credit_quota_percent` → 201
   with quota saved
8. `handleAddMember`: maintainer adds a maintainer with quota → 201 (existing
   passing case kept)

### Frontend

Manual smoke test:

- Log in as a project owner — confirm the "Set Quota" button is now clickable
  on the owner's own row and on other owner rows; saving works.
- Log in as a project maintainer — confirm the button is clickable on the
  maintainer's own row and on other maintainer rows; the owner row still shows
  plain text (no button).

## Risks

- An owner can lock themselves out by setting their own quota to 0%. Acceptable
  per user direction; they can recover via another owner or a superadmin.
- No impact on quota enforcement at request time.

## Out of Scope

- Denylist management permissions (already permissive on owner/self; see
  commit `83acb4f`).
- Any change to the `My Quota` panel or quota-usage endpoints.
- Refactoring the role checks into a central helper — the rules are simple
  enough to inline.
