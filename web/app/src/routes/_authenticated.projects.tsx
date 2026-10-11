import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { allProjectMembersQuery, principalQuery, allProjectsQuery, projectsQuery, repositoriesQuery, runsQuery, templatesQuery } from "@/api";
import { apiSnapshot, useSnapshotMutation } from "@/api/compat";
import { PageNavigation, usePage } from "@/components/common/PageNavigation";
import { ProjectsView } from "@/pages/ProjectsView";
import { canMaintainProject } from "@/lib/permissions";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/projects")({
  validateSearch,
  loader: ({ context }) => Promise.all([context.queryClient.ensureQueryData(projectsQuery({ limit: 50, offset: 0 })), context.queryClient.ensureQueryData(allProjectsQuery()), context.queryClient.ensureQueryData(repositoriesQuery())]),
  component: ProjectsRoute,
});

function ProjectsRoute() {
  const { q } = Route.useSearch();
  const page = usePage();
  const projects = useQuery(projectsQuery({ limit: page.limit, offset: page.offset }));
  const allProjects = useQuery(allProjectsQuery());
  const repositories = useQuery(repositoriesQuery());
  const templates = useQuery(templatesQuery());
  const runs = useQuery(runsQuery());
  const { busy, mutate } = useSnapshotMutation();
  const principal = useQuery(principalQuery());
  const members = useQuery(allProjectMembersQuery());
  const queries = [projects, allProjects, repositories, templates, runs, principal, members];
  const error = queries.find((query) => query.isError)?.error;
  if (error) return <p role="alert">{error.message}</p>;
  return <><ProjectsView canOperateProject={(projectID) => canMaintainProject(principal.data, members.data ?? [], projectID)} q={q} snapshot={apiSnapshot({ projects: projects.data, repositories: repositories.data, templates: templates.data, runs: runs.data })} formProjects={allProjects.data ?? []} token="" busy={busy} mutate={mutate} loading={queries.some((query) => query.isPending)} /><PageNavigation page={projects.data} label="projects" loading={projects.isFetching} onFirst={page.first} onPrevious={page.previous} onNext={page.next} /></>;
}
