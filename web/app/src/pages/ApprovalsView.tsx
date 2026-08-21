import { type ReactNode } from "react";
import { CheckCircle2, GitBranch, XCircle, ShieldCheck } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import type { Approval, Project, TaskRun, TaskTemplate } from "@/api";
import { StatusBadge } from "@/components/common/StatusBadge";
import { EmptyState } from "@/components/common/EmptyState";
import { SkeletonTable } from "@/components/common/SkeletonCard";
import { formatDate, matchesQuery, projectName, templateName } from "@/lib/format";

export function ApprovalList({ approvals, runs, projects, templates, approvingRunID, rejectingRunID, onApprove, onReject }: { approvals: Approval[]; runs: TaskRun[]; projects: Project[]; templates: TaskTemplate[]; approvingRunID?: string; rejectingRunID?: string; onApprove?: (runID: string) => void; onReject?: (runID: string) => void }): ReactNode {
  if (!approvals.length) return <EmptyState title="No approvals waiting" icon={ShieldCheck} />;
  return <div className="grid gap-2 p-3">{approvals.map((approval) => { const run = runs.find((item) => item.id === approval.run_id); return <div key={approval.id} className="grid gap-2 rounded-lg border p-3"><div className="flex justify-between"><div><p className="font-medium">{run ? templateName(templates, run.template_id, run) : approval.run_id}</p><p className="text-sm text-muted-foreground">{run ? projectName(projects, run.project_id) : "Run unavailable"} · requested {formatDate(approval.created_at)}</p></div><StatusBadge status={approval.status} /></div>{run ? <div className="text-sm text-muted-foreground"><GitBranch className="mr-1 inline h-4 w-4" />{run.run_spec.type} · {run.runner_tags.join(", ") || "untagged"}</div> : null}{approval.status === "pending" && onApprove && onReject ? <div className="flex gap-2"><Button size="sm" disabled={approvingRunID === approval.run_id} onClick={() => onApprove(approval.run_id)}><CheckCircle2 className="mr-1 h-4 w-4" />Approve</Button><Button size="sm" variant="outline" disabled={rejectingRunID === approval.run_id} onClick={() => onReject(approval.run_id)}><XCircle className="mr-1 h-4 w-4" />Reject</Button></div> : null}</div>; })}</div>;
}

export function ApprovalsView({ approvals, runs, projects, templates, q, loading, approvingRunID, rejectingRunID, onApprove, onReject }: { approvals: Approval[]; runs: TaskRun[]; projects: Project[]; templates: TaskTemplate[]; q?: string; loading?: boolean; approvingRunID?: string; rejectingRunID?: string; onApprove: (runID: string) => void; onReject: (runID: string) => void }): ReactNode {
  if (loading) return <SkeletonTable rows={5} />;
  const visibleApprovals = approvals.filter((approval) => matchesQuery(q, approval.id, approval.run_id, approval.status));
  const pending = visibleApprovals.filter((approval) => approval.status === "pending");
  return <section className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_320px]"><Card><CardHeader><CardTitle>Pending approvals {pending.length ? <span className="text-warning">· {pending.length}</span> : null}</CardTitle></CardHeader><CardContent className="p-0"><ApprovalList approvals={pending} runs={runs} projects={projects} templates={templates} approvingRunID={approvingRunID} rejectingRunID={rejectingRunID} onApprove={onApprove} onReject={onReject} /></CardContent></Card><Card><CardHeader><CardTitle>History</CardTitle></CardHeader><CardContent className="grid gap-2">{visibleApprovals.map((approval) => <div key={approval.id} className="flex justify-between rounded border p-2"><span>{approval.run_id}</span><StatusBadge status={approval.status} /></div>)}</CardContent></Card></section>;
}
