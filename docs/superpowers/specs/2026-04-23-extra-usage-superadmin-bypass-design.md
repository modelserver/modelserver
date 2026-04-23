# Extra-Usage Superadmin Bypass — Design

## Problem

The current extra-usage guard (`internal/proxy/extra_usage_guard_middleware.go`)
rejects a request when any of the following hold for the project:

1. global `cfg.Enabled=false`
2. `settings==nil` or `settings.Enabled=false`
3. `settings.BalanceFen <= 0`
4. monthly spend has hit `settings.MonthlyLimitFen`

Operationally we sometimes need to keep a specific project served even though
it has run out of credit — e.g. a paying customer whose balance has been
consumed during the night, a goodwill overdraft, or an internal ops project
that should never be blocked. Today the only lever is `/admin/extra-usage/projects/{id}/topup`,
which mutates money. We want a non-monetary opt-out that a superadmin can
toggle per project.

## Solution overview

Add a per-project `bypass_balance_check` flag. When set, the guard:

- treats the project as if `enabled=true` (skips the enabled check)
- skips the `balance_fen <= 0` check
- **still** enforces `monthly_limit_fen` (0 means unlimited)

`settleExtraUsage` continues to debit normally; `balance_fen` is allowed to go
negative under bypass so the audit trail retains the true economic cost. The
existing `CHECK (balance_fen >= 0)` constraint is dropped.

The flag is set only by superadmins, via a new endpoint on the existing
`/admin/extra-usage` route group. A new `dashboard/src/pages/admin/ExtraUsagePage.tsx`
page lists projects and exposes the toggle.

## Design decisions (resolved from brainstorming)

| Question | Decision |
|---|---|
| Bypass semantics | Deduct normally; allow overdraft (negative balance). |
| Overdraft cap | Reuse `monthly_limit_fen` — bypass still enforces the monthly budget. |
| `enabled` required alongside bypass? | No — bypass implies enabled at the guard level. |
| Behaviour when `monthly_limit_fen = 0` under bypass | Treated as "unlimited" (consistent with standard semantics). |
| Management entry point | Extend `/admin/extra-usage/overview` + new toggle endpoint under the same group. |
| Dashboard | New superadmin-only Extra Usage admin page. |

## Schema change

New migration `internal/store/migrations/022_extra_usage_bypass.sql`:

```sql
ALTER TABLE extra_usage_settings
    ADD COLUMN IF NOT EXISTS bypass_balance_check BOOLEAN NOT NULL DEFAULT FALSE;

-- Allow negative balance under bypass. The CHECK was auto-named by Postgres
-- during CREATE TABLE; DROP ... IF EXISTS tolerates slight name variations
-- across environments.
ALTER TABLE extra_usage_settings
    DROP CONSTRAINT IF EXISTS extra_usage_settings_balance_fen_check;

ALTER TABLE extra_usage_transactions
    DROP CONSTRAINT IF EXISTS extra_usage_transactions_balance_after_fen_check;
```

The ledger's `balance_after_fen` check also has to be relaxed because a
deduction under bypass produces a negative snapshot row.

**Constraint-name risk.** The inline CHECK on `balance_fen >= 0` was
auto-named by Postgres during `CREATE TABLE` in migration 017. Default
naming produces `extra_usage_settings_balance_fen_check`, but if an
environment manually renamed the constraint the `DROP ... IF EXISTS`
above would silently no-op and the migration would appear to succeed
while negative inserts continue to fail. To catch this, the migration
MUST include a post-migration assertion — a `DO $$ ... $$` block that
attempts `UPDATE extra_usage_settings SET balance_fen = -1 WHERE FALSE`
inside a planner-only path, or more pragmatically an integration test
(see Testing §3) that inserts a negative balance on a test DB after
applying the migration.

