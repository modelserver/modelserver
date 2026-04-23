# Extra-Usage Superadmin Bypass Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a superadmin flip a per-project flag that keeps the project served by the extra-usage path even when its balance is depleted or `enabled=false`, while still respecting `monthly_limit_fen`.

**Architecture:** Add `bypass_balance_check` to `extra_usage_settings`. Guard middleware and `DeductExtraUsage` short-circuit the enabled + balance checks when the flag is true; the monthly-limit clause and settlement path are unchanged. A new `PUT /admin/extra-usage/projects/{id}/bypass` endpoint and a dashboard admin page expose the toggle. DB `CHECK (balance_fen >= 0)` and `CHECK (balance_after_fen >= 0)` are dropped so deductions may drive the balance negative under bypass.

**Tech Stack:** Go 1.x, pgx/v5, Postgres, chi router, React + react-query (dashboard), Tailwind.

**Spec:** `docs/superpowers/specs/2026-04-23-extra-usage-superadmin-bypass-design.md`

---

## File Structure

- **Create** `internal/store/migrations/022_extra_usage_bypass.sql` — schema change.
- **Modify** `internal/types/extra_usage.go` — add `BypassBalanceCheck` field.
- **Modify** `internal/store/extra_usage.go` — extend Get/List/Upsert RETURNING lists; update `DeductExtraUsage` WHERE clause; update `classifyDeductFailure`; new `SetExtraUsageBypass`.
- **Modify** `internal/proxy/extra_usage_guard_middleware.go` — introduce `extraUsageStore` interface for testability; bypass branch.
- **Modify** `internal/proxy/extra_usage_guard_middleware_test.go` — bypass unit tests using a fake store that implements the new interface.
- **Create** `internal/store/extra_usage_db_test.go` — integration tests gated on `TEST_DATABASE_URL`; covers constraint drop + `SetExtraUsageBypass` + `DeductExtraUsage` overdraft.
- **Modify** `internal/proxy/executor.go` — add `"overdraft"` outcome to settle metric.
- **Modify** `internal/admin/handle_extra_usage.go` — add `handleAdminExtraUsageSetBypass`; extend `extraUsageGetResponse` with `bypass_balance_check`.
- **Modify** `internal/admin/routes.go` — wire the PUT route.
- **Create** `internal/admin/handle_extra_usage_bypass_test.go` — handler tests.
- **Modify** `dashboard/src/api/extra-usage.ts` — `useAdminExtraUsageOverview`, `useSetExtraUsageBypass`, `bypass_balance_check` in `ExtraUsageSettingsResponse`.
- **Create** `dashboard/src/pages/admin/ExtraUsagePage.tsx` — overview table + by-project-ID form.
- **Modify** `dashboard/src/App.tsx` — route.
- **Modify** `dashboard/src/components/layout/Sidebar.tsx` — nav link.

---

## Task 1: Migration — add column, drop CHECK constraints

**Files:**
- Create: `internal/store/migrations/022_extra_usage_bypass.sql`

- [ ] **Step 1: Write the migration**

```sql
-- 022_extra_usage_bypass.sql
--
-- Adds a superadmin-managed bypass flag on extra_usage_settings. When true,
-- the guard middleware skips the enabled and balance checks; settlement
-- continues to deduct, which can drive the balance negative. The monthly
-- limit is still enforced. See
-- docs/superpowers/specs/2026-04-23-extra-usage-superadmin-bypass-design.md.

ALTER TABLE extra_usage_settings
    ADD COLUMN IF NOT EXISTS bypass_balance_check BOOLEAN NOT NULL DEFAULT FALSE;

-- Allow negative balances under bypass. The CHECK constraints were
-- auto-named by Postgres during CREATE TABLE in migration 017; the default
-- names follow the pattern <table>_<column>_check.
ALTER TABLE extra_usage_settings
    DROP CONSTRAINT IF EXISTS extra_usage_settings_balance_fen_check;

ALTER TABLE extra_usage_transactions
    DROP CONSTRAINT IF EXISTS extra_usage_transactions_balance_after_fen_check;
```

- [ ] **Step 2: Verify file sorts after the current tail**

Run: `ls internal/store/migrations/ | tail -3`

Expected: `021_route_request_kinds.sql` then `022_extra_usage_bypass.sql`.

- [ ] **Step 3: Run migrations embed test**

Run: `go test ./internal/store -run TestMigrationsEmbed -v`

Expected: PASS (checks sort order of embedded migration files).

- [ ] **Step 4: Commit**

```bash
git add internal/store/migrations/022_extra_usage_bypass.sql
git commit -m "feat(extra-usage): migration 022 — bypass column, relax balance CHECKs"
```

---

## Task 2: Add `BypassBalanceCheck` to the type

**Files:**
- Modify: `internal/types/extra_usage.go`

- [ ] **Step 1: Edit the struct**

In `internal/types/extra_usage.go`, locate the `ExtraUsageSettings` struct and add `BypassBalanceCheck` between `MonthlyLimitFen` and `CreatedAt`:

```go
type ExtraUsageSettings struct {
	ProjectID          string    `json:"project_id"`
	Enabled            bool      `json:"enabled"`
	BalanceFen         int64     `json:"balance_fen"`
	MonthlyLimitFen    int64     `json:"monthly_limit_fen"`
	BypassBalanceCheck bool      `json:"bypass_balance_check"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./...`

Expected: PASS. The store Scan calls don't yet read the new field — that's a runtime mismatch (fixed in Task 3), not a compile error.

- [ ] **Step 3: Commit**

```bash
git add internal/types/extra_usage.go
git commit -m "feat(extra-usage): add BypassBalanceCheck field"
```

---

## Task 3: Extend Get/List/Upsert; add `SetExtraUsageBypass`; update Deduct + classify

**Files:**
- Modify: `internal/store/extra_usage.go`

- [ ] **Step 1: Extend `GetExtraUsageSettings`**

Replace the SELECT + Scan in `GetExtraUsageSettings` (currently lines ~40–54):

```go
func (s *Store) GetExtraUsageSettings(projectID string) (*types.ExtraUsageSettings, error) {
	row := s.pool.QueryRow(context.Background(), `
		SELECT project_id, enabled, balance_fen, monthly_limit_fen,
		       bypass_balance_check, created_at, updated_at
		FROM extra_usage_settings WHERE project_id = $1`, projectID)
	out := &types.ExtraUsageSettings{}
	err := row.Scan(&out.ProjectID, &out.Enabled, &out.BalanceFen,
		&out.MonthlyLimitFen, &out.BypassBalanceCheck,
		&out.CreatedAt, &out.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get extra_usage_settings: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 2: Extend `UpsertExtraUsageSettings`**

Update the RETURNING clause and Scan targets (bypass is NOT in the INSERT/ON CONFLICT sets, so it is preserved on update and defaults to FALSE on insert):

```go
func (s *Store) UpsertExtraUsageSettings(projectID string, enabled bool, monthlyLimitFen int64) (*types.ExtraUsageSettings, error) {
	row := s.pool.QueryRow(context.Background(), `
		INSERT INTO extra_usage_settings (project_id, enabled, monthly_limit_fen)
		VALUES ($1, $2, $3)
		ON CONFLICT (project_id) DO UPDATE
			SET enabled           = EXCLUDED.enabled,
			    monthly_limit_fen = EXCLUDED.monthly_limit_fen,
			    updated_at        = NOW()
		RETURNING project_id, enabled, balance_fen, monthly_limit_fen,
		          bypass_balance_check, created_at, updated_at`,
		projectID, enabled, monthlyLimitFen)
	out := &types.ExtraUsageSettings{}
	err := row.Scan(&out.ProjectID, &out.Enabled, &out.BalanceFen,
		&out.MonthlyLimitFen, &out.BypassBalanceCheck,
		&out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("upsert extra_usage_settings: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 3: Extend `ListExtraUsageSettings`**

```go
func (s *Store) ListExtraUsageSettings() ([]types.ExtraUsageSettings, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT project_id, enabled, balance_fen, monthly_limit_fen,
		       bypass_balance_check, created_at, updated_at
		FROM extra_usage_settings
		ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list extra_usage_settings: %w", err)
	}
	defer rows.Close()

	var out []types.ExtraUsageSettings
	for rows.Next() {
		var s types.ExtraUsageSettings
		if err := rows.Scan(&s.ProjectID, &s.Enabled, &s.BalanceFen,
			&s.MonthlyLimitFen, &s.BypassBalanceCheck,
			&s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan settings: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Add `SetExtraUsageBypass`**

Append to the file (order-wise, place after `UpsertExtraUsageSettings`):

```go
// SetExtraUsageBypass flips the bypass_balance_check flag for a project.
// Used by superadmins to keep a project served even when its balance has
// been depleted or `enabled` is false. Creates the settings row if none
// exists so the flag can be set on a project that has never topped up.
func (s *Store) SetExtraUsageBypass(projectID string, bypass bool) (*types.ExtraUsageSettings, error) {
	row := s.pool.QueryRow(context.Background(), `
		INSERT INTO extra_usage_settings (project_id, bypass_balance_check)
		VALUES ($1, $2)
		ON CONFLICT (project_id) DO UPDATE
			SET bypass_balance_check = EXCLUDED.bypass_balance_check,
			    updated_at           = NOW()
		RETURNING project_id, enabled, balance_fen, monthly_limit_fen,
		          bypass_balance_check, created_at, updated_at`,
		projectID, bypass)
	out := &types.ExtraUsageSettings{}
	err := row.Scan(&out.ProjectID, &out.Enabled, &out.BalanceFen,
		&out.MonthlyLimitFen, &out.BypassBalanceCheck,
		&out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("set extra_usage bypass: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 5: Update `DeductExtraUsage` WHERE clause**

Replace the `WHERE ... RETURNING s.balance_fen` block inside `DeductExtraUsage`:

```go
	err = tx.QueryRow(ctx, `
		WITH month_spend AS (
			SELECT COALESCE(SUM(-amount_fen), 0)::bigint AS spent
			FROM extra_usage_transactions
			WHERE project_id = $1
			  AND type = 'deduction'
			  AND created_at >= (date_trunc('month', NOW() AT TIME ZONE 'Asia/Shanghai')
			                   AT TIME ZONE 'Asia/Shanghai')
		)
		UPDATE extra_usage_settings s
		   SET balance_fen = balance_fen - $2, updated_at = NOW()
		  FROM month_spend
		 WHERE s.project_id = $1
		   AND (s.bypass_balance_check = TRUE OR s.enabled = TRUE)
		   AND (s.bypass_balance_check = TRUE OR s.balance_fen >= $2)
		   AND (s.monthly_limit_fen = 0 OR month_spend.spent + $2 <= s.monthly_limit_fen)
		RETURNING s.balance_fen`,
		req.ProjectID, req.AmountFen,
	).Scan(&newBalance)
```

- [ ] **Step 6: Update `classifyDeductFailure`**

Replace the function body:

```go
func classifyDeductFailure(ctx context.Context, tx pgx.Tx, projectID string, amount int64) error {
	var enabled, bypass bool
	var balance, monthlyLimit int64
	err := tx.QueryRow(ctx, `
		SELECT enabled, balance_fen, monthly_limit_fen, bypass_balance_check
		FROM extra_usage_settings
		WHERE project_id = $1`, projectID,
	).Scan(&enabled, &balance, &monthlyLimit, &bypass)
	if err != nil {
		return ErrExtraUsageNotEnabled
	}
	if !bypass && !enabled {
		return ErrExtraUsageNotEnabled
	}
	if !bypass && balance < amount {
		return ErrInsufficientBalance
	}
	if monthlyLimit > 0 {
		var spent int64
		_ = tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(-amount_fen), 0)::bigint
			FROM extra_usage_transactions
			WHERE project_id = $1
			  AND type = 'deduction'
			  AND created_at >= (date_trunc('month', NOW() AT TIME ZONE 'Asia/Shanghai')
			                   AT TIME ZONE 'Asia/Shanghai')`,
			projectID,
		).Scan(&spent)
		if spent+amount > monthlyLimit {
			return ErrMonthlyLimitReached
		}
	}
	return ErrInsufficientBalance
}
```

- [ ] **Step 7: Verify the package still builds and unit tests pass**

Run: `go build ./... && go test ./internal/store/... -v`

Expected: build PASS, existing tests PASS (only `TestMigrationsEmbed` exists at store level, so this is minimal).

- [ ] **Step 8: Commit**

```bash
git add internal/store/extra_usage.go
git commit -m "feat(extra-usage): store layer support for bypass flag"
```

---

## Task 4: DB integration test for the constraint drop and `SetExtraUsageBypass`

**Files:**
- Create: `internal/store/extra_usage_db_test.go`

This test is gated on `TEST_DATABASE_URL` (pattern copied from `internal/proxy/cch_roundtrip_test.go`). It creates a fresh schema, runs migrations, and exercises the bypass flow.

- [ ] **Step 1: Write the failing test**

```go
package store

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// openTestDB connects to TEST_DATABASE_URL, creates a throwaway schema, runs
// all embedded migrations inside it, and returns a Store scoped to that
// schema. The schema is dropped on cleanup.
func openTestDB(t *testing.T) *Store {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL to run (e.g. postgres://...)")
	}

	schema := "eubypass_" + strings.ReplaceAll(uuid.New().String()[:8], "-", "")
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	conn.Close(ctx)

	st, err := New(ctx, u.String())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		dropCtx := context.Background()
		c, err := pgx.Connect(dropCtx, dbURL)
		if err == nil {
			_, _ = c.Exec(dropCtx, "DROP SCHEMA "+schema+" CASCADE")
			c.Close(dropCtx)
		}
		st.Close()
	})
	return st
}

// TestBalanceFenCheckDropped asserts that migration 022 actually dropped
// the CHECK (balance_fen >= 0) constraint — the auto-generated name must
// match what the migration targeted.
func TestBalanceFenCheckDropped(t *testing.T) {
	st := openTestDB(t)
	ctx := context.Background()

	// Insert a row with balance_fen = -1 directly. Under the original
	// CHECK this would fail; after migration 022 it must succeed.
	projectID := uuid.New().String()
	// projects FK must be satisfied; create a fake projects row.
	_, err := st.pool.Exec(ctx, `INSERT INTO projects (id, owner_id, name) VALUES ($1, $2, 'test')`,
		projectID, uuid.New().String())
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	_, err = st.pool.Exec(ctx, `
		INSERT INTO extra_usage_settings (project_id, balance_fen)
		VALUES ($1, -1)`, projectID)
	if err != nil {
		t.Fatalf("negative balance INSERT must succeed after migration 022, got: %v", err)
	}
}

// TestBalanceAfterFenCheckDropped covers the ledger's CHECK.
func TestBalanceAfterFenCheckDropped(t *testing.T) {
	st := openTestDB(t)
	ctx := context.Background()

	projectID := uuid.New().String()
	_, err := st.pool.Exec(ctx, `INSERT INTO projects (id, owner_id, name) VALUES ($1, $2, 'test')`,
		projectID, uuid.New().String())
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	_, err = st.pool.Exec(ctx, `
		INSERT INTO extra_usage_transactions
		  (project_id, type, amount_fen, balance_after_fen)
		VALUES ($1, 'deduction', -100, -50)`, projectID)
	if err != nil {
		t.Fatalf("negative balance_after_fen INSERT must succeed, got: %v", err)
	}
}
```

Note on imports: this file uses `github.com/google/uuid`. Check the go.mod — the project already has this dependency (grep shows existing uses). If absent, run `go get github.com/google/uuid`.

- [ ] **Step 2: Run the test suite — it should skip without `TEST_DATABASE_URL`**

Run: `go test ./internal/store/... -run TestBalanceFen -v`

Expected: both tests SKIP with the env-var message.

- [ ] **Step 3: Run the tests with a local Postgres**

Start (or point to) a local Postgres, then:

```bash
TEST_DATABASE_URL="postgres://user:pass@localhost:5432/testdb?sslmode=disable" \
  go test ./internal/store/... -run TestBalanceFen -v
```

Expected: PASS. If FAIL with `violates check constraint`, the auto-generated constraint name differs from the default; open `\d extra_usage_settings` in psql to find the real name and update migration 022 accordingly.

- [ ] **Step 4: Add a bypass-path behavioural test**

Append to the same file:

```go
// TestSetExtraUsageBypassAndDeduct verifies the end-to-end bypass flow:
// enabled=false + balance=0 + bypass=true → DeductExtraUsage succeeds and
// drives the balance negative.
func TestSetExtraUsageBypassAndDeduct(t *testing.T) {
	st := openTestDB(t)
	ctx := context.Background()

	projectID := uuid.New().String()
	if _, err := st.pool.Exec(ctx, `INSERT INTO projects (id, owner_id, name) VALUES ($1, $2, 'test')`,
		projectID, uuid.New().String()); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	// Flip bypass; row should be created with enabled=false, balance=0.
	settings, err := st.SetExtraUsageBypass(projectID, true)
	if err != nil {
		t.Fatalf("SetExtraUsageBypass: %v", err)
	}
	if !settings.BypassBalanceCheck {
		t.Fatalf("BypassBalanceCheck=false, want true")
	}
	if settings.Enabled {
		t.Fatalf("Enabled=true on fresh bypass row, want false")
	}
	if settings.BalanceFen != 0 {
		t.Fatalf("BalanceFen=%d, want 0", settings.BalanceFen)
	}

	// Deduct 100 fen — should succeed and take balance to -100.
	newBal, err := st.DeductExtraUsage(DeductExtraUsageReq{
		ProjectID: projectID,
		AmountFen: 100,
		Reason:    "rate_limited",
	})
	if err != nil {
		t.Fatalf("DeductExtraUsage under bypass: %v", err)
	}
	if newBal != -100 {
		t.Fatalf("newBal=%d, want -100", newBal)
	}

	// Flip bypass off; same project, same (now negative) balance → deduction
	// must fail with ErrInsufficientBalance.
	if _, err := st.SetExtraUsageBypass(projectID, false); err != nil {
		t.Fatalf("SetExtraUsageBypass(false): %v", err)
	}
	_, err = st.DeductExtraUsage(DeductExtraUsageReq{
		ProjectID: projectID,
		AmountFen: 10,
		Reason:    "rate_limited",
	})
	if err == nil {
		t.Fatalf("deduct after bypass off must fail, got nil")
	}
}
```

- [ ] **Step 5: Run the new test**

Run: `TEST_DATABASE_URL=... go test ./internal/store/... -run TestSetExtraUsageBypass -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/store/extra_usage_db_test.go
git commit -m "test(extra-usage): DB integration tests for bypass and constraint drop"
```

---

## Task 5: Guard middleware — extract store interface, add bypass branch, add unit tests

**Files:**
- Modify: `internal/proxy/extra_usage_guard_middleware.go`
- Modify: `internal/proxy/extra_usage_guard_middleware_test.go`

- [ ] **Step 1: Introduce a minimal store interface**

At the top of `internal/proxy/extra_usage_guard_middleware.go`, before `ExtraUsageGuardMiddleware`:

```go
// extraUsageStore is the subset of *store.Store the guard needs. Extracted
// so tests can inject a fake without spinning up Postgres.
type extraUsageStore interface {
	GetExtraUsageSettings(projectID string) (*types.ExtraUsageSettings, error)
	GetMonthlyExtraSpendFen(projectID string) (int64, error)
}
```

Add `"github.com/modelserver/modelserver/internal/types"` to imports if not already present.

Change the function signature:

```go
func ExtraUsageGuardMiddleware(cfg config.ExtraUsageConfig, st extraUsageStore, logger *slog.Logger) func(http.Handler) http.Handler {
```

`*store.Store` satisfies this interface automatically, so the caller in `internal/proxy/router.go` compiles unchanged.

- [ ] **Step 2: Add the bypass branch**

Replace the post-`GetExtraUsageSettings` block. Current:

```go
if settings == nil || !settings.Enabled { ... reject "not_enabled" ... }
if settings.BalanceFen <= 0 { ... reject "balance_depleted" ... }
```

New:

```go
bypass := settings != nil && settings.BypassBalanceCheck

if !bypass {
	if settings == nil || !settings.Enabled {
		writeExtraUsageRejected(w, http.StatusTooManyRequests, intent.Reason, guardStateRejected{
			Enabled: false,
			Message: rejectedMessage(intent.Reason, "not_enabled"),
		})
		recordExtraUsageResult(intent.Reason, "rejected")
		return
	}
	if settings.BalanceFen <= 0 {
		writeExtraUsageRejected(w, http.StatusTooManyRequests, intent.Reason, guardStateRejected{
			Enabled:    true,
			BalanceFen: settings.BalanceFen,
			Message:    rejectedMessage(intent.Reason, "balance_depleted"),
		})
		recordExtraUsageResult(intent.Reason, "rejected")
		return
	}
}

// Monthly-limit check: runs for both bypass and normal paths. Uses `settings`
// which is guaranteed non-nil here — under bypass we already allowed a nil
// settings above? No: bypass requires a row. Guard: we entered bypass only
// when settings != nil, so settings is non-nil.
var monthlySpent int64
if settings.MonthlyLimitFen > 0 {
	spent, err := st.GetMonthlyExtraSpendFen(project.ID)
	if err != nil {
		logger.Error("extra_usage monthly spend query failed", "error", err, "project_id", project.ID)
		writeExtraUsageRejected(w, http.StatusInternalServerError, intent.Reason, guardStateRejected{
			Message: "extra usage monthly check failed",
		})
		return
	}
	if spent >= settings.MonthlyLimitFen {
		writeExtraUsageRejected(w, http.StatusTooManyRequests, intent.Reason, guardStateRejected{
			Enabled:    true,
			BalanceFen: settings.BalanceFen,
			Message:    rejectedMessage(intent.Reason, "monthly_limit"),
		})
		recordExtraUsageResult(intent.Reason, "rejected")
		return
	}
	monthlySpent = spent
}

ctx := withExtraUsageContext(r.Context(), ExtraUsageContext{
	Reason:            intent.Reason,
	BalanceFenAtEntry: settings.BalanceFen,
	MonthlyLimitFen:   settings.MonthlyLimitFen,
	MonthlySpentFen:   monthlySpent,
})
result := "allowed"
if bypass {
	result = "allowed_bypass"
}
recordExtraUsageResult(intent.Reason, result)
next.ServeHTTP(w, r.WithContext(ctx))
```

Delete the old monthly-limit block that followed the balance check (it has been merged above).

- [ ] **Step 3: Build to catch compilation drift**

Run: `go build ./...`

Expected: PASS.

- [ ] **Step 4: Write bypass unit tests**

Append to `internal/proxy/extra_usage_guard_middleware_test.go`:

```go
// fakeExtraUsageStore satisfies the extraUsageStore interface for tests.
type fakeExtraUsageStore struct {
	settings *types.ExtraUsageSettings
	spent    int64
}

func (f *fakeExtraUsageStore) GetExtraUsageSettings(_ string) (*types.ExtraUsageSettings, error) {
	return f.settings, nil
}

func (f *fakeExtraUsageStore) GetMonthlyExtraSpendFen(_ string) (int64, error) {
	return f.spent, nil
}

// runGuardWithIntent runs the middleware with a rate_limited intent attached
// and returns (recorder, innerCalled). Inner sets a flag; tests assert on
// the flag for "allowed" cases (the inner handler never writes a status, so
// rr.Code stays at its httptest default of 200 either way — checking the
// flag is the reliable signal).
func runGuardWithIntent(t *testing.T, cfg config.ExtraUsageConfig, st extraUsageStore, proj *types.Project) (*httptest.ResponseRecorder, *bool) {
	t.Helper()
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := ExtraUsageGuardMiddleware(cfg, st, slog.Default())(inner)

	r := httptest.NewRequest("POST", "/v1/messages", nil)
	ctx := context.WithValue(r.Context(), ctxExtraUsageIntent, ExtraUsageIntent{Reason: "rate_limited"})
	// ctxProject is the unexported key from auth_middleware.go; accessible
	// because the test lives in the proxy package.
	ctx = context.WithValue(ctx, ctxProject, proj)
	r = r.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr, &called
}

func TestExtraUsageGuard_Bypass_EnabledFalse_BalanceZero_Allowed(t *testing.T) {
	st := &fakeExtraUsageStore{
		settings: &types.ExtraUsageSettings{
			ProjectID:          "p1",
			Enabled:            false,
			BalanceFen:         0,
			BypassBalanceCheck: true,
		},
	}
	rr, called := runGuardWithIntent(t, dummyCfg(true), st, &types.Project{ID: "p1"})
	if !*called {
		t.Errorf("inner handler not called; body=%q", rr.Body.String())
	}
	if rr.Header().Get("X-Extra-Usage-Required") != "" {
		t.Errorf("allowed path must not attach X-Extra-Usage-Required header")
	}
}

func TestExtraUsageGuard_Bypass_MonthlyLimitExceeded_Rejected(t *testing.T) {
	st := &fakeExtraUsageStore{
		settings: &types.ExtraUsageSettings{
			ProjectID:          "p1",
			Enabled:            true,
			BalanceFen:         100000,
			MonthlyLimitFen:    30000,
			BypassBalanceCheck: true,
		},
		spent: 30000,
	}
	rr, called := runGuardWithIntent(t, dummyCfg(true), st, &types.Project{ID: "p1"})
	if *called {
		t.Error("inner must not be called when monthly limit is exceeded")
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rr.Code)
	}
}

func TestExtraUsageGuard_NoBypass_BalanceZero_Rejected(t *testing.T) {
	st := &fakeExtraUsageStore{
		settings: &types.ExtraUsageSettings{
			ProjectID:  "p1",
			Enabled:    true,
			BalanceFen: 0,
		},
	}
	rr, called := runGuardWithIntent(t, dummyCfg(true), st, &types.Project{ID: "p1"})
	if *called {
		t.Error("inner must not be called with zero balance and no bypass")
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rr.Code)
	}
}
```

Add imports at the top of the test file:

```go
import (
	"log/slog"

	"github.com/modelserver/modelserver/internal/types"
)
```

- [ ] **Step 5: Run guard tests**

Run: `go test ./internal/proxy -run TestExtraUsageGuard -v`

Expected: PASS on new and existing guard tests.

- [ ] **Step 6: Commit**

```bash
git add internal/proxy/extra_usage_guard_middleware.go internal/proxy/extra_usage_guard_middleware_test.go
git commit -m "feat(extra-usage): guard middleware honours bypass_balance_check"
```

---

## Task 6: Executor — emit `"overdraft"` metric outcome

**Files:**
- Modify: `internal/proxy/executor.go`

- [ ] **Step 1: Update the success branch of `settleExtraUsage`**

Locate the `case err == nil:` arm of the switch in `settleExtraUsage` (around line 1220). Replace:

```go
	case err == nil:
		rc.ExtraUsageBalanceAfterFen = newBal
		metrics.IncExtraUsageDeduction("ok")
		metrics.SetExtraUsageBalance(rc.ProjectID, newBal)
```

with:

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

- [ ] **Step 2: Verify compilation**

Run: `go build ./...`

Expected: PASS.

- [ ] **Step 3: Run proxy tests**

Run: `go test ./internal/proxy -v -run ExtraUsage`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/proxy/executor.go
git commit -m "feat(extra-usage): emit overdraft outcome when settled balance < 0"
```

---

## Task 7: Admin handler — PUT bypass + expose flag in project GET DTO

**Files:**
- Modify: `internal/admin/handle_extra_usage.go`
- Modify: `internal/admin/routes.go`

- [ ] **Step 1: Add `BypassBalanceCheck` to `extraUsageGetResponse`**

Edit the struct at the top of `handle_extra_usage.go`:

```go
type extraUsageGetResponse struct {
	Enabled            bool      `json:"enabled"`
	BalanceFen         int64     `json:"balance_fen"`
	MonthlyLimitFen    int64     `json:"monthly_limit_fen"`
	MonthlySpentFen    int64     `json:"monthly_spent_fen"`
	MonthlyWindowStart string    `json:"monthly_window_start"`
	CreditPriceFen     int64     `json:"credit_price_fen"`
	MinTopupFen        int64     `json:"min_topup_fen"`
	MaxTopupFen        int64     `json:"max_topup_fen"`
	DailyTopupLimitFen int64     `json:"daily_topup_limit_fen"`
	BypassBalanceCheck bool      `json:"bypass_balance_check"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
}
```

In `handleGetExtraUsage`, inside the `if settings != nil {` block, add:

```go
resp.BypassBalanceCheck = settings.BypassBalanceCheck
```

- [ ] **Step 2: Add the PUT bypass handler**

Append to `handle_extra_usage.go`:

```go
// handleAdminExtraUsageSetBypass flips the bypass flag on a project's
// extra-usage settings. Superadmin only. Creates the settings row if
// absent so the flag can be set on projects that have never topped up.
func handleAdminExtraUsageSetBypass(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		var body struct {
			Bypass *bool `json:"bypass"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.Bypass == nil {
			writeError(w, http.StatusBadRequest, "bad_request", "bypass field required")
			return
		}

		out, err := st.SetExtraUsageBypass(projectID, *body.Bypass)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to set bypass")
			return
		}

		actor := UserFromContext(r.Context())
		actorID := ""
		if actor != nil {
			actorID = actor.ID
		}
		slog.Default().Info("extra_usage_bypass_toggled",
			"project_id", projectID,
			"bypass", *body.Bypass,
			"actor_user_id", actorID)

		writeData(w, http.StatusOK, out)
	}
}
```

Add `"log/slog"` to the import block if it isn't already.

- [ ] **Step 3: Wire the route**

In `internal/admin/routes.go`, find the `/admin/extra-usage` route group (around line 143):

```go
r.Route("/admin/extra-usage", func(r chi.Router) {
	r.Use(RequireSuperadmin)
	r.Get("/overview", handleAdminExtraUsageOverview(st))
	r.Post("/projects/{projectID}/topup", handleAdminExtraUsageDirectTopup(st))
})
```

Add the PUT line:

```go
r.Route("/admin/extra-usage", func(r chi.Router) {
	r.Use(RequireSuperadmin)
	r.Get("/overview", handleAdminExtraUsageOverview(st))
	r.Post("/projects/{projectID}/topup", handleAdminExtraUsageDirectTopup(st))
	r.Put("/projects/{projectID}/bypass", handleAdminExtraUsageSetBypass(st))
})
```

- [ ] **Step 4: Build**

Run: `go build ./...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/admin/handle_extra_usage.go internal/admin/routes.go
git commit -m "feat(extra-usage): PUT /admin/extra-usage/projects/{id}/bypass"
```

---

## Task 8: Admin handler tests

**Files:**
- Create: `internal/admin/handle_extra_usage_bypass_test.go`

These tests are DB-backed (to use the real `SetExtraUsageBypass`), so they also gate on `TEST_DATABASE_URL` via the same skip pattern.

- [ ] **Step 1: Locate an existing admin test for the setup pattern**

Run: `ls internal/admin/*_test.go`

Expected: a few test files exist. Open one that exercises the real store (grep for `TEST_DATABASE_URL` or `openTestDB`). If none uses DB, the store tests from Task 4 are the only integration-level coverage — in that case this task becomes a pure-unit test against a fake store. Proceed with **Step 2A** if a DB pattern exists in admin, otherwise **Step 2B**.

- [ ] **Step 2A (if a DB test pattern exists in admin/): write DB-backed handler tests**

```go
package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func TestAdminSetBypass_RequiresBypassField(t *testing.T) {
	st := openTestStore(t) // reuse the admin package's helper
	h := handleAdminExtraUsageSetBypass(st)

	r := chi.NewRouter()
	r.Put("/projects/{projectID}/bypass", h)

	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest("PUT", "/projects/"+uuid.New().String()+"/bypass", body)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rr.Code)
	}
}

func TestAdminSetBypass_CreatesRow(t *testing.T) {
	st := openTestStore(t)
	projectID := seedProject(t, st) // whatever the existing helper is called

	h := handleAdminExtraUsageSetBypass(st)
	r := chi.NewRouter()
	r.Put("/projects/{projectID}/bypass", h)

	body := bytes.NewBufferString(`{"bypass": true}`)
	req := httptest.NewRequest("PUT", "/projects/"+projectID+"/bypass", body)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Data types.ExtraUsageSettings `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Data.BypassBalanceCheck {
		t.Errorf("BypassBalanceCheck=false, want true")
	}

	// Round-trip via the store to verify persistence.
	settings, err := st.GetExtraUsageSettings(projectID)
	if err != nil || settings == nil {
		t.Fatalf("settings not persisted: err=%v settings=%v", err, settings)
	}
	if !settings.BypassBalanceCheck {
		t.Error("persisted bypass is false")
	}
}
```

If the admin package lacks `openTestStore`/`seedProject` helpers, copy the pattern from `internal/store/extra_usage_db_test.go` into a new `internal/admin/testhelpers_test.go` (unexported file — keeps the DB wiring out of the production surface).

- [ ] **Step 2B (if no admin DB test pattern): skip this task's DB assertions**

If the admin package doesn't currently do DB-backed testing, limit this task to the *400-on-missing-field* case, which doesn't need a real store:

```go
func TestAdminSetBypass_RequiresBypassField(t *testing.T) {
	h := handleAdminExtraUsageSetBypass(nil) // nil store is OK; we never reach it
	r := chi.NewRouter()
	r.Put("/projects/{projectID}/bypass", h)

	req := httptest.NewRequest("PUT", "/projects/xyz/bypass", bytes.NewBufferString(`{}`))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rr.Code)
	}
}
```

Rationale: the full DB coverage is already in Task 4's `TestSetExtraUsageBypassAndDeduct`, so adding a second DB path here is redundant if the infrastructure isn't available.

- [ ] **Step 3: Run**

Run: `go test ./internal/admin -run TestAdminSetBypass -v`

Expected: PASS (with or without TEST_DATABASE_URL, given the guard).

- [ ] **Step 4: Commit**

```bash
git add internal/admin/handle_extra_usage_bypass_test.go
# plus any new testhelpers_test.go file
git commit -m "test(extra-usage): admin handler tests for PUT bypass"
```

---

## Task 9: Dashboard API hooks

**Files:**
- Modify: `dashboard/src/api/extra-usage.ts`

- [ ] **Step 1: Extend `ExtraUsageSettingsResponse` + add admin types**

Edit `dashboard/src/api/extra-usage.ts`. Add `bypass_balance_check` to the existing interface:

```ts
export interface ExtraUsageSettingsResponse {
  enabled: boolean;
  balance_fen: number;
  monthly_limit_fen: number;
  monthly_spent_fen: number;
  monthly_window_start: string;
  credit_price_fen: number;
  min_topup_fen: number;
  max_topup_fen: number;
  daily_topup_limit_fen: number;
  bypass_balance_check: boolean;
  updated_at?: string;
}
```

Append at the end of the file:

```ts
// Admin-only: shape of a row returned by /admin/extra-usage/overview.
export interface AdminExtraUsageRow {
  project_id: string;
  enabled: boolean;
  balance_fen: number;
  monthly_limit_fen: number;
  bypass_balance_check: boolean;
  created_at: string;
  updated_at: string;
  spend_7d_fen: number;
}

export function useAdminExtraUsageOverview() {
  return useQuery({
    queryKey: ["admin", "extra-usage", "overview"],
    queryFn: () =>
      api.get<DataResponse<AdminExtraUsageRow[]>>(
        `/api/v1/admin/extra-usage/overview`,
      ),
  });
}

export function useSetExtraUsageBypass() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      projectId,
      bypass,
    }: {
      projectId: string;
      bypass: boolean;
    }) =>
      api.put<DataResponse<unknown>>(
        `/api/v1/admin/extra-usage/projects/${projectId}/bypass`,
        { bypass },
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "extra-usage", "overview"] });
    },
  });
}
```

Verify the `api.put` helper exists by grepping: `grep -n "^\s*put" dashboard/src/api/client.ts`. If the client exposes a different verb name (e.g. `api.request`), adjust accordingly.

- [ ] **Step 2: Type-check**

Run: `cd dashboard && pnpm build` (the `build` script is `tsc -b && vite build`, which catches type errors).

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add dashboard/src/api/extra-usage.ts
git commit -m "feat(dashboard): extra-usage admin API hooks"
```

---

## Task 10: Dashboard admin Extra Usage page

**Files:**
- Create: `dashboard/src/pages/admin/ExtraUsagePage.tsx`
- Modify: `dashboard/src/App.tsx`
- Modify: `dashboard/src/components/layout/Sidebar.tsx`

- [ ] **Step 1: Build the page**

Create `dashboard/src/pages/admin/ExtraUsagePage.tsx`:

```tsx
import { useState } from "react";
import {
  useAdminExtraUsageOverview,
  useSetExtraUsageBypass,
  type AdminExtraUsageRow,
} from "@/api/extra-usage";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";

function fenToYuan(fen: number): string {
  return (fen / 100).toFixed(2);
}

export function AdminExtraUsagePage() {
  const { data, isLoading } = useAdminExtraUsageOverview();
  const setBypass = useSetExtraUsageBypass();

  const [lookupId, setLookupId] = useState("");

  const rows = data?.data ?? [];

  const cols: Column<AdminExtraUsageRow>[] = [
    { key: "project_id", header: "Project ID", render: (r) => <code className="text-xs">{r.project_id}</code> },
    { key: "enabled", header: "Enabled", render: (r) => (r.enabled ? "Yes" : "No") },
    { key: "balance_fen", header: "Balance ¥", render: (r) => fenToYuan(r.balance_fen) },
    { key: "monthly_limit_fen", header: "Monthly limit ¥", render: (r) => (r.monthly_limit_fen ? fenToYuan(r.monthly_limit_fen) : "—") },
    { key: "spend_7d_fen", header: "7d spend ¥", render: (r) => fenToYuan(r.spend_7d_fen) },
    {
      key: "bypass_balance_check",
      header: "Bypass",
      render: (r) => (
        <Switch
          checked={r.bypass_balance_check}
          onCheckedChange={(v) => setBypass.mutate({ projectId: r.project_id, bypass: v })}
          disabled={setBypass.isPending}
        />
      ),
    },
  ];

  return (
    <>
      <PageHeader title="Extra Usage" subtitle="Per-project balance + superadmin bypass" />

      <Card className="mb-4">
        <CardContent className="p-4 flex items-end gap-2">
          <div className="flex-1">
            <label className="text-xs text-muted-foreground block mb-1">
              Set bypass on a project by ID (creates the settings row if absent)
            </label>
            <input
              className="w-full border rounded px-2 py-1 text-sm"
              placeholder="project UUID"
              value={lookupId}
              onChange={(e) => setLookupId(e.target.value.trim())}
            />
          </div>
          <Button
            onClick={() => {
              if (lookupId) setBypass.mutate({ projectId: lookupId, bypass: true });
            }}
            disabled={!lookupId || setBypass.isPending}
          >
            Enable bypass
          </Button>
          <Button
            variant="outline"
            onClick={() => {
              if (lookupId) setBypass.mutate({ projectId: lookupId, bypass: false });
            }}
            disabled={!lookupId || setBypass.isPending}
          >
            Disable bypass
          </Button>
        </CardContent>
      </Card>

      <DataTable data={rows} columns={cols} loading={isLoading} />
    </>
  );
}
```

If `Switch` isn't a primitive in this project, fall back to a small inline `<button>` that toggles — grep `dashboard/src/components/ui/` to confirm. Similarly, `DataTable` / `Column` exist (see `ProjectsPage.tsx`); if the column type signature differs, copy it exactly from that file.

- [ ] **Step 2: Wire the route in `App.tsx`**

Add the import near the other admin pages (around the existing `AdminProjectsPage` import):

```tsx
import { AdminExtraUsagePage } from "@/pages/admin/ExtraUsagePage";
```

Add the route near the existing `admin/projects` route:

```tsx
<Route path="admin/extra-usage" element={<AdminExtraUsagePage />} />
```

- [ ] **Step 3: Add sidebar link**

Edit `dashboard/src/components/layout/Sidebar.tsx`. Import `Zap` icon if not already imported (it is — line 13 in current file). Inside the `user?.is_superadmin` block (after `SidebarLink to="/admin/plans"`), add:

```tsx
<SidebarLink to="/admin/extra-usage" icon={Zap}>
  Extra Usage
</SidebarLink>
```

- [ ] **Step 4: Build + dev-run smoke test**

Run: `cd dashboard && pnpm build && pnpm dev`.

Manual check:
1. Log in as a superadmin user.
2. Navigate to `/admin/extra-usage`.
3. Confirm the table loads. If no rows, confirm the "Set bypass by project ID" input is visible.
4. Paste a real project UUID, click "Enable bypass", watch the table refresh and show the project with bypass = on.
5. Click the table's Switch to turn bypass off; confirm it toggles and refreshes.

Expected: all four checks pass.

- [ ] **Step 5: Commit**

```bash
git add dashboard/src/pages/admin/ExtraUsagePage.tsx dashboard/src/App.tsx dashboard/src/components/layout/Sidebar.tsx
git commit -m "feat(dashboard): admin Extra Usage overview + bypass toggle"
```

---

## Task 11: End-to-end verification

- [ ] **Step 1: Full backend test suite**

Run: `go test ./...`

Expected: PASS (DB integration tests skip without TEST_DATABASE_URL).

- [ ] **Step 2: If a local Postgres is available, rerun with DB**

Run: `TEST_DATABASE_URL="postgres://..." go test ./internal/store/... ./internal/admin/... -v`

Expected: PASS (including the constraint-drop and `SetExtraUsageBypass` tests).

- [ ] **Step 3: Dashboard build (includes tsc type-check)**

Run: `cd dashboard && pnpm build`

Expected: PASS.

- [ ] **Step 4: Manual integration walk-through**

Spin up the full stack and verify:

1. As superadmin, PUT `/api/v1/admin/extra-usage/projects/{id}/bypass` with `{"bypass": true}` → 200, body includes `bypass_balance_check: true`.
2. Project currently has `enabled=false` and `balance_fen=0`. Fire a request that would normally be gated by the extra-usage guard (e.g. one from a non-claude-code client against an Anthropic model, which triggers `client_restriction` intent). Expect HTTP 200 from upstream.
3. Response headers show `X-Extra-Usage: true`, `X-Extra-Usage-Cost-Fen: <N>`, `X-Extra-Usage-Balance-Fen: <negative value>`.
4. Ledger row in `extra_usage_transactions` has `type='deduction'`, `amount_fen=-N`, `balance_after_fen < 0`.
5. Prometheus `extra_usage_requests_total{result="allowed_bypass"}` has incremented; `extra_usage_deductions_total{result="overdraft"}` has incremented.
6. PUT bypass=false → subsequent request of the same shape returns 429 with `X-Extra-Usage-Reason: rate_limited` (or `client_restriction`) and `X-Extra-Usage-Enabled: true`, `X-Extra-Usage-Balance-Fen: <the negative value from step 4>`.

- [ ] **Step 5: Final commit if there are outstanding tweaks, otherwise open PR**

```bash
git log --oneline -n 12  # confirm the task commits
gh pr create --title "feat(extra-usage): superadmin bypass flag" --body "$(cat <<'EOF'
## Summary
- Per-project `bypass_balance_check` flag — superadmin only
- When set, guard skips enabled + balance checks; monthly_limit_fen still enforced
- Settlement continues to deduct; balance may go negative
- New admin dashboard page + PUT endpoint

## Spec / plan
- docs/superpowers/specs/2026-04-23-extra-usage-superadmin-bypass-design.md
- docs/superpowers/plans/2026-04-23-extra-usage-superadmin-bypass.md

## Test plan
- [ ] `go test ./...`
- [ ] `TEST_DATABASE_URL=... go test ./internal/store/... ./internal/admin/...`
- [ ] Dashboard typecheck + lint + build
- [ ] Manual walk-through per plan Task 11 Step 4

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review notes

Key risks called out in the spec and addressed in the plan:

- **Constraint-name drift** → Task 4 Step 3 verifies the DROP actually worked by INSERTing a negative balance against a real Postgres.
- **`handleUpdateExtraUsage` leaking bypass to project owners** → accepted and made consistent; `extraUsageGetResponse` also exposes the flag (Task 7 Step 1).
- **Middleware unit-testability** → Task 5 Step 1 extracts `extraUsageStore` interface.
- **Monthly-limit semantics under bypass (persistent negative balance)** → documented in the spec's worked examples; the plan's Task 5 bypass + monthly_limit test makes the behaviour executable.
- **Finding a project with no settings row** → Task 10 Step 1 ships a by-project-ID form.
