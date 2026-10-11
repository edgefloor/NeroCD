import type { ReactNode } from "react";
import { Activity, ArchiveRestore, HeartPulse, ListRestart, ShieldAlert } from "lucide-react";
import type { OperationsStatus, RunLogRetentionExecution, RunLogRetentionStatus } from "@/api";
import { MetricCard } from "@/components/common/MetricCard";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { RunLogRetentionCard } from "@/components/operations/RunLogRetentionCard";

function seconds(value: number | undefined): string {
  if (!Number.isFinite(value) || value === undefined) return "—";
  if (value < 60) return `${Math.round(value)}s`;
  return `${Math.round(value / 60)}m`;
}

export function OperationsView({ status, loading, unavailable, canAdmin, retention, retentionLoading, retentionUnavailable, retentionBusy, retentionSaveError, retentionExecution, onRetentionReset, onRetentionUpdate, onRetentionExecute }: { status?: OperationsStatus; loading: boolean; unavailable: boolean; canAdmin: boolean; retention?: RunLogRetentionStatus; retentionLoading?: boolean; retentionUnavailable?: boolean; retentionBusy?: boolean; retentionSaveError?: string; retentionExecution?: RunLogRetentionExecution; onRetentionReset?: () => void; onRetentionUpdate?: (input: { enabled: boolean; keep_days: number; batch_size: number }) => void; onRetentionExecute?: (policyVersion: number, requestID: string) => Promise<unknown> | void }): ReactNode {
  if (!canAdmin) return <Card><CardContent className="p-6" role="alert">System Operations is restricted to global administrators.</CardContent></Card>;
  if (loading) return <Card><CardContent className="p-6" role="status">Loading authoritative operations status…</CardContent></Card>;
  if (unavailable || !status) return <Card className="border-warning/30"><CardContent className="p-6" role="alert"><div className="flex gap-2"><ShieldAlert className="mt-0.5 h-5 w-5 text-warning" /><div><p className="font-medium">Operations status is temporarily unavailable</p><p className="mt-1 text-sm text-muted-foreground">No partial operational snapshot is shown. Check readiness and retry after the database and schema are healthy.</p></div></div></CardContent></Card>;
  const snapshot = status.snapshot;
  const activeDeployments = (snapshot.deployments.applying ?? 0) + (snapshot.deployments.verifying ?? 0) + (snapshot.deployments.rolling_back ?? 0);
  return <section className="space-y-6" aria-label="System Operations status" aria-live="polite">
    <div className="flex flex-wrap items-end justify-between gap-3"><div><h2 className="text-xl font-semibold">System Operations</h2><p className="text-sm text-muted-foreground">Authoritative PostgreSQL snapshot. Refreshes while this page is visible.</p></div><span className="rounded-full bg-success/10 px-3 py-1 text-sm font-medium text-success">Ready</span></div>
    <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
      <MetricCard label="Queued work" value={snapshot.queue_depth} caption={`Oldest ${seconds(snapshot.queue_oldest_age_seconds)}`} icon={ListRestart} tone={snapshot.queue_depth > 0 ? "warning" : "neutral"} />
      <MetricCard label="Active leases" value={snapshot.active_leases} caption={`${snapshot.expired_leases} expired lease${snapshot.expired_leases === 1 ? "" : "s"}`} icon={Activity} tone={snapshot.expired_leases > 0 ? "warning" : "success"} />
      <MetricCard label="Runner heartbeat" value={seconds(snapshot.oldest_runner_heartbeat_seconds)} caption={`Journal depth ${snapshot.runner_journal_depth}`} icon={HeartPulse} tone={snapshot.oldest_runner_heartbeat_seconds > 120 ? "warning" : "success"} />
      <MetricCard label="Backup" value={snapshot.backup_outcome} caption={snapshot.backup_schedule_status === "disabled" ? "Local schedule disabled" : `${snapshot.backup_schedule_status}; next ${seconds(snapshot.backup_schedule_next_seconds)}`} icon={ArchiveRestore} tone={snapshot.backup_outcome === "failure" || snapshot.backup_schedule_status === "backoff" ? "warning" : snapshot.backup_outcome === "success" ? "success" : "neutral"} />
    </div>
    <div className="grid gap-4 lg:grid-cols-2">
      <Card><CardHeader><CardTitle>Deployments</CardTitle></CardHeader><CardContent className="space-y-2 text-sm"><p><strong>{activeDeployments}</strong> active apply, verify, or rollback operation{activeDeployments === 1 ? "" : "s"}.</p><p className="text-muted-foreground">Health: {snapshot.deployment_health_passed} passed · {snapshot.deployment_health_failed} failed</p><p className="text-muted-foreground">Rollbacks: {snapshot.rollback_succeeded} succeeded · {snapshot.rollback_failed} failed</p></CardContent></Card>
      <Card><CardHeader><CardTitle>Database and runner telemetry</CardTitle></CardHeader><CardContent className="space-y-2 text-sm"><p><strong>{snapshot.pool.acquired}</strong> PostgreSQL connections acquired of {snapshot.pool.total}; {snapshot.pool.idle} idle.</p><p className="text-muted-foreground">Runner retries: {snapshot.runner_retry_count} · renewal failures: {snapshot.runner_renew_failures}</p><p className="text-muted-foreground">Backup schedule: {snapshot.backup_schedule_status} · {snapshot.backup_schedule_failures} consecutive failure{snapshot.backup_schedule_failures === 1 ? "" : "s"}</p><p className="text-muted-foreground">Backup reason: {snapshot.backup_reason}</p></CardContent></Card>
    </div>
    <RunLogRetentionCard status={retention} loading={retentionLoading} unavailable={retentionUnavailable} busy={retentionBusy} saveError={retentionSaveError} execution={retentionExecution} onReset={onRetentionReset} onUpdate={onRetentionUpdate} onExecute={onRetentionExecute} />
    <Card><CardHeader><CardTitle>Operator response</CardTitle></CardHeader><CardContent className="text-sm text-muted-foreground"><p>Investigate an increasing queue age, expired leases, stale runner heartbeats, failed deployment health, or failed backups. The page intentionally omits raw metrics, identifiers, paths, and diagnostic payloads; use the protected operational runbook and audited resources for follow-up.</p></CardContent></Card>
  </section>;
}