Rollback: re-adding the CHECK constraints would fail if bypass was exercised,
so rollback requires either zero'ing out negative balances via admin adjust
or keeping the migration forward-only. We accept that; bypass is an opt-in
operational tool.

## Type change

`internal/types/extra_usage.go` — `ExtraUsageSettings` adds:

```go
BypassBalanceCheck bool `json:"bypass_balance_check"`
```

## Store changes

In `internal/store/extra_usage.go`:

1. `GetExtraUsageSettings` — extend `SELECT` + `Scan` to include the new column.
2. `UpsertExtraUsageSettings` — project-owner-facing upsert. Bypass is NOT
   writable here; the column is preserved across upserts by NOT including
   it in the `INSERT` list or the `ON CONFLICT DO UPDATE SET` list
   (equivalent to today's handling of `balance_fen`). **However**, its
   `RETURNING` clause MUST be extended to include `bypass_balance_check` so
   the new struct field is populated.
3. `TopUpExtraUsage` — same treatment: don't write bypass, but extend the
   `RETURNING` clause (currently `RETURNING balance_fen` — leave unchanged
   since only the balance is read back). Verified: no Scan of bypass needed
   in this method.
4. `ListExtraUsageSettings` — extend `SELECT` and `Scan`.
5. New method:

   ```go
   func (s *Store) SetExtraUsageBypass(projectID string, bypass bool) (*types.ExtraUsageSettings, error)
   ```

   - `INSERT INTO extra_usage_settings (project_id, bypass_balance_check) VALUES ($1, $2) ON CONFLICT (project_id) DO UPDATE SET bypass_balance_check = EXCLUDED.bypass_balance_check, updated_at = NOW() RETURNING <all columns incl. bypass>`
   - Creates the row if it doesn't exist yet, so bypass can be flipped even
     on a project that never topped up.
   - Returns the full struct so callers can echo the new state.
6. `DeductExtraUsage` — amend the conditional UPDATE:

   ```sql
   WHERE s.project_id = $1
     AND (s.bypass_balance_check = TRUE OR s.enabled = TRUE)
     AND (s.bypass_balance_check = TRUE OR s.balance_fen >= $2)
     AND (s.monthly_limit_fen = 0 OR month_spend.spent + $2 <= s.monthly_limit_fen)
   ```

   Monthly limit remains unconditional (per design decision above).
7. `classifyDeductFailure` — read `bypass_balance_check` alongside `enabled`;
   when bypass is on, the only reason the UPDATE fails is the monthly limit,
   so return `ErrMonthlyLimitReached`. If neither the enabled/balance nor
   the monthly check explains it (shouldn't happen under bypass), fall back
   to `ErrInsufficientBalance` as before to avoid leaking a confusing
   low-level error.

## Proxy / guard

`internal/proxy/extra_usage_guard_middleware.go`:

```go
settings, err := st.GetExtraUsageSettings(project.ID)
// ... existing error / nil handling

bypass := settings != nil && settings.BypassBalanceCheck

if !bypass {
    if settings == nil || !settings.Enabled { /* reject "not_enabled" */ }
    if settings.BalanceFen <= 0 { /* reject "balance_depleted" */ }
}

// Monthly limit check always runs (even for bypass). For bypass projects
// with monthly_limit_fen == 0, this is a no-op.
if settings != nil && settings.MonthlyLimitFen > 0 {
    /* unchanged */
}

// Attach context, but tag the result as "allowed_bypass" so we can count.
result := "allowed"
if bypass {
    result = "allowed_bypass"
}
recordExtraUsageResult(intent.Reason, result)
```

`ExtraUsageContext` gains nothing new — the settle path does not need to know
whether bypass was used, because DeductExtraUsage encapsulates it.

## Settle / executor

`settleExtraUsage` (`internal/proxy/executor.go`) is structurally unchanged.
The only observable difference: `DeductExtraUsage` may now return a negative
`newBalance`. Add one metric bucket:

```go
case err == nil:
    rc.ExtraUsageBalanceAfterFen = newBal
    outcome := "ok"
    if newBal < 0 {
        outcome = "overdraft"
    }
    metrics.IncExtraUsageDeduction(outcome)
    metrics.SetExtraUsageBalance(rc.ProjectID, newBal)
```

`metrics.SetExtraUsageBalance` already accepts any int64, but if the gauge
collector rejects negatives we wrap it. (Checked: current implementation
passes through.)

## Admin API

In `internal/admin/handle_extra_usage.go`:

1. `handleAdminExtraUsageOverview` — `adminExtraUsageOverviewRow` already
   embeds `types.ExtraUsageSettings`, which now includes `BypassBalanceCheck`.
   Nothing else to do.
2. New handler:

   ```go
   func handleAdminExtraUsageSetBypass(st *store.Store) http.HandlerFunc
   ```

   - Body: `{"bypass": *bool}` (pointer to distinguish omission). Reject
     with 400 if nil.
   - Extract actor via `admin.UserFromContext(r.Context())` (the request is
     already past `RequireSuperadmin`, so this is non-nil).
   - Call `st.SetExtraUsageBypass(projectID, *body.Bypass)`.
   - Emit audit log:

     ```go
     slog.Default().Info("extra_usage_bypass_toggled",
         "project_id", projectID,
         "bypass", *body.Bypass,
         "actor_user_id", actor.ID)
     ```

     This matches the logging pattern already in `handle_claudecode_oauth.go`
     and `handle_models.go` — we intentionally do NOT add a `*slog.Logger`
     parameter to `MountRoutes`.
   - Return updated settings on 200.

3. Route in `internal/admin/routes.go`:

   ```go
   r.Route("/admin/extra-usage", func(r chi.Router) {
       r.Use(RequireSuperadmin)
       r.Get("/overview", handleAdminExtraUsageOverview(st))
       r.Post("/projects/{projectID}/topup", handleAdminExtraUsageDirectTopup(st))
       r.Put("/projects/{projectID}/bypass", handleAdminExtraUsageSetBypass(st))
   })
   ```

4. Visibility to project owners. `extraUsageGetResponse` (the project-scoped
   `GET /projects/{id}/extra-usage` DTO) explicitly whitelists fields, so
   without further action bypass would appear via `handleUpdateExtraUsage`
   (which returns `types.ExtraUsageSettings` directly) but NOT via
   `handleGetExtraUsage`. To keep the two consistent, add
   `BypassBalanceCheck bool json:"bypass_balance_check"` to
   `extraUsageGetResponse` and copy it from `settings` when present.
   Decision rationale: project owners can already observe that their
   requests are being served under bypass (cost shows up in the ledger even
   after `enabled=false`), so exposing the flag avoids confusion without
   granting new capability.

## Metrics

- Add `"allowed_bypass"` as a value in the existing `IncExtraUsageRequest`
  result label (the counter already takes free-form strings).
- Add `"overdraft"` as a value in `IncExtraUsageDeduction`.
- No new series; both are new labels on existing counters.

## Dashboard

New file: `dashboard/src/pages/admin/ExtraUsagePage.tsx`.

- Query: `GET /api/v1/admin/extra-usage/overview` via a new `useAdminExtraUsageOverview` hook (add to `dashboard/src/api/extra-usage.ts`).
- Columns: project ID, balance_fen, monthly_limit_fen, monthly spent, 7d spent, enabled, bypass toggle, updated_at.
- Toggle handler calls `PUT /api/v1/admin/extra-usage/projects/{projectID}/bypass` (new mutation `useSetExtraUsageBypass`).
- **Project-ID lookup.** `ListExtraUsageSettings` only returns rows that
  already exist in `extra_usage_settings`. To flip bypass on a project that
  has never topped up (no row), the page MUST include a "Set bypass by
  project ID" form: input for UUID + Enable/Disable buttons that call the
  same PUT endpoint. After the call, the project appears in the table
  because `SetExtraUsageBypass` upserts a row.
- `App.tsx` — add route `/admin/extra-usage` guarded on `user.is_superadmin`.
- `Sidebar.tsx` — add link under the superadmin section.

The page is functional, not pretty — a simple table with inline switches,
matching `AdminProjectsPage`/`AdminUsersPage` style.

## Testing

1. `internal/proxy/extra_usage_guard_middleware_test.go`:
   - bypass + enabled=false + balance=0 → allowed (`allowed_bypass`)
   - bypass + monthly_limit reached → rejected (monthly limit still wins)
   - bypass=false + enabled=true + balance=0 → rejected (regression baseline)

2. `internal/store` extra-usage tests (extend existing `extra_usage_test.go`
   if present, or add one):
   - `DeductExtraUsage` with bypass=true and balance=0 → newBalance becomes
     negative, ledger row inserted, `balance_after_fen` negative.
   - `DeductExtraUsage` with bypass=true but monthly limit hit → returns
     `ErrMonthlyLimitReached`.
   - `SetExtraUsageBypass` on a project with no existing row → row created
     with bypass=true, enabled=false, balance_fen=0.
   - `SetExtraUsageBypass(..., false)` after a prior true → row preserved,
     only bypass flips.
   - **Constraint-drop smoke test**: directly execute
     `INSERT INTO extra_usage_settings (project_id, balance_fen) VALUES (gen_random_uuid(), -1)`
     and
     `INSERT INTO extra_usage_transactions (project_id, type, amount_fen, balance_after_fen) VALUES (..., 'deduction', -100, -50)`
     — both must succeed after migration 018. This is the only way to
     guarantee the `DROP CONSTRAINT IF EXISTS` actually dropped the right
     constraint.

3. `internal/admin` handler tests (new file or extension of existing):
   - Non-superadmin → 403 on PUT bypass.
   - Body missing `bypass` field → 400.
   - PUT bypass=true then GET overview shows bypass=true.
   - PUT bypass on a project without a settings row creates the row.

4. Dashboard: no automated tests in this repo for new pages; manual smoke test
   is acceptable given existing conventions.

## Semantics worked examples

Bypass projects accrue a negative `balance_fen` that persists across months
— the monthly limit caps spend-per-month but does not clear the debt.

- Project A: bypass=true, balance_fen=-5000 (¥-50, from last month),
  monthly_limit_fen=30000 (¥300), month_spend=0.
  First request this month for ¥10 → allowed; new balance_fen=-6000,
  month_spend=1000. `monthly_limit` still has ¥290 headroom.

- Project B: bypass=true, balance_fen=50 (¥0.50),
  monthly_limit_fen=30000, month_spend=30000.
  Request for ¥0.10 → rejected (`monthly_limit`), even though balance is
  positive.

- Project C: bypass=true, monthly_limit_fen=0 (no cap).
  Any request → allowed; balance drifts negative unbounded. Operators
  intending bypass-as-freebie MUST set a monthly_limit or accept this.

## Non-goals

- Time-bounded bypass with auto-expiry.
- Per-user or per-request override (e.g., a magic header).
- A ledger entry for the toggle event (use application log instead).
- Client-visible indication that the project is in bypass (the success headers
  `X-Extra-Usage-*` continue to reflect the settled cost).

## Risks

- **Forgotten bypass = silent losses.** The overview page surfaces bypass
  status prominently and the prometheus counters let us alert on sustained
  `allowed_bypass` traffic. A follow-up could add an email/Slack nudge when a
  project has been in bypass for > N days; out of scope here.
- **Schema rollback.** Dropping the CHECK constraints is forward-only if any
  negative balance is ever produced. Accepted.
- **Monthly-limit confusion.** Users may expect bypass to override the monthly
  limit too. Documented clearly in the admin UI tooltip and in this spec.
