# Requests Page – User Column Design

**Date**: 2026-04-26
**Status**: Approved (pending implementation)

## Goal

Add a leftmost "User" column to both request log pages, displaying the avatar and nickname of the user who made each proxied call. Today both pages identify the request source by API key name (project page) or project ID (admin page); the human user behind the call is invisible.

## Scope

In scope:
- Project-level Requests page: `dashboard/src/pages/requests/RequestsPage.tsx`
- Admin Requests page: `dashboard/src/pages/admin/RequestsPage.tsx`
- Backend list/get queries that feed those pages
- A small shared `UserCell` frontend component

Out of scope:
- Detail drawer (only the table column changes)
- Existing `created_by` filter dropdown on the admin page (already works)
- Any change to sort/filter SQL columns
- Trace page (not requested)

## Backend changes

### 1. `internal/types/request.go`

Add two JSON-serialized fields to the `Request` struct so the frontend can render avatar+nickname without an extra round-trip:

```go
CreatedByNickname string `json:"created_by_nickname,omitempty"`
CreatedByPicture  string `json:"created_by_picture,omitempty"`
```

`CreatedBy` already exists with `omitempty` — keep as-is.

### 2. `internal/store/requests.go`

Update four queries to LEFT JOIN `users` on `r.created_by` and select the new fields. The pattern mirrors `internal/store/keys.go:85-97`, which already does this for API keys.

Affected functions:
- `ListRequests` (project-scoped)
- `ListAllRequests` (admin)
- `ListRequestsByTraceID` (trace detail page — included so the type stays consistent end-to-end; trace page UI is unchanged)
- `GetRequest` (single-row fetch)

Each SELECT gains `r.created_by`, `COALESCE(u.nickname, '')`, `COALESCE(u.picture, '')`, and the FROM clause gains `LEFT JOIN users u ON u.id = r.created_by`. Each `Scan` gains the three matching destinations.

`r.created_by` is a UUID column that may be NULL for legacy rows; cast/COALESCE the same way as `api_key_id::text` in the existing query.

## Frontend changes

### 3. `dashboard/src/api/types.ts`

Add three optional fields to the `Request` interface:

```ts
created_by?: string;
created_by_nickname?: string;
created_by_picture?: string;
```

### 4. New shared component: `dashboard/src/components/shared/UserCell.tsx`

Single-purpose component used by both request tables:

```tsx
interface UserCellProps {
  nickname?: string;
  picture?: string;
  userId?: string;
}
```

Behavior:
- If neither `nickname` nor `userId` is present, render a muted `-`.
- Otherwise render `<Avatar>` (size-6) + nickname (or first 8 chars of `userId` as fallback) horizontally with `gap-2`.
- `AvatarImage` uses `picture`; `AvatarFallback` shows the first character of nickname (or `?`).
- Truncate nickname text with `truncate` and a sensible `max-w-[12rem]` so wide nicknames don't break the table.

### 5. `dashboard/src/pages/requests/RequestsPage.tsx`

Insert a new column at index 0 of the `columns` array:

```tsx
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
```

Existing "Auth" column stays — it carries different information (which API key / OAuth client was used).

### 6. `dashboard/src/pages/admin/RequestsPage.tsx`

Same insertion at index 0, identical accessor.

## Data flow

1. Proxy populates `requests.created_by` from `apiKey.CreatedBy` (regular API key flow) or from the `user_id` claim on the Hydra-introspected token (OAuth/Hydra flow). Both paths already do this — no proxy changes needed.
2. `ListRequests`/`ListAllRequests` JOIN `users` and return `nickname` + `picture`.
3. Frontend `UserCell` renders avatar + nickname; falls back gracefully when fields are empty.

## Edge cases

- **NULL `created_by`** (legacy rows or future code paths that bypass auth attribution): `UserCell` renders `-`.
- **User deleted after the request was logged**: `LEFT JOIN` returns NULL nickname/picture; `UserCell` falls back to `userId` first 8 chars so the row still has *some* identifier.
- **Empty `picture`**: `AvatarFallback` shows the nickname's initial.
- **OAuth calls**: `created_by` is set from the token's `user_id` claim, so OAuth requests show the actual human user — same code path, no special-casing.

## Testing

- Manually verify on dev: a project with at least two members making requests via different API keys; admin page should show avatars + nicknames distinct per row.
- Verify a request whose `created_by` user has been deleted still renders (truncated UUID fallback, no crash).
- Verify legacy rows (NULL `created_by`) render `-`.
- No new unit tests required for SQL — schema mirrors the existing `keys.go` JOIN, which is already covered by integration tests.

## Risks

- `users.picture` may be a URL that fails to load — `AvatarFallback` covers this.
- The extra LEFT JOIN adds negligible cost on the indexed FK column, but worth eyeballing query plan if the requests table is large.
