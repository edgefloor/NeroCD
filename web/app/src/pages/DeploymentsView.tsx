import { type FormEvent, type ReactNode, useEffect, useRef, useState } from "react";
import { Link } from "@tanstack/react-router";
import { Ban, CheckCircle2, ChevronRight, CircleAlert, Rocket } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Select } from "@/components/ui/select";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { StatusBadge } from "@/components/common/StatusBadge";
import type { Deployment, Environment, Project, Revision, RunLog, Service, TaskRun } from "@/api";
import { formatDate, matchesQuery } from "@/lib/format";

const activeStates = new Set(["queued", "waiting_confirmation", "assigned", "preparing", "applying", "verifying", "cancel_requested", "rolling_back"]);

export function newDeploymentIntentKey(): string {
  return globalThis.crypto?.randomUUID?.() ?? `deployment-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

export function deploymentStateMessage(deployment: Deployment): string {
  const code = deployment.failure_code?.toLowerCase() ?? "";
  if (code.includes("secret")) return "Blocked: a required secret is unavailable. Ask an operator to restore the approved binding.";
  if (code.includes("policy")) return "Blocked by deployment policy. Review the policy and immutable revision provenance.";
  if (code.includes("runner") || code.includes("expired")) return "Waiting for a live eligible runner; an expired lease will be reconciled automatically.";
  const messages: Record<string, string> = {
    queued: "Queued for an eligible runner.", waiting_confirmation: "Waiting for a maintainer confirmation.", assigned: "A runner has been assigned.", preparing: "Preparing the immutable deployment plan.", applying: "Applying the declared Compose configuration.", verifying: "Verifying health against the declared policy.", cancel_requested: "Cancellation receipt accepted; the runner will reconcile safely.", rolling_back: "Restoring the previous healthy revision.", rolled_back: "Rollback completed; the previous healthy revision remains active.", rollback_failed: "Rollback failed. Operator intervention is required.", manual_intervention: "Automatic recovery is unsafe. Operator intervention is required.", succeeded: "Healthy revision verified.", failed: "Deployment failed before a safe terminal recovery.", canceled: "Canceled before terminal success.",
  };
  return messages[deployment.status] ?? "Deployment state is being reconciled.";
}

function revisionLabel(revision: Revision, abbreviated = false): string {
  const ref = revision.requested_ref?.trim() || revision.id;
  const commit = revision.git_commit?.trim();
  return commit ? `${ref} (${abbreviated ? commit.slice(0, 12) : commit})` : `${ref} (commit unavailable)`;
}

function DeploymentRow({ deployment, environment, revision }: { deployment: Deployment; environment?: Environment; revision?: Revision }): ReactNode {
  return <Card><CardContent className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between"><div className="min-w-0"><Link className="font-medium underline-offset-4 hover:underline" to="/deployments/$deploymentId" params={{ deploymentId: deployment.id }} search={(previous) => ({ ...previous, environment_id: deployment.environment_id })}>{environment?.name ?? deployment.environment_id}</Link><p className="text-sm text-muted-foreground">{deploymentStateMessage(deployment)}</p><p className="mt-1 truncate font-mono text-xs text-muted-foreground">{revision?.git_commit?.trim() || "Commit unavailable"} · {deployment.id}</p></div><div className="flex items-center gap-2"><StatusBadge status={deployment.status} /><ChevronRight className="h-4 w-4 text-muted-foreground" /></div></CardContent></Card>;
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
  submissionError?: { environment_id: string; desired_revision_id: string; message: string };
  onCreate: (input: { environment_id: string; desired_revision_id: string; idempotency_key: string }) => void;
  onServiceChange: (id: string) => void;
  onEnvironmentChange: (id: string) => void;
};

export function DeploymentsView({
  deployments, projects, services, environments, revisions, q,
  selectedServiceID, selectedEnvironmentID, loading, canOperate,
  onCreate, onServiceChange, onEnvironmentChange, busy, submitting, submissionError,
}: DeploymentsViewProps): ReactNode {
  const [draftProjectID, setProjectID] = useState("");
  const [revisionID, setRevisionID] = useState("");
  const createKey = useRef(newDeploymentIntentKey());
  const service = services.find((item) => item.id === selectedServiceID);
  const serviceID = service?.id ?? "";
  const projectID = service?.project_id ?? draftProjectID;
  const scopedServices = projectID ? services.filter((item) => item.project_id === projectID) : [];
  const scopedEnvironments = serviceID ? environments.filter((item) => item.service_id === serviceID) : [];
  const environmentID = scopedEnvironments.find((item) => item.id === selectedEnvironmentID)?.id ?? "";
  const scopedRevisions = serviceID ? revisions.filter((item) => item.service_id === serviceID) : [];
  const visible = deployments.filter((item) => matchesQuery(q, item.id, item.status, item.environment_id, item.desired_revision_id, item.failure_code ?? ""));
  useEffect(() => { setRevisionID(""); }, [serviceID]);
  useEffect(() => { if (!scopedRevisions.some((item) => item.id === revisionID)) setRevisionID(""); }, [scopedRevisions, revisionID]);
  useEffect(() => { createKey.current = newDeploymentIntentKey(); }, [serviceID, environmentID, revisionID]);

  const failedSelection = !submitting && submissionError?.environment_id === environmentID
    && submissionError.desired_revision_id === revisionID ? submissionError : undefined;

  function submitDeployment(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy || submitting || !environmentID || !revisionID) return;
    onCreate({ environment_id: environmentID, desired_revision_id: revisionID, idempotency_key: createKey.current });
  }

  if (loading) return <SkeletonTable rows={5} />;
  return (
    <section className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_360px]">
      <div className="grid gap-3" aria-live="polite">
        {!environmentID ? <EmptyState title="Select an environment to view deployment history" icon={Rocket} />
          : !visible.length ? <EmptyState title="No deployments match this view" icon={Rocket} />
          : visible.map((deployment) => (
            <DeploymentRow key={deployment.id} deployment={deployment}
              environment={environments.find((item) => item.id === deployment.environment_id)}
              revision={revisions.find((item) => item.id === deployment.desired_revision_id)} />
          ))}
      </div>
      <Card>
        <CardHeader><CardTitle>{canOperate ? "Request deployment" : "Deployment history"}</CardTitle></CardHeader>
        <CardContent>
          <div className="grid gap-3">
            <label className="grid gap-1 text-sm font-medium">
              Project
              <Select disabled={submitting} aria-label="Project" value={projectID} required
                onChange={(event) => {
                  createKey.current = newDeploymentIntentKey();
                  setProjectID(event.target.value);
                  setRevisionID("");
                  onServiceChange("");
                }}>
                <option value="">Select project</option>
                {projects.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
              </Select>
            </label>
            <label className="grid gap-1 text-sm font-medium">
              Service
              <Select aria-label="Service" value={serviceID} disabled={submitting || !projectID} required
                onChange={(event) => {
                  createKey.current = newDeploymentIntentKey();
                  setRevisionID("");
                  onServiceChange(event.target.value);
                }}>
                <option value="">Select service</option>
                {scopedServices.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
              </Select>
            </label>
            <label className="grid gap-1 text-sm font-medium">
              Environment
              <Select aria-label="Environment" value={environmentID} disabled={submitting || !serviceID} required
                onChange={(event) => {
                  createKey.current = newDeploymentIntentKey();
                  onEnvironmentChange(event.target.value);
                }}>
                <option value="">Select environment</option>
                {scopedEnvironments.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
              </Select>
            </label>
            {canOperate ? (
              <form className="grid gap-3" aria-label="Request deployment" aria-busy={Boolean(submitting)} onSubmit={submitDeployment}>
                <label className="grid gap-1 text-sm font-medium">
                  Immutable revision
                  <Select aria-label="Immutable revision" value={revisionID} disabled={submitting || !serviceID} required
                    onChange={(event) => {
                      createKey.current = newDeploymentIntentKey();
                      setRevisionID(event.target.value);
                    }}>
                    <option value="">Select revision</option>
                    {scopedRevisions.map((item) => <option key={item.id} value={item.id}>{revisionLabel(item, true)}</option>)}
                  </Select>
                </label>
                <Button type="submit" disabled={!environmentID || !revisionID || !service || busy || submitting}>
                  <Rocket className="mr-1 h-4 w-4" />
                  {submitting ? "Requesting deployment..." : "Request deployment"}
                </Button>
                {failedSelection ? <p role="alert" className="break-words text-sm text-destructive [overflow-wrap:anywhere]">Unable to request deployment: {failedSelection.message}</p> : null}
              </form>
            ) : (
              <p className="text-sm text-muted-foreground" role="status">You can inspect deployment history, but an operator role is required to request, confirm, or cancel deployments.</p>
            )}
          </div>
        </CardContent>
      </Card>
    </section>
  );
}

export function DeploymentDetailView({ deployment, environment, service, revision, previousRevision, related, taskRun, logs, logsError, logsLoading, logsRetrying, onRetryLogs, canOperate, busy, pendingAction, actionError, onConfirm, onCancel }: { deployment?: Deployment; environment?: Environment; service?: Service; revision?: Revision; previousRevision?: Revision; related: Deployment[]; taskRun?: TaskRun; logs: RunLog[]; logsError?: Error; logsLoading?: boolean; logsRetrying?: boolean; onRetryLogs?: () => void; canOperate: boolean; busy?: boolean; pendingAction?: "confirm" | "cancel"; actionError?: string; onConfirm: (id: string) => void; onCancel: (input: { deployment_id: string; request_id: string }) => void }): ReactNode {
  const cancelKey = useRef(newDeploymentIntentKey());
  if (!deployment) return <Card className="min-w-0"><CardContent className="p-6" role="alert"><CircleAlert className="mr-2 inline h-4 w-4" />Deployment is unavailable or you do not have access.</CardContent></Card>;
  const canCancel = canOperate && activeStates.has(deployment.status);
  const canConfirm = canOperate && deployment.status === "waiting_confirmation";
  return <section className="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_320px]"><Card className="min-w-0"><CardHeader><div className="flex flex-wrap items-center justify-between gap-2"><CardTitle>Deployment {deployment.id}</CardTitle><StatusBadge status={deployment.status} /></div></CardHeader><CardContent className="grid min-w-0 gap-4"><p role="status" aria-live="polite">{deploymentStateMessage(deployment)}</p>{deployment.failure_code ? <p className="rounded border border-destructive/30 p-3 text-sm"><CircleAlert className="mr-1 inline h-4 w-4" />{deployment.failure_code}</p> : null}<dl className="grid gap-3 text-sm sm:grid-cols-2"><Info label="Environment" value={environment?.name ?? deployment.environment_id} /><Info label="Requested" value={formatDate(deployment.created_at)} /><Info label="Desired revision" value={revision ? revisionLabel(revision) : deployment.desired_revision_id} /><Info label="Previous healthy" value={previousRevision ? revisionLabel(previousRevision) : deployment.previous_healthy_revision_id || "None"} /><Info label="Compose hash" value={revision?.compose_hash?.trim() || "Not recorded"} /><Info label="Content identity" value={revision?.content_identity?.trim() || "Not recorded"} /></dl><div><h3 className="mb-1 font-medium">Image digests</h3>{revision?.image_digests?.length ? <ul className="grid gap-1 font-mono text-xs">{revision.image_digests.map((digest) => <li key={digest}>{digest}</li>)}</ul> : <p className="text-sm text-muted-foreground">No resolved image digests yet.</p>}</div><div className="flex flex-wrap gap-2" aria-busy={Boolean(pendingAction)}>{canConfirm ? <Button disabled={busy || Boolean(pendingAction)} onClick={() => onConfirm(deployment.id)}><CheckCircle2 className="mr-1 h-4 w-4" />{pendingAction === "confirm" ? "Confirming…" : "Confirm"}</Button> : null}{canCancel ? <Button variant="destructive" disabled={busy || Boolean(pendingAction)} onClick={() => onCancel({ deployment_id: deployment.id, request_id: cancelKey.current })}><Ban className="mr-1 h-4 w-4" />{pendingAction === "cancel" ? "Requesting cancellation…" : "Cancel"}</Button> : null}</div>{actionError && !pendingAction ? <p role="alert" className="break-words text-sm text-destructive [overflow-wrap:anywhere]">{actionError}</p> : null}</CardContent></Card><Card className="min-w-0"><CardHeader><CardTitle>Execution and rollback</CardTitle></CardHeader><CardContent className="grid min-w-0 gap-3">{taskRun ? <Link className="flex min-w-0 flex-wrap items-center gap-2 rounded border p-2 text-sm hover:bg-muted" to="/runs/$runId" params={{ runId: taskRun.id }} search={(previous) => previous}>Run <span className="min-w-0 break-all font-mono">{taskRun.id}</span> <StatusBadge status={taskRun.status} /></Link> : <p className="text-sm text-muted-foreground">No linked run is available yet.</p>}{logsError ? <div className="grid gap-2"><p role="alert" className="text-sm text-destructive">Unable to load run output.</p>{onRetryLogs ? <Button variant="outline" disabled={logsRetrying} onClick={onRetryLogs}>Retry logs</Button> : null}</div> : null}{logs.length ? <ul className="max-h-32 space-y-1 overflow-auto rounded border p-2 font-mono text-xs" aria-label="Deployment run logs">{logs.slice(-20).map((log) => <li key={log.id}>{log.message}</li>)}</ul> : taskRun && !logsError ? <p className="text-xs text-muted-foreground">{logsLoading ? "Loading run output..." : "No run logs have been recorded."}</p> : null}{related.length ? related.map((item) => <Link key={item.id} className="flex min-w-0 flex-wrap items-center justify-between gap-2 rounded border p-2 text-sm hover:bg-muted" to="/deployments/$deploymentId" params={{ deploymentId: item.id }} search={(previous) => previous}><span className="min-w-0 break-all font-mono">{item.id}</span><StatusBadge status={item.status} /></Link>) : <p className="text-sm text-muted-foreground">No linked rollback records.</p>}{taskRun ? <p className="border-t pt-3 text-xs text-muted-foreground">Showing up to 20 recent log entries. Open the linked run for full output and history.</p> : null}</CardContent></Card></section>;
}

function Info({ label, value }: { label: string; value: string }): ReactNode { return <div><dt className="text-xs uppercase tracking-wide text-muted-foreground">{label}</dt><dd className="break-all font-mono text-xs">{value}</dd></div>; }
