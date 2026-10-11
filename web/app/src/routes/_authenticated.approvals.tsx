import { createFileRoute } from "@tanstack/react-router";
import { useQueries, useQuery } from "@tanstack/react-query";
import { allProjectMembersQuery, principalQuery, allProjectsQuery, allTemplatesQuery, approvalsQuery, runQuery } from "@/api";
import { apiSnapshot, useSnapshotMutation } from "@/api/compat";
import { PageNavigation, usePage } from "@/components/common/PageNavigation";
import { ApprovalsView } from "@/pages/ApprovalsView";
import { canMaintainProject } from "@/lib/permissions";
import { Button } from "@/components/ui/button";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/approvals")({
  validateSearch,
  loaderDeps: ({ search }) => ({ offset: search.offset ?? 0 }),
  loader: ({ context, deps }) => Promise.all([context.queryClient.prefetchQuery(approvalsQuery({ limit: 50, offset: deps.offset })), context.queryClient.prefetchQuery(allProjectsQuery()), context.queryClient.prefetchQuery(allTemplatesQuery())]),
  component: ApprovalsRoute,
});

function ApprovalsRoute() {
  const { q } = Route.useSearch();
  const page = usePage();
  const approvals = useQuery({ ...approvalsQuery({ limit: page.limit, offset: page.offset }), retryOnMount: false });
  const runQueries = useQueries({ queries: (approvals.data?.items ?? []).map((approval) => runQuery(approval.run_id)) });
  const projects = useQuery({ ...allProjectsQuery(), retryOnMount: false });
  const templates = useQuery({ ...allTemplatesQuery(), retryOnMount: false });
  const { busy, mutate } = useSnapshotMutation();
  const principal = useQuery({ ...principalQuery(), retryOnMount: false });
  const members = useQuery({ ...allProjectMembersQuery(), retryOnMount: false });
  const queries = [approvals, projects, templates, principal, members, ...runQueries];
  const failed = queries.filter((query) => query.isError);
  const runs = runQueries.flatMap((query) => query.data ? [query.data] : []);
  return <div className="grid min-w-0 gap-4">
    {failed.length > 0 ? <section className="space-y-3">
      <p role="alert">{approvals.data ? "Unable to refresh approval information. Decisions are unavailable until retry succeeds." : "Unable to load approvals."}</p>
      <Button disabled={failed.some((query) => query.isFetching)} onClick={() => { for (const query of failed) void query.refetch(); }}>Retry approvals</Button>
    </section> : null}
    {approvals.data || !approvals.isError ? <ApprovalsView
      q={q} snapshot={apiSnapshot({ approvals: approvals.data, runs, projects: projects.data, templates: templates.data })}
      token="" busy={failed.length > 0 ? "refresh-unavailable" : busy} mutate={mutate}
      canOperateProject={(projectID) => canMaintainProject(principal.data, members.data ?? [], projectID)}
      loading={queries.some((query) => query.isPending)}
    /> : null}
    <PageNavigation page={approvals.data ?? { count: 0, total: 0, limit: page.limit, offset: page.offset }} label="approvals" loading={approvals.isFetching} onFirst={page.first} onPrevious={page.previous} onNext={page.next} />
  </div>;
}
