import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { projectsQuery, repositoriesQuery, runsQuery, templatesQuery } from "@/api";
import { apiSnapshot, useSnapshotMutation } from "@/api/compat";
import { ProjectsView } from "@/pages/ProjectsView";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/projects")({
  validateSearch,
  loader: ({ context }) => Promise.all([context.queryClient.ensureQueryData(projectsQuery()), context.queryClient.ensureQueryData(repositoriesQuery())]),
  component: ProjectsRoute,
});

function ProjectsRoute() {
  const projects = useQuery(projectsQuery());
  const repositories = useQuery(repositoriesQuery());
  const templates = useQuery(templatesQuery());
  const runs = useQuery(runsQuery());
  const { busy, mutate } = useSnapshotMutation();
  const queries = [projects, repositories, templates, runs];
  const error = queries.find((query) => query.isError)?.error;
  if (error) return <p role="alert">{error.message}</p>;
  return <ProjectsView snapshot={apiSnapshot({ projects: projects.data, repositories: repositories.data, templates: templates.data, runs: runs.data })} token="" busy={busy} mutate={mutate} loading={queries.some((query) => query.isPending)} />;
}
