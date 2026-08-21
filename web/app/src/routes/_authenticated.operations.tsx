import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { operationsStatusQuery, principalQuery, runLogRetentionStatusQuery, useRunLogRetentionMutations } from "@/api";
import { OperationsView } from "@/pages/OperationsView";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/operations")({ validateSearch, loader: ({ context }) => context.queryClient.ensureQueryData(principalQuery()), component: OperationsRoute });
function OperationsRoute() {
  const principal = useQuery(principalQuery());
  const canAdmin = (principal.data?.roles ?? []).includes("system_admin");
  const operations = useQuery({ ...operationsStatusQuery(), enabled: canAdmin });
  const retention = useQuery({ ...runLogRetentionStatusQuery(), enabled: canAdmin });
  const mutations = useRunLogRetentionMutations();
  return <OperationsView status={operations.data} loading={principal.isPending || (canAdmin && operations.isPending)} unavailable={operations.isError} canAdmin={canAdmin} retention={retention.data} retentionLoading={canAdmin && retention.isPending} retentionUnavailable={retention.isError} retentionBusy={mutations.update.isPending || mutations.execute.isPending} retentionExecution={mutations.execute.data} onRetentionUpdate={(input) => mutations.update.mutate(input)} onRetentionExecute={(policyVersion, requestID) => mutations.execute.mutate({ policyVersion, requestID })} />;
}
