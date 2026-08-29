import { ReactNode } from "react";
import { CheckCircle2, GitBranch, XCircle, ShieldCheck, Clock } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import type { ApiSnapshot, Approval } from "@/api";
import { approveRun, rejectRun } from "@/api";
import type { MutateFn } from "@/hooks/useApi";
import { StatusBadge } from "@/components/common/StatusBadge";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { formatDate, projectName, templateName } from "@/lib/format";

export function ApprovalList({
  approvals,
  snapshot,
  token,
  busy,
  mutate,
}: {
  approvals: Approval[];
  snapshot: ApiSnapshot;
  token: string;
  busy: string;
  mutate: MutateFn;
}): ReactNode {
  if (approvals.length === 0) {
    return <EmptyState title="No approvals waiting" icon={ShieldCheck} />;
  }
  return (
    <div className="grid gap-2 p-3">
      {approvals.map((approval) => {
        const run = snapshot.runs.find((item) => item.id === approval.run_id);
        return (
          <div key={approval.id} className="grid gap-2 rounded-xl border border-border/60 bg-card p-3 shadow-sm">
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <p className="font-medium text-sm">{run ? templateName(snapshot.templates, run.template_id, run) : approval.run_id}</p>
                <p className="text-sm text-muted-foreground">
                  {run ? projectName(snapshot.projects, run.project_id) : "Run unavailable"} · requested {formatDate(approval.created_at)}
                </p>
              </div>
              <StatusBadge status={approval.status} />
            </div>
            {run ? (
              <div className="rounded-lg bg-muted/60 px-3 py-2 text-sm border border-border/40">
                <div className="flex items-center gap-2 text-muted-foreground">
                  <GitBranch className="h-4 w-4" /> 
                  <span className="font-medium">{run.run_spec.type}</span>
                  <span>·</span>
                  <span>{run.runner_tags.join(", ") || "untagged"}</span>
                </div>
              </div>
            ) : null}
            {approval.status === "pending" ? (
              <div className="flex gap-2">
                <Button
                  className="flex-1 sm:flex-none h-8 rounded-xl"
                  size="sm"
                  onClick={() => void mutate(`approve:${approval.run_id}`, () => approveRun(token, approval.run_id), "Run approved")}
                  disabled={busy === `approve:${approval.run_id}`}
                >
                  <CheckCircle2 className="h-4 w-4 mr-1.5" /> 
                  Approve
                </Button>
                <Button
                  className="flex-1 sm:flex-none h-8 rounded-xl"
                  size="sm"
                  variant="outline"
                  onClick={() => void mutate(`reject:${approval.run_id}`, () => rejectRun(token, approval.run_id), "Run rejected")}
                  disabled={busy === `reject:${approval.run_id}`}
                >
                  <XCircle className="h-4 w-4 mr-1.5" /> 
                  Reject
                </Button>
              </div>
            ) : null}
          </div>
        );
      })}
    </div>
  );
}

export function ApprovalsView({
  snapshot,
  token,
  busy,
  mutate,
  loading,
}: {
  snapshot: ApiSnapshot;
  token: string;
  busy: string;
  mutate: MutateFn;
  loading?: boolean;
}): ReactNode {
  if (loading) {
    return (
      <section className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_320px]">
        <SkeletonTable rows={5} />
        <SkeletonTable rows={5} />
      </section>
    );
  }

  const pendingCount = snapshot.approvals.filter((approval) => approval.status === "pending").length;

  return (
    <section className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_320px]">
      <Card>
        <CardHeader className="border-b py-3">
          <div className="flex items-center justify-between">
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
          <ApprovalList
            approvals={snapshot.approvals.filter((approval) => approval.status === "pending")}
            snapshot={snapshot}
            token={token}
            busy={busy}
            mutate={mutate}
          />
        </CardContent>
      </Card>
      <Card>
        <CardHeader className="border-b py-3">
          <CardTitle className="text-base font-semibold">History</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-2 p-3">
          {snapshot.approvals.map((approval) => (
            <div key={approval.id} className="rounded-xl border border-border/60 bg-card p-3 shadow-sm">
              <div className="flex items-center justify-between gap-2">
                <span className="truncate font-medium text-sm">{approval.run_id}</span>
                <StatusBadge status={approval.status} />
              </div>
              <p className="mt-1 text-xs text-muted-foreground">{formatDate(approval.created_at)}</p>
            </div>
          ))}
        </CardContent>
      </Card>
    </section>
  );
}
