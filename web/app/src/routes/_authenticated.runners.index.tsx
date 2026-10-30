import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { createRunnerEnrollment, principalQuery, runnersQuery } from "@/api";
import { RunnersView } from "@/pages/RunnersView";
import { HostingServers } from "@/pages/HostingServers";
import { PageNavigation, usePage } from "@/components/common/PageNavigation";
import { validateSearch } from "@/router/search";
import { Button } from "@/components/ui/button";
import { SkeletonTable } from "@/components/common/SkeletonCard";

export const Route = createFileRoute("/_authenticated/runners/")({
  validateSearch,
  loaderDeps: ({ search }) => ({ offset: search.offset ?? 0 }),
  loader: ({ context, deps }) => Promise.all([
    context.queryClient.prefetchQuery(runnersQuery({ limit: 50, offset: deps.offset })),
    context.queryClient.prefetchQuery(principalQuery()),
  ]),
  component: RunnersRoute,
});

function RunnersRoute() {
  const { q } = Route.useSearch();
  const [setup, setSetup] = useState<"manual" | "ssh" | "existing">("manual");
  const page = usePage();
  const runners = useQuery({
    ...runnersQuery({ limit: page.limit, offset: page.offset }),
    retryOnMount: false,
    refetchInterval: (query) => query.state.status === "error" ? false : 10_000,
  });
  const principal = useQuery({ ...principalQuery(), retryOnMount: false });
  const failedQueries = [runners, principal].filter((query) => query.isError);
  const error = failedQueries.length ? <section className="space-y-3">
    <p role="alert">Runner inventory is unavailable.</p>
    <div className="flex flex-wrap gap-2">
      <Button disabled={failedQueries.some((query) => query.isFetching)} onClick={() => { for (const query of failedQueries) void query.refetch(); }}>Retry</Button>
      {!runners.data && page.offset > 0 ? <Button variant="outline" onClick={page.first}>First runners page</Button> : null}
    </div>
  </section> : null;
  if (error && (!runners.data || !principal.data)) return error;
  if (runners.isPending || principal.isPending) return <section className="space-y-3"><p role="status">Loading runners...</p><SkeletonTable rows={5} /></section>;
  const canAdmin = (principal.data?.roles ?? []).some((role) => role === "system_admin" || role === "runner_admin");
  return <>
    {error}
    {canAdmin && <section aria-label="Server setup" className="mb-4 space-y-3">
      <h2 className="text-lg font-semibold">Connect a server</h2>
      <p className="text-sm text-muted-foreground">Choose how to connect your host.</p>
      <div className="flex flex-wrap gap-2">
        <Button variant={setup === "manual" ? "default" : "outline"} aria-pressed={setup === "manual"} onClick={() => setSetup("manual")}>Enroll manually</Button>
        <Button variant={setup === "ssh" ? "default" : "outline"} aria-pressed={setup === "ssh"} onClick={() => setSetup("ssh")}>Set up over SSH</Button>
        <Button variant={setup === "existing" ? "default" : "outline"} aria-pressed={setup === "existing"} onClick={() => setSetup("existing")}>Register existing runner</Button>
      </div>
    </section>}
    <RunnersView
      showEnrollment={setup === "manual"}
      controlsDisabled={failedQueries.length > 0}
      runners={runners.data?.items ?? []}
      q={q}
      canAdmin={canAdmin}
      onEnroll={createRunnerEnrollment}
    />
    <PageNavigation page={runners.data} label="runners" loading={runners.isFetching} onFirst={page.first} onPrevious={page.previous} onNext={page.next} />
    <HostingServers setupMode={setup} canAdmin={canAdmin} blocked={failedQueries.length > 0} />
  </>;
}
