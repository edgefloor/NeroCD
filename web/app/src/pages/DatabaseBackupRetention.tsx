import type { RetentionContext, RetentionCopy } from "@/api/hosting-database-backup-retention";
import { readBackupMetadata } from "@/components/hosting/backup-access";
import { BackupAccessBoundary } from "@/components/hosting/BackupAccessBoundary";
import type { UseQueryResult } from "@tanstack/react-query";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { ApiError } from "../api/errors";
import {
  createRetentionPreview,
  executeRetention,
  getRetentionPolicy,
  listRetentionExecutions,
  retentionBytesToGiB,
  retentionGiBToBytes,
  retentionKey,
  saveRetentionPolicy,
  type RetentionExecution,
  type RetentionExecutionInput,
  type RetentionPolicy,
  type RetentionPolicyInput,
  type RetentionPreview,
  type RetentionPreviewInput,
} from "../api/hosting-database-backup-retention";
import {
  databaseDenied,
  databaseError,
  databaseRequestID,
  type DatabaseScope,
} from "../api/hosting-databases";
import { Button } from "../components/ui/button";

type Props = {
  database: RetentionContext;
  sourceEligible?: boolean;
  scope: DatabaseScope;
  owner: boolean;
  blocked: boolean;
  onDenied: () => void;
};
type Fields = { count: string; days: string; size: string };
type Draft = {
  revision: number;
  enabled: boolean;
  automatic: boolean;
  local: Fields;
  remote: Fields;
};
type Attempt =
  | { kind: "save"; input: RetentionPolicyInput }
  | { kind: "preview"; input: RetentionPreviewInput }
  | { kind: "execute"; input: RetentionExecutionInput }
  | { kind: "recover"; execution: string; input: RetentionExecutionInput };
const recoverable = (execution: RetentionExecution) =>
  execution.reservation_held &&
  ["dispatched", "applying", "uncertain"].includes(execution.state) &&
  execution.items.length > 0;
