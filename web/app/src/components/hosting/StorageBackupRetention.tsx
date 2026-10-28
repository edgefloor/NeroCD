import { ApiError } from "@/api/errors";
import type { Resource } from "@/api/hosting";
import { storageRequestID } from "@/api/hosting-storage";
import type { ArchiveScope } from "@/api/hosting-storage-archives";
import type {
  RetentionExecution,
  RetentionPolicy,
} from "@/api/hosting-storage-backup-retention";
import {
  createRetentionPreview,
  executeRetention,
  getRetentionPolicy,
  listRetentionExecutions,
  saveRetentionPolicy,
  type RetentionExecutionInput,
  type RetentionPolicyInput,
  type RetentionPreview,
  type RetentionPreviewInput,
} from "@/api/hosting-storage-backup-retention";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { UseQueryResult } from "@tanstack/react-query";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

type Attempt =
  | { kind: "policy"; body: RetentionPolicyInput }
  | { kind: "preview"; body: RetentionPreviewInput }
  | { kind: "execute"; body: RetentionExecutionInput };
const defaults = () => ({ keep_count: 7, keep_days: 0, max_bytes: 0 });
export function StorageBackupRetention({
  scope,
  resource,
  owner,
  blocked,
  onDenied,
}: {
  scope: ArchiveScope;
  resource: Resource;
  owner: boolean;
  blocked: boolean;
  onDenied: () => void;
}) {
  const client = useQueryClient();
  const key = [
    "storage-retention",
    scope.actor,
    scope.project,
    scope.resource,
    scope.destination,
    scope.binding,
  ];
  const policy = useQuery({
    queryKey: [...key, "policy"],
    queryFn: ({ signal }) => getRetentionPolicy(scope, resource, signal),
    enabled: !blocked,
    retry: false,
  });
  const executions = useQuery({
    queryKey: [...key, "executions"],
    queryFn: ({ signal }) => listRetentionExecutions(scope, resource, signal),
    enabled: !blocked,
    retry: false,
    refetchInterval: 5000,
  });
  const [local, setLocal] = useState(defaults),
    [remote, setRemote] = useState(defaults),
    [enabled, setEnabled] = useState(false),
    [automatic, setAutomatic] = useState(false);
  const [preview, setPreview] = useState<RetentionPreview | null>(null),
    [attempt, setAttempt] = useState<Attempt | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  useEffect(() => {
    if (policy.data) {
      setLocal(policy.data.local);
      setRemote(policy.data.remote);
      setEnabled(policy.data.enabled);
      setAutomatic(policy.data.automatic);
    }
  }, [policy.data]);
  useEffect(() => {
    const e = policy.error || executions.error;
    if (e instanceof ApiError && [401, 403].includes(e.status)) onDenied();
  }, [policy.error, executions.error, onDenied]);
  async function send(next: Attempt) {
    if (blocked || busy || !owner) return;
    if (
      next.kind === "policy" &&
      [next.body.local, next.body.remote].some(
        (limits) =>
          !Number.isSafeInteger(limits.keep_count) ||
          limits.keep_count < 0 ||
          limits.keep_count > 10000 ||
          !Number.isSafeInteger(limits.keep_days) ||
          limits.keep_days < 0 ||
          limits.keep_days > 36500 ||
          !Number.isSafeInteger(limits.max_bytes) ||
          limits.max_bytes < 0 ||
          limits.max_bytes > 2 ** 50,
      )
    ) {
      setError(
        "Enter whole nonnegative limits within the supported range, then save again.",
      );
      return;
    }
    setAttempt(next);
    setBusy(true);
    setError("");
    try {
      if (next.kind === "policy") {
        await saveRetentionPolicy(scope, resource, next.body);
        if (mounted.current) setPreview(null);
      } else if (next.kind === "preview") {
        const out = await createRetentionPreview(scope, resource, next.body);
        if (mounted.current) setPreview(out);
      } else await executeRetention(scope, resource, next.body);
      if (!mounted.current) return;
      setAttempt(null);
      await client.invalidateQueries({ queryKey: key });
    } catch (e) {
      if (!mounted.current) return;
      if (e instanceof ApiError && [401, 403].includes(e.status)) {
        onDenied();
        setPreview(null);
      }
      if (
        e instanceof ApiError &&
        [400, 401, 403, 404, 409, 422].includes(e.status)
      ) {
        setAttempt(null);
        setPreview(null);
        setError(
          e.status === 409
            ? "The policy or copies changed. Refresh the current policy and review a new preview before deleting."
            : "The request was rejected. Correct the limits or refresh access, then try again.",
        );
        await client.invalidateQueries({ queryKey: key });
      } else {
        setError(
          "The request result is unconfirmed. Retry the identical saved request or discard it locally. Discarding does not cancel work already accepted by the server. Check batch history before starting another deletion.",
        );
      }
    } finally {
      if (mounted.current) setBusy(false);
    }
  }
  const disabled = blocked || busy || !!attempt || !owner;
  return (
    <section
      aria-label="Backup retention"
      className="min-w-0 space-y-3 border-t pt-3"
    >
      <h5 className="font-medium">Backup retention</h5>
      <p>
        Retention applies to this resource's saved upload volume. Local and
        remote limits are independent. Zero means unlimited. Successful backup
        history remains after copy deletion. Existing resources have cleanup
        disabled until an owner saves a policy.
      </p>
      {policy.isError || executions.isError ? (
        <p role="alert">
          Retention status is unavailable. Refresh access before continuing.
        </p>
      ) : null}
      {owner ? (
        <StorageRetentionPolicyEditor
          enabled={enabled}
          disabled={disabled}
          setEnabled={setEnabled}
          automatic={automatic}
          setAutomatic={setAutomatic}
          local={local}
          remote={remote}
          setLocal={setLocal}
          setRemote={setRemote}
          policy={policy}
          send={send}
          resource={resource}
        />
      ) : null}
      {preview ? (
        <StorageRetentionSelection
          preview={preview}
          disabled={disabled}
          policyRevision={policy.data?.revision}
          send={send}
        />
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
      {attempt && !busy ? (
        <div className="flex flex-wrap gap-2">
          <Button disabled={blocked || !owner} onClick={() => send(attempt)}>
            Retry saved retention request
          </Button>
          <Button
            variant="outline"
            disabled={blocked || !owner}
            onClick={() => {
              setAttempt(null);
              setPreview(null);
              setError(
                "Saved request discarded locally. This does not cancel work already accepted by the server. Check batch history before starting another deletion.",
              );
              void client.invalidateQueries({ queryKey: key });
            }}
          >
            Discard saved request locally
          </Button>
        </div>
      ) : null}
      <ul>
        {executions.data?.map((execution) => (
          <StorageRetentionExecution
            key={execution.id}
            execution={execution}
            owner={owner}
            disabled={disabled}
            send={send}
          />
        ))}
      </ul>
    </section>
  );
}

function StorageRetentionExecution({
  execution,
  owner,
  disabled,
  send,
}: {
  execution: RetentionExecution;
  owner: boolean;
  disabled: boolean;
  send: (next: Attempt) => Promise<void>;
}) {
  return (
    <li key={execution.id} className="break-all">
      Batch {execution.id}: {execution.state}. Local {execution.local_state};
      remote {execution.remote_state}.{" "}
      {execution.reservation_held
        ? "Copies remain reserved while effects are unresolved."
        : ""}
      <ul>
        {execution.items.map((item) => (
          <li key={item.copy_id}>
            {item.backup_id} · {item.location} · {item.state}
            {item.failure_code ? ` · ${item.failure_code}` : ""}
          </li>
        ))}
      </ul>
      {owner && execution.reservation_held ? (
        <Button
          variant="outline"
          disabled={disabled}
          onClick={() =>
            send({
              kind: "execute",
              body: {
                request_id: storageRequestID(),
                preview_id: execution.preview_id,
                expected_policy_revision: execution.policy_revision,
                expected_plan_hash: execution.plan_hash,
              },
            })
          }
        >
          Resume this exact batch
        </Button>
      ) : null}
    </li>
  );
}

function StorageRetentionPolicyEditor({
  enabled,
  disabled,
  setEnabled,
  automatic,
  setAutomatic,
  local,
  remote,
  setLocal,
  setRemote,
  policy,
  send,
  resource,
}: {
  enabled: boolean;
  disabled: boolean;
  setEnabled: React.Dispatch<React.SetStateAction<boolean>>;
  automatic: boolean;
  setAutomatic: React.Dispatch<React.SetStateAction<boolean>>;
  local: { keep_count: number; keep_days: number; max_bytes: number };
  remote: { keep_count: number; keep_days: number; max_bytes: number };
  setLocal: React.Dispatch<
    React.SetStateAction<{
      keep_count: number;
      keep_days: number;
      max_bytes: number;
    }>
  >;
  setRemote: React.Dispatch<
    React.SetStateAction<{
      keep_count: number;
      keep_days: number;
      max_bytes: number;
    }>
  >;
  policy: UseQueryResult<RetentionPolicy | null, Error>;
  send: (next: Attempt) => Promise<void>;
  resource: Resource;
}) {
  return (
    <>
      <label className="flex gap-2">
        <input
          type="checkbox"
          checked={enabled}
          disabled={disabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        Enable retention
      </label>
      <label className="flex gap-2">
        <input
          type="checkbox"
          checked={automatic}
          disabled={disabled}
          onChange={(e) => setAutomatic(e.target.checked)}
        />
        Run after successful backup publication
      </label>
      {(["local", "remote"] as const).map((location) => {
        const value = location === "local" ? local : remote;
        const update = location === "local" ? setLocal : setRemote;
        return (
          <fieldset
            key={location}
            disabled={disabled}
            className="min-w-0 space-y-2"
          >
            <legend>
              {location === "local" ? "Local copies" : "Remote copies"}
            </legend>
            {(["keep_count", "keep_days", "max_bytes"] as const).map(
              (field) => (
                <label key={field} className="block">
                  {field === "keep_count"
                    ? "Keep count"
                    : field === "keep_days"
                      ? "Keep days"
                      : "Maximum bytes"}
                  <Input
                    aria-label={`${location} ${field}`}
                    type="number"
                    min="0"
                    step="1"
                    value={value[field]}
                    onChange={(e) =>
                      update({ ...value, [field]: Number(e.target.value) })
                    }
                  />
                </label>
              ),
            )}
          </fieldset>
        );
      })}
      <Button
        disabled={disabled || policy.isPending || policy.isError}
        onClick={() =>
          send({
            kind: "policy",
            body: {
              request_id: storageRequestID(),
              expected_revision: policy.data?.revision ?? 0,
              expected_resource_generation: resource.generation,
              enabled,
              automatic,
              local,
              remote,
            },
          })
        }
      >
        Save retention policy
      </Button>
      <Button
        variant="outline"
        disabled={disabled || !policy.data?.enabled}
        onClick={() =>
          send({
            kind: "preview",
            body: {
              request_id: storageRequestID(),
              expected_policy_revision: policy.data!.revision,
            },
          })
        }
      >
        Review deletion preview
      </Button>
    </>
  );
}

function StorageRetentionSelection({
  preview,
  disabled,
  policyRevision,
  send,
}: {
  preview: RetentionPreview;
  disabled: boolean;
  policyRevision: number | undefined;
  send: (next: Attempt) => Promise<void>;
}) {
  return (
    <div className="space-y-2">
      <p>
        Preview evaluated at {preview.evaluated_at}. Batch {preview.id}.{" "}
        {preview.selection_truncated
          ? "The selection is bounded. Review another preview after this batch completes."
          : ""}
      </p>
      <ul>
        {preview.items.map((copy) => (
          <li key={copy.copy_id} className="break-all">
            {copy.backup_id} · {copy.location} · {copy.availability} ·{" "}
            {copy.bytes} bytes ·{" "}
            {copy.selected ? "Selected for deletion" : "Retained"}
            {copy.protected_reasons.length
              ? ` · ${copy.protected_reasons.join(", ")}`
              : ""}
          </li>
        ))}
      </ul>
      <Button
        disabled={
          disabled ||
          !preview.items.some((c) => c.selected) ||
          preview.policy_revision !== policyRevision
        }
        onClick={() =>
          send({
            kind: "execute",
            body: {
              request_id: storageRequestID(),
              preview_id: preview.id,
              expected_policy_revision: preview.policy_revision,
              expected_plan_hash: preview.plan_hash,
            },
          })
        }
      >
        Delete selected copies
      </Button>
    </div>
  );
}
