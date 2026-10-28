import type {
  Deployment,
  Environment,
  Project,
  Revision,
  Service,
} from "@/api";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { StatusBadge } from "@/components/common/StatusBadge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Select } from "@/components/ui/select";
import {
  deploymentStateMessage,
  newDeploymentIntentKey,
  revisionLabel,
} from "@/lib/deploymentPresentation";
import { matchesQuery } from "@/lib/format";
import { Link } from "@tanstack/react-router";
import { ChevronRight, Rocket } from "lucide-react";
import {
  type FormEvent,
  type ReactNode,
  useEffect,
  useRef,
  useState,
} from "react";

function DeploymentRow({
  deployment,
  environment,
  revision,
}: {
  deployment: Deployment;
  environment?: Environment;
  revision?: Revision;
}): ReactNode {
  return (
    <Card className="min-w-0">
      <CardContent className="flex min-w-0 flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0 flex-1">
          <Link
            className="break-words [overflow-wrap:anywhere] font-medium underline-offset-4 hover:underline"
            to="/deployments/$deploymentId"
            params={{ deploymentId: deployment.id }}
            search={(previous) => ({
              ...previous,
              environment_id: deployment.environment_id,
            })}
          >
            {environment?.name ?? deployment.environment_id}
          </Link>
          <p className="text-sm text-muted-foreground">
            {deploymentStateMessage(deployment)}
          </p>
          <p className="mt-1 break-all font-mono text-xs text-muted-foreground">
            {revision?.git_commit?.trim() || "Commit unavailable"} ·{" "}
            {deployment.id}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <StatusBadge status={deployment.status} />
          <ChevronRight className="h-4 w-4 text-muted-foreground" />
        </div>
      </CardContent>
    </Card>
  );
}

type DeploymentsViewProps = {
  deployments: Deployment[];
  projects: Project[];
  services: Service[];
  environments: Environment[];
  revisions: Revision[];
  q?: string;
  selectedServiceID?: string;
  selectedEnvironmentID?: string;
  loading?: boolean;
  canOperate: boolean;
  busy?: boolean;
  submitting?: boolean;
  submissionError?: {
    environment_id: string;
    desired_revision_id: string;
    message: string;
  };
  onCreate: (input: {
    environment_id: string;
    desired_revision_id: string;
    idempotency_key: string;
  }) => void;
  onServiceChange: (id: string) => void;
  onEnvironmentChange: (id: string) => void;
};

export function DeploymentsView(props: DeploymentsViewProps): ReactNode {
  const {
    projects,
    loading,
    canOperate,
    onServiceChange,
    onEnvironmentChange,
    busy,
    submitting,
    setProjectID,
    revisionID,
    setRevisionID,
    createKey,
    service,
    serviceID,
    projectID,
    scopedServices,
    scopedEnvironments,
    environmentID,
    scopedRevisions,
    environmentByID,
    revisionByID,
    visible,
    failedSelection,
    submitDeployment,
  } = useDeploymentsView(props);
  if (loading) return <SkeletonTable rows={5} />;
  return (
    <section className="grid min-w-0 grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1fr)_360px]">
      <div className="grid min-w-0 grid-cols-1 gap-3" aria-live="polite">
        {!environmentID ? (
          <EmptyState
            title="Select an environment to view deployment history"
            icon={Rocket}
          />
        ) : !visible.length ? (
          <EmptyState title="No deployments match this view" icon={Rocket} />
        ) : (
          visible.map((deployment) => (
            <DeploymentRow
              key={deployment.id}
              deployment={deployment}
              environment={environmentByID.get(deployment.environment_id)}
              revision={revisionByID.get(deployment.desired_revision_id)}
            />
          ))
        )}
      </div>
      <Card className="min-w-0">
        <CardHeader>
          <CardTitle>
            {canOperate ? "Request deployment" : "Deployment history"}
          </CardTitle>
        </CardHeader>
        <CardContent className="min-w-0">
          <div className="grid min-w-0 grid-cols-1 gap-3">
            <label className="grid min-w-0 gap-1 text-sm font-medium">
              Project
              <Select
                className="min-w-0 max-w-full"
                disabled={submitting}
                aria-label="Project"
                value={projectID}
                required
                onChange={(event) => {
                  createKey.current = newDeploymentIntentKey();
                  setProjectID(event.target.value);
                  setRevisionID("");
                  onServiceChange("");
                }}
              >
                <option value="">Select project</option>
                {projects.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name}
                  </option>
                ))}
              </Select>
            </label>
            <label className="grid min-w-0 gap-1 text-sm font-medium">
              Service
              <Select
                className="min-w-0 max-w-full"
                aria-label="Service"
                value={serviceID}
                disabled={submitting || !projectID}
                required
                onChange={(event) => {
                  createKey.current = newDeploymentIntentKey();
                  setRevisionID("");
                  onServiceChange(event.target.value);
                }}
              >
                <option value="">Select service</option>
                {scopedServices.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name}
                  </option>
                ))}
              </Select>
            </label>
            <label className="grid min-w-0 gap-1 text-sm font-medium">
              Environment
              <Select
                className="min-w-0 max-w-full"
                aria-label="Environment"
                value={environmentID}
                disabled={submitting || !serviceID}
                required
                onChange={(event) => {
                  createKey.current = newDeploymentIntentKey();
                  onEnvironmentChange(event.target.value);
                }}
              >
                <option value="">Select environment</option>
                {scopedEnvironments.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name}
                  </option>
                ))}
              </Select>
            </label>
            {canOperate ? (
              <form
                className="grid min-w-0 grid-cols-1 gap-3"
                aria-label="Request deployment"
                aria-busy={Boolean(submitting)}
                onSubmit={submitDeployment}
              >
                <label className="grid min-w-0 gap-1 text-sm font-medium">
                  Immutable revision
                  <Select
                    className="min-w-0 max-w-full"
                    aria-label="Immutable revision"
                    value={revisionID}
                    disabled={submitting || !serviceID}
                    required
                    onChange={(event) => {
                      createKey.current = newDeploymentIntentKey();
                      setRevisionID(event.target.value);
                    }}
                  >
                    <option value="">Select revision</option>
                    {scopedRevisions.map((item) => (
                      <option key={item.id} value={item.id}>
                        {revisionLabel(item, true)}
                      </option>
                    ))}
                  </Select>
                </label>
                <Button
                  type="submit"
                  disabled={
                    !environmentID ||
                    !revisionID ||
                    !service ||
                    busy ||
                    submitting
                  }
                >
                  <Rocket className="mr-1 h-4 w-4" />
                  {submitting
                    ? "Requesting deployment..."
                    : "Request deployment"}
                </Button>
                {failedSelection ? (
                  <p
                    role="alert"
                    className="break-words text-sm text-destructive [overflow-wrap:anywhere]"
                  >
                    Unable to request deployment: {failedSelection.message}
                  </p>
                ) : null}
              </form>
            ) : (
              <p className="text-sm text-muted-foreground" role="status">
                You can inspect deployment history, but an operator role is
                required to request, confirm, or cancel deployments.
              </p>
            )}
          </div>
        </CardContent>
      </Card>
    </section>
  );
}

