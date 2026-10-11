import { createFileRoute, Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { runQuery, shouldPollRun } from "@/api";
import { SkeletonCard } from "@/components/common/SkeletonCard";
import { Button } from "@/components/ui/button";
import { WorkflowDiagnostics } from "@/components/runs/WorkflowDiagnostics";
import { RunsRoute } from "./_authenticated.runs.index";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/runs/$runId")({
  validateSearch,
  // Prefetch keeps request errors in the query so the page can offer Retry.
  loader: ({ context, params }) => context.queryClient.prefetchQuery(runQuery(params.runId)),
  component: RunDetailRoute,
});

function RunDetailRoute() {
  const { runId } = Route.useParams();
  const { q } = Route.useSearch();
  const run = useQuery({
    ...runQuery(runId),
    retryOnMount: false,
    refetchInterval: (query) => query.state.status !== "error" && shouldPollRun(query.state.data) ? 3_000 : false,
  });
  if (run.isPending) return <SkeletonCard rows={4} />;
  const back = <Button asChild variant="outline"><Link to="/runs" search={true}>Back to runs</Link></Button>;
  if (run.isError && !run.data) {
    return <section className="space-y-3">
      <p role="alert">Unable to load run. Try again.</p>
      <div className="flex flex-wrap gap-2">
        <Button onClick={() => void run.refetch()} disabled={run.isFetching}>Retry</Button>
        {back}
      </div>
    </section>;
  }
  if (!run.data) {
    return <section className="space-y-3">
      <h2 className="text-base font-semibold">Run unavailable</h2>
      <p className="text-sm text-muted-foreground">This run does not exist or you do not have access to it.</p>
      {back}
    </section>;
  }
  return <><RunsRoute runId={runId} q={q} /><div className="mt-4"><WorkflowDiagnostics run={run.data} /></div></>;
}
