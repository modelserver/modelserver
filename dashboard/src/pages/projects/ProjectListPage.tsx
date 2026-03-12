import { Link } from "react-router";
import { useProjects } from "@/api/projects";
import { PageHeader } from "@/components/layout/PageHeader";
import { buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { StatusBadge } from "@/components/shared/StatusBadge";
import { Plus } from "lucide-react";

export function ProjectListPage() {
  const { data, isLoading } = useProjects();
  const projects = data?.data ?? [];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Projects"
        description="Manage your API projects"
        actions={
          <Link to="/projects/new" className={buttonVariants()}>
            <Plus className="mr-2 h-4 w-4" />
            New Project
          </Link>
        }
      />

      {isLoading ? (
        <p className="text-muted-foreground">Loading projects...</p>
      ) : projects.length === 0 ? (
        <Card>
          <CardContent className="flex flex-col items-center justify-center py-12">
            <p className="text-muted-foreground">No projects yet</p>
            <Link to="/projects/new" className={buttonVariants({ className: "mt-4" })}>
              Create your first project
            </Link>
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {projects.map((project) => (
            <Link key={project.id} to={`/projects/${project.id}`}>
              <Card className="transition-colors hover:bg-accent/50">
                <CardHeader>
                  <div className="flex items-center justify-between">
                    <CardTitle className="text-base">{project.name}</CardTitle>
                    <StatusBadge status={project.status} />
                  </div>
                  {project.description && (
                    <CardDescription className="line-clamp-2">
                      {project.description}
                    </CardDescription>
                  )}
                </CardHeader>
                <CardContent>
                  <p className="text-xs text-muted-foreground">
                    Created {new Date(project.created_at).toLocaleDateString()}
                  </p>
                </CardContent>
              </Card>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}
