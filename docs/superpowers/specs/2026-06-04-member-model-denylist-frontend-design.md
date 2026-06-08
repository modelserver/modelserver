# Per-Member Model Denylist — Frontend

Date: 2026-06-04
Status: Approved (design)

Companion to the backend spec
`docs/superpowers/specs/2026-06-04-member-model-denylist-design.md`,
which already shipped to `main` as PR #32. This adds the dashboard UI
for reading and configuring `project_members.denied_models`.

## Problem

The backend exposes per-member denylist via
`PUT /api/v1/projects/{id}/members/{userId}` and surfaces the field on
member-fetching responses, but the dashboard does not yet display or
edit it. Today an operator has to call the API directly with `curl` or
a script. We need a one-click path for owners and maintainers.

## Goals

- Show, on the project Members page, which models each member is
  currently denied — at a glance and on demand.
- Let owners and maintainers edit a member's denylist using the project's
  model catalog as the source of selectable options.
- Match existing UX patterns (quota editor, role gating, toast feedback).
- Zero new test infrastructure.

## Non-goals (explicit)

- No bulk editor (set denylist across many members in one shot).
- No "show all catalog models" toggle — the Dialog shows only models
  configured for *this* project (`useProjectModels(projectId)`).
- No `denied_models` field in the "Add Member" Dialog. New members start
  with `[]`; configure via Actions afterward.
- Self-edit and owner-edit guard rails. Any owner or maintainer can
  manage the denylist on **any** member — including owners, other
  maintainers, and themselves. This deliberately diverges from the
  Quota gate (which blocks owner rows and self-rows) because the
  backend spec (Q1) treats `denied_models` as a project-policy lever,
  not a credit gate. Recovery from an owner self-lockout is "another
  owner clears it."
- No frontend tests. The dashboard has no test framework today
  (no vitest/jest, no `test` script); we will not add one for this
  feature. Verification is manual (checklist below).
- No i18n. Project uses inline English strings; we follow suit.

## UX

### Members table — new column "Denied Models"

File: `dashboard/src/pages/members/MembersPage.tsx`

Placement: between **Usage** and **Joined** (or before **Actions** —
implementer picks whichever reads better with the existing column
spacing).

Width: ~120px (narrow; the column is a summary, not the editor).

Rendering:

| Member state                | Cell content                                                                                          |
|-----------------------------|-------------------------------------------------------------------------------------------------------|
| `denied_models.length == 0` | `—` in `text-muted-foreground`                                                                        |
| `denied_models.length >  0` | `"N denied"` (plain text or muted `Badge`) with a `Tooltip` listing the models                        |

Tooltip body: comma-separated model names. If the list exceeds 10
entries, show the first 10 followed by `"… +K more"` (`K = total - 10`).

This cell is **purely read-only** — no click handler.

### Actions menu — new item "Manage Denied Models"

In the existing `DropdownMenu` per row, add an item below
`Set Quota`:

```tsx
<DropdownMenuItem onClick={() => openDeniedDialog(m)}>
  Manage Denied Models
</DropdownMenuItem>
```

Visibility gate (deliberately wider than Quota):
```ts
canManageQuota
```

We deliberately do NOT add either an `m.role !== "owner"` gate or an
`m.user_id !== currentUser?.id` gate. Owners and maintainers can
configure the denylist on any member — including owners, other
maintainers, and themselves. This is consistent with the backend
(Q1 of the backend spec, and the explicit comment at
`internal/admin/handle_projects.go:553-561` documenting that the
maintainer→maintainer quota restriction does NOT carry over to
`denied_models`).

### Denied Models Dialog

New component: `dashboard/src/pages/members/DeniedModelsDialog.tsx`.

Props:
```ts
interface DeniedModelsDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectId: string;
  member: ProjectMember; // .user_id, .denied_models, .user?.nickname/email
}
```

Layout (`Dialog > DialogContent`):
- `DialogHeader`
  - Title: `Denied models for {nickname || email}`
  - Description: `Models listed here will return 403 for this member, regardless of which API key they use.`
- Body
  - `ModelMultiSelect` (existing component at
    `dashboard/src/components/shared/ModelCombobox.tsx`)
    - `value`: local `selected` state, initialized from `member.denied_models ?? []`
    - `onChange`: `setSelected`
    - `placeholder`: `"Select models to deny…"`
    - Options source: `useProjectModels(projectId)` mapped to their `name`
  - Empty-project hint: if `useProjectModels` returns `data.length === 0`,
    render `"This project has no models configured yet. Add channel routes first."`
    in `text-muted-foreground` below (or in place of) the MultiSelect.
- `DialogFooter`
  - `Cancel` button — calls `onOpenChange(false)`, no save
  - `Save` button
    - `disabled = updateMember.isPending || arraysEqual(selected, member.denied_models ?? [])`
    - `onClick`:
      ```ts
      await updateMember.mutateAsync({
        userId: member.user_id,
        denied_models: selected,
      });
      ```
    - On success: `toast.success("Denied models updated")` and `onOpenChange(false)`
    - On error: `toast.error(err instanceof APIError ? err.message : "Failed to update denied models")`

No explicit "Clear" button — emptying the multi-select IS the clear
action (Q3 of brainstorm). Sending `[]` clears the column server-side.

### MembersPage integration