function matchesRetentionBatch(
  saved: RetentionExecution | undefined,
  expected: Pick<
    RetentionExecutionInput,
    "preview_id" | "expected_policy_revision" | "expected_plan_hash"
  >,
) {
  return (
    !!saved &&
    recoverable(saved) &&
    saved.preview_id === expected.preview_id &&
    saved.policy_revision === expected.expected_policy_revision &&
    saved.plan_hash === expected.expected_plan_hash
  );
}
const emptyFields = (): Fields => ({ count: "0", days: "0", size: "0" });
function draftFor(policy: RetentionPolicy | null): Draft {
  const fields = (v: RetentionPolicy["local"]) => ({
    count: String(v.keep_count),
    days: String(v.keep_days),
    size: retentionBytesToGiB(v.max_bytes),
  });
  return policy
    ? {
        revision: policy.revision,
        enabled: policy.enabled,
        automatic: policy.automatic,
        local: fields(policy.local),
        remote: fields(policy.remote),
      }
    : {
        revision: 0,
        enabled: false,
        automatic: false,
        local: emptyFields(),
        remote: emptyFields(),
      };
}
function limits(fields: Fields) {
  const count = /^\d{1,5}$/.test(fields.count) ? Number(fields.count) : NaN;
  const days = /^\d{1,5}$/.test(fields.days) ? Number(fields.days) : NaN;
  const bytes = retentionGiBToBytes(fields.size);
  return Number.isInteger(count) &&
    count <= 10000 &&
    Number.isInteger(days) &&
    days <= 36500 &&
    bytes !== undefined
    ? { keep_count: count, keep_days: days, max_bytes: bytes }
    : undefined;
}
export function DatabaseBackupRetention(props: Props) {
  const identity = JSON.stringify([
    props.scope.actor,
    props.scope.project,
    props.scope.environment,
    props.database.resource.id,
    props.database.resource.generation,
    props.database.engine,
    props.database.source_scope?.backup_version,
    props.database.source_scope?.component,
    props.owner,
    props.blocked,
  ]);
  return (
    <BackupAccessBoundary
      key={identity}
      onDenied={props.onDenied}
      message="Backup retention access is unavailable."
    >
      {(onDenied) => <RetentionPanel {...props} onDenied={onDenied} />}
    </BackupAccessBoundary>
  );
}
function RetentionPanel(props: Parameters<typeof useDatabaseRetention>[0]) {
  const {
    database,
    denied,
    owner,
    policy,
    history,
    draft,
    unavailable,
    local,
    remote,
    submit,
    edit,
    setError,
    current,
    changed,
    setPreview,
    setConfirmed,
    preview,
    selected,
    confirmed,
    pending,
    error,
    attempt,
    blocked,
    message,
    setRecovery,
    setRecoveryConfirmed,
    recovery,
    recoveryCurrent,
    recoveryConfirmed,
    sourceEligible,
  } = useDatabaseRetention(props);

  if (!["postgresql", "mysql"].includes(database.engine)) return null;
  if (denied)
    return <p role="alert">Backup retention access is unavailable.</p>;
  if (database.source_scope && !sourceEligible && policy.data === null)
    return <p>Compose MySQL retention is unavailable until a qualified mysql component or a matching saved policy is available.</p>;
  return (
    <section
      aria-label={database.source_scope ? "Compose MySQL backup retention" : "Database backup retention"}
      className="min-w-0 space-y-3 rounded-xl border p-3"
    >
      {database.source_scope ? <p>Compose MySQL retention applies only to logical backups of component {database.source_scope.component} recorded under this policy's saved source scope. It requires hosting_mysql_backup_retention_v1. Local and remote copies have independent limits and execution results.</p> : database.engine === "mysql" ? <p>Native MySQL retention requires hosting_mysql_backup_retention_v1. Local and remote copies have independent limits and execution results.</p> : null}
      <h4 className="font-medium">{database.source_scope ? "Compose MySQL" : database.engine === "mysql" ? "MySQL" : "PostgreSQL"} backup retention</h4>
      <p>
        Local and remote copies have independent limits. The first reached limit
        selects the oldest eligible copies. Zero means unlimited. Age is
        measured from the newest successful backup at each location, not the
        current time. The newest successful copy is protected and excluded from
        the size total.
      </p>
      <p>
        Preview lists owned copies and protection reasons. Each execution
        deletes only the selected batch, up to 100 copies from an inventory of
        up to 1,000. Every manual batch needs a new explicit preview and
        confirmation. Automatic cleanup acts under the saved enabled policy
        authorization when automatic retention is enabled. Runner support and
        current destination authorization are checked by the server.
      </p>
      <RetentionPolicyStatus owner={owner} policy={policy} history={history} />
      <RetentionPolicyEditor
        owner={owner}
        draft={draft}
        unavailable={unavailable}
        local={local}
        remote={remote}
        submit={submit}
        database={database}
        edit={edit}
        policy={policy}
        setError={setError}
        sourceEligible={sourceEligible}
      />
      <RetentionPreviewAction
        owner={owner}
        unavailable={unavailable}
        current={current}
        changed={changed}
        policy={policy}
        setPreview={setPreview}
        setConfirmed={setConfirmed}
        submit={submit}
      />
      {changed ? <p>Save these changes before previewing deletion.</p> : null}
      {preview ? (
        <RetentionPreviewReview
          preview={preview}
          selected={selected}
          confirmed={confirmed}
          unavailable={unavailable}
          setConfirmed={setConfirmed}
          current={current}
          changed={changed}
          policy={policy}
          submit={submit}
        />
      ) : null}
      <RetentionSubmissionStatus
        pending={pending}
        error={error}
        attempt={attempt}
        owner={owner}
        blocked={blocked}
        submit={submit}
        message={message}
      />
      <RetentionHistory
        history={history}
        owner={owner}
        unavailable={unavailable}
        current={current}
        setRecovery={setRecovery}
        setRecoveryConfirmed={setRecoveryConfirmed}
        setPreview={setPreview}
        setConfirmed={setConfirmed}
        setError={setError}
        recovery={recovery}
        recoveryCurrent={recoveryCurrent}
        recoveryConfirmed={recoveryConfirmed}
        submit={submit}
      />
      <Button
        className="h-auto max-w-full whitespace-normal"
        type="button"
        variant="outline"
        disabled={
          blocked ||
          pending ||
          Boolean(attempt) ||
          policy.isFetching ||
          history.isFetching
        }
        onClick={() => {
          setPreview(undefined);
          setConfirmed(false);
          void policy.refetch();
          void history.refetch();
        }}
      >
        Refresh retention history
      </Button>
    </section>
  );
}
function useDatabaseRetention({
  database,
  scope,
  owner,
  blocked,
  onDenied,
  sourceEligible,
}: Props) {
  const client = useQueryClient();
  const key = [...retentionKey(scope, database.resource.id), database.engine, database.source_scope ?? null];
  const [editedDraft, setDraft] = useState<Draft>();
  const [preview, setPreview] = useState<RetentionPreview>();
  const [confirmed, setConfirmed] = useState(false);
  const receivedPolicy = useRef("");
  const [recovery, setRecovery] = useState<RetentionExecution>();
  const [recoveryConfirmed, setRecoveryConfirmed] = useState(false);
  const [attempt, setAttempt] = useState<Attempt>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  const [message, setMessage] = useState("");
  const lifetime = useRef<AbortController | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    return () => {
      controller.abort();
      if (lifetime.current === controller) lifetime.current = null;
    };
  }, []);
  const enabled = !blocked && ["postgresql", "mysql"].includes(database.engine);
  const policy = useQuery({
    queryKey: [...key, "policy"],
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        async () => {
          const out = await getRetentionPolicy(scope, database, signal);
          const identity = JSON.stringify([out?.revision, out?.resource_generation, out?.source_scope]);
          if (!attempt && receivedPolicy.current !== identity) {
            setConfirmed(false);
            setRecoveryConfirmed(false);
          }
          receivedPolicy.current = identity;
          return out;
        },
        retireRetentionAccess,
        [401, 403],
      ),
    enabled,
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
  });
  const history = useQuery({
    queryKey: [...key, "executions"],
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => listRetentionExecutions(scope, database, signal),
        retireRetentionAccess,
        [401, 403],
      ),
    enabled,
    retry: false,
    gcTime: 0,
    refetchInterval: (q) =>
      q.state.data?.some((v) =>
        ["queued", "dispatched", "applying"].includes(v.state),
      )
        ? 3000
        : false,
  });
  const draft =
    editedDraft ??
    (policy.data !== undefined ? draftFor(policy.data) : undefined);
  const denied =
    databaseDenied(policy.error) ||
    databaseDenied(history.error) ||
    databaseDenied(error);
  function retireRetentionAccess() {
    lifetime.current?.abort();
    void client.cancelQueries({ queryKey: key });
    client.removeQueries({ queryKey: key });
    onDenied();
  }

  const unavailable =
    blocked ||
    pending ||
    Boolean(attempt) ||
    policy.isPending ||
    policy.isFetching ||
    policy.isError ||
    history.isPending ||
    history.isError;
  const { local, remote, changed, current } = retentionDraftStatus(
    draft,
    policy.data,
    database.resource.generation,
  );
  const [reviewedPolicy, setReviewedPolicy] = useState("");
  const policyIdentity = JSON.stringify([policy.data?.revision, policy.data?.resource_generation, policy.data?.source_scope, sourceEligible]);
  if (!attempt && reviewedPolicy !== policyIdentity) {
    setReviewedPolicy(policyIdentity);
    setConfirmed(false);
    setRecoveryConfirmed(false);
  }
  const selected = preview?.items.filter((v) => v.selected) ?? [];
  const latestRecovery = history.data?.find((v) => v.id === recovery?.id);
  const recoveryCurrent = Boolean(
    recovery &&
      matchesRetentionBatch(latestRecovery, {
        preview_id: recovery.preview_id,
        expected_policy_revision: recovery.policy_revision,
        expected_plan_hash: recovery.plan_hash,
      }),
  );
  function edit(next: Draft) {
    setDraft(next);
    setPreview(undefined);
    setConfirmed(false);
    setMessage("");
    setRecovery(undefined);
    setRecoveryConfirmed(false);
  }
  async function submit(next: Attempt) {
    const controller = lifetime.current;
    if (
      !owner ||
      blocked ||
      pending ||
      (attempt && next !== attempt) ||
      !controller ||
      controller.signal.aborted
    )
      return;
    if (next.kind === "recover" && !attempt) {
      const saved = history.data?.find((v) => v.id === next.execution);
      if (
        !current ||
        history.isFetching ||
        !matchesRetentionBatch(saved, next.input)
      )
        return;
    }
    setAttempt(next);
    setPending(true);
    setError(undefined);
    setMessage("");
    try {
      if (next.kind === "save") {
        const out = await saveRetentionPolicy(
          scope,
          database,
          next.input,
          controller.signal,
        );
        if (controller.signal.aborted) return;
        client.setQueryData([...key, "policy"], out);
        setDraft(draftFor(out));
        setPreview(undefined);
        setConfirmed(false);
        setMessage(`Saved retention revision ${out.revision}.`);
      } else if (next.kind === "preview") {
        const out = await createRetentionPreview(
          scope,
          database,
          next.input,
          controller.signal,
        );
        if (controller.signal.aborted) return;
        setPreview(out);
        setConfirmed(false);
      } else {
        const out = await executeRetention(
          scope,
          database,
          next.input,
          controller.signal,
          next.kind === "recover" ? next.execution : undefined,
        );
        if (controller.signal.aborted) return;
        setPreview(undefined);
        setConfirmed(false);
        setRecovery(undefined);
        setRecoveryConfirmed(false);
        setMessage(
          `Accepted retention execution ${out.id}. Review its independent local and remote results below.`,
        );
        client.setQueryData(
          [...key, "executions"],
          (
            old:
              | Awaited<ReturnType<typeof listRetentionExecutions>>
              | undefined,
          ) =>
            [out, ...(old ?? []).filter((v) => v.id !== out.id)].slice(0, 100),
        );
        void history.refetch();
      }
      setAttempt(undefined);
    } catch (cause) {
      if (!controller.signal.aborted) {
        setError(cause);
        if (databaseDenied(cause)) retireRetentionAccess();
        if (
          cause instanceof ApiError &&
          [400, 404, 409].includes(cause.status)
        ) {
          setAttempt(undefined);
          setRecovery(undefined);
          setRecoveryConfirmed(false);
          setPreview(undefined);
          setConfirmed(false);
        }
      }
    } finally {
      setPending(false);
    }
  }
  return {
    database,
    denied,
    owner,
    policy,
    history,
    draft,
    unavailable,
    local,
    remote,
    submit,
    edit,
    setError,
    current,
    changed,
    setPreview,
    setConfirmed,
    preview,
    selected,
    confirmed,
    pending,
    error,
    attempt,
    blocked,
    message,
    setRecovery,
    setRecoveryConfirmed,
    recovery,
    recoveryCurrent,
    recoveryConfirmed,
    sourceEligible,
  };
}

