# Per-Member Model Denylist

Date: 2026-06-04
Status: Approved (design)

## Problem

Today, "which models a caller may use" is controlled at exactly one place:
`api_keys.allowed_models` (a per-API-key allowlist). To restrict a specific
member of a project, an admin must either issue every API key for that
member personally and remember to set `allowed_models` on each one, or trust
the member not to issue an unrestricted key. There is no project-level
mechanism that follows the *member*, regardless of which key they use.

This spec adds a per-member denylist that the proxy enforces on every
request, in addition to the existing key-level allowlist.

## Goals

- Owners and maintainers can mark specific models as forbidden for a
  specific member of a project.
- The denylist binds to the member, not to any one API key — issuing a new
  key (with or without `allowed_models`) cannot circumvent it.
- The denylist applies uniformly to all roles (including owner) — see
  *Non-goals* for the rationale.
- Zero new round-trips on the proxy hot path.

## Non-goals (explicitly out of scope)

- Wildcards / model families (e.g. `claude-opus-*`). The column is plain
  strings; expanding to glob/family semantics is a future change with its
  own design.
- Plan-tier-gated model availability (`plans.available_models`).
- Bulk endpoints for setting denylists across multiple members at once.
- Validating denylist entries against the catalog at write time. The
  catalog is a read-only snapshot; storing names not currently in the
  catalog is a no-op and acceptable.
- Role-based exemptions. The user explicitly chose "all roles constrained,
  only owner/maintainer can configure" — there is no escape hatch for
  owners who deny-list themselves out of a model. The recovery path is the
  superadmin tier or a direct DB update.
- Cross-validation between an API key's `allowed_models` and the member's
  `denied_models`. A configuration where `allowed_models` ⊆ `denied_models`
  is legal (every request 403s); the operator gets a clear error.

## Naming

The project uses **member** for "user attached to a project"
(`project_members` table) and **user** for the system user record
(`users`). All public-facing names in this design use *member*.

## Data model

New migration `internal/store/migrations/043_project_member_denied_models.sql`:

```sql
ALTER TABLE project_members
  ADD COLUMN denied_models TEXT[] NOT NULL DEFAULT '{}';
```

- `NOT NULL DEFAULT '{}'` — empty array means "no model is denied". Avoids
  three-valued logic (NULL vs `{}`) at every read site.
- **Existing rows after migration are guaranteed empty.** PostgreSQL 11+
  applies `ADD COLUMN ... NOT NULL DEFAULT '{}'` as a "fast default":
  no table rewrite, every pre-existing row reads back as `{}`. No backfill
  script is needed. Confirmed minimum is PG 11; the project already
  targets a newer version (see other migrations using `gen_random_uuid()`,
  PG 13+).
- No index. Lookups are always by `(project_id, user_id)` (single-row read
  on an existing PK), never by model name.
- No FK to the catalog. See *Non-goals*.

`internal/types/user.go` — `ProjectMember` gains:

```go
DeniedModels []string `json:"denied_models"`
```

Serialized even when empty (no `omitempty`) so clients always see the
field and can reason about its presence.

## Proxy enforcement

Four code-level checks already gate on `apiKey.AllowedModels`:

| Handler                       | File                          | Line |
|-------------------------------|-------------------------------|------|
| `handleImagesEditsMultipart`  | `internal/proxy/handler.go`   | 154  |
| `HandleGemini`                | `internal/proxy/handler.go`   | 287  |
| `handleProxyRequest` (shared) | `internal/proxy/handler.go`   | 386  |
| `HandleCountTokens`           | `internal/proxy/handler.go`   | 503  |

`handleProxyRequest` is shared by `HandleMessages`, `HandleResponses`,
`HandleResponsesCompact`, `HandleChatCompletions`, and
`HandleImagesGenerations` — one check covers all of them.

There is also one **non-request** read site, `HandleListModels`
(`internal/proxy/router.go:80`), which uses `apiKey.AllowedModels` to
filter the `/v1/models` response. This must also subtract the member
denylist; otherwise a member sees model names that every actual request
will 403 on — confusing and inconsistent.

### Both authentication paths covered

The denylist applies to every authenticated request, regardless of
credential type:

- **API-key path** (`handleAPIKeyAuth`, `auth_middleware.go:257`) already
  reads the `ProjectMember` row via `apiKey.CreatedBy`. We piggyback on
  this read to get `DeniedModels`.
