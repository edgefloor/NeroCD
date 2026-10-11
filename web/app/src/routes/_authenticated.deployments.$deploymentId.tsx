import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { allProjectMembersQuery, allEnvironmentsQuery, principalQuery, allRevisionsQuery, useSelectedRunLogsPollingQuery, shouldPollRun, runQuery, allServicesQuery, useDeploymentMutations, useDeploymentPollingQuery, deploymentHistoryQuery } from "@/api";
import { DeploymentDetailView } from "@/pages/DeploymentsView";
import { canMaintainProject } from "@/lib/permissions";
import { validateSearch } from "@/router/search";

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
  return <p role="alert">Deployment is unavailable or you do not have access.</p>;
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
  const runs = useQuery({ ...runQuery(deployment.data?.task_run_id ?? ""), enabled: Boolean(deployment.data?.task_run_id), refetchInterval: (query) => shouldPollRun(query.state.data) ? 3_000 : false });
  const taskRun = runs.data ?? undefined;
  const logs = useSelectedRunLogsPollingQuery(taskRun?.id ?? "", shouldPollRun(taskRun));
  const error = [deployment, environments, services, revisions, history, principal, members, runs].find((query) => query.isError)?.error;
  if (error) return <p role="alert">Unable to load deployment: {error.message}</p>;
  const canOperate = canMaintainProject(principal.data, members.data ?? [], service?.project_id);
  return <DeploymentDetailView deployment={deployment.data ?? undefined} environment={environment} service={service} revision={revision} previousRevision={previousRevision} related={related} taskRun={taskRun} logs={logs.data} logsError={logs.error} logsLoading={logs.isPending} logsRetrying={logs.isFetching} onRetryLogs={() => void logs.retry()} canOperate={canOperate} busy={mutation.confirm.isPending || mutation.cancel.isPending} onConfirm={(id) => mutation.confirm.mutate({ id }, { onSuccess: () => toast.success("Deployment confirmed"), onError: (cause) => toast.error(cause.message) })} onCancel={(input) => mutation.cancel.mutate(input, { onSuccess: () => toast.success("Cancellation requested"), onError: (cause) => toast.error(cause.message) })} />;
}
