import { useParams } from "react-router";

export function useCurrentProject() {
  const { projectId } = useParams<{ projectId: string }>();
  return projectId!;
}
