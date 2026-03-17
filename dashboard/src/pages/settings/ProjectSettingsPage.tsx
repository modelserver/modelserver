import { useState, useEffect, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { useCurrentProject } from "@/hooks/useCurrentProject";
import { useProject, useUpdateProject, useArchiveProject, useUnarchiveProject } from "@/api/projects";
import { PageHeader } from "@/components/layout/PageHeader";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
  DialogDescription,
} from "@/components/ui/dialog";

export function ProjectSettingsPage() {
  const projectId = useCurrentProject();
  const navigate = useNavigate();
  const { data } = useProject(projectId);
  const updateProject = useUpdateProject(projectId);
  const archiveProject = useArchiveProject(projectId);
  const unarchiveProject = useUnarchiveProject(projectId);
  const [showArchive, setShowArchive] = useState(false);

  const project = data?.data;
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");

  const isArchived = project?.status === "archived";

  useEffect(() => {
    if (project) {
      setName(project.name);
      setDescription(project.description ?? "");
    }
  }, [project]);

  async function handleUpdate(e: FormEvent) {
    e.preventDefault();
    await updateProject.mutateAsync({ name, description: description || undefined });
  }

  async function handleArchive() {
    await archiveProject.mutateAsync();
    navigate("/projects");
  }

  async function handleUnarchive() {
    await unarchiveProject.mutateAsync();
  }

  if (!project) {
    return <p className="text-muted-foreground">Loading...</p>;
  }

  return (
    <div className="space-y-6 max-w-lg">
      <PageHeader title="Project Settings" />

      <Card>
        <CardHeader>
          <CardTitle className="text-base">General</CardTitle>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleUpdate} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="proj-name">Project Name</Label>
              <Input
                id="proj-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="proj-desc">Description</Label>
              <Input
                id="proj-desc"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </div>
            <Button type="submit" disabled={updateProject.isPending}>
              {updateProject.isPending ? "Saving..." : "Save Changes"}
            </Button>
          </form>
        </CardContent>
      </Card>

      <Separator />

      {isArchived ? (
        <Card className="border-blue-500/50">
          <CardHeader>
            <CardTitle className="text-base">Archived</CardTitle>
            <CardDescription>
              This project is archived. API requests are disabled. Unarchive to restore full access.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Button onClick={handleUnarchive} disabled={unarchiveProject.isPending}>
              {unarchiveProject.isPending ? "Unarchiving..." : "Unarchive Project"}
            </Button>
          </CardContent>
        </Card>
      ) : (
        <Card className="border-destructive/50">
          <CardHeader>
            <CardTitle className="text-base text-destructive-foreground">Danger Zone</CardTitle>
            <CardDescription>
              Archive this project to disable all API access. You can unarchive it later.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Button variant="destructive" onClick={() => setShowArchive(true)}>
              Archive Project
            </Button>
          </CardContent>
        </Card>
      )}

      <Dialog open={showArchive} onOpenChange={setShowArchive}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Archive Project</DialogTitle>
            <DialogDescription>
              Are you sure you want to archive &quot;{project.name}&quot;? All API requests will be disabled. You can unarchive it later.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowArchive(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={handleArchive}
              disabled={archiveProject.isPending}
            >
              {archiveProject.isPending ? "Archiving..." : "Archive"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
