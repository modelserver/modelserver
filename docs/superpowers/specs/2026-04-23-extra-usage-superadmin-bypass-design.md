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

New migration `internal/store/migrations/018_extra_usage_bypass.sql`:

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
2. `UpsertExtraUsageSettings` — unchanged. Project owners cannot set bypass
   themselves; the column is preserved across upserts by NOT including it in
   the INSERT/ON CONFLICT list (equivalent to today's handling of
   `balance_fen`).
3. New method:

   ```go
   func (s *Store) SetExtraUsageBypass(projectID string, bypass bool) (*types.ExtraUsageSettings, error)
   ```

   - `INSERT ... ON CONFLICT (project_id) DO UPDATE SET bypass_balance_check = EXCLUDED.bypass_balance_check, updated_at = NOW() RETURNING ...`
   - INSERT default fields `(project_id, bypass_balance_check)` with zeros for
     everything else — creates the row if it doesn't exist yet, so bypass can
     be flipped even on a project that never topped up.
4. `DeductExtraUsage` — amend the conditional UPDATE:

   ```sql
   WHERE s.project_id = $1
     AND (s.bypass_balance_check = TRUE OR s.enabled = TRUE)
     AND (s.bypass_balance_check = TRUE OR s.balance_fen >= $2)
     AND (s.monthly_limit_fen = 0 OR month_spend.spent + $2 <= s.monthly_limit_fen)
   ```

   Monthly limit remains unconditional (per design decision above).
5. `classifyDeductFailure` — read `bypass_balance_check` alongside `enabled`;
   when bypass is on, the only reason the UPDATE fails is the monthly limit,
   so return `ErrMonthlyLimitReached`.

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
   func handleAdminExtraUsageSetBypass(st *store.Store, logger *slog.Logger) http.HandlerFunc
   ```

   - Body: `{"bypass": true|false}` (pointer to distinguish omission).
   - Validate `projectID` exists (reuse existing pattern).
   - Call `st.SetExtraUsageBypass(projectID, body.Bypass)`.
   - `logger.Info("extra_usage_bypass_toggled", "project_id", projectID, "bypass", body.Bypass, "actor_user_id", actor)` for audit.
   - Return updated settings.

3. Route in `internal/admin/routes.go`:

   ```go
   r.Route("/admin/extra-usage", func(r chi.Router) {
       r.Use(RequireSuperadmin)
       r.Get("/overview", handleAdminExtraUsageOverview(st))
       r.Post("/projects/{projectID}/topup", handleAdminExtraUsageDirectTopup(st))
       r.Put("/projects/{projectID}/bypass", handleAdminExtraUsageSetBypass(st, logger))
   })
   ```

   `logger` needs to be passed through `MountRoutes` if not already; verify
   during implementation.

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
- `App.tsx` — add route `/admin/extra-usage` guarded on `user.is_superadmin`.
- `Sidebar.tsx` — add link under the superadmin section.

The page is functional, not pretty — a simple table with inline switches,
matching `AdminProjectsPage`/`AdminUsersPage` style.

## Testing

1. `internal/proxy/extra_usage_guard_middleware_test.go`:
   - bypass + enabled=false + balance=0 → allowed (`allowed_bypass`)
   - bypass + monthly_limit reached → rejected (monthly limit still wins)
   - bypass=false + enabled=true + balance=0 → rejected (regression baseline)

2. `internal/store` extra-usage tests (if present — will add one file
   `extra_usage_test.go` if not):
   - `DeductExtraUsage` with bypass=true and balance=0 → newBalance becomes
     negative, ledger row inserted, `balance_after_fen` negative.
   - `DeductExtraUsage` with bypass=true but monthly limit hit → returns
     `ErrMonthlyLimitReached`.

3. `internal/admin` handler tests (new file or extension of existing):
   - Non-superadmin → 403 on PUT bypass.
   - PUT bypass=true then GET overview shows bypass=true.
   - PUT bypass on a project without a settings row creates the row.

4. Dashboard: no automated tests in this repo for new pages; manual smoke test
   is acceptable given existing conventions.

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
