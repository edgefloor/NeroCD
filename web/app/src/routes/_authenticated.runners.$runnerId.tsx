import { createFileRoute, Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { principalQuery, revokeRunnerToken, runnerQuery } from "@/api";
import { RunnerDetailView } from "@/pages/RunnersView";
import { Button } from "@/components/ui/button";
import { SkeletonCard } from "@/components/common/SkeletonCard";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/runners/$runnerId")({
  validateSearch,
  // Prefetch retains failures in query state so the detail page can offer recovery.
  loader: ({ context, params }) => Promise.all([
    context.queryClient.prefetchQuery(runnerQuery(params.runnerId)),
    context.queryClient.prefetchQuery(principalQuery()),
  ]),
  component: RunnerDetailRoute,
});

function RunnerDetailRoute() {
  const { runnerId } = Route.useParams();
  const runner = useQuery({ ...runnerQuery(runnerId), retryOnMount: false });
  const principal = useQuery({ ...principalQuery(), retryOnMount: false });
  const failedQueries = [runner, principal].filter((query) => query.isError);
  if (failedQueries.length || (!runner.isPending && !runner.data)) return <section className="space-y-3">
    <p role="alert">Runner is unavailable.</p>
    <div className="flex flex-wrap gap-2">
      {failedQueries.length ? <Button disabled={failedQueries.some((query) => query.isFetching)} onClick={() => { for (const query of failedQueries) void query.refetch(); }}>Retry</Button> : null}
      <Button asChild variant="outline"><Link to="/runners" search={true}>Back to runners</Link></Button>
    </div>
  </section>;
  if (runner.isPending || principal.isPending) return <section className="space-y-3"><p role="status">Loading runner...</p><SkeletonCard rows={4} /></section>;
  return <RunnerDetailView runner={runner.data} canAdmin={(principal.data?.roles ?? []).some((role) => role === "system_admin" || role === "runner_admin")} onRevoke={async () => { await revokeRunnerToken({ runner_id: runnerId }); await runner.refetch(); }} />;
}
