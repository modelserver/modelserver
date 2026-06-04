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
  // status as active.
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