function RetentionPolicyEditor({
  owner,
  draft,
  unavailable,
  local,
  remote,
  submit,
  database,
  edit,
  policy,
  setError,
  sourceEligible,
}: {
  sourceEligible?: boolean;
  owner: boolean;
  draft: Draft | undefined;
  unavailable: boolean;
  local:
    | { keep_count: number; keep_days: number; max_bytes: number }
    | undefined;
  remote:
    | { keep_count: number; keep_days: number; max_bytes: number }
    | undefined;
  submit: (next: Attempt) => Promise<void>;
  database: RetentionContext;
  edit: (next: Draft) => void;
  policy: UseQueryResult<RetentionPolicy | null>;
  setError: React.Dispatch<unknown>;
}) {
  return (
    <>
      {owner && draft ? (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            if (unavailable || !local || !remote || (database.source_scope && !sourceEligible && (!policy.data || draft.enabled))) return;
            void submit({
              kind: "save",
              input: {
                request_id: databaseRequestID(),
                expected_revision: draft.revision,
                expected_resource_generation: database.resource.generation,
                enabled: draft.enabled,
                automatic: draft.automatic,
                local,
                remote,
              },
            });
          }}
          className="space-y-3"
        >
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={draft.enabled}
              disabled={unavailable}
              onChange={(e) => edit({ ...draft, enabled: e.target.checked })}
            />
            Retention enabled
          </label>
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={draft.automatic}
              disabled={unavailable}
              onChange={(e) => edit({ ...draft, automatic: e.target.checked })}
            />
            Automatic retention
          </label>
          <p>
            When enabled, automatic retention authorizes cleanup under the saved
            enabled policy when supported by the runner. Each manual batch
            requires the exact preview confirmation below.
          </p>
          {(["local", "remote"] as const).map((location) => (
            <fieldset
              key={location}
              disabled={unavailable}
              className="min-w-0 space-y-2"
            >
              <legend>
                {location === "local" ? "Local copies" : "Remote copies"}
              </legend>
              {(
                [
                  ["count", "Backups to keep", "10000"],
                  ["days", "Days to keep", "36500"],
                  ["size", "Maximum storage in GiB", "1048576"],
                ] as const
              ).map(([field, label, max]) => (
                <label key={field} className="block">
                  {location === "local" ? "Local" : "Remote"}{" "}
                  {label[0]!.toLowerCase() + label.slice(1)}
                  <input
                    aria-label={`${location === "local" ? "Local" : "Remote"} ${label[0]!.toLowerCase() + label.slice(1)}`}
                    inputMode={field === "size" ? "decimal" : "numeric"}
                    value={draft[location][field]}
                    maxLength={40}
                    className="block w-full min-w-0 rounded border p-2"
                    onChange={(e) =>
                      edit({
                        ...draft,
                        [location]: {
                          ...draft[location],
                          [field]: e.target.value,
                        },
                      })
                    }
                  />
                  <span>
                    0 = unlimited; maximum {max}
                    {field === "size"
                      ? " GiB. Must convert to whole bytes exactly."
                      : ". Whole numbers only."}
                  </span>
                </label>
              ))}
            </fieldset>
          ))}
          {!local || !remote ? (
            <p role="alert">
              Enter whole counts from 0 to 10,000, days from 0 to 36,500, and
              exact byte sizes from 0 to 1,048,576 GiB.
            </p>
          ) : null}
          <Button
            className="h-auto max-w-full whitespace-normal"
            type="submit"
            disabled={unavailable || !local || !remote || Boolean(database.source_scope && !sourceEligible && (!policy.data || draft.enabled))}
          >
            Save retention policy
          </Button>
          <Button
            className="h-auto max-w-full whitespace-normal"
            type="button"
            variant="outline"
            disabled={unavailable}
            onClick={() => {
              edit(draftFor(policy.data ?? null));
              setError(undefined);
            }}
          >
            Review saved retention policy
          </Button>
        </form>
      ) : null}
    </>
  );
}

