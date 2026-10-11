import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { createRunnerEnrollment, principalQuery, runnersQuery } from "@/api";
import { RunnersView } from "@/pages/RunnersView";
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
    <RunnersView
      controlsDisabled={failedQueries.length > 0}
      runners={runners.data?.items ?? []}
      q={q}
      canAdmin={canAdmin}
      onEnroll={async (input) => {
        const created = await createRunnerEnrollment(input);
        await runners.refetch();
        return created;
      }}
    />
    <PageNavigation page={runners.data} label="runners" loading={runners.isFetching} onFirst={page.first} onPrevious={page.previous} onNext={page.next} />
  </>;
}
