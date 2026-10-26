import { StorageBackupDeletion } from "./StorageBackupDeletion";
import { StorageBackupRetention } from "./StorageBackupRetention";
import { StorageBackupSchedules } from "./StorageBackupSchedules";
import {
  DatabaseBackupDestinations,
  BackupDestinationSelect,
  useDatabaseBackupDestinations,
  validSelectedBackupDestination,
} from "@/pages/DatabaseBackupDestinations";
import {
  listStorageTransfers,
  retryStorageTransfer,
  type StorageBackupTransfer,
} from "@/api/hosting-storage-backup-remote";
import type { RemoteBackupTarget } from "@/api/hosting-database-backup-targets";
import { useEffect, useId, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError } from "@/api/errors";
import { storageRequestID, type HostingStorage } from "@/api/hosting-storage";
import type { Resource } from "@/api/hosting";
import {
  archiveBackupEligible,
  getArchiveBackup,
  getArchiveOperation,
  getArchiveRestore,
  listArchiveBackups,
  requestArchiveBackup,
  requestArchiveRecovery,
  requestArchiveRestore,
  refreshArchiveConfiguration,
  type ArchiveOperation,
  type ArchiveRestore,
  type ArchiveScope,
  type BackupInput,
  type RecoveryInput,
  type RestoreInput,
} from "@/api/hosting-storage-archives";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

const button = "h-auto max-w-full whitespace-normal";
const denied = (e: unknown) =>
  e instanceof ApiError && [401, 403].includes(e.status);
function message(e: unknown) {
  if (denied(e))
    return "Archive access is unavailable. Reload the page to verify access before continuing.";
  if (e instanceof ApiError && e.status === 503)
    return "Storage archives are unavailable. Qualified archive configuration or compatible PostgreSQL metadata is missing. No archive result is confirmed.";
  if (e instanceof ApiError && e.status === 409)
    return "The resource, backup or hold changed. Refresh and review current evidence before starting a separate request. The saved request is preserved.";
  if (e instanceof ApiError && [400, 413].includes(e.status))
    return "The server rejected the archive request. Review the inputs before starting a separate request.";
  return "Delivery or current status is unknown. Retry the identical saved request to recover its result. No automatic mutation or recovery will run.";
}
type Attempt =
  | { kind: "backup"; body: BackupInput }
  | { kind: "restore"; backup: string; body: RestoreInput }
  | { kind: "recover"; operation: string; backup: string; body: RecoveryInput };
