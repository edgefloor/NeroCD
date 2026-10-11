import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { allProjectMembersQuery, allProjectsQuery, allEnvironmentsQuery, principalQuery, allRevisionsQuery, allServicesQuery, useDeploymentMutations, useDeploymentsPollingQuery } from "@/api";
import { DeploymentsView } from "@/pages/DeploymentsView";
import { PageNavigation, usePage } from "@/components/common/PageNavigation";
import { canMaintainProject } from "@/lib/permissions";
import { Button } from "@/components/ui/button";
import { validateSearch } from "@/router/search";

export const Route = createFileRoute("/_authenticated/deployments/")({
  validateSearch,
  loader: ({ context }) => Promise.all([context.queryClient.prefetchQuery(allProjectsQuery()), context.queryClient.prefetchQuery(allServicesQuery()), context.queryClient.prefetchQuery(allEnvironmentsQuery()), context.queryClient.prefetchQuery(principalQuery())]),
  component: DeploymentsRoute,
});

function DeploymentsRoute() {
  const navigate = useNavigate();
  const { q, service_id: serviceID, environment_id: environmentID } = Route.useSearch();
  const page = usePage();
  const deployments = useDeploymentsPollingQuery(environmentID ? { environment_id: environmentID, limit: page.limit, offset: page.offset } : undefined, Boolean(environmentID));
  const projects = useQuery({ ...allProjectsQuery(), retryOnMount: false });
  const services = useQuery({ ...allServicesQuery(), retryOnMount: false });
  const environments = useQuery({ ...allEnvironmentsQuery(), retryOnMount: false });
  const revisions = useQuery({ ...allRevisionsQuery(serviceID ?? ""), enabled: Boolean(serviceID), retryOnMount: false });
  const principal = useQuery({ ...principalQuery(), retryOnMount: false });
  const members = useQuery({ ...allProjectMembersQuery(), retryOnMount: false });
  const mutation = useDeploymentMutations();
  const failed = [deployments, projects, services, environments, revisions, principal, members].filter((query) => query.isError);
  const recovery = failed.length ? <section className="space-y-3">
    <p role="alert">Unable to refresh deployment operations. Retry before requesting a deployment.</p>
    <Button disabled={failed.some((query) => query.isFetching)} onClick={() => { for (const query of failed) void query.refetch(); }}>Retry deployments</Button>
  </section> : null;
  const canOperate = canMaintainProject(principal.data, members.data ?? [], services.data?.find((item) => item.id === serviceID)?.project_id);
  const busy = failed.length > 0 || mutation.create.isPending;
  return <>{recovery}<DeploymentsView deployments={deployments.data?.items ?? []} projects={projects.data ?? []} services={services.data ?? []} environments={environments.data ?? []} revisions={revisions.data ?? []} q={q} selectedServiceID={serviceID} selectedEnvironmentID={environmentID} loading={[projects, services, environments, principal, members].some((query) => query.isPending) || Boolean(serviceID && revisions.isPending) || Boolean(environmentID && deployments.isPending)} canOperate={canOperate} busy={busy} submitting={mutation.create.isPending} submissionError={mutation.create.error && mutation.create.variables ? { ...mutation.create.variables, message: mutation.create.error.message } : undefined} onServiceChange={(id) => void navigate({ to: "/deployments", search: (previous) => ({ ...previous, service_id: id || undefined, environment_id: undefined, offset: undefined }) })} onEnvironmentChange={(id) => void navigate({ to: "/deployments", search: (previous) => ({ ...previous, environment_id: id || undefined, offset: undefined }) })} onCreate={(input) => mutation.create.mutate(input, { onSuccess: (deployment) => { toast.success("Deployment requested"); void navigate({ to: "/deployments/$deploymentId", params: { deploymentId: deployment.id }, search: (previous) => ({ ...previous, environment_id: input.environment_id, service_id: serviceID }) }); } })} /><PageNavigation page={deployments.data ?? { count: 0, total: 0, limit: page.limit, offset: page.offset }} label="deployments" loading={deployments.isFetching} onFirst={page.first} onPrevious={page.previous} onNext={page.next} /></>;
}
