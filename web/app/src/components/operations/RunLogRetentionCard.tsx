import { type FormEvent, type ReactNode, useRef, useState } from "react";
import type { RunLogRetentionExecution, RunLogRetentionStatus } from "@/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";

type RunLogRetentionCardProps = {
  status?: RunLogRetentionStatus;
  loading?: boolean;
  unavailable?: boolean;
  busy?: boolean;
  saveError?: string;
  execution?: RunLogRetentionExecution;
  onUpdate?: (input: { enabled: boolean; keep_days: number; batch_size: number }) => void;
  onExecute?: (policyVersion: number, requestID: string) => Promise<unknown> | void;
};

export function RunLogRetentionCard({
  status, loading, unavailable, busy, saveError, execution, onUpdate, onExecute,
}: RunLogRetentionCardProps): ReactNode {
  const [confirming, setConfirming] = useState(false);
  const [confirmation, setConfirmation] = useState<RunLogRetentionStatus>();
  const [executing, setExecuting] = useState(false);
  const [executionError, setExecutionError] = useState("");
  const executingRef = useRef(false);
  const requestID = useRef<string>("");
  const policy = status?.policy;
  const preview = status?.preview;
  if (loading) return <Card><CardHeader><CardTitle>Run-log retention</CardTitle></CardHeader><CardContent role="status">Loading retention policy…</CardContent></Card>;
  if (unavailable || !policy || !preview) return <Card><CardHeader><CardTitle>Run-log retention</CardTitle></CardHeader><CardContent role="alert">Retention controls are unavailable. No deletion can be requested until the authoritative policy is available.</CardContent></Card>;
  const update = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy) return;
    const form = new FormData(event.currentTarget);
    onUpdate?.({
      enabled: form.get("enabled") === "on",
      keep_days: Number(form.get("keep_days")),
      batch_size: Number(form.get("batch_size")),
    });
  };
  const openConfirm = () => {
    requestID.current = crypto.randomUUID();
    setConfirmation(status);
    setExecutionError("");
    setConfirming(true);
  };
  const confirmedPolicy = confirmation?.policy ?? policy;
  const confirmedPreview = confirmation?.preview ?? preview;
  const executeBatch = async () => {
    if (executingRef.current || busy || !onExecute) return;
    executingRef.current = true;
    setExecuting(true);
    setExecutionError("");
    try {
      await onExecute(confirmedPolicy.version, requestID.current);
      setConfirming(false);
    } catch (cause) {
      setExecutionError(cause instanceof Error ? cause.message : "Please try again.");
    } finally {
      executingRef.current = false;
      setExecuting(false);
    }
  };
  return (
    <Card>
      <CardHeader><CardTitle>Manual run-log retention</CardTitle></CardHeader>
      <CardContent className="grid gap-4">
        <p className="text-sm text-muted-foreground">Only terminal-run logs older than the policy cutoff and without an active lease are eligible. Artifacts, audit evidence, deployments, and runs are never removed.</p>
        <dl className="grid gap-1 text-sm sm:grid-cols-3">
          <div><dt className="text-muted-foreground">Eligible logs</dt><dd>{preview.eligible_logs}</dd></div>
          <div><dt className="text-muted-foreground">Eligible bytes</dt><dd>{preview.eligible_bytes}</dd></div>
          <div><dt className="text-muted-foreground">Cutoff</dt><dd><time dateTime={preview.cutoff}>{new Date(preview.cutoff).toLocaleString()}</time></dd></div>
        </dl>
        <form className="grid gap-3 sm:grid-cols-3" aria-label="Run-log retention policy" aria-busy={Boolean(busy)} onSubmit={update}>
          <label className="flex items-center gap-2 text-sm">
            <input disabled={busy} name="enabled" type="checkbox" defaultChecked={policy.enabled} /> Enable manual retention
          </label>
          <label className="grid gap-1 text-sm">
            Keep days
            <Input disabled={busy} name="keep_days" type="number" min={1} max={3650} defaultValue={policy.keep_days} required />
          </label>
          <label className="grid gap-1 text-sm">
            Batch size
            <Input disabled={busy} name="batch_size" type="number" min={1} max={10000} defaultValue={policy.batch_size} required />
          </label>
          <div className="sm:col-span-3 flex flex-wrap gap-2">
            <Button type="submit" variant="outline" disabled={busy}>Save policy</Button>
            <Button type="button" disabled={!policy.enabled || busy} onClick={openConfirm}>Previewed delete batch</Button>
          </div>
          {saveError && !busy ? <p role="alert" className="break-words text-sm text-destructive [overflow-wrap:anywhere] sm:col-span-3">Unable to save retention policy: {saveError}</p> : null}
        </form>
        {execution ? <p className="rounded border p-3 text-sm" role="status">Execution receipt: deleted {execution.deleted} logs ({execution.deleted_bytes} bytes). Aggregate result only.</p> : null}
        <Dialog open={confirming} onOpenChange={(open) => { if (!executingRef.current) setConfirming(open); }}>
          <DialogContent showCloseButton={!executing} className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
            <DialogHeader>
              <DialogTitle>Execute one retention batch?</DialogTitle>
              <DialogDescription>This is a bounded, audited deletion of up to {confirmedPolicy.batch_size} eligible terminal-run log records. The request is idempotent; it cannot delete artifacts, audit records, deployments, or runs.</DialogDescription>
            </DialogHeader>
            <dl className="grid gap-1 text-sm">
              <div>Eligible logs: {confirmedPreview.eligible_logs}</div>
              <div>Eligible bytes: {confirmedPreview.eligible_bytes}</div>
              <div>Cutoff: {new Date(confirmedPreview.cutoff).toLocaleString()}</div>
            </dl>
            {executing ? <p role="status">Executing retention batch...</p> : null}
            {executionError ? <p role="alert" className="break-words text-sm text-destructive [overflow-wrap:anywhere]">Unable to execute retention batch: {executionError}</p> : null}
            <DialogFooter>
              <Button variant="outline" disabled={executing} onClick={() => setConfirming(false)}>Cancel</Button>
              <Button disabled={busy || executing} onClick={() => void executeBatch()}>{executing ? "Executing..." : "Execute retained batch"}</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </CardContent>
    </Card>
  );
}
