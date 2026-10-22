import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { allProjectMembersQuery, principalQuery, allProjectsQuery, projectsQuery, repositoriesQuery, runsQuery, templatesQuery } from "@/api";
import { apiSnapshot } from "@/api/compat";
import { useProjectActions } from "@/api/mutations";
import { PageNavigation, usePage } from "@/components/common/PageNavigation";
import { ProjectsView } from "@/pages/ProjectsView";
import { ProjectHosting } from "@/pages/ProjectHosting";
import { ProjectSecrets } from "@/pages/ProjectSecrets";
import { canMaintainProject } from "@/lib/permissions";
import { Button } from "@/components/ui/button";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/projects")({
  validateSearch,
  loaderDeps: ({ search }) => ({ offset: search.offset ?? 0 }),
  loader: ({ context, deps }) => Promise.all([context.queryClient.prefetchQuery(projectsQuery({ limit: 50, offset: deps.offset })), context.queryClient.prefetchQuery(allProjectsQuery()), context.queryClient.prefetchQuery(repositoriesQuery())]),
  component: ProjectsRoute,
});

function ProjectsRoute() {
  const { q } = Route.useSearch();
  const page = usePage();
  const projects = useQuery({ ...projectsQuery({ limit: page.limit, offset: page.offset }), retryOnMount: false });
  const allProjects = useQuery({ ...allProjectsQuery(), retryOnMount: false });
  const repositories = useQuery({ ...repositoriesQuery(), retryOnMount: false });
  const templates = useQuery({ ...templatesQuery(), retryOnMount: false });
  const runs = useQuery({ ...runsQuery(), retryOnMount: false });
  const principal = useQuery({ ...principalQuery(), retryOnMount: false });
  const members = useQuery({ ...allProjectMembersQuery(), retryOnMount: false });
  const queries = [projects, allProjects, repositories, templates, runs, principal, members];
  const failed = queries.filter((query) => query.isError);
  const { busy, error, actions } = useProjectActions(failed.length > 0);
  const recovery = failed.length > 0 ? <section className="space-y-3">
    <p role="alert">{projects.data ? "Unable to refresh project information. Your work is preserved; retry before making changes." : "Unable to load projects."}</p>
    <Button type="button" disabled={failed.some((query) => query.isFetching)} onClick={() => { for (const query of failed) void query.refetch(); }}>Retry projects</Button>
  </section> : null;
  return <div className="grid min-w-0 gap-4">
    {recovery}
    {projects.data || !projects.isError ? <ProjectsView
      canOperateProject={(projectID) => canMaintainProject(principal.data, members.data ?? [], projectID)} q={q}
      snapshot={apiSnapshot({ projects: projects.data, repositories: repositories.data, templates: templates.data, runs: runs.data })}
      formProjects={allProjects.data ?? []} busy={busy} actions={actions} actionError={error} recovery={recovery}
      loading={queries.some((query) => query.isPending)}
    /> : null}
    <PageNavigation page={projects.data ?? { count: 0, total: 0, limit: page.limit, offset: page.offset }} label="projects" loading={projects.isFetching} onFirst={page.first} onPrevious={page.previous} onNext={page.next} />
    <ProjectHosting projects={allProjects.data ?? []} unavailable={failed.length > 0} canOperateProject={(projectID) => canMaintainProject(principal.data, members.data ?? [], projectID)} />
    <ProjectSecrets projects={allProjects.data ?? []} principal={principal.data} members={members.data ?? []} unavailable={failed.length > 0} />
  </div>;
}
