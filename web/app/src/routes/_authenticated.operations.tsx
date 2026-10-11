import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { operationsStatusQuery, principalQuery, runLogRetentionStatusQuery, useRunLogRetentionMutations } from "@/api";
import { OperationsView } from "@/pages/OperationsView";
import { Button } from "@/components/ui/button";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/operations")({ validateSearch, loader: ({ context }) => context.queryClient.ensureQueryData(principalQuery()), component: OperationsRoute });
function OperationsRoute() {
  const principal = useQuery(principalQuery());
  const canAdmin = (principal.data?.roles ?? []).includes("system_admin");
  const operations = useQuery({ ...operationsStatusQuery(), enabled: canAdmin, retryOnMount: false });
  const retention = useQuery({ ...runLogRetentionStatusQuery(), enabled: canAdmin, retryOnMount: false });
  const mutations = useRunLogRetentionMutations();
  const failed = canAdmin ? [operations, retention].filter((query) => query.isError) : [];
  const recovery = failed.length ? <section className="space-y-3">
    <p role="alert">Unable to refresh operations data.</p>
    <Button disabled={failed.some((query) => query.isFetching)} onClick={() => { for (const query of failed) void query.refetch(); }}>Retry operations</Button>
  </section> : null;
  return <div className="space-y-4">{recovery}<OperationsView status={operations.data} loading={principal.isPending || (canAdmin && operations.isPending)} unavailable={operations.isError} canAdmin={canAdmin} retention={retention.data} retentionLoading={canAdmin && retention.isPending} retentionUnavailable={retention.isError} retentionBusy={mutations.update.isPending || mutations.execute.isPending} retentionExecution={mutations.execute.data} onRetentionUpdate={(input) => mutations.update.mutate(input)} onRetentionExecute={(policyVersion, requestID) => mutations.execute.mutate({ policyVersion, requestID })} /></div>;
}
