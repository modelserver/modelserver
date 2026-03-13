import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams } from "react-router";
import { api } from "@/api/client";
import type { ListResponse, Project } from "@/api/types";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

export function ProjectSwitcher() {
  const navigate = useNavigate();
  const { projectId } = useParams<{ projectId: string }>();

  const { data } = useQuery({
    queryKey: ["projects"],
    queryFn: () =>
      api.get<ListResponse<Project>>("/api/v1/projects?per_page=100"),
  });

  const projects = data?.data ?? [];

  if (projects.length === 0) return null;

  const currentProject = projects.find((p) => p.id === projectId);

  return (
    <Select
      value={projectId ?? ""}
      onValueChange={(id) => { if (id) navigate(`/projects/${id}`); }}
    >
      <SelectTrigger className="w-full">
        <SelectValue placeholder="Select project">
          {currentProject?.name ?? "Select project"}
        </SelectValue>
      </SelectTrigger>
      <SelectContent>
        {projects.map((p) => (
          <SelectItem key={p.id} value={p.id}>
            {p.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
