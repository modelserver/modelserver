# Requests Page User Column Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a leftmost "User" column (avatar + nickname) to the project-level and admin Requests pages, populated by joining `requests.created_by` against the `users` table.

**Architecture:** Extend the existing `Request` Go struct with two JSON fields (`created_by_nickname`, `created_by_picture`), add a `LEFT JOIN users` to the four request-listing SQL queries (mirroring the existing pattern in `internal/store/keys.go`), surface the fields on the TypeScript `Request` interface, and render them in both `RequestsPage` tables via a new shared `UserCell` component.

**Tech Stack:** Go (pgx), PostgreSQL, React + TypeScript, Vite, Tailwind, `@base-ui/react/avatar`.

**Spec:** `docs/superpowers/specs/2026-04-26-requests-page-user-column-design.md`

---

## File map

**Backend (Go):**
- Modify: `internal/types/request.go` — add 2 fields to the `Request` struct.
- Modify: `internal/store/requests.go` — update 4 queries (`ListRequests`, `ListAllRequests`, `ListRequestsByTraceID`, `GetRequest`) to LEFT JOIN `users` and SELECT/Scan the new fields plus `created_by`.

**Frontend (TypeScript/React):**
- Modify: `dashboard/src/api/types.ts` — add 3 optional fields to the `Request` interface.
- Create: `dashboard/src/components/shared/UserCell.tsx` — shared avatar+nickname cell.
- Modify: `dashboard/src/pages/requests/RequestsPage.tsx` — insert "User" column at index 0.
- Modify: `dashboard/src/pages/admin/RequestsPage.tsx` — insert "User" column at index 0.

No tests are added: the backend SQL change is structurally identical to `internal/store/keys.go:85-97` (already integration-tested), and there is no existing component-test infrastructure in the dashboard. Verification is done via `go build` + `pnpm build` + manual smoke check.

---

### Task 1: Add nickname/picture fields to the Go `Request` struct

**Files:**
- Modify: `internal/types/request.go` (around line 47)

- [ ] **Step 1: Add the two fields right after `CreatedBy`**

In `internal/types/request.go`, locate:

```go
	CreatedBy           string    `json:"created_by,omitempty"`
```

Insert two new lines immediately after it:

```go
	CreatedBy           string    `json:"created_by,omitempty"`
	CreatedByNickname   string    `json:"created_by_nickname,omitempty"`
	CreatedByPicture    string    `json:"created_by_picture,omitempty"`
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /root/coding/modelserver && go build ./internal/types/...`
Expected: exit 0, no output.

- [ ] **Step 3: Commit**

```bash
git add internal/types/request.go
git commit -m "feat(types): add created_by_nickname/picture to Request"
```

---

### Task 2: Update `ListRequests` to JOIN users and return creator info

**Files:**
- Modify: `internal/store/requests.go:107-155`

- [ ] **Step 1: Update the SELECT, FROM, and Scan in `ListRequests`**

Replace the body of `ListRequests` (lines ~118-150) so the query becomes:

