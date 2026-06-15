# Simplify Member Quota Permissions — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let owners set quota on any member (including themselves and other owners) and let maintainers set quota on any non-owner member (including themselves). Only restriction left: maintainers cannot set quota on owners.

**Architecture:** The current rules live inline in two HTTP handlers (`handleAddMember`, `handleUpdateMember`) in `internal/admin/handle_projects.go`, plus two filters in `dashboard/src/pages/members/MembersPage.tsx`. We extract the permission check into a small pure helper so we can unit-test the full role matrix with no database, then replace the inline branches with a single call to the helper. Frontend filters are loosened to match.

**Tech Stack:** Go (chi router, stdlib `net/http`/`testing`), React + TypeScript + Vite (no JS unit tests in this repo — verification is `tsc -b && vite build` + manual smoke test). Spec: `docs/superpowers/specs/2026-06-15-self-quota-permissions-design.md`.

---

## File Structure

**Modify**
- `internal/admin/handle_projects.go` — extract permission helper, replace inline checks in `handleAddMember` (≈ L425–481) and `handleUpdateMember` (≈ L483–585).
- `dashboard/src/pages/members/MembersPage.tsx` — relax the `editable` predicate (≈ L160–182), the dropdown-menu condition (≈ L274–280), and the "Add Member" dialog's quota-input visibility (≈ L376).

**Create**
- `internal/admin/quota_permissions.go` — single file holding `canSetMemberQuota`, a pure function deciding `(allowed bool, status int, code, msg string)` from `(callerMember, targetMemberRole, isSelf bool)`. Lives next to handler code; one responsibility.
- `internal/admin/quota_permissions_test.go` — exhaustive table test for the helper across the role matrix. No DB needed.

**Do not change**
- `internal/store/projects.go` — store layer is unaffected.
- `internal/types/user.go` — role constants unchanged.
- Runtime rate-limit/proxy code — unaffected.

---

## Task 1: Extract permission helper (pure function, TDD)

**Files:**
- Create: `internal/admin/quota_permissions.go`
- Create: `internal/admin/quota_permissions_test.go`

The current rules are scattered across two handlers with overlapping conditions. Pulling them into one pure function lets us prove the full matrix with no DB and gives both handlers a single source of truth.

The function signature reflects what handlers know at call time:
- `caller` — the calling user's `*types.ProjectMember` for this project. May be `nil` when the caller is a superadmin (their `MemberFromContext` returns `nil`; see `internal/admin/admin.go:90-93`).
- `targetRole` — the target member's role string. For `handleAddMember`, this is the new role from the request body; the target member doesn't exist yet. For `handleUpdateMember`, it's `targetMember.Role` loaded from the store.
- `isSelf` — `true` iff the caller is acting on their own row. For add, this is `caller != nil && caller.UserID == newUserID`. For update, `caller != nil && caller.UserID == userID`.

`caller == nil` (superadmin) ⇒ always allowed.

- [ ] **Step 1: Write the failing test**

Create `internal/admin/quota_permissions_test.go`:

