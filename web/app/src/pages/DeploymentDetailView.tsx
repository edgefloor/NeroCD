import { type ReactNode, useRef } from "react";
import { Link } from "@tanstack/react-router";
import { Ban, CheckCircle2, CircleAlert } from "lucide-react";
import type { Deployment, Environment, Revision, RunLog, TaskRun } from "@/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { StatusBadge } from "@/components/common/StatusBadge";
import { formatDate } from "@/lib/format";
import { deploymentStateMessage, newDeploymentIntentKey, revisionLabel } from "@/lib/deploymentPresentation";

const cancellableStates = new Set(["queued", "waiting_confirmation", "assigned", "preparing", "applying", "verifying"]);

type DeploymentDetailViewProps = {
  deployment?: Deployment;
  environment?: Environment;
  revision?: Revision;
  previousRevision?: Revision;
  related: Deployment[];
  taskRun?: TaskRun;
  logs: RunLog[];
  logsError?: Error;
  logsLoading?: boolean;
  logsRetrying?: boolean;
  onRetryLogs?: () => void;
  canOperate: boolean;
  busy?: boolean;
  pendingAction?: "confirm" | "cancel";
  actionError?: string;
  onConfirm: (id: string) => void;
  onCancel: (input: { deployment_id: string; request_id: string }) => void;
};

export function DeploymentDetailView({
  deployment, environment, revision, previousRevision, related, taskRun, logs,
  logsError, logsLoading, logsRetrying, onRetryLogs, canOperate, busy,
  pendingAction, actionError, onConfirm, onCancel,
}: DeploymentDetailViewProps): ReactNode {
  const cancelIntent = useRef<{ deploymentID?: string; key: string } | null>(null);
  if (!cancelIntent.current || cancelIntent.current.deploymentID !== deployment?.id) {
    cancelIntent.current = { deploymentID: deployment?.id, key: newDeploymentIntentKey() };
  }
  const cancelKey = cancelIntent.current.key;
  if (!deployment) return (
    <Card className="min-w-0">
      <CardContent className="p-6" role="alert">
        <CircleAlert className="mr-2 inline h-4 w-4" />Deployment is unavailable or you do not have access.
      </CardContent>
    </Card>
  );
  const canCancel = canOperate && !deployment.rollback_of_id && cancellableStates.has(deployment.status);
  const canConfirm = canOperate && deployment.status === "waiting_confirmation";
  return (
    <section className="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_320px]">
      <Card className="min-w-0">
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle>Deployment {deployment.id}</CardTitle>
            <StatusBadge status={deployment.status} />
          </div>
        </CardHeader>
        <CardContent className="grid min-w-0 gap-4">
          <p role="status" aria-live="polite">{deploymentStateMessage(deployment)}</p>
          {deployment.failure_code ? (
            <p className="min-w-0 break-words rounded border border-destructive/30 p-3 text-sm [overflow-wrap:anywhere]">
              <CircleAlert className="mr-1 inline h-4 w-4" />{deployment.failure_code}
            </p>
          ) : null}
          <dl className="grid gap-3 text-sm sm:grid-cols-2">
            <Info label="Environment" value={environment?.name ?? deployment.environment_id} />
            <Info label="Requested" value={formatDate(deployment.created_at)} />
            <Info label="Desired revision" value={revision ? revisionLabel(revision) : deployment.desired_revision_id} />
            <Info label="Previous healthy" value={previousRevision ? revisionLabel(previousRevision) : deployment.previous_healthy_revision_id || "None"} />
            <Info label="Compose hash" value={revision?.compose_hash?.trim() || "Not recorded"} />
            <Info label="Content identity" value={revision?.content_identity?.trim() || "Not recorded"} />
          </dl>
          <div className="min-w-0">
            <h3 className="mb-1 font-medium">Image digests</h3>
            {revision?.image_digests?.length ? (
              <ul className="grid min-w-0 gap-1 break-all font-mono text-xs">
                {revision.image_digests.map((digest) => <li key={digest}>{digest}</li>)}
              </ul>
            ) : <p className="text-sm text-muted-foreground">No resolved image digests yet.</p>}
          </div>
          <div className="flex flex-wrap gap-2" aria-busy={Boolean(pendingAction)}>
            {canConfirm ? (
              <Button disabled={busy || Boolean(pendingAction)} onClick={() => onConfirm(deployment.id)}>
                <CheckCircle2 className="mr-1 h-4 w-4" />{pendingAction === "confirm" ? "Confirming…" : "Confirm"}
              </Button>
            ) : null}
            {canCancel ? (
              <Button variant="destructive" disabled={busy || Boolean(pendingAction)} onClick={() => onCancel({ deployment_id: deployment.id, request_id: cancelKey })}>
                <Ban className="mr-1 h-4 w-4" />{pendingAction === "cancel" ? "Requesting cancellation…" : "Cancel"}
              </Button>
            ) : null}
          </div>
          {actionError && !pendingAction ? <p role="alert" className="break-words text-sm text-destructive [overflow-wrap:anywhere]">{actionError}</p> : null}
        </CardContent>
      </Card>
      <Card className="min-w-0">
        <CardHeader><CardTitle>Execution and rollback</CardTitle></CardHeader>
        <CardContent className="grid min-w-0 gap-3">
          {taskRun ? (
            <Link className="flex min-w-0 flex-wrap items-center gap-2 rounded border p-2 text-sm hover:bg-muted" to="/runs/$runId" params={{ runId: taskRun.id }} search={(previous) => previous}>
              Run <span className="min-w-0 break-all font-mono">{taskRun.id}</span> <StatusBadge status={taskRun.status} />
            </Link>
          ) : <p className="text-sm text-muted-foreground">No linked run is available yet.</p>}
          {logsError ? (
            <div className="grid gap-2">
              <p role="alert" className="text-sm text-destructive">Unable to load run output.</p>
              {onRetryLogs ? <Button variant="outline" disabled={logsRetrying} onClick={onRetryLogs}>Retry logs</Button> : null}
            </div>
          ) : null}
          {logs.length ? (
            <ul className="max-h-32 space-y-1 overflow-auto rounded border p-2 font-mono text-xs" aria-label="Deployment run logs">
              {logs.slice(-20).map((log) => <li key={log.id}>{log.message}</li>)}
            </ul>
          ) : taskRun && !logsError ? <p className="text-xs text-muted-foreground">{logsLoading ? "Loading run output..." : "No run logs have been recorded."}</p> : null}
          {related.length ? related.map((item) => (
            <Link key={item.id} className="flex min-w-0 flex-wrap items-center justify-between gap-2 rounded border p-2 text-sm hover:bg-muted" to="/deployments/$deploymentId" params={{ deploymentId: item.id }} search={(previous) => previous}>
              <span className="min-w-0 break-all font-mono">{item.id}</span><StatusBadge status={item.status} />
            </Link>
          )) : <p className="text-sm text-muted-foreground">No linked rollback records.</p>}
          {taskRun ? <p className="border-t pt-3 text-xs text-muted-foreground">Showing up to 20 recent log entries. Open the linked run for full output and history.</p> : null}
        </CardContent>
      </Card>
    </section>
  );
}

function Info({ label, value }: { label: string; value: string }): ReactNode {
  return <div><dt className="text-xs uppercase tracking-wide text-muted-foreground">{label}</dt><dd className="break-all font-mono text-xs">{value}</dd></div>;
}
