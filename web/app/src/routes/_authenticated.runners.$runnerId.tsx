import { createFileRoute, Link } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { principalQuery, queryKeys, revokeRunnerToken, runnerQuery, type RunnerDetail, type RunnerListResponse } from "@/api";
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

function BackToRunners() {
  return <Button asChild variant="outline" className="w-fit"><Link to="/runners" search={true}>Back to runners</Link></Button>;
}

function RunnerDetailRoute() {
  const { runnerId } = Route.useParams();
  const client = useQueryClient();
  const runner = useQuery({
    ...runnerQuery(runnerId),
    retryOnMount: false,
    // Heartbeat age changes even when a successful refresh returns identical data.
    notifyOnChangeProps: "all",
    refetchInterval: (query) => query.state.status === "error" ? false : 10_000,
  });
  const principal = useQuery({ ...principalQuery(), retryOnMount: false });
  const failedQueries = [runner, principal].filter((query) => query.isError);
  const error = failedQueries.length || (!runner.isPending && !runner.data) ? <section className="space-y-3">
    <p role="alert">Runner is unavailable.</p>
    <div className="flex flex-wrap gap-2">
      {failedQueries.length ? <Button disabled={failedQueries.some((query) => query.isFetching)} onClick={() => { for (const query of failedQueries) void query.refetch(); }}>Retry</Button> : null}
      <BackToRunners />
    </div>
  </section> : null;
  if (error && (!runner.data || !principal.data)) return error;
  if (runner.isPending || principal.isPending) return <section className="space-y-3"><BackToRunners /><p role="status">Loading runner...</p><SkeletonCard rows={4} /></section>;
  async function revokeCredential() {
    const revoked = await revokeRunnerToken({ runner_id: runnerId });
    client.setQueryData<RunnerDetail>(queryKeys.runner(runnerId), (cached) => ({ ...cached, ...revoked }));
    client.setQueriesData<RunnerListResponse>({ queryKey: ["runners"] }, (cached) => cached ? {
      ...cached,
      items: cached.items.map((item) => item.id === revoked.id ? revoked : item),
    } : cached);
    await Promise.all([runner.refetch(), client.invalidateQueries({ queryKey: ["runners"] })]);
  }

  return <div className="grid min-w-0 gap-4">
    {error ?? <BackToRunners />}
    <RunnerDetailView
      controlsDisabled={failedQueries.length > 0}
      runner={runner.data}
      canAdmin={(principal.data?.roles ?? []).some((role) => role === "system_admin" || role === "runner_admin")}
      onRevoke={revokeCredential}
    />
  </div>;
}