- **OAuth path** (`handleTokenIntrospectionAuth`,
  `auth_middleware.go:369`) already reads the `ProjectMember` row via the
  token's `user_id` claim. Same piggyback. The synthesized `APIKey`
  constructed at line 333 has an empty `AllowedModels`, so the existing
  allowlist check is a no-op on this path — but the denylist check in
  `checkModelAllowed` still fires from the context value.

### Loading the denylist

`internal/proxy/auth_middleware.go` already loads the
`ProjectMember` row to read `CreditQuotaPct` (line 257 for the API-key
path, line 369 for the OAuth token-introspection path). The same row read
yields `DeniedModels` for free.

- Add `deniedModelsCache` next to `quotaCache` (independent instance, same
  10s TTL, key = `projectID + ":" + userID`, value = `[]string`).
  `quotaCache` is `float64`-typed and cannot host this without refactoring;
  a separate cache is the cheaper option.
- Store the slice in request context under a new `ctxUserDeniedModels`
  key. Accessor `UserDeniedModelsFromContext(ctx) []string` returns nil on
  miss; callers use `len(...) > 0` to gate.
- Failure to load the member row continues to **fail open** for both
  quota and denylist, matching the existing comment "Fail open: proceed
  without quota enforcement." Transient DB issues must not lock everyone
  out of every model.

### Check order and error messages

Per the design discussion (Q3), the member denylist is checked **before**
the API key allowlist. Rationale: the denylist is a project-administrator
policy; the allowlist is a per-key customization. The harder constraint
applies first, and its error message names the right layer.

A shared helper in `internal/proxy/handler.go`:

```go
// checkModelAllowed enforces, in order:
//   1. the member-level denied_models (project policy)
//   2. the api_key.allowed_models (per-key customization)
// On rejection it writes a 403 via writeErr and returns false.
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

Each of the four call sites replaces its current 4-line `if` block with
a single call to `checkModelAllowed`, passing `writeGeminiError` for the
Gemini path and `writeProxyError` otherwise.

`HandleListModels` does not use `checkModelAllowed` (it's not enforcing
on a single request; it's filtering a list). Logic:

```go
names := h.router.ActiveModels()
if len(apiKey.AllowedModels) > 0 {
    names = apiKey.AllowedModels
}
if denied := UserDeniedModelsFromContext(r.Context()); len(denied) > 0 {
    names = subtract(names, denied)
}
```

`subtract` returns the names from the first slice that are not present in
the second.

`modelInList` is reused unchanged; matching is exact-string against the
canonical model name resolved by `h.resolveModel`.

## Admin API

Existing endpoint: `PATCH /admin/projects/{id}/members/{user_id}`
(`internal/admin/handle_projects.go` ≈ line 475).

### Request body

```go
type updateMemberBody struct {
    Role           *string   `json:"role"`
    CreditQuotaPct *float64  `json:"credit_quota_percent"`
    ClearQuota     bool      `json:"clear_quota"`
    DeniedModels   *[]string `json:"denied_models"`
}
```

Tri-state semantics matching the DB's `NOT NULL DEFAULT '{}'`:

| JSON value             | Behavior                          |
|------------------------|-----------------------------------|
| field omitted (`nil`)  | column unchanged                  |
| `"denied_models": []`  | clear (store `{}`)                |
| `"denied_models": [...]` | replace whole array             |

### Validation

1. **Role gate** is already enforced by the existing
   `requireRole(w, r, types.RoleOwner, types.RoleMaintainer)` call at
   `handle_projects.go:471`, which guards the whole PATCH endpoint. A
   developer never reaches the body parser, so no extra in-body role
   check is needed for `denied_models`.
2. **Non-empty body**: extend the existing "at least one of role,
   credit_quota_percent, or clear_quota must be provided" check to also
   accept `DeniedModels != nil`. New message includes `denied_models` in
   the list.
3. **Element validation**: each entry trimmed, non-empty, deduped. Hard
   cap of 256 entries per member; over → 400. No catalog membership check.
4. **Owner self-deny is allowed**: the user explicitly chose to make the
   runtime constraint uniform across roles. No special-case rejection of
   "owner adds themselves to their own denylist."
5. **No "maintainer can't touch another maintainer's denylist" rule.**
   Existing quota code (line 522) blocks maintainer→maintainer quota
   edits. Per the design discussion (Q1), `denied_models` is configurable
   by any owner or maintainer for *all members*; we deliberately do not
   port that quota-style restriction here. Worth a comment in the handler
   pointing this out, so a future reader does not "fix" what looks like an
   omission.

### Store layer

`store.UpdateProjectMember` signature today:

```go
UpdateProjectMember(projectID, userID string, role *string, creditQuotaPct **float64) error
```

Extend to:

```go
UpdateProjectMember(projectID, userID string, role *string, creditQuotaPct **float64, deniedModels *[]string) error
```

`nil` means "do not touch this column", reusing the existing dynamic SET
pattern. The two existing call sites
(`internal/admin/handle_projects.go:459` and `541`) pass `nil` explicitly.

### Response shape

`GET .../members` and the single-member detail response add
`denied_models` to their JSON. The compact list shape (`memberCompact`,
≈ line 383) and the detail response (≈ line 597) both gain the field.

### Cache invalidation

On a successful PATCH that touched `denied_models`, actively delete the
matching entry from `deniedModelsCache`. The existing `quotaCache` relies
on TTL alone; for denylist (a security-relevant policy) we prefer
immediate effect over the 10-second TTL delay. Pre-existing `quotaCache`
behavior is unchanged.

## Testing

Style follows existing `_test.go` files in `internal/store`,
`internal/admin`, and `internal/proxy`.

### Store layer (`internal/store/projects_test.go`)

- Migration up → existing rows have `DeniedModels == []string{}` (not nil)
  after `GetProjectMember` round-trip.
- `UpdateProjectMember` matrix: role-only / quota-only / denylist-only /
  all three / all nil (no-op, no UPDATE issued).
- `GetProjectMember` returns the persisted denylist.

### Admin layer (`internal/admin/handle_projects_test.go`)

- Developer PATCH with `denied_models` → 403.
- Maintainer PATCH with `denied_models` → 200; row updated.
- Owner sets their own denylist → 200 (allowed by design).
- Body with all fields nil/false → 400 (existing message).
- `denied_models` over 256 entries → 400.
- Duplicates deduped server-side.
- On success, `deniedModelsCache` entry is gone (verify via Peek or test
  hook).

### Proxy layer (`internal/proxy/handler_test.go`, alongside existing
`AllowedModels` cases)

- Member denylist contains model → 403 with
  `"model denied for this member by project policy"`.
- Member denylist set but model not in it → request proceeds.
- **Order test**: model is in member denylist AND not in key allowlist →
  returned error is the denylist message (proves Q3 ordering).
- `GetProjectMember` errors → fail open; request proceeds.
- One denylist-hit case per request kind that has its own code path:
  Anthropic messages (via shared `handleProxyRequest`), Gemini (its own
  branch), count tokens (its own branch), and images-edits multipart (its
  own branch). The other shared-handler kinds (responses, chat
  completions, images generations) get one smoke-test each to confirm the
  shared check fires.
- **OAuth-authenticated request** with the caller's member-row denylist
  set → 403, just like the API-key path. Confirms the synthetic-key
  branch in `auth_middleware.go` populates the context correctly.
- **`HandleListModels`** returns names with denylist subtracted:
  - no allowlist, denylist `["claude-opus-4-8"]` → opus name absent
  - allowlist `["a","b","c"]`, denylist `["b"]` → only `["a","c"]`
  - empty denylist → identical output to before

### Auth middleware

- Cache hit and miss paths both populate `ctxUserDeniedModels`.
- Cache isolation between users in the same project, and between projects
  for the same user.

## Observability

- On denylist rejection, `h.logger.Info("member denylist hit", ...)` with
  `project_id`, `user_id`, `model`. Only if existing
  `"model not allowed for this API key"` rejections already log at this
  level — match the local convention rather than introduce a new one.
- No new Prometheus metric yet. If usage suggests it, a follow-up can add
  `member_denylist_hits_total{project,model}`.

## Documentation

Update the admin API reference under "project members" with the
`denied_models` field, its tri-state semantics, the role gate, and the
order of evaluation against `api_keys.allowed_models`. If a public-facing
docs page covers per-key `allowed_models`, cross-link.

## Migration order with in-flight schema work

Migration number `043` follows the latest committed `042_add_opus_4_8.sql`.
If another `043_*` lands first, renumber.
