# Per-Member Model Denylist — Frontend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a read-only "Denied Models" column to the project Members page and a Dialog-driven editor (gated to owner/maintainer) that writes `denied_models` via the existing `useUpdateMember` mutation. Options are sourced from the project's model catalog (`useProjectModels`).

**Architecture:** Three small surgical edits + one new Dialog component. `ProjectMember` type gains a required `denied_models: string[]` field. `useUpdateMember` extends its input type and body construction to conditionally include `denied_models`. `ModelMultiSelect` / `ComboboxShell` accept an optional `rows` prop so callers can supply a pre-fetched list instead of always hitting the admin catalog. `MembersPage` adds a column, a menu item, and integrates a new `DeniedModelsDialog` component that mounts beside the existing Set Quota Dialog.

**Tech Stack:** React 19 + TypeScript + @tanstack/react-query 5 + base-ui (Dialog/Tooltip/Popover) + shadcn-style primitives + sonner toasts. No test framework — verification is manual against the spec's checklist.

**Reference spec:** `docs/superpowers/specs/2026-06-04-member-model-denylist-frontend-design.md`
**Backend (already merged):** PR #32, spec at `docs/superpowers/specs/2026-06-04-member-model-denylist-design.md`
**Branch (already created):** `feat/member-model-denylist-frontend`

---

## File Map

**Create:**
- `dashboard/src/pages/members/DeniedModelsDialog.tsx` — the Dialog component (~80 lines)

**Modify:**
- `dashboard/src/api/types.ts` — add `denied_models: string[]` to `ProjectMember` (1 line)
- `dashboard/src/api/members.ts` — extend `useUpdateMember` input type + conditional body construction
- `dashboard/src/components/shared/ModelCombobox.tsx` — add optional `rows` prop to `ComboboxShell` and `ModelMultiSelect`; when provided, skip the admin `useModels()` call
- `dashboard/src/pages/members/MembersPage.tsx` — add column, menu item, state, and Dialog mount

**Working directory for all commands:** `/root/coding/modelserver/dashboard`

**Verification command:** `pnpm build` (the project uses `tsc -b && vite build` — see `dashboard/package.json`). There is no `pnpm test`; type-check + build is the only automated gate.

---

## Task 1: Type — add `denied_models` to `ProjectMember`

**Files:**
- Modify: `dashboard/src/api/types.ts` (around line 64)

- [ ] **Step 1: Read the file to find `ProjectMember`**

Run: `grep -n "^export interface ProjectMember" /root/coding/modelserver/dashboard/src/api/types.ts`
Expected: prints one line number (currently 64).

- [ ] **Step 2: Inspect the struct so you edit the right block**

Read 15 lines starting at the matched line:
```
sed -n '64,80p' /root/coding/modelserver/dashboard/src/api/types.ts
```
Confirm it's the member type with `user_id`, `project_id`, `role`, `credit_quota_percent`, `created_at`, `user`, etc.

- [ ] **Step 3: Add the field**

