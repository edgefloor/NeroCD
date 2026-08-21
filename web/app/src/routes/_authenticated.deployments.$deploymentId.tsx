import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { environmentsQuery, principalQuery, revisionsQuery, runLogsQuery, runsQuery, servicesQuery, useDeploymentMutations, useDeploymentPollingQuery, useDeploymentsPollingQuery } from "@/api";
import { DeploymentDetailView } from "@/pages/DeploymentsView";
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
  const environments = useQuery(environmentsQuery());
  const environment = environments.data?.find((item) => item.id === deployment.data?.environment_id);
  const services = useQuery(servicesQuery());
  const service = services.data?.find((item) => item.id === environment?.service_id);
  const revisions = useQuery({ ...revisionsQuery(environment ? { service_id: environment.service_id } : undefined), enabled: Boolean(environment) });
  const history = useDeploymentsPollingQuery(deployment.data ? { environment_id: deployment.data.environment_id } : undefined, Boolean(deployment.data));
  const principal = useQuery(principalQuery());
  const mutation = useDeploymentMutations();
  const revision = revisions.data?.find((item) => item.id === deployment.data?.desired_revision_id);
  const previousRevision = revisions.data?.find((item) => item.id === deployment.data?.previous_healthy_revision_id);
  const related = (history.data ?? []).filter((item) => item.id !== deploymentId && (item.rollback_of_id === deploymentId || deployment.data?.rollback_of_id === item.id || (item.rollback_of_id && item.rollback_of_id === deployment.data?.rollback_of_id)));
  const runs = useQuery(runsQuery());
  const taskRun = runs.data?.find((item) => item.id === deployment.data?.task_run_id);
  const logs = useQuery({ ...runLogsQuery(deployment.data?.task_run_id ? { run_id: deployment.data.task_run_id, limit: 100, offset: 0 } : { limit: 100, offset: 0 }), enabled: Boolean(deployment.data?.task_run_id) });
  const error = [deployment, environments, services, revisions, history, principal, runs, logs].find((query) => query.isError)?.error;
  if (error) return <p role="alert">Unable to load deployment: {error.message}</p>;
  const roles = principal.data?.roles ?? [];
  const canOperate = roles.some((role) => ["system_admin", "owner", "maintainer"].includes(role));
  return <DeploymentDetailView deployment={deployment.data ?? undefined} environment={environment} service={service} revision={revision} previousRevision={previousRevision} related={related} taskRun={taskRun} logs={logs.data ?? []} canOperate={canOperate} busy={mutation.confirm.isPending || mutation.cancel.isPending} onConfirm={(id) => mutation.confirm.mutate({ id }, { onSuccess: () => toast.success("Deployment confirmed"), onError: (cause) => toast.error(cause.message) })} onCancel={(input) => mutation.cancel.mutate(input, { onSuccess: () => toast.success("Cancellation requested"), onError: (cause) => toast.error(cause.message) })} />;
}