function RetentionPreviewReview({
  preview,
  selected,
  confirmed,
  unavailable,
  setConfirmed,
  current,
  changed,
  policy,
  submit,
}: {
  preview: import("@/api/hosting-database-backup-retention").RetentionPreview;
  selected: RetentionCopy[];
  confirmed: boolean;
  unavailable: boolean;
  setConfirmed: React.Dispatch<React.SetStateAction<boolean>>;
  current: boolean | null | undefined;
  changed: boolean;
  policy: UseQueryResult<RetentionPolicy | null>;
  submit: (next: Attempt) => Promise<void>;
}) {
  return (
    <section
      aria-label="Retention deletion preview"
      className="min-w-0 space-y-2"
    >
      <h5>Review exact copies before deletion</h5>
      <p className="break-all">
        Preview {preview.id}, policy revision {preview.policy_revision}. Plan{" "}
        {preview.plan_hash}
      </p>
      <p>
        Local age reference:{" "}
        {preview.local_reference_at ?? "No successful copy"}. Remote age
        reference: {preview.remote_reference_at ?? "No successful copy"}.
      </p>
      {preview.selection_truncated ? (
        <p role="alert">
          Selection is truncated. Only this selected batch can execute.
          Remaining eligible copies need a new explicit preview after
          settlement.
        </p>
      ) : null}
      <ul className="space-y-2">
        {preview.items.map((copy) => (
          <li key={copy.copy_id} className="break-all">
            <p>
              {copy.selected ? "Selected for deletion" : "Not selected"}:{" "}
              {copy.backup_id} · {copy.location} · copy {copy.copy_id}
              {copy.transfer_id ? ` · transfer ${copy.transfer_id}` : ""}
            </p>
            <p>
              {copy.bytes.toLocaleString()} bytes · {copy.created_at} ·{" "}
              {copy.availability} · {copy.digest}
            </p>
            {copy.protected_reasons.length ? (
              <p>
                Protected:{" "}
                {copy.protected_reasons
                  .map((v) => v.replaceAll("_", " "))
                  .join(", ")}
              </p>
            ) : null}
          </li>
        ))}
      </ul>
      {selected.length ? (
        <>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={confirmed}
              disabled={unavailable}
              onChange={(e) => setConfirmed(e.target.checked)}
            />
            <span>
              I confirm permanent deletion of exactly these {selected.length}{" "}
              selected copies at their listed local or remote locations.
            </span>
          </label>
          <Button
            className="h-auto max-w-full whitespace-normal"
            type="button"
            disabled={
              unavailable ||
              !confirmed ||
              !current ||
              changed ||
              preview.policy_revision !== policy.data?.revision
            }
            onClick={() =>
              void submit({
                kind: "execute",
                input: {
                  request_id: databaseRequestID(),
                  preview_id: preview.id,
                  expected_policy_revision: preview.policy_revision,
                  expected_plan_hash: preview.plan_hash,
                },
              })
            }
          >
            Delete selected backup copies
          </Button>
        </>
      ) : (
        <p>No copies are selected for deletion.</p>
      )}
    </section>
  );
}