```ts
const [deniedOpen, setDeniedOpen] = useState(false);
const [deniedTarget, setDeniedTarget] = useState<ProjectMember | null>(null);

function openDeniedDialog(m: ProjectMember) {
  setDeniedTarget(m);
  setDeniedOpen(true);
}

// In JSX, after the existing Set Quota Dialog:
{deniedTarget && (
  <DeniedModelsDialog
    open={deniedOpen}
    onOpenChange={(o) => {
      setDeniedOpen(o);
      if (!o) setDeniedTarget(null);
    }}
    projectId={projectId}
    member={deniedTarget}
  />
)}
```

## API / Types

### Type change

File: `dashboard/src/api/types.ts`

Add a required field to `ProjectMember`:
```ts
denied_models: string[];
```

Required (not optional). The backend always returns this — empty array
when nothing is denied. The only oddity is the synthetic superadmin
membership in `handleMyMembership`, which the backend now explicitly
initializes to `[]string{}` (commit `e1f1242` of PR #32). So the
frontend can rely on `member.denied_models` always being an array.

### Mutation input

File: `dashboard/src/api/members.ts`

Extend `UpdateMemberInput`:
```ts
type UpdateMemberInput = {
  userId: string;
  role?: string;
  credit_quota_percent?: number | null;
  clear_quota?: boolean;
  denied_models?: string[]; // undefined = unchanged; [] = clear; [...] = replace
};
```

Body construction must omit `denied_models` from the request JSON when
the field is `undefined` — i.e., conditional spread:
```ts
const body = {
  ...(role !== undefined ? { role } : {}),
  ...(credit_quota_percent !== undefined ? { credit_quota_percent } : {}),
  ...(clear_quota ? { clear_quota } : {}),
  ...(denied_models !== undefined ? { denied_models } : {}),
};
```

(Matches the existing tri-state convention in the file.) The existing
`useUpdateMember` already invalidates `["members", projectId]` and
`["members-compact", projectId]` on success — no change needed there.

### Catalog source

Reuse `useProjectModels(projectId)` from `dashboard/src/api/models.ts`.
No changes to that file.

## Edge cases

| Case                                          | Behavior                                                                                                   |
|-----------------------------------------------|------------------------------------------------------------------------------------------------------------|
| Project has no models configured              | Dialog shows empty MultiSelect with hint text; Save is naturally disabled by the equality check (selection equals existing empty list) |
| Member's `denied_models` references model names not in current project catalog | They show as unrecognized chips in the MultiSelect (existing component handles this); Save still works |
| Network error on Save                         | Toast with error message; Dialog stays open so the user can retry without losing selection                 |
| Concurrent edit (another admin set denylist between fetch and Save) | Last write wins; no merge logic. Backend cap of 256 still enforced on server side    |
| User opens Dialog for a different member while a Save is in flight | Mutation in flight finishes against the prior `userId`; second Dialog opens cleanly because `deniedTarget` updates synchronously |

## Strings (inline, English)

| Where                                | String                                                                                         |
|--------------------------------------|------------------------------------------------------------------------------------------------|
| Column header                        | `Denied Models`                                                                                |
| Empty cell                           | `—`                                                                                            |
| Populated cell                       | `{N} denied`                                                                                   |
| Tooltip overflow suffix              | `… +{K} more`                                                                                  |
| Actions menu item                    | `Manage Denied Models`                                                                         |
| Dialog title                         | `Denied models for {name}`                                                                     |
| Dialog description                   | `Models listed here will return 403 for this member, regardless of which API key they use.`    |
| MultiSelect placeholder              | `Select models to deny…`                                                                       |
| Empty-project hint                   | `This project has no models configured yet. Add channel routes first.`                         |
| Success toast                        | `Denied models updated`                                                                        |
| Error toast (generic)                | `Failed to update denied models`                                                               |
| Save button                          | `Save`                                                                                         |
| Cancel button                        | `Cancel`                                                                                       |

## Testing

No automated tests (see Non-goals). Manual verification checklist —
operator runs through these after deployment:

1. As **maintainer** on a project, open Members page. Confirm new
   "Denied Models" column appears; rows with no denylist show `—`.
2. Click Actions → "Manage Denied Models". Dialog opens; MultiSelect
   shows the project's configured models.
3. Select 2 models, click Save. Toast appears; Dialog closes; cell
   updates to `"2 denied"`. Hover → tooltip lists both model names.
4. Reopen the Dialog for the same member. The 2 models are
   pre-selected.
5. Remove all selections, Save. Cell reverts to `—`.
6. Pick a project with no channel routes configured. Open the Dialog
   for any member. Empty-state hint renders; Save is disabled.
7. Log in as a **developer**. Confirm Actions menu does NOT show
   "Manage Denied Models".
8. As an **owner** or **maintainer**, confirm "Manage Denied Models"
   IS available on every row — including the owner row, other
   maintainer rows, and the caller's own row. Apply a denylist to an
   owner, then to yourself, and verify both round-trip.
9. End-to-end backend check: with a member denylist set, send a
   request with that model — confirm 403
   `"model denied for this member by project policy"`.

## Out-of-scope follow-ups (not this PR)

- A bulk editor on the project settings page.
- A "show full catalog" toggle in the Dialog.
- Surfacing `denied_models` count in the `/members/compact` filter
  dropdown.
- Frontend test infrastructure.
- i18n.