In `dashboard/src/api/types.ts`, inside the `ProjectMember` interface, add:
```ts
denied_models: string[];
```
Place it immediately after `credit_quota_percent` (the natural sibling). The backend (PR #32) always serializes this as an array, including the synthetic superadmin path (commit `e1f1242`), so the field is **required, not optional**.

- [ ] **Step 4: Build to confirm nothing else needs updating**

Run: `cd /root/coding/modelserver/dashboard && pnpm build 2>&1 | tail -30`

Expected: TypeScript errors at every site that constructs a `ProjectMember` literal without `denied_models`. Those are the next steps to fix.

If the build succeeds with zero errors, that means nothing in the codebase constructs a `ProjectMember` literal (it's only ever decoded from JSON via `api.get<...>`). That's the expected case — proceed.

If there ARE errors, they will be in test fixtures or mock objects. For each, add `denied_models: []` to the literal.

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add dashboard/src/api/types.ts
git commit -m "feat(dashboard): add denied_models to ProjectMember type

Backend (PR #32) always returns this as an array, including the
synthetic superadmin membership path. Required, not optional.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: API — extend `useUpdateMember` to accept `denied_models`

**Files:**
- Modify: `dashboard/src/api/members.ts:58-82`

- [ ] **Step 1: Read the current shape**

```
sed -n '58,82p' /root/coding/modelserver/dashboard/src/api/members.ts
```

Confirm the function takes a destructured object with `userId`, `role`, `credit_quota_percent`, `clear_quota` and PUTs the body to `/api/v1/projects/{projectId}/members/{userId}`.

- [ ] **Step 2: Replace the function**

In `dashboard/src/api/members.ts`, replace the entire `useUpdateMember` function with:

```ts
export function useUpdateMember(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      userId,
      role,
      credit_quota_percent,
      clear_quota,
      denied_models,
    }: {
      userId: string;
      role?: string;
      credit_quota_percent?: number;
      clear_quota?: boolean;
      // undefined  = leave unchanged
      // []         = clear the denylist
      // [...names] = replace
      denied_models?: string[];
    }) => {
      // Build body conditionally so undefined fields never serialize
      // as `null` (which the backend would reject for the wrong reason).
      const body: Record<string, unknown> = {};
      if (role !== undefined) body.role = role;
      if (credit_quota_percent !== undefined) body.credit_quota_percent = credit_quota_percent;
      if (clear_quota) body.clear_quota = clear_quota;
      if (denied_models !== undefined) body.denied_models = denied_models;
      return api.put(`/api/v1/projects/${projectId}/members/${userId}`, body);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["members", projectId] });
      qc.invalidateQueries({ queryKey: ["members-compact", projectId] });
    },
  });
}
```

Note: the existing implementation always sends all four keys (some as `undefined`). The new implementation omits them when not provided. The backend accepts both shapes — its body decode treats missing fields as "unchanged" — but the explicit construction matches the design's tri-state semantics and avoids the risk of an `undefined → null` JSON serialization changing meaning at a future backend version.

- [ ] **Step 3: Build**

Run: `cd /root/coding/modelserver/dashboard && pnpm build 2>&1 | tail -10`
Expected: clean build.

- [ ] **Step 4: Commit**

```bash
cd /root/coding/modelserver
git add dashboard/src/api/members.ts
git commit -m "feat(dashboard): useUpdateMember accepts denied_models

Tri-state: undefined = unchanged, [] = clear, [...] = replace.
Body is now built conditionally so undefined fields never appear
as JSON null (matches backend spec semantics).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: ModelMultiSelect — accept a `rows` override

**Files:**
- Modify: `dashboard/src/components/shared/ModelCombobox.tsx`

This task lets callers supply a pre-fetched model list instead of having `ComboboxShell` always call the admin-scoped `useModels()`. The Denied Models Dialog will pass `useProjectModels(projectId)` results in.

- [ ] **Step 1: Read the current shape**

```
sed -n '19,55p' /root/coding/modelserver/dashboard/src/components/shared/ModelCombobox.tsx
sed -n '105,193p' /root/coding/modelserver/dashboard/src/components/shared/ModelCombobox.tsx
```

Confirm:
- `ComboboxShell` (line 19+) calls `const { data, isLoading } = useModels();` and partitions rows by `status === "active"`.
- `ModelMultiSelect` (line 105+) takes `{value, onChange, placeholder, disabled}` — no rows prop.

- [ ] **Step 2: Add a `rows` prop to `ComboboxShell`**

Replace the `ComboboxShell` function signature and the data-loading block. Find:

```ts
function ComboboxShell({
  placeholder,
  disabled,
  triggerLabel,
  renderRows,
}: {
  placeholder: string;
  disabled?: boolean;
  triggerLabel: React.ReactNode;
  renderRows: (args: {
    query: string;
    active: ModelListRow[];
    dimmed: ModelListRow[];
    isLoading: boolean;
    close: () => void;
  }) => React.ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const { data, isLoading } = useModels();
  const rows = data?.data ?? [];
```

Replace with:

```ts
function ComboboxShell({
  placeholder,
  disabled,
  triggerLabel,
  renderRows,
  rows: rowsOverride,
  isLoadingOverride,
}: {
  placeholder: string;
  disabled?: boolean;
  triggerLabel: React.ReactNode;
  renderRows: (args: {
    query: string;
    active: ModelListRow[];
    dimmed: ModelListRow[];
    isLoading: boolean;
    close: () => void;
  }) => React.ReactNode;
  // When rows is provided, the admin useModels() query is skipped.
  // Use this when the caller already has a scoped catalog (e.g.
  // useProjectModels) and shouldn't trigger an admin query.
  rows?: ModelListRow[];
  isLoadingOverride?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  // Only call the admin query when no override is provided. React
  // conditional-hook rule: we use the same hook every render in each
  // code path — the conditional is which branch the component takes
  // before the hook call, not which hook to call.
  const adminQuery = useModels();
  const rows = rowsOverride ?? adminQuery.data?.data ?? [];
  const isLoading = isLoadingOverride ?? adminQuery.isLoading;
```

WAIT — that violates React's rules-of-hooks if `useModels` runs when caller doesn't want it. But the hook *itself* always runs; what the caller cares about is whether the admin endpoint is hit. The compromise: always run `useModels()` but pass `enabled: false` when not needed. Simpler approach: extract the "load admin catalog" into a tiny inner component, or accept that calling `useModels()` always fires an admin query.

**Decision: do NOT skip `useModels()`.** Reasons:
1. React hooks can't be conditional.
2. `useModels()` is cached by react-query. If another page already loaded it, we get a free hit. If not, the query fires harmlessly in the background while we render the (different) `rowsOverride` rows.
3. Avoids restructuring the shell.

Replace the previous "with conditional hook" version. Use this **final** replacement instead:

```ts
function ComboboxShell({
  placeholder,
  disabled,
  triggerLabel,
  renderRows,
  rows: rowsOverride,
  isLoadingOverride,
}: {
  placeholder: string;
  disabled?: boolean;
  triggerLabel: React.ReactNode;
  renderRows: (args: {
    query: string;
    active: ModelListRow[];
    dimmed: ModelListRow[];
    isLoading: boolean;
    close: () => void;
  }) => React.ReactNode;
  // When provided, these override the default admin-catalog source.
  // Use for project-scoped or otherwise pre-fetched lists.
  rows?: ModelListRow[];
  isLoadingOverride?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  // useModels is always invoked to keep the hook stable across renders,
  // but its result is discarded when an override is supplied.
  const adminQuery = useModels();
  const rows = rowsOverride ?? adminQuery.data?.data ?? [];
  const isLoading = isLoadingOverride ?? adminQuery.isLoading;
```

The rest of `ComboboxShell` (the `useMemo` partition into active/dimmed by `status`, the `Popover`/`Input`/`PopoverContent` JSX) stays unchanged. Project-scoped rows have no `status` field — the filter `m.status === "active"` will be `undefined === "active"` → false → they all end up in `dimmed`. That breaks the visual grouping.

To fix: in the `useMemo`, treat rows lacking `status` as active:

Find:
```ts
return [
  matching.filter((m) => m.status === "active"),
  matching.filter((m) => m.status !== "active"),
] as const;
```

Replace with:
```ts
return [
  // Rows without an explicit status (e.g. project-scoped catalog)
  // are treated as active so they appear in the top group.
  matching.filter((m) => m.status === undefined || m.status === "active"),
  matching.filter((m) => m.status !== undefined && m.status !== "active"),
] as const;
```

- [ ] **Step 3: Add a `rows` prop to `ModelMultiSelect`**

Find:
```ts
export function ModelMultiSelect({
  value,
  onChange,
  placeholder = "Select models...",
  disabled,
}: {
  value: string[];
  onChange: (next: string[]) => void;
  placeholder?: string;
  disabled?: boolean;
}) {
```

Replace with:
```ts
export function ModelMultiSelect({
  value,
  onChange,
  placeholder = "Select models...",
  disabled,
  rows,
  isLoadingOverride,
}: {
  value: string[];
  onChange: (next: string[]) => void;
  placeholder?: string;
  disabled?: boolean;
  // Optional pre-fetched model list. When omitted, the component
  // falls back to the admin catalog via useModels().
  rows?: ModelListRow[];
  isLoadingOverride?: boolean;
}) {
```

Then in the same function, find the `<ComboboxShell` JSX block and add the two props:

Find:
```tsx
      <ComboboxShell
        placeholder={placeholder}
        disabled={disabled}
        triggerLabel={null}
        renderRows={({ query, active, dimmed }) => (
```

Replace with:
```tsx
      <ComboboxShell
        placeholder={placeholder}
        disabled={disabled}
        rows={rows}
        isLoadingOverride={isLoadingOverride}
        triggerLabel={null}
        renderRows={({ query, active, dimmed }) => (
```

Do NOT touch `ModelSingleSelect` — only `ModelMultiSelect` needs this; `ModelSingleSelect` callers don't need the override yet (YAGNI).

- [ ] **Step 4: Build**

Run: `cd /root/coding/modelserver/dashboard && pnpm build 2>&1 | tail -10`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add dashboard/src/components/shared/ModelCombobox.tsx
git commit -m "feat(dashboard): ModelMultiSelect accepts optional rows override

Lets callers supply a pre-fetched model list (e.g. project-scoped
useProjectModels) instead of always pulling the admin catalog.
Rows without a 'status' field are treated as active for grouping.
useModels is still called per the rules of hooks but its result is
discarded when an override is supplied (cached by react-query, so
harmless).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Component — `DeniedModelsDialog`

**Files:**
- Create: `dashboard/src/pages/members/DeniedModelsDialog.tsx`

- [ ] **Step 1: Create the file**

File: `dashboard/src/pages/members/DeniedModelsDialog.tsx`

```tsx
import { useState } from "react";
import { toast } from "sonner";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { ModelMultiSelect } from "@/components/shared/ModelCombobox";
import { useUpdateMember } from "@/api/members";
import { useProjectModels } from "@/api/models";
import { APIError } from "@/api/client";
import type { ProjectMember, ModelListRow } from "@/api/types";

interface DeniedModelsDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectId: string;
  member: ProjectMember;
}

function arraysEqual(a: readonly string[], b: readonly string[]): boolean {
  if (a.length !== b.length) return false;
  const sortedA = [...a].sort();
  const sortedB = [...b].sort();
  for (let i = 0; i < sortedA.length; i++) {
    if (sortedA[i] !== sortedB[i]) return false;
  }
  return true;
}

export function DeniedModelsDialog({
  open,
  onOpenChange,
  projectId,
  member,
}: DeniedModelsDialogProps) {
  const initial = member.denied_models ?? [];
  const [selected, setSelected] = useState<string[]>(initial);

  const projectModels = useProjectModels(projectId);
  const updateMember = useUpdateMember(projectId);

  // useProjectModels returns ProjectModel[] (no status/reference_counts);
  // ModelMultiSelect expects ModelListRow[]. The shapes overlap enough
  // that a cast is safe — the combobox only reads name/display_name/
  // aliases/status, and the rows-without-status branch treats missing
  // status as active (see Task 3).
  const rows = (projectModels.data?.data ?? []) as unknown as ModelListRow[];
  const hasNoModels = !projectModels.isLoading && rows.length === 0;
  const unchanged = arraysEqual(selected, initial);

  async function handleSave() {
    try {
      await updateMember.mutateAsync({
        userId: member.user_id,
        denied_models: selected,
      });
      toast.success("Denied models updated");
      onOpenChange(false);
    } catch (err) {
      const msg = err instanceof APIError ? err.message : "Failed to update denied models";
      toast.error(msg);
    }
  }

  const name = member.user?.nickname || member.user?.email || member.user_id;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Denied models for {name}</DialogTitle>
          <DialogDescription>
            Models listed here will return 403 for this member, regardless of which API key they use.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3 py-2">
          <ModelMultiSelect
            value={selected}
            onChange={setSelected}
            placeholder="Select models to deny…"
            rows={rows}
            isLoadingOverride={projectModels.isLoading}
            disabled={hasNoModels}
          />
          {hasNoModels && (
            <p className="text-xs text-muted-foreground">
              This project has no models configured yet. Add channel routes first.
            </p>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button onClick={handleSave} disabled={updateMember.isPending || unchanged}>
            {updateMember.isPending ? "Saving…" : "Save"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
```

Note about imports:
- `APIError` is the existing error class in `dashboard/src/api/client.ts`. If your editor flags it as missing, confirm with `grep -n "export class APIError\|export.*APIError" dashboard/src/api/client.ts`. If the class is named differently, adapt the catch block to whatever convention the rest of the project uses (`err instanceof Error ? err.message : "..."` is a safe fallback).
- `DialogDescription` may not be exported from `@/components/ui/dialog`. If `pnpm build` complains, check the file with `grep -n "^export" dashboard/src/components/ui/dialog.tsx`. If `DialogDescription` is missing, either (a) drop the import and render the description as a plain `<p className="text-sm text-muted-foreground">…</p>` inside the body, or (b) add a shadcn-style export. Prefer (a) — smaller change.

- [ ] **Step 2: Build**

Run: `cd /root/coding/modelserver/dashboard && pnpm build 2>&1 | tail -15`
Expected: clean. Fix any of the two notes above if needed.

- [ ] **Step 3: Commit**

```bash
cd /root/coding/modelserver
git add dashboard/src/pages/members/DeniedModelsDialog.tsx
git commit -m "feat(dashboard): DeniedModelsDialog component

Edits per-member denied_models via useUpdateMember. Uses project-
scoped useProjectModels as the option source. Save is disabled when
the selection equals the existing value, so 'no-op saves' never
hit the network.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: MembersPage — column + menu item + dialog mount

**Files:**
- Modify: `dashboard/src/pages/members/MembersPage.tsx`

- [ ] **Step 1: Update imports**

In `dashboard/src/pages/members/MembersPage.tsx`, add to the imports at the top:

After the existing `import type { ProjectMember, MemberUsage } from "@/api/types";` add:
```ts
import { DeniedModelsDialog } from "./DeniedModelsDialog";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
```

(`TooltipProvider` is already mounted in `dashboard/src/App.tsx` — don't re-mount it here.)

- [ ] **Step 2: Add Dialog state**

Find the existing `// Quota dialog state` block (around line 54-58):
```ts
  // Quota dialog state
  const [showQuota, setShowQuota] = useState(false);
  const [quotaTarget, setQuotaTarget] = useState<ProjectMember | null>(null);
  const [quotaValue, setQuotaValue] = useState<string>("100");
  const [removeQuota, setRemoveQuota] = useState(false);
```

Immediately after it, add:
```ts
  // Denied-models dialog state
  const [showDenied, setShowDenied] = useState(false);
  const [deniedTarget, setDeniedTarget] = useState<ProjectMember | null>(null);

  function openDeniedDialog(m: ProjectMember) {
    setDeniedTarget(m);
    setShowDenied(true);
  }
```

- [ ] **Step 3: Add the new column**

Find the existing `Joined` column (around line 200-202):
```ts
    {
      header: "Joined",
      accessor: (m) => new Date(m.created_at).toLocaleDateString(),
    },
```

Insert this BEFORE the Joined column (so order is: Usage → Denied Models → Joined → Actions):

```ts
    {
      header: "Denied Models",
      accessor: (m) => {
        const denied = m.denied_models ?? [];
        if (denied.length === 0) {
          return <span className="text-xs text-muted-foreground">—</span>;
        }
        const MAX = 10;
        const overflow = denied.length - MAX;
        const lines = denied.slice(0, MAX);
        return (
          <Tooltip>
            <TooltipTrigger
              render={
                <span className="cursor-default text-xs tabular-nums text-muted-foreground">
                  {denied.length} denied
                </span>
              }
            />
            <TooltipContent>
              <div className="space-y-0.5 text-left">
                {lines.map((m) => (
                  <div key={m} className="font-mono text-[11px]">{m}</div>
                ))}
                {overflow > 0 && (
                  <div className="text-[11px] opacity-70">… +{overflow} more</div>
                )}
              </div>
            </TooltipContent>
          </Tooltip>
        );
      },
      className: "w-32",
    },
```

- [ ] **Step 4: Add the Actions menu item**

Find the existing "Set Quota" menu item (around line 225-231):
```ts
            {canManageQuota &&
              m.role !== "owner" &&
              m.user_id !== currentUser?.id && (
                <DropdownMenuItem onClick={() => openQuotaDialog(m)}>
                  Set Quota
                </DropdownMenuItem>
              )}
```

Immediately after it, add:
```ts
            {canManageQuota && m.role !== "owner" && (
              <DropdownMenuItem onClick={() => openDeniedDialog(m)}>
                Manage Denied Models
              </DropdownMenuItem>
            )}
```

Note: this gate is intentionally narrower than the Quota gate — it does NOT exclude `m.user_id === currentUser?.id`. Per the spec (Q1), maintainers/owners can configure their own denylist; the backend treats the constraint uniformly across roles, only the write side is gated.

- [ ] **Step 5: Mount the Dialog**

Find the closing `</Dialog>` of the existing Set Quota Dialog (around line 389):
```tsx
        </DialogContent>
      </Dialog>
```
(There are multiple `</Dialog>` tags in this file; this one closes the Set Quota Dialog — the one whose title is "Set Quota for…".)

Immediately after that closing `</Dialog>`, add:
```tsx

      {/* Denied Models Dialog */}
      {deniedTarget && (
        <DeniedModelsDialog
          open={showDenied}
          onOpenChange={(o) => {
            setShowDenied(o);
            if (!o) setDeniedTarget(null);
          }}
          projectId={projectId}
          member={deniedTarget}
        />
      )}
```

- [ ] **Step 6: Build**

Run: `cd /root/coding/modelserver/dashboard && pnpm build 2>&1 | tail -15`
Expected: clean.

- [ ] **Step 7: Commit**

```bash
cd /root/coding/modelserver
git add dashboard/src/pages/members/MembersPage.tsx
git commit -m "feat(dashboard): show + edit denied_models on Members page

- New 'Denied Models' column with count + tooltip listing names.
- 'Manage Denied Models' item in the Actions menu, gated to
  owner/maintainer (no self-edit exclusion — backend treats the
  constraint uniformly).
- DeniedModelsDialog mounts alongside the existing Set Quota Dialog.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Manual verification + PR

- [ ] **Step 1: Run the full build one more time**

```
cd /root/coding/modelserver/dashboard
pnpm build 2>&1 | tail -20
```
Expected: clean. No TypeScript errors, no Vite errors.

- [ ] **Step 2: Optional smoke run**

If a local backend instance is available, start the dev server:
```
cd /root/coding/modelserver/dashboard && pnpm dev
```
Walk through the manual checklist from the spec (Section "Testing"):

1. As maintainer, see new "Denied Models" column; empty cells show `—`.
2. Actions → "Manage Denied Models" opens the Dialog; project models load in the MultiSelect.
3. Pick 2 models → Save → toast + cell shows `2 denied` + hover shows names.
4. Reopen → prior selection pre-checked.
5. Clear all → Save → cell back to `—`.
6. On a project with no model routes → empty-state hint visible, Save disabled.
7. As developer → Actions menu lacks "Manage Denied Models".
8. On an owner row → Actions menu lacks "Manage Denied Models".

If no local backend, skip — the user will run these against their own environment after merge.

- [ ] **Step 3: Push branch**

```
cd /root/coding/modelserver
git push -u origin feat/member-model-denylist-frontend
```

- [ ] **Step 4: Open PR**

```bash
cd /root/coding/modelserver
gh pr create --base main --title "feat(dashboard): per-member model denylist UI" --body "$(cat <<'EOF'
Dashboard companion to PR #32. Adds a read-only "Denied Models" column
to the Members page and a Dialog-driven editor in the Actions menu,
gated to owner / maintainer.

## Changes
- `ProjectMember.denied_models: string[]` added to the type
  (backend always serializes this as an array).
- `useUpdateMember` accepts `denied_models` with tri-state semantics
  (undefined unchanged / `[]` clear / `[...]` replace). Body is built
  conditionally so undefined fields never serialize as JSON `null`.
- `ModelMultiSelect` accepts an optional pre-fetched `rows` list +
  loading flag, so the new Dialog can use `useProjectModels` instead
  of the admin-scoped `useModels`. Rows lacking `status` (project-
  scoped shape) are treated as active for the visual grouping.
- New `DeniedModelsDialog` component edits the denylist via
  `useUpdateMember`. Save is disabled when selection equals current
  value, so noop saves never hit the network.
- Members page: new "Denied Models" column shows count + tooltip
  (first 10 names + overflow); "Manage Denied Models" item in the
  Actions menu.

## Permission gate
- Column is visible to everyone (read-only).
- Menu item only shows when `canManageQuota && m.role !== "owner"`.
  Unlike Quota, there is NO `m.user_id !== currentUser?.id` exclusion
  — admins can configure their own denylist (backend treats the
  constraint uniformly across roles per the design spec, only the
  write side is gated).
- Owner rows do not expose the editor. Owners who need to deny
  themselves a model can call the API directly.

## Spec
`docs/superpowers/specs/2026-06-04-member-model-denylist-frontend-design.md`

## Out of scope (per spec)
- No `denied_models` in the Add Member dialog (configure post-create).
- No bulk editor.
- No "show full catalog" toggle in the Dialog.
- No frontend tests (no test framework in dashboard).
- No i18n.

## Verification
Manual checklist in the spec under "Testing". Build (`pnpm build`)
passes cleanly.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 5: Report PR URL**

After `gh pr create` returns, capture the URL printed to stdout for handoff.

---

## Self-Review

**Spec coverage:**
- Column with count + tooltip → Task 5 Step 3 ✓
- "Manage Denied Models" menu item, gated to owner/maintainer, hidden on owner rows → Task 5 Step 4 ✓
- Standalone Dialog with ModelMultiSelect + Save/Cancel → Task 4 ✓
- Save calls useUpdateMember with denied_models → Task 4 + Task 2 ✓
- Empty-project hint, Save disabled when unchanged → Task 4 ✓
- Toast on success/error → Task 4 ✓
- Type addition → Task 1 ✓
- Mutation input extension with tri-state semantics → Task 2 ✓
- Use useProjectModels as catalog source → Task 4 ✓ (via Task 3's rows override)
- No frontend tests (matches non-goal) → Task 6 manual checklist ✓
- No i18n (matches non-goal) — all inline English ✓

**Placeholder scan:** No "TBD", no "TODO", no "fill in details". Two "if the editor flags X" notes in Task 4 Step 1 give concrete fallback instructions rather than vague suggestions. The conditional-hook reasoning in Task 3 Step 2 walks through the wrong-then-right approach explicitly — kept that explanation to prevent the implementer from "fixing" the always-call by adding `enabled: false` (which is wrong because that just disables fetching, not the hook call).

**Type consistency:**
- `denied_models: string[]` (required) — same name, same shape in types.ts, members.ts mutation input, Dialog props, MembersPage column accessor. ✓
- `ProjectMember` has `user?: User` already; Dialog uses `member.user?.nickname || member.user?.email || member.user_id`. ✓
- `useUpdateMember` mutation arg key is `denied_models` (snake_case), consistent with backend JSON. ✓
- `useProjectModels` returns `DataResponse<ProjectModel[]>`; the cast to `ModelListRow[]` in Task 4 is documented and safe because the combobox only reads overlapping fields. ✓
- `ModelMultiSelect` new props: `rows?: ModelListRow[]` and `isLoadingOverride?: boolean`. Same names appear in `ComboboxShell` and at the Dialog call site. ✓

**Risks flagged for implementer:**
- The `as unknown as ModelListRow[]` cast in Task 4 is the only place where types are bypassed. A cleaner long-term fix is to narrow `ComboboxShell`'s `rows` type to a `Pick<ModelListRow, "name" | "display_name" | "aliases" | "status">` that ProjectModel can satisfy directly. Not done here to keep the diff small.
- `DialogDescription` may not exist in the project's dialog primitive. Task 4 Step 1's note tells the implementer how to handle either case.
