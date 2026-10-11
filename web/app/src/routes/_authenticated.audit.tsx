import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { auditEventsQuery } from "@/api";
import { AuditView } from "@/pages/AuditView";
import { PageNavigation, usePage } from "@/components/common/PageNavigation";
import { validateSearch } from "@/router/search";
import { Button } from "@/components/ui/button";

export const Route = createFileRoute("/_authenticated/audit")({
  validateSearch,
  loaderDeps: ({ search }) => ({ offset: search.offset ?? 0 }),
  loader: ({ context, deps }) => context.queryClient.prefetchQuery(auditEventsQuery({ limit: 50, offset: deps.offset })),
  component: AuditRoute,
});

function AuditRoute() {
  const { q } = Route.useSearch();
  const page = usePage();
  const audit = useQuery({ ...auditEventsQuery({ limit: page.limit, offset: page.offset }), retryOnMount: false });
  return <div className="grid min-w-0 gap-4">
    {audit.isError ? <section className="space-y-3">
      <p role="alert">{audit.data ? "Unable to refresh audit events. Showing previously loaded events." : "Unable to load audit events."}</p>
      <Button disabled={audit.isFetching} onClick={() => void audit.refetch()}>Retry audit events</Button>
    </section> : null}
    {audit.data || !audit.isError ? <>
      <AuditView events={audit.data?.items ?? []} q={q} loading={audit.isPending} />
      <PageNavigation page={audit.data} label="audit events" loading={audit.isFetching} onFirst={page.first} onPrevious={page.previous} onNext={page.next} />
    </> : null}
  </div>;
}
