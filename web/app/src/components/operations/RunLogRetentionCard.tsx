import type { RunLogRetentionExecution, RunLogRetentionStatus } from "@/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  type Dispatch,
  type FormEvent,
  type ReactNode,
  type RefObject,
  type SetStateAction,
  useEffect,
  useRef,
  useState,
} from "react";

type RunLogRetentionCardProps = {
  status?: RunLogRetentionStatus;
  loading?: boolean;
  unavailable?: boolean;
  busy?: boolean;
  saveError?: string;
  execution?: RunLogRetentionExecution;
  onReset?: () => void;
  onUpdate?: (input: {
    enabled: boolean;
    keep_days: number;
    batch_size: number;
  }) => void;
  onExecute?: (
    policyVersion: number,
    requestID: string,
  ) => Promise<unknown> | void;
};

function hasPolicyChanges(
  form: HTMLFormElement,
  policy: RunLogRetentionStatus["policy"],
): boolean {
  const enabled = form.elements.namedItem("enabled") as HTMLInputElement;
  const days = form.elements.namedItem("keep_days") as HTMLInputElement;
  const batch = form.elements.namedItem("batch_size") as HTMLInputElement;
  return (
    enabled.checked !== policy.enabled ||
    Number(days.value) !== policy.keep_days ||
    Number(batch.value) !== policy.batch_size
  );
}

export function RunLogRetentionCard({
  status,
  loading,
  unavailable,
  busy,
  saveError,
  execution,
  onReset,
  onUpdate,
  onExecute,
}: RunLogRetentionCardProps): ReactNode {
  const formRef = useRef<HTMLFormElement>(null);
  const [draftChanged, setDraftChanged] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [confirmation, setConfirmation] = useState<RunLogRetentionStatus>();
  const [executing, setExecuting] = useState(false);
  const [executionError, setExecutionError] = useState("");
  const executingRef = useRef(false);
  const requestID = useRef<string>("");
  const policy = status?.policy;
  const preview = status?.preview;
  useEffect(() => {
    if (formRef.current && policy)
      setDraftChanged(hasPolicyChanges(formRef.current, policy));
  }, [
    policy?.enabled,
    policy?.keep_days,
    policy?.batch_size,
    loading,
    unavailable,
  ]);
  if (loading)
    return (
      <Card>
        <CardHeader>
          <CardTitle>Run-log retention</CardTitle>
        </CardHeader>
        <CardContent role="status">Loading retention policy…</CardContent>
      </Card>
    );
  if (!policy || !preview)
    return (
      <Card>
        <CardHeader>
          <CardTitle>Run-log retention</CardTitle>
        </CardHeader>
        <CardContent role="alert">
          Retention controls are unavailable. No deletion can be requested until
          the authoritative policy is available.
        </CardContent>
      </Card>
    );
  const update = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy || unavailable) return;
    const form = new FormData(event.currentTarget);
    onUpdate?.({
      enabled: form.get("enabled") === "on",
      keep_days: Number(form.get("keep_days")),
      batch_size: Number(form.get("batch_size")),
    });
  };
  const openConfirm = () => {
    if (
      busy ||
      unavailable ||
      !policy.enabled ||
      !formRef.current ||
      hasPolicyChanges(formRef.current, policy)
    )
      return;
    requestID.current = crypto.randomUUID();
    setConfirmation(status);
    setExecutionError("");
    setConfirming(true);
  };
  const confirmedPolicy = confirmation?.policy ?? policy;
  const confirmedPreview = confirmation?.preview ?? preview;
  const executeBatch = async () => {
    if (executingRef.current || busy || unavailable || !onExecute) return;
    executingRef.current = true;
    setExecuting(true);
    setExecutionError("");
    try {
      await onExecute(confirmedPolicy.version, requestID.current);
      setConfirming(false);
    } catch (cause) {
      setExecutionError(
        cause instanceof Error ? cause.message : "Please try again.",
      );
    } finally {
      executingRef.current = false;
      setExecuting(false);
    }
  };
  return (
    <Card>
      <CardHeader>
        <CardTitle>Manual run-log retention</CardTitle>
      </CardHeader>
      <CardContent className="grid gap-4">
        {unavailable ? (
          <p role="alert" className="text-sm text-warning">
            Refresh failed. Your draft is preserved. Retry operations before
            saving or deleting logs.
          </p>
        ) : null}
        <p className="text-sm text-muted-foreground">
          Only terminal-run logs older than the policy cutoff and without an
          active lease are eligible. Artifacts, audit evidence, deployments, and
          runs are never removed.
        </p>
        <dl className="grid gap-1 text-sm sm:grid-cols-3">
          <div>
            <dt className="text-muted-foreground">Eligible logs</dt>
            <dd>{preview.eligible_logs}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">Eligible bytes</dt>
            <dd>{preview.eligible_bytes}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">Cutoff</dt>
            <dd>
              <time dateTime={preview.cutoff}>
                {new Date(preview.cutoff).toLocaleString()}
              </time>
            </dd>
          </div>
        </dl>
        <RetentionPolicyForm
          formRef={formRef}
          setDraftChanged={setDraftChanged}
          policy={policy}
          busy={busy}
          update={update}
          onReset={onReset}
          unavailable={unavailable}
          draftChanged={draftChanged}
          openConfirm={openConfirm}
          saveError={saveError}
        />
        {execution ? (
          <p className="rounded border p-3 text-sm" role="status">
            Execution receipt: deleted {execution.deleted} logs (
            {execution.deleted_bytes} bytes). Aggregate result only.
          </p>
        ) : null}
        <RetentionConfirmation
          confirming={confirming}
          executingRef={executingRef}
          setConfirming={setConfirming}
          executing={executing}
          confirmedPolicy={confirmedPolicy}
          confirmedPreview={confirmedPreview}
          executionError={executionError}
          unavailable={unavailable}
          busy={busy}
          executeBatch={executeBatch}
        />
      </CardContent>
    </Card>
  );
}