function RetentionHistory({
  history,
  owner,
  unavailable,
  current,
  setRecovery,
  setRecoveryConfirmed,
  setPreview,
  setConfirmed,
  setError,
  recovery,
  recoveryCurrent,
  recoveryConfirmed,
  submit,
}: {
  history: ReturnType<typeof useDatabaseRetention>["history"];
  owner: boolean;
  unavailable: boolean;
  current: boolean | null | undefined;
  setRecovery: ReturnType<typeof useDatabaseRetention>["setRecovery"];
  setRecoveryConfirmed: React.Dispatch<React.SetStateAction<boolean>>;
  setPreview: ReturnType<typeof useDatabaseRetention>["setPreview"];
  setConfirmed: React.Dispatch<React.SetStateAction<boolean>>;
  setError: React.Dispatch<unknown>;
  recovery:
    | import("@/api/hosting-database-backup-retention").RetentionExecution
    | undefined;
  recoveryCurrent: boolean;
  recoveryConfirmed: boolean;
  submit: (next: Attempt) => Promise<void>;
}) {
  return (
    <section
      aria-label="Retention execution history"
      className="min-w-0 space-y-2"
    >
      <h5>Retention execution history</h5>
      {history.data?.length === 0 ? <p>No retention executions yet.</p> : null}
      <ul className="space-y-3">
        {history.data?.map((execution) => (
          <li key={execution.id} className="min-w-0 break-all border-t pt-2">
            <p>
              {execution.id} · {execution.state} · policy revision{" "}
              {execution.policy_revision} · preview {execution.preview_id}
            </p>
            <p>
              Local result: {execution.local_state}. Remote result:{" "}
              {execution.remote_state}.
            </p>
            <p>
              {execution.created_at} · updated {execution.updated_at}
            </p>
            {execution.reservation_held ? (
              <p>
                Reservation remains held. Uncertain work is not automatically
                repeated.
              </p>
            ) : null}
            {execution.state === "uncertain" ? (
              <p role="alert">
                Deletion is uncertain. Review history and retained reservations
                before further action.
              </p>
            ) : null}
            <ul>
              {execution.items.map((item) => (
                <li key={item.copy_id}>
                  {item.backup_id} · {item.location} · copy {item.copy_id} ·{" "}
                  {item.state}
                  {item.failure_code
                    ? ` · ${item.failure_code.replaceAll("_", " ")}`
                    : ""}
                  {item.reservation_held ? " · reservation held" : ""}
                </li>
              ))}
            </ul>
            {owner && recoverable(execution) ? (
              <Button
                className="h-auto max-w-full whitespace-normal"
                type="button"
                variant="outline"
                disabled={unavailable || history.isFetching || !current}
                onClick={() => {
                  setRecovery(execution);
                  setRecoveryConfirmed(false);
                  setPreview(undefined);
                  setConfirmed(false);
                  setError(undefined);
                }}
              >
                Retry this batch
              </Button>
            ) : null}
          </li>
        ))}
      </ul>
      {owner && recovery ? (
        <RetentionRecovery
          recovery={recovery}
          recoveryCurrent={recoveryCurrent}
          recoveryConfirmed={recoveryConfirmed}
          unavailable={unavailable}
          setRecoveryConfirmed={setRecoveryConfirmed}
          history={history}
          current={current}
          submit={submit}
        />
      ) : null}
    </section>
  );
}

