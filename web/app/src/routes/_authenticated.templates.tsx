import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { allProjectMembersQuery, principalQuery, allProjectsQuery, templatesQuery } from "@/api";
import { apiSnapshot } from "@/api/compat";
import { useTemplateActions } from "@/api/mutations";
import { PageNavigation, usePage } from "@/components/common/PageNavigation";
import { TemplatesView } from "@/pages/TemplatesView";
import { canMaintainProject } from "@/lib/permissions";
import { Button } from "@/components/ui/button";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/templates")({
  validateSearch,
  loaderDeps: ({ search }) => ({ offset: search.offset ?? 0 }),
  loader: ({ context, deps }) => Promise.all([context.queryClient.prefetchQuery(allProjectsQuery()), context.queryClient.prefetchQuery(templatesQuery({ limit: 50, offset: deps.offset }))]),
  component: TemplatesRoute,
});

function TemplatesRoute() {
  const { q } = Route.useSearch();
  const navigate = Route.useNavigate();
  const page = usePage();
  const projects = useQuery({ ...allProjectsQuery(), retryOnMount: false });
  const templates = useQuery({ ...templatesQuery({ limit: page.limit, offset: page.offset }), retryOnMount: false });
  const principal = useQuery({ ...principalQuery(), retryOnMount: false });
  const members = useQuery({ ...allProjectMembersQuery(), retryOnMount: false });
  const queries = [projects, templates, principal, members];
  const failed = queries.filter((query) => query.isError);
  const { busy, actions } = useTemplateActions(failed.length > 0);
  const recovery = failed.length > 0 ? <section className="space-y-3">
    <p role="alert">{templates.data ? "Unable to refresh templates. Your work is preserved; retry before saving or running a template." : "Unable to load templates."}</p>
    <Button type="button" disabled={failed.some((query) => query.isFetching)} onClick={() => { for (const query of failed) void query.refetch(); }}>Retry templates</Button>
  </section> : null;
  return <div className="grid min-w-0 gap-4">
    {recovery}
    {templates.data || !templates.isError ? <TemplatesView
      canOperateProject={(projectID) => canMaintainProject(principal.data, members.data ?? [], projectID)}
      onRunRequested={(runId) => void navigate({ to: "/runs/$runId", params: { runId }, search: {} })}
      q={q} snapshot={apiSnapshot({ projects: projects.data, templates: templates.data })} busy={busy} actions={actions}
      recovery={recovery} loading={queries.some((query) => query.isPending)}
    /> : null}
    <PageNavigation page={templates.data ?? { count: 0, total: 0, limit: page.limit, offset: page.offset }} label="templates" loading={templates.isFetching} onFirst={page.first} onPrevious={page.previous} onNext={page.next} />
  </div>;
}