```go
package admin

import (
	"net/http"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func TestCanSetMemberQuota(t *testing.T) {
	owner := &types.ProjectMember{UserID: "u-owner", Role: types.RoleOwner}
	maint := &types.ProjectMember{UserID: "u-maint", Role: types.RoleMaintainer}
	dev := &types.ProjectMember{UserID: "u-dev", Role: types.RoleDeveloper}

	cases := []struct {
		name       string
		caller     *types.ProjectMember
		targetRole string
		isSelf     bool
		wantOK     bool
		wantStatus int
	}{
		// Superadmin (nil caller) — always allowed.
		{"superadmin_on_owner", nil, types.RoleOwner, false, true, 0},
		{"superadmin_on_self_owner", nil, types.RoleOwner, true, true, 0},

		// Owner caller — can set quota on anyone, including self and other owners.
		{"owner_on_owner", owner, types.RoleOwner, false, true, 0},
		{"owner_on_self", owner, types.RoleOwner, true, true, 0},
		{"owner_on_maintainer", owner, types.RoleMaintainer, false, true, 0},
		{"owner_on_developer", owner, types.RoleDeveloper, false, true, 0},

		// Maintainer caller — can set on non-owners, including self and other maintainers.
		{"maintainer_on_owner", maint, types.RoleOwner, false, false, http.StatusForbidden},
		{"maintainer_on_self", maint, types.RoleMaintainer, true, true, 0},
		{"maintainer_on_other_maintainer", maint, types.RoleMaintainer, false, true, 0},
		{"maintainer_on_developer", maint, types.RoleDeveloper, false, true, 0},

		// Developer caller — never has quota permissions (route-level requireRole
		// already blocks them; helper still returns 403 for defence in depth).
		{"developer_on_developer", dev, types.RoleDeveloper, false, false, http.StatusForbidden},
		{"developer_on_self", dev, types.RoleDeveloper, true, false, http.StatusForbidden},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, status, code, msg := canSetMemberQuota(c.caller, c.targetRole, c.isSelf)
			if ok != c.wantOK {
				t.Fatalf("ok=%v, want %v (status=%d code=%q msg=%q)", ok, c.wantOK, status, code, msg)
			}
			if !ok && status != c.wantStatus {
				t.Fatalf("status=%d, want %d (msg=%q)", status, c.wantStatus, msg)
			}
			if !ok && (code == "" || msg == "") {
				t.Fatalf("rejection must include code+msg; got code=%q msg=%q", code, msg)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /root/coding/modelserver && go test ./internal/admin/ -run TestCanSetMemberQuota -v`

Expected: FAIL — `canSetMemberQuota` undeclared.

- [ ] **Step 3: Write the helper**

Create `internal/admin/quota_permissions.go`:

```go
package admin

import (
	"net/http"

	"github.com/modelserver/modelserver/internal/types"
)

// canSetMemberQuota encodes the only remaining rule for member-quota mutation:
// only an owner may set a quota on an owner. Maintainers may set quota on any
// non-owner (including themselves and other maintainers); owners may set
// quota on anyone (including themselves and other owners); developers have no
// quota permissions (the calling route's requireRole already enforces this,
// but the helper also returns 403 for any unexpected caller role).
//
// caller == nil means the request is from a superadmin (MemberFromContext
// returns nil in that case); superadmin bypasses all checks.
//
// On rejection, the returned (status, code, msg) match writeError's signature
// so callers can forward them verbatim.
func canSetMemberQuota(caller *types.ProjectMember, targetRole string, isSelf bool) (ok bool, status int, code, msg string) {
	_ = isSelf // self-checks are intentionally absent; kept in signature so the
	// rule's domain is explicit at call sites and future tightening is local.

	if caller == nil {
		return true, 0, "", ""
	}
	switch caller.Role {
	case types.RoleOwner:
		return true, 0, "", ""
	case types.RoleMaintainer:
		if targetRole == types.RoleOwner {
			return false, http.StatusForbidden, "forbidden", "maintainers cannot set quota on an owner"
		}
		return true, 0, "", ""
	default:
		return false, http.StatusForbidden, "forbidden", "insufficient permissions"
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /root/coding/modelserver && go test ./internal/admin/ -run TestCanSetMemberQuota -v`

Expected: PASS — all 12 cases green.

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add internal/admin/quota_permissions.go internal/admin/quota_permissions_test.go
git commit -m "feat(admin): add canSetMemberQuota helper covering the simplified rule"
```

---

## Task 2: Wire helper into `handleUpdateMember`, remove old branches

**Files:**
- Modify: `internal/admin/handle_projects.go` (`handleUpdateMember`, ≈ L483–585)

We replace three branches (self block, owner-target block, maintainer-on-maintainer block) with a single call to `canSetMemberQuota`. The call has to happen *after* `targetMember` is loaded (we need its role).

- [ ] **Step 1: Read the current handler to confirm line locations**

Run: `cd /root/coding/modelserver && sed -n '529,562p' internal/admin/handle_projects.go`

Expected output should include the four lines we're about to remove/change:
- L532–536: `// Cannot set quota on yourself.` + 3-line block
- L545–549: `// Cannot set quota on an owner.` + 3-line block
- L551–561: `// Maintainers cannot set quota on other maintainers.` + comment + 5-line block

- [ ] **Step 2: Edit the handler**

Replace the entire range from L532 through L561 (inclusive) with a single permission check. Use this exact old/new for the Edit tool:

`old_string`:
```go
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
```