```go
	args = append(args, p.Limit(), p.Offset())
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT r.id, r.project_id, COALESCE(r.api_key_id::text, ''), COALESCE(r.oauth_grant_id::text, ''),
			COALESCE(r.upstream_id::text, ''), COALESCE(r.trace_id::text, ''), COALESCE(r.msg_id, ''),
			r.provider, r.request_kind, r.model, r.streaming, r.status, r.input_tokens, r.output_tokens, r.cache_creation_tokens, r.cache_read_tokens,
			r.credits_consumed, r.latency_ms, r.ttft_ms, COALESCE(r.error_message, ''), r.client_ip, r.created_at,
			COALESCE(og.client_name, '') as oauth_grant_client_name,
			r.metadata,
			COALESCE(r.http_log_path, ''),
			COALESCE(r.created_by::text, ''),
			COALESCE(u.nickname, ''),
			COALESCE(u.picture, '')
		FROM requests r
		LEFT JOIN oauth_grants og ON og.id = r.oauth_grant_id
		LEFT JOIN users u ON u.id = r.created_by
		%s ORDER BY %s %s LIMIT $%d OFFSET $%d`,
		where, sanitizeSort(p.Sort, "r.created_at"), sanitizeOrder(p.Order), argN, argN+1),
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var requests []types.Request
	for rows.Next() {
		var r types.Request
		var metadataJSON []byte
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.APIKeyID, &r.OAuthGrantID, &r.UpstreamID, &r.TraceID, &r.MsgID,
			&r.Provider, &r.RequestKind, &r.Model, &r.Streaming, &r.Status,
			&r.InputTokens, &r.OutputTokens, &r.CacheCreationTokens, &r.CacheReadTokens,
			&r.CreditsConsumed, &r.LatencyMs, &r.TTFTMs, &r.ErrorMessage, &r.ClientIP, &r.CreatedAt,
			&r.OAuthGrantClientName, &metadataJSON, &r.HttpLogPath,
			&r.CreatedBy, &r.CreatedByNickname, &r.CreatedByPicture); err != nil {
			return nil, 0, err
		}
		scanMetadata(&r, metadataJSON)
		requests = append(requests, r)
	}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /root/coding/modelserver && go build ./internal/store/...`
Expected: exit 0.

(No commit yet — Task 3 finishes the same file.)

---

### Task 3: Apply the same JOIN to `ListAllRequests`, `ListRequestsByTraceID`, and `GetRequest`

**Files:**
- Modify: `internal/store/requests.go:208-298` (`ListAllRequests`)
- Modify: `internal/store/requests.go:300-333` (`ListRequestsByTraceID`)
- Modify: `internal/store/requests.go:357-382` (`GetRequest`)

- [ ] **Step 1: Update `ListAllRequests`**

Replace the query body so the SELECT, FROM, and Scan match Task 2's pattern. Concretely, the SELECT clause appends three columns in this order (`COALESCE(r.created_by::text, '')`, `COALESCE(u.nickname, '')`, `COALESCE(u.picture, '')`), the FROM gains `LEFT JOIN users u ON u.id = r.created_by` after the existing oauth_grants join, and the Scan call gains `&r.CreatedBy, &r.CreatedByNickname, &r.CreatedByPicture` at the end. Full updated function body:

```go
func (s *Store) ListAllRequests(p types.PaginationParams, filters RequestFilters) ([]types.Request, int, error) {
	ctx := context.Background()
	where, args, argN := buildGlobalRequestFilters(filters)

	var total int
	if err := s.pool.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) FROM requests r %s", where), args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, p.Limit(), p.Offset())
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT r.id, r.project_id, COALESCE(r.api_key_id::text, ''), COALESCE(r.oauth_grant_id::text, ''),
			COALESCE(r.upstream_id::text, ''), COALESCE(r.trace_id::text, ''), COALESCE(r.msg_id, ''),
			r.provider, r.request_kind, r.model, r.streaming, r.status, r.input_tokens, r.output_tokens, r.cache_creation_tokens, r.cache_read_tokens,
			r.credits_consumed, r.latency_ms, r.ttft_ms, COALESCE(r.error_message, ''), r.client_ip, r.created_at,
			COALESCE(og.client_name, '') as oauth_grant_client_name,
			r.metadata,
			COALESCE(r.http_log_path, ''),
			COALESCE(r.created_by::text, ''),
			COALESCE(u.nickname, ''),
			COALESCE(u.picture, '')
		FROM requests r
		LEFT JOIN oauth_grants og ON og.id = r.oauth_grant_id
		LEFT JOIN users u ON u.id = r.created_by
		%s ORDER BY %s %s LIMIT $%d OFFSET $%d`,
		where, sanitizeSort(p.Sort, "r.created_at"), sanitizeOrder(p.Order), argN, argN+1),
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var requests []types.Request
	for rows.Next() {
		var r types.Request
		var metadataJSON []byte
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.APIKeyID, &r.OAuthGrantID, &r.UpstreamID, &r.TraceID, &r.MsgID,
			&r.Provider, &r.RequestKind, &r.Model, &r.Streaming, &r.Status,
			&r.InputTokens, &r.OutputTokens, &r.CacheCreationTokens, &r.CacheReadTokens,
			&r.CreditsConsumed, &r.LatencyMs, &r.TTFTMs, &r.ErrorMessage, &r.ClientIP, &r.CreatedAt,
			&r.OAuthGrantClientName, &metadataJSON, &r.HttpLogPath,
			&r.CreatedBy, &r.CreatedByNickname, &r.CreatedByPicture); err != nil {
			return nil, 0, err
		}
		scanMetadata(&r, metadataJSON)
		requests = append(requests, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return requests, total, nil
}
```

