import { createFileRoute, Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { allProjectMembersQuery, allEnvironmentsQuery, principalQuery, allRevisionsQuery, useSelectedRunLogsPollingQuery, shouldPollRun, runQuery, allServicesQuery, useDeploymentMutations, useDeploymentPollingQuery, deploymentHistoryQuery } from "@/api";
import { DeploymentDetailView } from "@/pages/DeploymentsView";
import { canMaintainProject } from "@/lib/permissions";
import { validateSearch } from "@/router/search";
import { Button } from "@/components/ui/button";
import { SkeletonCard } from "@/components/common/SkeletonCard";

export const Route = createFileRoute("/_authenticated/deployments/$deploymentId")({
  validateSearch,
  // Keep an inaccessible or deleted deployment in the route-local query state
  // rather than throwing from the loader. That gives the detail view the same
  // intentionally non-enumerating unavailable state for both cases.
  loader: ({ context }) => context.queryClient.ensureQueryData(principalQuery()),
  errorComponent: DeploymentUnavailable,
  component: DeploymentDetailRoute,
});

function DeploymentUnavailable() {
  return <section className="space-y-3"><p role="alert">Deployment is unavailable or you do not have access.</p><BackToDeployments /></section>;
}

function BackToDeployments() {
  return <Button asChild variant="outline"><Link to="/deployments" search={true}>Back to deployments</Link></Button>;
}

function DeploymentDetailRoute() {
  const { deploymentId } = Route.useParams();
  const deployment = useDeploymentPollingQuery(deploymentId);
  const environments = useQuery(allEnvironmentsQuery());
  const environment = environments.data?.find((item) => item.id === deployment.data?.environment_id);
  const services = useQuery(allServicesQuery());
  const service = services.data?.find((item) => item.id === environment?.service_id);
  const revisions = useQuery({ ...allRevisionsQuery(environment?.service_id ?? ""), enabled: Boolean(environment) });
  const history = useQuery(deploymentHistoryQuery(deployment.data?.environment_id ?? ""));
  const principal = useQuery(principalQuery());
  const members = useQuery(allProjectMembersQuery());
  const mutation = useDeploymentMutations();
  const revision = revisions.data?.find((item) => item.id === deployment.data?.desired_revision_id);
  const previousRevision = revisions.data?.find((item) => item.id === deployment.data?.previous_healthy_revision_id);
  const related = (history.data ?? []).filter((item) => item.id !== deploymentId && (item.rollback_of_id === deploymentId || deployment.data?.rollback_of_id === item.id || (item.rollback_of_id && item.rollback_of_id === deployment.data?.rollback_of_id)));
  const runs = useQuery({ ...runQuery(deployment.data?.task_run_id ?? ""), enabled: Boolean(deployment.data?.task_run_id), retryOnMount: false, refetchInterval: (query) => query.state.status !== "error" && shouldPollRun(query.state.data) ? 3_000 : false });
  const taskRun = runs.data ?? undefined;
  const logs = useSelectedRunLogsPollingQuery(taskRun?.id ?? "", shouldPollRun(taskRun));
  const failedQueries = [deployment, environments, services, revisions, history, principal, members, runs].filter((query) => query.isError);
  if (deployment.isPending) return <section className="space-y-3"><p role="status">Loading deployment...</p><SkeletonCard rows={4} /></section>;
  if (!deployment.isError && !deployment.data) return <DeploymentUnavailable />;
  const hasLoadedData = Boolean(deployment.data) && failedQueries.every((query) => query.data !== undefined);
  const error = failedQueries.length ? <section className="space-y-3">
    <p role="alert">{hasLoadedData ? "Unable to refresh deployment. Showing previously loaded data." : "Unable to load deployment. Try again."}</p>
    <div className="flex flex-wrap gap-2">
      <Button disabled={failedQueries.some((query) => query.isFetching)} onClick={() => { for (const query of failedQueries) void query.refetch(); }}>Retry</Button>
      <BackToDeployments />
    </div>
  </section> : null;
  if (error && !hasLoadedData) return error;
  const canOperate = canMaintainProject(principal.data, members.data ?? [], service?.project_id);
  const confirm = mutation.confirm.variables?.id === deploymentId ? mutation.confirm : undefined;
  const cancel = mutation.cancel.variables?.deployment_id === deploymentId ? mutation.cancel : undefined;
  const pendingAction = confirm?.isPending ? "confirm" : cancel?.isPending ? "cancel" : undefined;
  const actionError = confirm?.error ? `Unable to confirm deployment: ${confirm.error.message}`
    : cancel?.error ? `Unable to request cancellation: ${cancel.error.message}` : undefined;
  return <div className="grid min-w-0 gap-4">{error}<DeploymentDetailView deployment={deployment.data ?? undefined} environment={environment} service={service} revision={revision} previousRevision={previousRevision} related={related} taskRun={taskRun} logs={logs.data} logsError={logs.error} logsLoading={logs.isPending} logsRetrying={logs.isFetching} onRetryLogs={() => void logs.retry()} canOperate={canOperate} pendingAction={pendingAction} actionError={actionError} busy={failedQueries.length > 0 || mutation.confirm.isPending || mutation.cancel.isPending} onConfirm={(id) => { mutation.cancel.reset(); mutation.confirm.mutate({ id }, { onSuccess: () => toast.success("Deployment confirmed") }); }} onCancel={(input) => { mutation.confirm.reset(); mutation.cancel.mutate(input, { onSuccess: () => toast.success("Cancellation requested") }); }} /></div>;
}