function RetentionRecovery({
  recovery,
  recoveryCurrent,
  recoveryConfirmed,
  unavailable,
  setRecoveryConfirmed,
  history,
  current,
  submit,
}: {
  recovery: import("@/api/hosting-database-backup-retention").RetentionExecution;
  recoveryCurrent: boolean;
  recoveryConfirmed: boolean;
  unavailable: boolean;
  setRecoveryConfirmed: React.Dispatch<React.SetStateAction<boolean>>;
  history: ReturnType<typeof useDatabaseRetention>["history"];
  current: boolean | null | undefined;
  submit: (next: Attempt) => Promise<void>;
}) {
  return (
    <section
      aria-label="Retry saved retention batch"
      className="min-w-0 space-y-2"
    >
      <h5>Confirm retry of saved batch {recovery.id}</h5>
      <p className="break-all">
        Saved preview {recovery.preview_id}, policy revision{" "}
        {recovery.policy_revision}, plan {recovery.plan_hash}.
      </p>
      <p>
        This requests recovery of the same execution and exact copies below. It
        does not select another batch. The server checks current owner and
        destination authority and blocks a live or unsafe attempt. Copies with
        unknown prior effects may remain blocked.
      </p>
      <ul>
        {recovery.items.map((item) => (
          <li key={item.copy_id} className="break-all">
            {item.backup_id} · {item.location} · copy {item.copy_id} ·{" "}
            {item.state}
          </li>
        ))}
      </ul>
      {!recoveryCurrent ? (
        <p role="alert">
          This saved batch is no longer eligible for retry. Refresh and review
          its current history.
        </p>
      ) : null}
      <label className="flex items-start gap-2">
        <input
          type="checkbox"
          aria-label="I confirm continuing only this saved batch"
          checked={recoveryConfirmed}
          disabled={unavailable || !recoveryCurrent}
          onChange={(event) => setRecoveryConfirmed(event.target.checked)}
        />
        <span>
          I confirm retry of exactly the saved copies in execution {recovery.id}
          , with no new batch selection.
        </span>
      </label>
      <Button
        className="h-auto max-w-full whitespace-normal"
        type="button"
        disabled={
          unavailable ||
          history.isFetching ||
          !current ||
          !recoveryCurrent ||
          !recoveryConfirmed
        }
        onClick={() =>
          void submit({
            kind: "recover",
            execution: recovery.id,
            input: {
              request_id: databaseRequestID(),
              preview_id: recovery.preview_id,
              expected_policy_revision: recovery.policy_revision,
              expected_plan_hash: recovery.plan_hash,
            },
          })
        }
      >
        Confirm retry of this batch
      </Button>
    </section>
  );
}

