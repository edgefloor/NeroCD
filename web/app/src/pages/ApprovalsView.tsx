import { ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { CheckCircle2, GitBranch, XCircle, ShieldCheck, Clock } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import type { ActionError, ApiSnapshot, Approval, ApprovalDecision } from "@/api";
import { StatusBadge } from "@/components/common/StatusBadge";
import { ActionErrorMessage } from "@/components/common/ActionErrorMessage";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { formatDate, matchesQuery, matchesRunQuery, projectName, templateName } from "@/lib/format";

export function ApprovalList({
  approvals,
  snapshot,
  busy,
  onDecide,
  decisionError,
  canOperateProject,
}: {
  approvals: Approval[];
  snapshot: ApiSnapshot;
  busy: boolean;
  onDecide: (runID: string, decision: ApprovalDecision) => void;
  decisionError?: ActionError;
  canOperateProject?: (projectID: string) => boolean;
}): ReactNode {
  if (approvals.length === 0) {
    return <EmptyState title="No approvals waiting" icon={ShieldCheck} />;
  }
  return (
    <div className="grid min-w-0 grid-cols-1 gap-2 p-3">
      {approvals.map((approval) => {
        const run = snapshot.runs.find((item) => item.id === approval.run_id);
        return (
          <div key={approval.id} className="grid min-w-0 grid-cols-1 gap-2 rounded-xl border border-border/60 bg-card p-3 shadow-sm">
            <div className="flex min-w-0 flex-wrap items-start justify-between gap-2">
              <div className="min-w-0 flex-1 basis-48">
                <p className="break-words font-medium text-sm [overflow-wrap:anywhere]">{run ? templateName(snapshot.templates, run.template_id, run) : approval.run_id}</p>
                <p className="break-words text-sm text-muted-foreground [overflow-wrap:anywhere]">
                  {run ? projectName(snapshot.projects, run.project_id) : "Run unavailable"} · requested {formatDate(approval.created_at)}
                </p>
              </div>
              <StatusBadge className="shrink-0" status={approval.status} />
            </div>
            {run ? (
              <div className="min-w-0 rounded-lg bg-muted/60 px-3 py-2 text-sm border border-border/40">
                <div className="flex min-w-0 flex-wrap items-center gap-2 text-muted-foreground">
                  <GitBranch className="h-4 w-4 shrink-0" />
                  <span className="min-w-0 break-all font-medium">{run.run_spec.type}</span>
                  <span>·</span>
                  <span className="min-w-0 break-all">{(run.runner_tags ?? []).join(", ") || "untagged"}</span>
                </div>
              </div>
            ) : null}
            {run ? <Link className="text-sm underline underline-offset-4" to="/runs/$runId" params={{ runId: run.id }} search={(previous) => previous}>Run details</Link> : null}
            {run && approval.status === "pending" && run.status !== "waiting_approval" ? <p className="text-sm text-muted-foreground">This run is no longer waiting for approval.</p> : null}
            {run && approval.status === "pending" && run.status === "waiting_approval" && canOperateProject?.(run.project_id) ? (
              <div className="flex gap-2">
                <Button
                  className="flex-1 sm:flex-none h-8 rounded-xl"
                  size="sm"
                  onClick={() => onDecide(approval.run_id, "approve")}
                  disabled={Boolean(busy)}
                >
                  <CheckCircle2 className="h-4 w-4 mr-1.5" /> 
                  Approve
                </Button>
                <Button
                  className="flex-1 sm:flex-none h-8 rounded-xl"
                  size="sm"
                  variant="outline"
                  onClick={() => onDecide(approval.run_id, "reject")}
                  disabled={Boolean(busy)}
                >
                  <XCircle className="h-4 w-4 mr-1.5" /> 
                  Reject
                </Button>
              </div>
            ) : null}
            <ActionErrorMessage error={decisionError} actionKey={approval.run_id} label="Unable to record decision" />
          </div>
        );
      })}
    </div>
  );
}

export function ApprovalsView({
  snapshot,
  q,
  busy,
  onDecide,
  decisionError,
  canOperateProject,
  loading,
}: {
  snapshot: ApiSnapshot;
  q?: string;
  busy: boolean;
  onDecide: (runID: string, decision: ApprovalDecision) => void;
  decisionError?: ActionError;
  canOperateProject?: (projectID: string) => boolean;
  loading?: boolean;
}): ReactNode {
  if (loading) {
    return (
      <section className="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_320px]">
        <SkeletonTable rows={5} />
        <SkeletonTable rows={5} />
      </section>
    );
  }

  const filtering = Boolean(q?.trim());
  const visibleApprovals = snapshot.approvals.filter((approval) => {
    const run = snapshot.runs.find((item) => item.id === approval.run_id);
    return matchesQuery(q, approval.id, approval.run_id, approval.status, approval.requested_by, approval.approved_by)
      || Boolean(run && matchesRunQuery(q, run, snapshot.projects, snapshot.templates));
  });
  const visiblePending = visibleApprovals.filter((approval) => approval.status === "pending");
  const pendingCount = snapshot.approvals.filter((approval) => approval.status === "pending").length;

  return (
    <section className="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_320px]">
      <Card className="min-w-0">
        <CardHeader className="border-b py-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <CardTitle className="text-base font-semibold">Pending approvals</CardTitle>
            </div>
            {pendingCount > 0 && (
              <span className="inline-flex items-center gap-1.5 rounded-full bg-warning/10 px-2.5 py-1 text-xs font-medium text-warning border border-warning/15">
                <Clock className="h-3 w-3" />
                {pendingCount} waiting
              </span>
            )}
          </div>
        </CardHeader>
        <CardContent className="p-0">
          {filtering && <p className="px-4 pt-3 text-sm text-muted-foreground" role="status">{visiblePending.length} of {pendingCount} pending approvals match on this page.</p>}
          {filtering && visiblePending.length === 0 ? <p className="px-4 py-5 text-sm text-muted-foreground">No pending approvals match this filter on the loaded page.</p> : <ApprovalList
            approvals={visiblePending}
            snapshot={snapshot}
            busy={busy}
            onDecide={onDecide} decisionError={decisionError}
            canOperateProject={canOperateProject}
          />}
        </CardContent>
      </Card>
      <Card className="min-w-0">
        <CardHeader className="border-b py-3">
          <CardTitle className="text-base font-semibold">History</CardTitle>
        </CardHeader>
        <CardContent className="grid min-w-0 grid-cols-1 gap-2 p-3">
          {filtering && visibleApprovals.length === 0 && <p className="text-sm text-muted-foreground">No approval history matches this filter on the loaded page.</p>}
          {visibleApprovals.map((approval) => (
            <div key={approval.id} className="rounded-xl border border-border/60 bg-card p-3 shadow-sm">
              <div className="flex items-center justify-between gap-2">
                <Link className="min-w-0 break-all font-mono text-sm underline underline-offset-4" to="/runs/$runId" params={{ runId: approval.run_id }} search={(previous) => previous}>{approval.run_id}</Link>
                <StatusBadge className="shrink-0" status={approval.status} />
              </div>
              <p className="mt-1 text-xs text-muted-foreground">{formatDate(approval.created_at)}</p>
            </div>
          ))}
        </CardContent>
      </Card>
    </section>
  );
}
