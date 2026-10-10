import { createFileRoute } from "@tanstack/react-router";
import { useQueries, useQuery } from "@tanstack/react-query";
import { allProjectsQuery, allTemplatesQuery, approvalsQuery, runQuery } from "@/api";
import { apiSnapshot, useSnapshotMutation } from "@/api/compat";
import { PageNavigation, usePage } from "@/components/common/PageNavigation";
import { ApprovalsView } from "@/pages/ApprovalsView";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/approvals")({
  validateSearch,
  loader: ({ context }) => Promise.all([context.queryClient.ensureQueryData(approvalsQuery({ limit: 50, offset: 0 })), context.queryClient.ensureQueryData(allProjectsQuery()), context.queryClient.ensureQueryData(allTemplatesQuery())]),
  component: ApprovalsRoute,
});

function ApprovalsRoute() {
  const page = usePage();
  const approvals = useQuery(approvalsQuery({ limit: page.limit, offset: page.offset }));
  const runQueries = useQueries({ queries: (approvals.data?.items ?? []).map((approval) => runQuery(approval.run_id)) });
  const projects = useQuery(allProjectsQuery());
  const templates = useQuery(allTemplatesQuery());
  const { busy, mutate } = useSnapshotMutation();
  const queries = [approvals, projects, templates, ...runQueries];
  const error = queries.find((query) => query.isError)?.error;
  if (error) return <p role="alert">{error.message}</p>;
  const runs = runQueries.flatMap((query) => query.data ? [query.data] : []);
  return <><ApprovalsView snapshot={apiSnapshot({ approvals: approvals.data, runs, projects: projects.data, templates: templates.data })} token="" busy={busy} mutate={mutate} loading={queries.some((query) => query.isPending)} /><PageNavigation page={approvals.data} label="approvals" loading={approvals.isFetching} onPrevious={page.previous} onNext={page.next} /></>;
}