function RetentionPolicyStatus({
  owner,
  policy,
  history,
}: {
  owner: boolean;
  policy: ReturnType<typeof useDatabaseRetention>["policy"];
  history: ReturnType<typeof useDatabaseRetention>["history"];
}) {
  return (
    <>
      {!owner ? (
        <p>
          Project owners can change retention and confirm deletion. You can
          inspect the saved policy and execution history.
        </p>
      ) : null}
      {policy.isPending || history.isPending ? (
        <p role="status">Loading backup retention…</p>
      ) : null}
      {policy.isError || history.isError ? (
        <p role="alert">
          Retention metadata is unavailable.{" "}
          {databaseError(policy.error ?? history.error)}
        </p>
      ) : null}
      {policy.data ? (
        <p>
          Saved revision {policy.data.revision}. Retention{" "}
          {policy.data.enabled ? "enabled" : "disabled"}. Automatic retention{" "}
          {policy.data.automatic ? "enabled" : "disabled"}.
        </p>
      ) : policy.isSuccess ? (
        <p>No retention policy saved.</p>
      ) : null}
      {!owner && policy.data ? (
        <ul>
          {(["local", "remote"] as const).map((location) => (
            <li key={location}>
              {location}: count {policy.data![location].keep_count}, days{" "}
              {policy.data![location].keep_days}, size{" "}
              {retentionBytesToGiB(policy.data![location].max_bytes)} GiB. Zero
              means unlimited.
            </li>
          ))}
        </ul>
      ) : null}
    </>
  );
}