export function StorageArchives({
  actor,
  resource,
  storage,
  owner,
  paused,
  recoveryBackup,
}: {
  actor: string;
  resource: Resource;
  storage: HostingStorage;
  owner: boolean;
  paused: boolean;
  recoveryBackup?: string;
}) {
  const client = useQueryClient();
  const scope: ArchiveScope = {
    actor,
    project: resource.project_id,
    resource: resource.id,
    destination: storage.destination_id,
    binding: storage.binding_id,
  };
  const key = [
    "storageArchives",
    actor,
    scope.project,
    scope.resource,
    scope.destination,
    scope.binding,
  ] as const;
  const id = useId();
  const [remoteTarget, setRemoteTarget] = useState<RemoteBackupTarget>();
  const [remoteRestore, setRemoteRestore] = useState(false);
  const [remoteRetry, setRemoteRetry] = useState<{
    backup: string;
    input: { request_id: string; expected_transfer_id: string };
  }>();
  const [remoteBusy, setRemoteBusy] = useState(false);
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState(recoveryBackup ?? "");
  useEffect(() => {
    setRemoteRestore(false);
  }, [selected]);
  const [restoreID, setRestoreID] = useState("");
  const [restoreDraft, setRestoreDraft] = useState("");
  const [name, setName] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [recoveryConfirmed, setRecoveryConfirmed] = useState(false);
  const [attempt, setAttempt] = useState<Attempt>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [lost, setLost] = useState(false);
  const [recovery, setRecovery] = useState<ArchiveOperation>();
  const [polling, setPolling] = useState(true);
  const [reviewedGeneration, setReviewedGeneration] = useState<number>();
  const generation = reviewedGeneration ?? resource.generation;
  useEffect(() => setReviewedGeneration(undefined), [resource.generation]);
  const controller = useRef<AbortController | undefined>(undefined);
  const lock = useRef(false);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      controller.current?.abort();
    };
  }, []);
  const base = {
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  } as const;
  const targetScope = {
    actor,
    project: resource.project_id,
    environment: resource.project_environment_id ?? "",
  };
  const destinationProps = {
    storage: { resource, destination_id: storage.destination_id },
    scope: targetScope,
    owner,
    blocked: paused || lost,
    onDenied: () => setLost(true),
  };
  const destinations = useDatabaseBackupDestinations(destinationProps);
  const transfers = useQuery({
    ...base,
    queryKey: [...key, "remote", selected],
    enabled: !!selected && !lost,
    queryFn: ({ signal }) =>
      listStorageTransfers(targetScope, selected, signal),
    refetchInterval: polling ? 5000 : false,
  });
  const remoteUpload = transfers.data?.find((v) => v.mode === "upload");
  const remoteSource =
    remoteRestore && remoteUpload?.state === "succeeded"
      ? {
          transfer_id: remoteUpload.id,
          expected_target_revision: remoteUpload.target_revision,
        }
      : undefined;
  async function retryRemote(
    transfer: StorageBackupTransfer | undefined = remoteUpload,
  ) {
    if (!owner || remoteBusy || lost || !transfer) return;
    if (
      remoteRetry &&
      (remoteRetry.backup !== selected ||
        remoteRetry.input.expected_transfer_id !== transfer.id)
    )
      return;
    const input = remoteRetry?.input ?? {
      request_id: storageRequestID(),
      expected_transfer_id: transfer.id,
    };
    setRemoteRetry({ backup: selected, input });
    setRemoteBusy(true);
    try {
      await retryStorageTransfer(targetScope, selected, input);
      setRemoteRetry(undefined);
      await transfers.refetch();
    } catch (cause) {
      setError(message(cause));
      if (denied(cause)) setLost(true);
    } finally {
      setRemoteBusy(false);
    }
  }
  const history = useQuery({
    ...base,
    queryKey: [...key, "history", offset],
    queryFn: ({ signal }) => listArchiveBackups(scope, offset, signal),
  });
  const eligibility = useQuery({
    ...base,
    queryKey: [...key, "eligible", generation, storage.hash],
    enabled: owner,
    queryFn: ({ signal }) =>
      archiveBackupEligible(scope, storage, generation, signal),
  });
  const configuration = useQuery({
    ...base,
    queryKey: [...key, "configuration", storage.hash],
    enabled: false,
    queryFn: ({ signal }) =>
      refreshArchiveConfiguration(scope, storage, signal),
  });
  const backup = useQuery({
    ...base,
    queryKey: [...key, "backup", selected],
    enabled: !!selected,
    queryFn: ({ signal }) => getArchiveBackup(scope, selected, signal),
    refetchInterval: (q) =>
      polling &&
      !q.state.error &&
      !["succeeded", "failed", "canceled"].includes(q.state.data?.state ?? "")
        ? 5000
        : false,
  });
  const detail = useQuery({
    ...base,
    queryKey: [...key, "operation", backup.data?.operation_id],
    enabled: !!backup.data && !backup.isError,
    queryFn: ({ signal }) =>
      getArchiveOperation(scope, backup.data!.operation_id, selected, signal),
    refetchInterval: (q) =>
      polling &&
      !q.state.error &&
      !["succeeded", "failed", "canceled"].includes(q.state.data?.state ?? "")
        ? 5000
        : false,
  });
  const restored = useQuery({
    ...base,
    queryKey: [...key, "restore", restoreID],
    enabled: !!restoreID,
    queryFn: ({ signal }) => getArchiveRestore(scope, restoreID, signal),
    refetchInterval: (q) =>
      polling &&
      !q.state.error &&
      !["succeeded", "failed", "canceled"].includes(
        q.state.data?.operation.state ?? "",
      )
        ? 5000
        : false,
  });
  const recovered = useQuery({
    ...base,
    queryKey: [...key, "recovery", recovery?.id],
    enabled: !!recovery,
    queryFn: ({ signal }) =>
      getArchiveOperation(scope, recovery!.id, recovery!.backup, signal),
    refetchInterval: (q) =>
      polling &&
      !q.state.error &&
      !["succeeded", "failed", "canceled"].includes(q.state.data?.state ?? "")
        ? 5000
        : false,
  });
  useEffect(() => {
    if (!polling) return;
    const timer = setTimeout(() => setPolling(false), 60000);
    return () => clearTimeout(timer);
  }, [polling, selected, restoreID, recovery?.id]);
  const queries = [
    transfers,
    history,
    backup,
    detail,
    restored,
    recovered,
    configuration,
    ...(owner ? [eligibility] : []),
  ];
  const accessLost = lost || queries.some((q) => denied(q.error));
  useEffect(() => {
    if (accessLost) {
      setLost(true);
      controller.current?.abort();
    }
  }, [accessLost]);
  const readFailed = queries.some((q) => q.isError);
  const blocked =
    paused ||
    !polling ||
    busy ||
    remoteBusy ||
    accessLost ||
    readFailed ||
    queries.some((q) => q.isFetching);
  const operation =
    !restored.isError && restored.data
      ? restored.data.operation
      : !detail.isError && !backup.isError
        ? detail.data
        : undefined;
  useEffect(() => setRecoveryConfirmed(false), [operation?.id]);
  async function submit(next: Attempt) {
    if (!owner || blocked || lock.current || (attempt && next !== attempt))
      return;
    setAttempt(next);
    setBusy(true);
    lock.current = true;
    setError("");
    const transport = new AbortController();
    controller.current = transport;
    try {
      if (next.kind === "backup") {
        const value = await requestArchiveBackup(
          scope,
          next.body,
          transport.signal,
        );
        if (!mounted.current || transport.signal.aborted) return;
        setSelected(value.id);
        setRestoreID("");
      } else if (next.kind === "restore") {
        const value = await requestArchiveRestore(
          scope,
          next.backup,
          next.body,
          transport.signal,
        );
        if (!mounted.current || transport.signal.aborted) return;
        setRestoreID(value.operation.id);
        void client.invalidateQueries({
          queryKey: ["hostingResources", scope.project],
        });
      } else {
        const value = await requestArchiveRecovery(
          scope,
          next.operation,
          next.backup,
          next.body,
          transport.signal,
        );
        if (!mounted.current || transport.signal.aborted) return;
        setRecovery(value);
      }
      setAttempt(undefined);
      setConfirmed(false);
      setRecoveryConfirmed(false);
      setPolling(true);
      void history.refetch();
      void eligibility.refetch();
    } catch (cause) {
      if (mounted.current && controller.current === transport) {
        setError(message(cause));
        if (denied(cause)) setLost(true);
      }
    } finally {
      if (controller.current === transport) {
        controller.current = undefined;
        lock.current = false;
        if (mounted.current) setBusy(false);
      }
    }
  }
  async function refresh() {
    if (busy || accessLost) return;
    setPolling(true);
    const current = await configuration.refetch();
    if (!mounted.current || current.isError || !current.data) return;
    setReviewedGeneration(current.data.generation);
    setConfirmed(false);
    for (const query of [history, backup, detail, restored, recovered])
      if (query.isEnabled) void query.refetch();
    if (current.data.generation === generation && owner)
      void eligibility.refetch();
  }
  function readRestore() {
    if (busy || !/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(restoreDraft)) return;
    setRestoreID(restoreDraft);
    setPolling(true);
  }
  if (accessLost)
    return (
      <p role="alert">
        Archive access is unavailable. Reload the page to verify access before
        continuing.
      </p>
    );
  return (
    <section
      aria-label="Upload backups and restores"
      className="min-w-0 space-y-3 border-t pt-3"
    >
      <h5 className="font-medium">Upload backups and restores</h5>
      <p>
        Back up the owned upload volume for a single image application. Writers
        may stop during backup. Archives remain on this destination's local
        disk. An optional remote transfer runs after writers resume. Host or
        disk loss can destroy local archives.
      </p>
      <p>
        Restore always creates a new resource on the same destination. File
        validation does not prove application read/write behavior. Deployment
        and application verification remain separate operator steps.
      </p>
      <DatabaseBackupDestinations {...destinationProps} />
      {owner ? (
        <BackupDestinationSelect
          label="Remote backup target"
          value={remoteTarget}
          onChange={setRemoteTarget}
          destinations={destinations}
          disabled={blocked || !!attempt}
        />
      ) : null}
      <StorageBackupRetention
        key={"retention:" + key.join(":")}
        scope={scope}
        resource={{ ...resource, generation }}
        owner={owner}
        blocked={blocked}
        onDenied={() => setLost(true)}
      />
      <StorageBackupSchedules
        key={key.join(":")}
        scope={scope}
        resource={{ ...resource, generation }}
        owner={owner}
        blocked={blocked}
        remoteTarget={remoteTarget}
        onRemoteTarget={setRemoteTarget}
        onDenied={() => setLost(true)}
        onSelectBackup={setSelected}
      />
      <Button
        type="button"
        variant="outline"
        className={button}
        disabled={busy || queries.some((q) => q.isFetching)}
        onClick={refresh}
      >
        Refresh archive status
      </Button>
      {reviewedGeneration !== undefined && !configuration.isError ? (
        <p role="status">
          Reviewed current resource generation {reviewedGeneration}. A separate
          backup submission uses this generation. Any saved retry keeps its
          original inputs.
        </p>
      ) : null}
      {!polling ? (
        <p role="status">
          Automatic status reads stopped after one minute. Status may be stale
          or unknown. Refresh to read current evidence.
        </p>
      ) : null}
      {readFailed ? (
        <p role="alert">
          {message(queries.find((q) => q.isError)?.error)} Archive actions are
          paused until a successful refresh.
        </p>
      ) : null}
      {history.isPending ? (
        <p role="status">Loading backup history...</p>
      ) : !history.isError && history.data ? (
        <>
          {history.data.items.length === 0 ? (
            <p>No upload backups recorded.</p>
          ) : (
            <ul className="min-w-0 space-y-2">
              {history.data.items.map((item) => (
                <li key={item.id} className="min-w-0 break-all">
                  <Button
                    type="button"
                    variant="outline"
                    className={button}
                    disabled={busy}
                    onClick={() => {
                      setSelected(item.id);
                      setRestoreID("");
                      setRecovery(undefined);
                      setPolling(true);
                      setConfirmed(false);
                      setRecoveryConfirmed(false);
                    }}
                  >
                    Backup {item.id}
                  </Button>
                  <span className="block">
                    {item.state} · {item.created_at}
                  </span>
                </li>
              ))}
            </ul>
          )}
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              variant="outline"
              disabled={busy || offset === 0}
              onClick={() => setOffset(Math.max(0, offset - 20))}
            >
              Previous backups
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={
                busy || offset + history.data.count >= history.data.total
              }
              onClick={() => setOffset(offset + 20)}
            >
              Next backups
            </Button>
          </div>
        </>
      ) : null}
      {!owner ? (
        <p>
          Project owner access is required to request backup, restore or
          recovery.
        </p>
      ) : (
        <>
          {!eligibility.data ? (
            <p>
              Backup requires a current healthy owned upload deployment,
              matching storage, and no unresolved holds. Eligibility is pending
              or unavailable.
            </p>
          ) : null}
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={confirmed}
              disabled={blocked || !!attempt}
              onChange={(e) => setConfirmed(e.target.checked)}
            />
            <span>
              I understand writers may stop and this local backup cannot survive
              host disk loss.
            </span>
          </label>
          <Button
            type="button"
            className={button}
            disabled={
              blocked ||
              !!attempt ||
              !confirmed ||
              !eligibility.data ||
              !validSelectedBackupDestination(remoteTarget, destinations)
            }
            onClick={() =>
              void submit({
                kind: "backup",
                body: {
                  request_id: storageRequestID(),
                  expected_resource_generation: generation,
                  ...(remoteTarget ? { remote_target: remoteTarget } : {}),
                },
              })
            }
          >
            Request upload backup
          </Button>
        </>
      )}
      {!backup.isError && backup.data ? (
        <dl
          aria-label="Selected backup metadata"
          className="min-w-0 break-all text-sm"
        >
          <dt>Backup</dt>
          <dd>{backup.data.id}</dd>
          <dt>Destination</dt>
          <dd>{scope.destination}</dd>
          <dt>Local artifact checksum</dt>
          <dd>{backup.data.blob?.sha256 ?? "No published artifact"}</dd>
          <dt>Local artifact bytes</dt>
          <dd>{backup.data.blob?.size_bytes ?? "unknown"}</dd>
          <dt>Copy availability</dt>
          <dd>
            <ul>
              {backup.data.copies?.map((c) => (
                <li key={c.copy_id}>
                  {c.location}: {c.availability}
                  {c.protected_reasons.length
                    ? ` · ${c.protected_reasons.join(", ")}`
                    : ""}
                </li>
              ))}
            </ul>
          </dd>
        </dl>
      ) : null}
      {!backup.isError && backup.data ? <StorageBackupDeletion key={[actor, scope.project, scope.resource, scope.binding, scope.destination, selected, resource.generation].join(":")} scope={scope} resource={resource} backup={selected} copies={backup.data.copies ?? []} owner={owner} blocked={blocked} onDenied={() => setLost(true)} /> : null}
      {selected ? (
        <section
          aria-label="Remote archive transfers"
          className="space-y-2 break-all"
        >
          <h5>Remote archive transfers</h5>
          {transfers.data?.length === 0 ? (
            <p>No remote transfer requested.</p>
          ) : null}
          {transfers.data?.map((v) => (
            <p key={v.id}>
              {v.mode} {v.id}: {v.state}
              {v.failure_code ? ` · ${v.failure_code}` : ""}. Local archive
              remains independent.
              {owner &&
              v.mode === "download" &&
              ["failed", "uncertain"].includes(v.state) ? (
                <>
                  <span>
                    {" "}
                    Restore remains held until a verified download completes.
                  </span>
                  <Button
                    disabled={
                      blocked ||
                      remoteBusy ||
                      (!!remoteRetry &&
                        (remoteRetry.backup !== selected ||
                          remoteRetry.input.expected_transfer_id !== v.id))
                    }
                    onClick={() => void retryRemote(v)}
                  >
                    Retry remote download
                  </Button>
                </>
              ) : null}
            </p>
          ))}
          {owner &&
          remoteUpload &&
          ["failed", "uncertain"].includes(remoteUpload.state) ? (
            <Button
              disabled={
                blocked ||
                remoteBusy ||
                (!!remoteRetry &&
                  (remoteRetry.backup !== selected ||
                    remoteRetry.input.expected_transfer_id !== remoteUpload.id))
              }
              onClick={() => void retryRemote()}
            >
              Retry remote upload of the same archive
            </Button>
          ) : null}
          {remoteRetry ? (
            <p>
              Saved remote retry {remoteRetry.input.request_id} for transfer{" "}
              {remoteRetry.input.expected_transfer_id} in backup{" "}
              {remoteRetry.backup}. Retry that transfer to keep the same archive
              and request identity.
            </p>
          ) : null}
        </section>
      ) : null}
      {operation ? <OperationDetails operation={operation} /> : null}
      {owner &&
      !backup.isError &&
      backup.data?.state === "succeeded" &&
      backup.data.blob ? (
        <form
          aria-label="Restore backup into a new resource"
          className="min-w-0 space-y-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (!attempt && name.trim() && (!remoteRestore || remoteSource))
              void submit({
                kind: "restore",
                backup: selected,
                body: {
                  request_id: storageRequestID(),
                  name,
                  ...(remoteSource ? { remote_source: remoteSource } : {}),
                },
              });
          }}
        >
          {remoteRestore || remoteUpload?.state === "succeeded" ? (
            <label className="flex gap-2">
              <input
                type="checkbox"
                checked={remoteRestore}
                disabled={blocked || !!attempt}
                onChange={(e) => setRemoteRestore(e.target.checked)}
              />
              Download and verify the remote archive before restoring
            </label>
          ) : null}
          <label htmlFor={`${id}-name`}>New restored resource name</label>
          <Input
            id={`${id}-name`}
            value={name}
            required
            maxLength={128}
            disabled={busy || !!attempt}
            onChange={(e) => setName(e.target.value)}
          />
          <p>
            The source is preserved. The restored resource starts under a hold
            while bounded file validation completes.
          </p>
          <Button
            type="submit"
            className={button}
            disabled={
              blocked ||
              !!attempt ||
              !name.trim() ||
              (remoteRestore && !remoteSource)
            }
          >
            Restore into new resource
          </Button>
        </form>
      ) : null}
      {!readFailed && history.data ? (
        <div
          role="group"
          aria-label="Read restore history by operation ID"
          className="min-w-0 space-y-2"
        >
          <label htmlFor={`${id}-restore`}>Restore operation ID</label>
          <Input
            id={`${id}-restore`}
            value={restoreDraft}
            maxLength={128}
            disabled={busy}
            onChange={(e) => setRestoreDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                readRestore();
              }
            }}
          />
          <Button
            type="button"
            variant="outline"
            className={button}
            disabled={busy || !restoreDraft}
            onClick={readRestore}
          >
            Read restore details
          </Button>
        </div>
      ) : null}
      {!restored.isError && restored.data ? (
        <RestoreDetails value={restored.data} />
      ) : null}
      {owner &&
      operation &&
      operation.mode !== "recover" &&
      (operation.mutationHold ||
        operation.ownershipHold ||
        operation.result?.uncertain ||
        operation.state === "reconciliation_required") ? (
        <div className="min-w-0 space-y-2">
          <p>
            Recovery requests fresh verification and writer resumption
            authority. It does not repeat archive creation or extraction. Review
            the held operation and plan before requesting it.
          </p>
          <p className="break-all">Recovery plan {operation.planHash}</p>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={recoveryConfirmed}
              disabled={blocked || !!attempt}
              onChange={(e) => setRecoveryConfirmed(e.target.checked)}
            />
            <span>
              I reviewed this operation's hold and exact recovery plan.
            </span>
          </label>
          <Button
            type="button"
            className={button}
            disabled={
              blocked ||
              !!attempt ||
              !recoveryConfirmed ||
              !["failed", "canceled", "reconciliation_required"].includes(
                operation.state,
              )
            }
            onClick={() =>
              void submit({
                kind: "recover",
                operation: operation.id,
                backup: operation.backup,
                body: {
                  request_id: storageRequestID(),
                  expected_plan_hash: operation.planHash,
                },
              })
            }
          >
            Request archive recovery
          </Button>
        </div>
      ) : null}
      {!recovered.isError && recovered.data ? (
        <OperationDetails operation={recovered.data} />
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
      {attempt ? (
        <div className="min-w-0 space-y-2">
          <p className="break-all">
            Saved {attempt.kind} request {attempt.body.request_id}. Its inputs
            are frozen for retry.
          </p>
          <Button
            type="button"
            className={button}
            disabled={blocked}
            onClick={() => void submit(attempt)}
          >
            Retry identical archive request
          </Button>
          <Button
            type="button"
            variant="outline"
            className={button}
            disabled={busy}
            onClick={() => {
              setAttempt(undefined);
              setError("");
              setConfirmed(false);
              setRecoveryConfirmed(false);
            }}
          >
            Discard saved request and review a separate submission
          </Button>
          <p>
            Discarding does not cancel a request already accepted by the server.
            Check history before submitting again.
          </p>
        </div>
      ) : null}
    </section>
  );
}
function OperationDetails({ operation: o }: { operation: ArchiveOperation }) {
  return (
    <section
      aria-label={`${o.mode} operation ${o.id}`}
      className="min-w-0 space-y-1 break-all text-sm"
    >
      <p>
        Operation {o.id} · {o.mode} · {o.state}
      </p>
      <p>
        Mutation hold: {o.mutationHold ? "held" : "not reported"}. Restore
        ownership hold: {o.ownershipHold ? "held" : "not reported"}.
      </p>
      <p>
        Public progress:{" "}
        {o.phases.length ? o.phases.join(", ") : "No phase evidence yet"}
      </p>
      <p>Archive outcome: {o.result?.archive ?? "unknown or pending"}</p>
      <p>Writer resumption: {o.result?.resume ?? "unknown or pending"}</p>
      <p>
        Uncertainty:{" "}
        {o.result
          ? o.result.uncertain
            ? "unresolved"
            : "not reported"
          : "unknown"}
      </p>
      {o.result?.failure ? <p>Failure code: {o.result.failure}</p> : null}
      <p>
        Restore file validation:{" "}
        {o.result?.verified ? "verified" : "not verified"}. Application
        read/write verification remains pending.
      </p>
    </section>
  );
}
function RestoreDetails({ value }: { value: ArchiveRestore }) {
  return (
    <p className="min-w-0 break-all">
      New resource {value.name} ({value.resource}).{" "}
      <a className="underline" href="/projects">
        Open Projects
      </a>
      . In Project hosting, select the source project and environment, then find{" "}
      {value.name} by resource ID {value.resource} to review and deploy it.
      Restore operation {value.operation.id}.
    </p>
  );
}
