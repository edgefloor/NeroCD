import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { environmentsQuery, principalQuery, projectsQuery, revisionsQuery, servicesQuery, useDeploymentMutations, useDeploymentsPollingQuery } from "@/api";
import { DeploymentsView } from "@/pages/DeploymentsView";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/deployments/")({
  validateSearch,
  loader: ({ context }) => Promise.all([context.queryClient.ensureQueryData(projectsQuery()), context.queryClient.ensureQueryData(servicesQuery()), context.queryClient.ensureQueryData(environmentsQuery()), context.queryClient.ensureQueryData(principalQuery())]),
  component: DeploymentsRoute,
});

function DeploymentsRoute() {
  const navigate = useNavigate();
  const { q, service_id: serviceID, environment_id: environmentID } = Route.useSearch();
  const deployments = useDeploymentsPollingQuery(environmentID ? { environment_id: environmentID } : undefined, Boolean(environmentID));
  const projects = useQuery(projectsQuery());
  const services = useQuery(servicesQuery());
  const environments = useQuery(environmentsQuery());
  const revisions = useQuery({ ...revisionsQuery(serviceID ? { service_id: serviceID } : undefined), enabled: Boolean(serviceID) });
  const principal = useQuery(principalQuery());
  const mutation = useDeploymentMutations();
  const error = [deployments, projects, services, environments, revisions, principal].find((query) => query.isError)?.error;
  if (error) return <p role="alert">Unable to load deployment operations: {error.message}</p>;
  const roles = principal.data?.roles ?? [];
  const canOperate = roles.some((role) => ["system_admin", "owner", "maintainer"].includes(role));
  const busy = mutation.create.isPending || mutation.confirm.isPending || mutation.cancel.isPending;
  return <DeploymentsView deployments={deployments.data ?? []} projects={projects.data ?? []} services={services.data ?? []} environments={environments.data ?? []} revisions={revisions.data ?? []} q={q} selectedServiceID={serviceID} selectedEnvironmentID={environmentID} loading={[projects, services, environments, principal].some((query) => query.isPending) || Boolean(serviceID && revisions.isPending) || Boolean(environmentID && deployments.isPending)} canOperate={canOperate} busy={busy} onServiceChange={(id) => void navigate({ to: "/deployments", search: (previous) => ({ ...previous, service_id: id || undefined, environment_id: undefined }) })} onEnvironmentChange={(id) => void navigate({ to: "/deployments", search: (previous) => ({ ...previous, environment_id: id || undefined }) })} onCreate={(input) => mutation.create.mutate(input, { onSuccess: (deployment) => { toast.success("Deployment requested"); void navigate({ to: "/deployments/$deploymentId", params: { deploymentId: deployment.id }, search: (previous) => ({ ...previous, environment_id: input.environment_id, service_id: serviceID }) }); }, onError: (cause) => toast.error(cause.message) })} onCancel={(input) => mutation.cancel.mutate(input, { onSuccess: () => toast.success("Cancellation requested"), onError: (cause) => toast.error(cause.message) })} />;
}