`new_string`:
```go
		// Load target member to check their role.
		targetMember, err := st.GetProjectMember(projectID, userID)
		if err != nil || targetMember == nil {
			writeError(w, http.StatusNotFound, "not_found", "member not found")
			return
		}

		// Per the simplified rule (see spec
		// 2026-06-15-self-quota-permissions-design.md): the only remaining
		// restriction on quota changes is that maintainers may not set quota
		// on owners. Self-quota and same-level quota are both allowed.
		// denied_models is intentionally NOT subject to this check.
		if body.CreditQuotaPct != nil || body.ClearQuota {
			if ok, status, code, msg := canSetMemberQuota(callerMember, targetMember.Role, userID == caller.ID); !ok {
				writeError(w, status, code, msg)
				return
			}
		}
```

- [ ] **Step 3: Build to confirm compiles and no unused imports**

Run: `cd /root/coding/modelserver && go build ./internal/admin/...`

Expected: exit 0, no output. If the build complains that `types` or other imports are unused, drop them. (None should become unused — `types.RoleOwner`/`types.RoleMaintainer` are still referenced via `requireRole` and via the helper file.)

- [ ] **Step 4: Run existing admin tests to confirm no regression**

Run: `cd /root/coding/modelserver && go test ./internal/admin/...`

Expected: PASS (including pre-existing tests for denylist, extra-usage bypass, etc.).

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add internal/admin/handle_projects.go
git commit -m "feat(admin): allow self-quota & same-level quota in update member handler"
```

---

## Task 3: Wire helper into `handleAddMember`

**Files:**
- Modify: `internal/admin/handle_projects.go` (`handleAddMember`, ≈ L425–481)

The add path currently rejects any quota whenever the new role is `owner` (L459–462), regardless of caller. New rule: owners may add a new owner with a quota; maintainers may not. (Maintainers also can't create owners at all in practice — `handleUpdateMember` does the promotion gate elsewhere — but we keep the defensive check.)

- [ ] **Step 1: Read the current branch**

Run: `cd /root/coding/modelserver && sed -n '450,465p' internal/admin/handle_projects.go`

Expected:
```go
		userID := user.ID

		// Validate quota if provided.
		if body.CreditQuotaPct != nil {
			if *body.CreditQuotaPct < 0 || *body.CreditQuotaPct > 100 {
				writeError(w, http.StatusBadRequest, "bad_request", "credit_quota_percent must be between 0 and 100")
				return
			}
			if body.Role == types.RoleOwner {
				writeError(w, http.StatusBadRequest, "bad_request", "cannot set quota on an owner")
				return
			}
		}
```

- [ ] **Step 2: Apply edit**

`old_string`:
```go
		// Validate quota if provided.
		if body.CreditQuotaPct != nil {
			if *body.CreditQuotaPct < 0 || *body.CreditQuotaPct > 100 {
				writeError(w, http.StatusBadRequest, "bad_request", "credit_quota_percent must be between 0 and 100")
				return
			}
			if body.Role == types.RoleOwner {
				writeError(w, http.StatusBadRequest, "bad_request", "cannot set quota on an owner")
				return
			}
		}
```

`new_string`:
```go
		// Validate quota if provided.
		if body.CreditQuotaPct != nil {
			if *body.CreditQuotaPct < 0 || *body.CreditQuotaPct > 100 {
				writeError(w, http.StatusBadRequest, "bad_request", "credit_quota_percent must be between 0 and 100")
				return
			}
			// Defer to the central permission helper. The new member doesn't
			// exist yet, so we pass the requested role as targetRole. isSelf is
			// always false on add: a user cannot add themselves to a project
			// via this endpoint (caller must already be owner/maintainer).
			callerMember := MemberFromContext(r.Context())
			if ok, status, code, msg := canSetMemberQuota(callerMember, body.Role, false); !ok {
				writeError(w, status, code, msg)
				return
			}
		}