- [ ] **Step 2: Update `ListRequestsByTraceID`**

Replace it with:

```go
func (s *Store) ListRequestsByTraceID(traceID string) ([]types.Request, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT r.id, r.project_id, COALESCE(r.api_key_id::text, ''), COALESCE(r.oauth_grant_id::text, ''),
			COALESCE(r.upstream_id::text, ''), COALESCE(r.trace_id::text, ''), COALESCE(r.msg_id, ''),
			r.provider, r.request_kind, r.model, r.streaming, r.status, r.input_tokens, r.output_tokens, r.cache_creation_tokens, r.cache_read_tokens,
			r.credits_consumed, r.latency_ms, r.ttft_ms, COALESCE(r.error_message, ''), r.client_ip, r.created_at,
			COALESCE(og.client_name, '') as oauth_grant_client_name,
			r.metadata,
			COALESCE(r.http_log_path, ''),
			COALESCE(r.created_by::text, ''),
			COALESCE(u.nickname, ''),
			COALESCE(u.picture, '')
		FROM requests r
		LEFT JOIN oauth_grants og ON og.id = r.oauth_grant_id
		LEFT JOIN users u ON u.id = r.created_by
		WHERE r.trace_id = $1 ORDER BY r.created_at ASC`, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []types.Request
	for rows.Next() {
		var r types.Request
		var metadataJSON []byte
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.APIKeyID, &r.OAuthGrantID, &r.UpstreamID, &r.TraceID, &r.MsgID,
			&r.Provider, &r.RequestKind, &r.Model, &r.Streaming, &r.Status,
			&r.InputTokens, &r.OutputTokens, &r.CacheCreationTokens, &r.CacheReadTokens,
			&r.CreditsConsumed, &r.LatencyMs, &r.TTFTMs, &r.ErrorMessage, &r.ClientIP, &r.CreatedAt,
			&r.OAuthGrantClientName, &metadataJSON, &r.HttpLogPath,
			&r.CreatedBy, &r.CreatedByNickname, &r.CreatedByPicture); err != nil {
			return nil, err
		}
		scanMetadata(&r, metadataJSON)
		requests = append(requests, r)
	}
	return requests, rows.Err()
}
```

- [ ] **Step 3: Update `GetRequest`**

Replace it with:

```go
func (s *Store) GetRequest(id string) (*types.Request, error) {
	var r types.Request
	var metadataJSON []byte
	err := s.pool.QueryRow(context.Background(), `
		SELECT r.id, r.project_id, COALESCE(r.api_key_id::text, ''), COALESCE(r.oauth_grant_id::text, ''),
			COALESCE(r.upstream_id::text, ''), COALESCE(r.trace_id::text, ''), COALESCE(r.msg_id, ''),
			r.provider, r.request_kind, r.model, r.streaming, r.status, r.input_tokens, r.output_tokens, r.cache_creation_tokens, r.cache_read_tokens,
			r.credits_consumed, r.latency_ms, r.ttft_ms, COALESCE(r.error_message, ''), r.client_ip, r.created_at,
			COALESCE(og.client_name, '') as oauth_grant_client_name,
			r.metadata,
			COALESCE(r.http_log_path, ''),
			COALESCE(r.created_by::text, ''),
			COALESCE(u.nickname, ''),
			COALESCE(u.picture, '')
		FROM requests r
		LEFT JOIN oauth_grants og ON og.id = r.oauth_grant_id
		LEFT JOIN users u ON u.id = r.created_by
		WHERE r.id = $1`, id,
	).Scan(&r.ID, &r.ProjectID, &r.APIKeyID, &r.OAuthGrantID, &r.UpstreamID, &r.TraceID, &r.MsgID,
		&r.Provider, &r.RequestKind, &r.Model, &r.Streaming, &r.Status,
		&r.InputTokens, &r.OutputTokens, &r.CacheCreationTokens, &r.CacheReadTokens,
		&r.CreditsConsumed, &r.LatencyMs, &r.TTFTMs, &r.ErrorMessage, &r.ClientIP, &r.CreatedAt,
		&r.OAuthGrantClientName, &metadataJSON, &r.HttpLogPath,
		&r.CreatedBy, &r.CreatedByNickname, &r.CreatedByPicture)
	if err != nil {
		return nil, err
	}
	scanMetadata(&r, metadataJSON)
	return &r, nil
}
```

- [ ] **Step 4: Build the whole backend**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: exit 0.

- [ ] **Step 5: Run existing store tests if any**

Run: `cd /root/coding/modelserver && go test ./internal/store/... -count=1`
Expected: PASS (or "no test files" — both are acceptable; we're not adding new tests).

- [ ] **Step 6: Commit**

```bash
git add internal/store/requests.go
git commit -m "feat(store): join users on request listings to return creator nickname/picture"
```

---

### Task 4: Add new fields to TypeScript `Request` interface

**Files:**
- Modify: `dashboard/src/api/types.ts:144-169`

- [ ] **Step 1: Add three optional fields**

In the `Request` interface, locate the line `client_ip?: string;` and insert these fields immediately after it (alongside the other optional metadata fields, before `metadata?:`):

```ts
  client_ip?: string;
  created_by?: string;
  created_by_nickname?: string;
  created_by_picture?: string;
  metadata?: Record<string, string>;
