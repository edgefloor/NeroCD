import { createFileRoute } from "@tanstack/react-router";
import { useQueries, useQuery } from "@tanstack/react-query";
import { runLogsQuery, runQuery } from "@/api";
import { LogsView } from "@/pages/LogsView";
import { PageNavigation, usePage } from "@/components/common/PageNavigation";
import { validateSearch } from "@/router/search";
import { Button } from "@/components/ui/button";

export const Route = createFileRoute("/_authenticated/logs")({
  validateSearch,
  loaderDeps: ({ search }) => ({ offset: search.offset ?? 0 }),
  loader: ({ context, deps }) => context.queryClient.prefetchQuery(runLogsQuery({ limit: 50, offset: deps.offset })),
  component: LogsRoute,
});

function LogsRoute() {
  const { q } = Route.useSearch();
  const page = usePage();
  const logs = useQuery({ ...runLogsQuery({ limit: page.limit, offset: page.offset }), retryOnMount: false });
  const runIDs = [...new Set((logs.data?.items ?? []).map((log) => log.run_id))];
  const runs = useQueries({ queries: runIDs.map((id) => runQuery(id)) });
  const failedRuns = runs.filter((query) => query.isError);

  return <div className="grid min-w-0 gap-4">
    {logs.isError ? <section className="space-y-3">
      <p role="alert">{logs.data ? "Unable to refresh logs. Showing previously loaded output." : "Unable to load logs."}</p>
      <div className="flex flex-wrap gap-2">
        <Button disabled={logs.isFetching} onClick={() => void logs.refetch()}>Retry logs</Button>
        {!logs.data && page.offset > 0 ? <Button variant="outline" onClick={page.first}>First logs page</Button> : null}
      </div>
    </section> : null}
    {failedRuns.length ? <section className="space-y-3">
      <p role="alert">Some run statuses are unavailable. Log output is still available.</p>
      <Button disabled={failedRuns.some((query) => query.isFetching)} onClick={() => { for (const query of failedRuns) void query.refetch(); }}>Retry run statuses</Button>
    </section> : null}
    {logs.data || !logs.isError ? <>
      <LogsView logs={logs.data?.items ?? []} runs={runs.flatMap((query) => query.data && !query.isError ? [query.data] : [])} q={q} loading={logs.isPending} />
      <PageNavigation page={logs.data} label="logs" loading={logs.isFetching} onFirst={page.first} onPrevious={page.previous} onNext={page.next} />
    </> : null}
  </div>;
}