function useDeploymentsView({
  deployments,
  projects,
  services,
  environments,
  revisions,
  q,
  selectedServiceID,
  selectedEnvironmentID,
  loading,
  canOperate,
  onCreate,
  onServiceChange,
  onEnvironmentChange,
  busy,
  submitting,
  submissionError,
}: DeploymentsViewProps) {
  const [draftProjectID, setProjectID] = useState("");
  const [revisionID, setRevisionID] = useState("");
  const [initialCreateKey] = useState(newDeploymentIntentKey);
  const createKey = useRef(initialCreateKey);
  const service = services.find((item) => item.id === selectedServiceID);
  const serviceID = service?.id ?? "";
  const projectID = service?.project_id ?? draftProjectID;
  const scopedServices = projectID
    ? services.filter((item) => item.project_id === projectID)
    : [];
  const scopedEnvironments = serviceID
    ? environments.filter((item) => item.service_id === serviceID)
    : [];
  const environmentID =
    scopedEnvironments.find((item) => item.id === selectedEnvironmentID)?.id ??
    "";
  const scopedRevisions = serviceID
    ? revisions.filter((item) => item.service_id === serviceID)
    : [];
  const environmentByID = new Map(environments.map((item) => [item.id, item]));
  const revisionByID = new Map(revisions.map((item) => [item.id, item]));
  const visible = deployments.filter((item) => {
    const revision = revisionByID.get(item.desired_revision_id);
    return matchesQuery(
      q,
      item.id,
      item.status,
      item.environment_id,
      item.desired_revision_id,
      item.failure_code ?? "",
      environmentByID.get(item.environment_id)?.name ?? "",
      revision?.requested_ref ?? "",
      revision?.git_commit ?? "",
    );
  });
  const [observedServiceID, setObservedServiceID] = useState(serviceID);
  if (observedServiceID !== serviceID) {
    setObservedServiceID(serviceID);
    setRevisionID("");
  }
  if (revisionID && !scopedRevisions.some((item) => item.id === revisionID))
    setRevisionID("");
  useEffect(() => {
    createKey.current = newDeploymentIntentKey();
  }, [serviceID, environmentID, revisionID]);
  const failedSelection =
    !submitting &&
    submissionError?.environment_id === environmentID &&
    submissionError.desired_revision_id === revisionID
      ? submissionError
      : undefined;
  function submitDeployment(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy || submitting || !environmentID || !revisionID) return;
    onCreate({
      environment_id: environmentID,
      desired_revision_id: revisionID,
      idempotency_key: createKey.current,
    });
  }
  return {
    deployments,
    projects,
    loading,
    canOperate,
    onServiceChange,
    onEnvironmentChange,
    busy,
    submitting,
    setProjectID,
    revisionID,
    setRevisionID,
    createKey,
    service,
    serviceID,
    projectID,
    scopedServices,
    scopedEnvironments,
    environmentID,
    scopedRevisions,
    environmentByID,
    revisionByID,
    visible,
    failedSelection,
    submitDeployment,
  };
}