```

- [ ] **Step 2: Typecheck**

Run: `cd /root/coding/modelserver/dashboard && pnpm exec tsc -b --noEmit`
Expected: exit 0.

- [ ] **Step 3: Commit**

```bash
git add dashboard/src/api/types.ts
git commit -m "feat(dashboard): add created_by fields to Request type"
```

---

### Task 5: Create the shared `UserCell` component

**Files:**
- Create: `dashboard/src/components/shared/UserCell.tsx`

- [ ] **Step 1: Write the component**

Write this exact file:

```tsx
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";

interface UserCellProps {
  nickname?: string;
  picture?: string;
  userId?: string;
}

export function UserCell({ nickname, picture, userId }: UserCellProps) {
  if (!nickname && !userId) {
    return <span className="text-muted-foreground">-</span>;
  }
  const displayName = nickname || `${userId!.slice(0, 8)}…`;
  const initials = (nickname || userId || "?").slice(0, 2).toUpperCase();
  return (
    <div className="flex items-center gap-2">
      <Avatar size="sm">
        {picture && <AvatarImage src={picture} alt={displayName} />}
        <AvatarFallback>{initials}</AvatarFallback>
      </Avatar>
      <span className="truncate max-w-[12rem]">{displayName}</span>
    </div>
  );
}
```

- [ ] **Step 2: Typecheck**

Run: `cd /root/coding/modelserver/dashboard && pnpm exec tsc -b --noEmit`
Expected: exit 0.

- [ ] **Step 3: Commit**

```bash
git add dashboard/src/components/shared/UserCell.tsx
git commit -m "feat(dashboard): add shared UserCell component"
```

---

### Task 6: Add "User" column to project-level RequestsPage

**Files:**
- Modify: `dashboard/src/pages/requests/RequestsPage.tsx`

- [ ] **Step 1: Import `UserCell`**

Add the import next to the other `@/components/shared/...` imports (around line 11):

```tsx
import { UserCell } from "@/components/shared/UserCell";
```

- [ ] **Step 2: Insert the column at index 0 of the `columns` array**

In `RequestsPage.tsx` around line 94, the `columns` array currently begins with `{ header: "Msg ID", ... }`. Insert this new entry before it:

```tsx
  const columns: Column<Request>[] = [
    {
      header: "User",
      accessor: (r) => (
        <UserCell
          nickname={r.created_by_nickname}
          picture={r.created_by_picture}
          userId={r.created_by}
        />
      ),
    },
    {
      header: "Msg ID",
      accessor: (r) => r.msg_id ? (
        // ...rest unchanged
```

- [ ] **Step 3: Typecheck**

Run: `cd /root/coding/modelserver/dashboard && pnpm exec tsc -b --noEmit`
Expected: exit 0.

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/pages/requests/RequestsPage.tsx
git commit -m "feat(dashboard): add User column to project Requests page"
```

---

### Task 7: Add "User" column to admin RequestsPage

**Files:**
- Modify: `dashboard/src/pages/admin/RequestsPage.tsx`

- [ ] **Step 1: Import `UserCell`**

Add next to the other `@/components/shared/...` imports (around line 9):

```tsx
import { UserCell } from "@/components/shared/UserCell";
```

- [ ] **Step 2: Insert the column at index 0 of the `columns` array**

In `dashboard/src/pages/admin/RequestsPage.tsx` around line 87, the `columns` array currently begins with `{ header: "Project", ... }`. Insert this new entry before it:

```tsx
  const columns: Column<Request>[] = [
    {
      header: "User",
      accessor: (r) => (
        <UserCell
          nickname={r.created_by_nickname}
          picture={r.created_by_picture}
          userId={r.created_by}
        />
      ),
    },
    {
      header: "Project",
      accessor: (r) => (
        <span className="font-mono text-xs">{r.project_id.slice(0, 8)}</span>
      ),
    },
    // ...rest unchanged
```

- [ ] **Step 3: Build the dashboard end-to-end**

Run: `cd /root/coding/modelserver/dashboard && pnpm build`
Expected: exit 0, build succeeds.

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/pages/admin/RequestsPage.tsx
git commit -m "feat(dashboard): add User column to admin Requests page"
```

---

### Task 8: Manual smoke verification

**Files:** none

- [ ] **Step 1: Bring up dev**

Run: `cd /root/coding/modelserver/dashboard && pnpm dev` (background). The Go server is assumed to already be running locally; if not, start it per the project's normal workflow.

- [ ] **Step 2: Verify project Requests page**

Open the project Requests page in the browser. Confirm:
- A new "User" column is the leftmost column.
- For requests made via an API key, it shows the avatar + nickname of the key creator (or whoever made the call — should match the API key's creator under the existing flow).
- For requests made via OAuth/Hydra token, it also shows the actual user's avatar + nickname (not the OAuth client).
- Empty state: if there's a row whose `created_by` is NULL (legacy data), it shows `-`.

- [ ] **Step 3: Verify admin Requests page**

Navigate to `/admin/requests`. Confirm the same column behavior across multiple projects.

- [ ] **Step 4: Stop dev server**

Kill the `pnpm dev` background process.

(No commit — verification only.)

---

## Self-review checklist (already run)

- **Spec coverage:** every section of the spec maps to a task — types (Task 1), 4 SQL queries (Tasks 2–3), TS types (Task 4), shared UserCell (Task 5), project column (Task 6), admin column (Task 7), manual verification (Task 8). ✓
- **Placeholder scan:** no TBD/TODO/"similar to" left in steps. ✓
- **Type consistency:** struct fields `CreatedByNickname`/`CreatedByPicture`, JSON tags `created_by_nickname`/`created_by_picture`, TS fields `created_by_nickname`/`created_by_picture`, and component props `nickname`/`picture` are consistent through the chain. ✓