function RetentionSubmissionStatus({
  pending,
  error,
  attempt,
  owner,
  blocked,
  submit,
  message,
}: {
  pending: boolean;
  error: unknown;
  attempt: Attempt | undefined;
  owner: boolean;
  blocked: boolean;
  submit: (next: Attempt) => Promise<void>;
  message: string;
}) {
  return (
    <>
      {pending ? (
        <p role="status">Submitting the saved retention request…</p>
      ) : null}
      {error ? (
        <p role="alert">
          {databaseError(error)}{" "}
          {attempt
            ? `Retained request ${attempt.input.request_id}. Retry only this same request to resolve an unconfirmed response.`
            : "Input is preserved. Refresh and review the saved policy before correcting a rejected request."}
        </p>
      ) : null}
      {owner && attempt && error && !pending ? (
        <Button
          className="h-auto max-w-full whitespace-normal"
          type="button"
          disabled={blocked}
          onClick={() => void submit(attempt)}
        >
          Retry the same retention request
        </Button>
      ) : null}
      {message ? (
        <p role="status" className="break-all">
          {message}
        </p>
      ) : null}
    </>
  );
}

function RetentionPreviewAction({
  owner,
  unavailable,
  current,
  changed,
  policy,
  setPreview,
  setConfirmed,
  submit,
}: {
  owner: boolean;
  unavailable: boolean;
  current: boolean | null | undefined;
  changed: boolean;
  policy: ReturnType<typeof useDatabaseRetention>["policy"];
  setPreview: ReturnType<typeof useDatabaseRetention>["setPreview"];
  setConfirmed: React.Dispatch<React.SetStateAction<boolean>>;
  submit: (next: Attempt) => Promise<void>;
}) {
  return (
    <>
      {owner ? (
        <Button
          className="h-auto max-w-full whitespace-normal"
          type="button"
          disabled={unavailable || !current || changed || !policy.data?.enabled}
          onClick={() => {
            setPreview(undefined);
            setConfirmed(false);
            void submit({
              kind: "preview",
              input: {
                request_id: databaseRequestID(),
                expected_policy_revision: policy.data!.revision,
              },
            });
          }}
        >
          Preview retention deletion
        </Button>
      ) : null}
    </>
  );
}

function retentionDraftStatus(
  draft: Draft | undefined,
  policy: RetentionPolicy | null | undefined,
  generation: number,
) {
  const local = draft && limits(draft.local),
    remote = draft && limits(draft.remote);
  const changed = Boolean(
    draft && JSON.stringify(draft) !== JSON.stringify(draftFor(policy ?? null)),
  );
  const current =
    policy &&
    draft?.revision === policy.revision &&
    policy.resource_generation === generation;
  return { local, remote, changed, current };
}