function RetentionConfirmation({
  confirming,
  executingRef,
  setConfirming,
  executing,
  confirmedPolicy,
  confirmedPreview,
  executionError,
  unavailable,
  busy,
  executeBatch,
}: {
  confirming: boolean;
  executingRef: RefObject<boolean>;
  setConfirming: Dispatch<SetStateAction<boolean>>;
  executing: boolean;
  confirmedPolicy: {
    enabled: boolean;
    keep_days: number;
    batch_size: number;
    version: number;
    updated_at: string;
  };
  confirmedPreview: {
    cutoff: string;
    eligible_logs: number;
    eligible_bytes: number;
  };
  executionError: string;
  unavailable: boolean | undefined;
  busy: boolean | undefined;
  executeBatch: () => Promise<void>;
}) {
  return (
    <Dialog
      open={confirming}
      onOpenChange={(open) => {
        if (!executingRef.current) setConfirming(open);
      }}
    >
      <DialogContent
        showCloseButton={!executing}
        className="max-h-[calc(100dvh-2rem)] overflow-y-auto"
      >
        <DialogHeader>
          <DialogTitle>Execute one retention batch?</DialogTitle>
          <DialogDescription>
            This is a bounded, audited deletion of up to{" "}
            {confirmedPolicy.batch_size} eligible terminal-run log records. The
            request is idempotent; it cannot delete artifacts, audit records,
            deployments, or runs.
          </DialogDescription>
        </DialogHeader>
        <dl className="grid gap-1 text-sm">
          <div>Eligible logs: {confirmedPreview.eligible_logs}</div>
          <div>Eligible bytes: {confirmedPreview.eligible_bytes}</div>
          <div>
            Cutoff: {new Date(confirmedPreview.cutoff).toLocaleString()}
          </div>
        </dl>
        {executing ? <p role="status">Executing retention batch...</p> : null}
        {executionError ? (
          <p
            role="alert"
            className="break-words text-sm text-destructive [overflow-wrap:anywhere]"
          >
            Unable to execute retention batch: {executionError}
          </p>
        ) : null}
        {unavailable && !executing ? (
          <p role="alert" className="text-sm text-warning">
            Refresh failed. Close this dialog and retry operations before
            executing a batch.
          </p>
        ) : null}
        <DialogFooter>
          <Button
            variant="outline"
            disabled={executing}
            onClick={() => setConfirming(false)}
          >
            Cancel
          </Button>
          <Button
            disabled={busy || unavailable || executing}
            onClick={() => void executeBatch()}
          >
            {executing ? "Executing..." : "Execute retained batch"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function RetentionPolicyForm({
  formRef,
  setDraftChanged,
  policy,
  busy,
  update,
  onReset,
  unavailable,
  draftChanged,
  openConfirm,
  saveError,
}: {
  formRef: RefObject<HTMLFormElement | null>;
  setDraftChanged: Dispatch<SetStateAction<boolean>>;
  policy: {
    enabled: boolean;
    keep_days: number;
    batch_size: number;
    version: number;
    updated_at: string;
  };
  busy: boolean | undefined;
  update: (event: FormEvent<HTMLFormElement>) => void;
  onReset: (() => void) | undefined;
  unavailable: boolean | undefined;
  draftChanged: boolean;
  openConfirm: () => void;
  saveError: string | undefined;
}) {
  return (
    <form
      ref={formRef}
      onChange={(event) =>
        setDraftChanged(hasPolicyChanges(event.currentTarget, policy))
      }
      className="grid gap-3 sm:grid-cols-3"
      aria-label="Run-log retention policy"
      aria-busy={Boolean(busy)}
      onSubmit={update}
      onReset={() => {
        setDraftChanged(false);
        onReset?.();
      }}
    >
      <label className="flex items-center gap-2 text-sm">
        <input
          disabled={busy || unavailable}
          name="enabled"
          type="checkbox"
          defaultChecked={policy.enabled}
        />{" "}
        Enable manual retention
      </label>
      <label className="grid gap-1 text-sm">
        Keep days
        <Input
          disabled={busy || unavailable}
          name="keep_days"
          type="number"
          min={1}
          max={3650}
          defaultValue={policy.keep_days}
          required
        />
      </label>
      <label className="grid gap-1 text-sm">
        Batch size
        <Input
          disabled={busy || unavailable}
          name="batch_size"
          type="number"
          min={1}
          max={10000}
          defaultValue={policy.batch_size}
          required
        />
      </label>
      <div className="sm:col-span-3 flex flex-wrap gap-2">
        <Button type="submit" variant="outline" disabled={busy || unavailable}>
          Save policy
        </Button>
        <Button type="reset" variant="outline" disabled={busy || unavailable}>
          Reset changes
        </Button>
        <Button
          type="button"
          disabled={!policy.enabled || busy || unavailable || draftChanged}
          onClick={openConfirm}
        >
          Previewed delete batch
        </Button>
      </div>
      {draftChanged ? (
        <p
          className="text-sm text-muted-foreground sm:col-span-3"
          role="status"
        >
          Save or reset your changes before previewing a deletion.
        </p>
      ) : null}
      {saveError && !busy ? (
        <p
          role="alert"
          className="break-words text-sm text-destructive [overflow-wrap:anywhere] sm:col-span-3"
        >
          Unable to save retention policy: {saveError}
        </p>
      ) : null}
    </form>
  );
}