```

- [ ] **Step 3: Build and test**

Run: `cd /root/coding/modelserver && go build ./internal/admin/... && go test ./internal/admin/...`

Expected: exit 0, all tests pass.

- [ ] **Step 4: Commit**

```bash
cd /root/coding/modelserver
git add internal/admin/handle_projects.go
git commit -m "feat(admin): allow owners to add new owners with a quota"
```

---

## Task 4: Frontend — quota column editability

**Files:**
- Modify: `dashboard/src/pages/members/MembersPage.tsx` (≈ L160–182)

The current `editable` predicate hides the "Set Quota" button on the caller's own row and on any owner row. New rule mirrors backend: hide only when the caller is a maintainer and the target is an owner.

- [ ] **Step 1: Apply edit**

`old_string`:
```ts
        const editable =
          canManageQuota &&
          m.role !== "owner" &&
          m.user_id !== currentUser?.id;
```

`new_string`:
```ts
        // Mirror backend rule (spec 2026-06-15-self-quota-permissions-design):
        // any owner/maintainer may set quota on anyone, except maintainers
        // cannot set quota on owners.
        const editable =
          canManageQuota &&
          !(currentRole === "maintainer" && m.role === "owner");
```

- [ ] **Step 2: TypeScript build**

Run: `cd /root/coding/modelserver/dashboard && pnpm exec tsc -b`

Expected: exit 0, no errors. (We're not running `vite build` yet — full build comes after all dashboard edits in Task 6.)

- [ ] **Step 3: Commit**

```bash
cd /root/coding/modelserver
git add dashboard/src/pages/members/MembersPage.tsx
git commit -m "feat(dashboard): allow quota edits on self and owner rows"
```

---

## Task 5: Frontend — dropdown menu "Set Quota" item

**Files:**
- Modify: `dashboard/src/pages/members/MembersPage.tsx` (≈ L274–280)

Same rule for the row's overflow dropdown item.

- [ ] **Step 1: Apply edit**

`old_string`:
```tsx
            {canManageQuota &&
              m.role !== "owner" &&
              m.user_id !== currentUser?.id && (
                <DropdownMenuItem onClick={() => openQuotaDialog(m)}>
                  Set Quota
                </DropdownMenuItem>
              )}
```

`new_string`:
```tsx
            {canManageQuota &&
              !(currentRole === "maintainer" && m.role === "owner") && (
                <DropdownMenuItem onClick={() => openQuotaDialog(m)}>
                  Set Quota
                </DropdownMenuItem>
              )}
```

- [ ] **Step 2: TypeScript build**

Run: `cd /root/coding/modelserver/dashboard && pnpm exec tsc -b`

Expected: exit 0.

- [ ] **Step 3: Commit**

```bash
cd /root/coding/modelserver
git add dashboard/src/pages/members/MembersPage.tsx
git commit -m "feat(dashboard): show Set Quota dropdown on self and owner rows"
```

---

## Task 6: Frontend — "Add Member" dialog quota input + submit handler

**Files:**
- Modify: `dashboard/src/pages/members/MembersPage.tsx` (`handleAdd` ≈ L94–107 and the dialog body ≈ L376)

Two coupled edits:

1. The quota input currently renders only when `role !== "owner"`. New rule: hide only when the caller is a maintainer and the chosen role is `owner` (the case the backend would reject).
2. `handleAdd` currently silently drops the quota when `role === "owner"`. Update to mirror the new visibility rule.

- [ ] **Step 1: Update `handleAdd`**

`old_string`:
```ts
  async function handleAdd() {
    const params: { email: string; role: string; credit_quota_percent?: number } = { email, role };
    if (addQuota !== "" && role !== "owner") {
      const parsed = parseFloat(addQuota);
      if (!isNaN(parsed) && parsed >= 0 && parsed <= 100) {
        params.credit_quota_percent = parsed;
      }
    }
    await addMember.mutateAsync(params);
```

`new_string`:
```ts
  // Mirror backend rule: a maintainer cannot create an owner with a quota.
  // Owners may now set a quota on any role at create time, including owner.
  const canSetQuotaForRole = (r: string) =>
    !(currentRole === "maintainer" && r === "owner");

  async function handleAdd() {
    const params: { email: string; role: string; credit_quota_percent?: number } = { email, role };
    if (addQuota !== "" && canSetQuotaForRole(role)) {
      const parsed = parseFloat(addQuota);
      if (!isNaN(parsed) && parsed >= 0 && parsed <= 100) {
        params.credit_quota_percent = parsed;
      }
    }
    await addMember.mutateAsync(params);
```

- [ ] **Step 2: Update the dialog input visibility**

`old_string`:
```tsx
            {role !== "owner" && (
              <div className="space-y-2">
                <Label htmlFor="add-quota">Credit Quota % (optional)</Label>
                <Input
                  id="add-quota"
                  type="number"
                  min={0}
                  max={100}
                  value={addQuota}
                  onChange={(e) => setAddQuota(e.target.value)}
                  placeholder="Leave empty for 100%"
                />
              </div>
            )}
```

`new_string`:
```tsx
            {canSetQuotaForRole(role) && (
              <div className="space-y-2">
                <Label htmlFor="add-quota">Credit Quota % (optional)</Label>
                <Input
                  id="add-quota"
                  type="number"
                  min={0}
                  max={100}
                  value={addQuota}
                  onChange={(e) => setAddQuota(e.target.value)}
                  placeholder="Leave empty for 100%"
                />
              </div>
            )}
```

- [ ] **Step 3: Full dashboard build**

Run: `cd /root/coding/modelserver/dashboard && pnpm exec tsc -b && pnpm exec vite build`

Expected: exit 0, build artifacts under `dist/`. If `vite build` complains about anything unrelated to this change, surface it — do not silence.

- [ ] **Step 4: Commit**

```bash
cd /root/coding/modelserver
git add dashboard/src/pages/members/MembersPage.tsx
git commit -m "feat(dashboard): owners can set quota when adding a new owner"
```

---

## Task 7: Manual verification

No automated frontend tests in this repo. Smoke-test the UI behaviours that the unit tests can't cover.

- [ ] **Step 1: Start the stack** (project's standard command — typically `docker compose up` from repo root; check `docker-compose.yml` for current services).

- [ ] **Step 2: Log in as a project owner**

  - Members page → own row shows a clickable "%" with pencil icon → click → save 50% → reloads showing 50%.
  - Another owner row (if any) shows the clickable "%" → can change quota.
  - Add Member dialog with role=`owner` now exposes the "Credit Quota %" input.

- [ ] **Step 3: Log in as a project maintainer**

  - Own row shows a clickable "%" → can change quota.
  - Another maintainer's row shows a clickable "%" → can change quota.
  - Any owner row shows plain text "%" (no button).
  - Add Member dialog with role=`owner` does NOT show the quota input (the maintainer can't create an owner anyway, but the field is correctly hidden).

- [ ] **Step 4: Log in as a developer**

  - Quota column shows plain text for all rows. No "Set Quota" button or dropdown item.

- [ ] **Step 5: Confirm backend rejects the one remaining forbidden case**

  As a maintainer, attempt the API directly:

  ```bash
  curl -X PUT \
    -H "Authorization: Bearer <maintainer-jwt>" \
    -H "Content-Type: application/json" \
    -d '{"credit_quota_percent": 50}' \
    http://localhost:8080/admin/projects/<projectID>/members/<owner-user-id>
  ```

  Expected: HTTP 403, body includes `"maintainers cannot set quota on an owner"`.

- [ ] **Step 6: If everything looks correct, no commit needed.** If a defect surfaces, file a follow-up and stop here — do not paper over with extra patches in this plan.

---

## Self-review (already performed during plan write)

- **Spec coverage:** All four bullets in the spec's "Backend Changes" and both bullets in "Frontend Changes" map to Tasks 1–6. Test list in spec maps to Task 1's matrix (rules 1–6) plus Tasks 2–3's build+regression run (rules 7–8 — `handleAddMember`'s "owner adds owner with quota" path is implicitly proved by the helper accepting it, and we ship it as part of the existing handler integration; explicit handler test is omitted because it would require a live DB and the existing repo skips DB-backed admin tests).
- **Placeholder scan:** No TBD/TODO/"add validation"/"similar to". Every code change is shown verbatim.
- **Type consistency:** Helper name `canSetMemberQuota` is used identically in Tasks 1, 2, 3. Frontend predicate `canSetQuotaForRole` defined once in Task 6 — used twice in the same task. `currentRole` is the same variable already declared at L91. `MemberFromContext` is referenced in Task 3 step 2 — it already exists at `internal/admin/admin.go:32`.
- **No stale references:** Task 2's old-string removes both the self block and the maintainer-on-maintainer block including the obsolete comment about denylist, satisfying the spec amendment.
