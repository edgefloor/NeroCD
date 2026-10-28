import { ApiError } from "@/api/errors";
import type { Resource } from "@/api/hosting";
import type { RemoteBackupTarget } from "@/api/hosting-database-backup-targets";
import type { DatabaseScope } from "@/api/hosting-databases";
import { storageRequestID, type HostingStorage } from "@/api/hosting-storage";
import type { Backup } from "@/api/hosting-storage-archives";
import {
  archiveBackupEligible,
  getArchiveBackup,
  getArchiveOperation,
  getArchiveRestore,
  listArchiveBackups,
  refreshArchiveConfiguration,
  requestArchiveBackup,
  requestArchiveRecovery,
  requestArchiveRestore,
  type ArchiveOperation,
  type ArchiveRestore,
  type ArchiveScope,
  type BackupInput,
  type RecoveryInput,
  type RestoreInput,
} from "@/api/hosting-storage-archives";
import {
  listStorageTransfers,
  retryStorageTransfer,
  type StorageBackupTransfer,
} from "@/api/hosting-storage-backup-remote";
import { readBackupMetadata } from "@/components/hosting/backup-access";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  BackupDestinationSelect,
  DatabaseBackupDestinations,
} from "@/pages/DatabaseBackupDestinations";
import {
  useDatabaseBackupDestinations,
  validSelectedBackupDestination,
} from "@/pages/database-backup-destinations";
import type {
  QueryObserverPlaceholderResult,
  QueryObserverSuccessResult,
  UseQueryResult,
} from "@tanstack/react-query";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { StorageArchiveDownload } from "./StorageArchiveDownload";
import { StorageArchiveTransfers } from "./StorageArchiveTransfers";
import { StorageBackupDeletion } from "./StorageBackupDeletion";
import { StorageBackupRetention } from "./StorageBackupRetention";
import { StorageBackupSchedules } from "./StorageBackupSchedules";

const archiveReadOptions = {
  retry: false,
  gcTime: 0,
  refetchOnWindowFocus: false,
  refetchOnReconnect: false,
} as const;
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
export function StorageArchives(
  props: Parameters<typeof useStorageArchives>[0],
) {
  return (
    <StorageArchivesSession
      key={JSON.stringify([
        props.actor,
        props.resource.project_id,
        props.resource.id,
        props.storage.destination_id,
        props.storage.binding_id,
      ])}
      {...props}
    />
  );
}
function StorageArchivesSession(
  props: Parameters<typeof useStorageArchives>[0],
) {
  const {
    accessLost,
    destinationProps,
    owner,
    remoteTarget,
    setRemoteTarget,
    destinations,
    blocked,
    attempt,
    key,
    scope,
    resource,
    retireArchiveAccess,
    setSelected,
    busy,
    queries,
    refresh,
    reviewedGeneration,
    configuration,
    polling,
    readFailed,
    history,
    setRestoreID,
    setRecovery,
    setPolling,
    setConfirmed,
    offset,
    setOffset,
    eligibility,
    confirmed,
    submit,
    generation,
    backup,
    paused,
    actor,
    selected,
    transfers,
    remoteBusy,
    remoteRetry,
    retryRemote,
    operation,
    name,
    remoteRestore,
    remoteSource,
    remoteUpload,
    setRemoteRestore,
    id,
    setName,
    restoreDraft,
    setRestoreDraft,
    readRestore,
    restored,
    recovered,
    error,
    setAttempt,
    setError,
  } = useStorageArchives(props);

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
        validation does not prove application read/write behavior. For an
        application with runtime configuration, restore copies the backed-up
        deployment's public variables and exact secret references into the new
        resource. Deployment and application verification remain separate
        operator steps.
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
        onDenied={retireArchiveAccess}
      />
      <StorageBackupSchedules
        key={key.join(":")}
        scope={scope}
        resource={{ ...resource, generation }}
        owner={owner}
        blocked={blocked}
        remoteTarget={remoteTarget}
        onRemoteTarget={setRemoteTarget}
        onDenied={retireArchiveAccess}
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
      <ArchiveHistory
        history={history}
        busy={busy}
        setSelected={setSelected}
        setRestoreID={setRestoreID}
        setRecovery={setRecovery}
        setPolling={setPolling}
        setConfirmed={setConfirmed}
        offset={offset}
        setOffset={setOffset}
      />
      <ArchiveBackupRequest
        owner={owner}
        eligibility={eligibility}
        confirmed={confirmed}
        blocked={blocked}
        attempt={attempt}
        setConfirmed={setConfirmed}
        remoteTarget={remoteTarget}
        destinations={destinations}
        submit={submit}
        generation={generation}
      />
      <ArchiveSelection
        backup={backup}
        scope={scope}
        owner={owner}
        paused={paused}
        accessLost={accessLost}
        retireArchiveAccess={retireArchiveAccess}
        actor={actor}
        selected={selected}
        resource={resource}
        blocked={blocked}
        transfers={transfers}
        remoteBusy={remoteBusy}
        remoteRetry={remoteRetry}
        retryRemote={retryRemote}
        operation={operation}
        attempt={attempt}
        name={name}
        remoteRestore={remoteRestore}
        remoteSource={remoteSource}
        submit={submit}
        remoteUpload={remoteUpload}
        setRemoteRestore={setRemoteRestore}
        id={id}
        busy={busy}
        setName={setName}
        readFailed={readFailed}
        history={history}
        restoreDraft={restoreDraft}
        setRestoreDraft={setRestoreDraft}
        readRestore={readRestore}
        restored={restored}
        recovered={recovered}
      />
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
function useStorageArchives({
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
  const [offset, setOffset] = useState(0);
  const [selected, setSelectedBackup] = useState(recoveryBackup ?? "");
  const [restoreID, setRestoreID] = useState("");
  const [restoreDraft, setRestoreDraft] = useState("");
  const [name, setName] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [attempt, setAttempt] = useState<Attempt>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [lost, setLost] = useState(false);
  const [recovery, setRecovery] = useState<ArchiveOperation>();
  const [polling, setPolling] = useState(true);
  const [generationReview, setGenerationReview] = useState<{
    sourceGeneration: number;
    generation: number;
  }>();
  const reviewedGeneration =
    generationReview?.sourceGeneration === resource.generation
      ? generationReview.generation
      : undefined;
  const generation = reviewedGeneration ?? resource.generation;
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
    onDenied: retireArchiveAccess,
  };
  const destinations = useDatabaseBackupDestinations(destinationProps);
  const {
    transfers,
    remoteUpload,
    remoteSource,
    remoteRetry,
    remoteBusy,
    remoteRestore,
    setRemoteRestore,
    retryRemote,
  } = useArchiveTransfers({
    targetScope,
    key,
    selected,
    owner,
    blocked: paused || lost,
    polling,
    onDenied: retireArchiveAccess,
    onError: setError,
  });
  function setSelected(value: string) {
    if (value !== selected) setRemoteRestore(false);
    setSelectedBackup(value);
  }
  const {
    history,
    eligibility,
    configuration,
    backup,
    detail,
    restored,
    recovered,
  } = useArchiveEvidence({
    scope,
    key,
    storage,
    generation,
    owner,
    lost,
    selected,
    polling,
    restoreID,
    recovery,
    offset,
    onDenied: retireArchiveAccess,
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
  function retireArchiveAccess() {
    setLost(true);
    controller.current?.abort();
    void client.cancelQueries({ queryKey: key });
    client.removeQueries({ queryKey: key });
  }
  const { accessLost, readFailed, blocked } = archiveReadStatus(queries, {
    lost,
    paused,
    polling,
    busy,
    remoteBusy,
  });
  const operation =
    !restored.isError && restored.data
      ? restored.data.operation
      : !detail.isError && !backup.isError
        ? detail.data
        : undefined;
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
      setPolling(true);
      void history.refetch();
      void eligibility.refetch();
    } catch (cause) {
      if (mounted.current && controller.current === transport) {
        setError(message(cause));
        if (denied(cause)) retireArchiveAccess();
      }
    } finally {
      // The mutation lock prevents another submission until this request settles.
      controller.current = undefined;
      lock.current = false;
      setBusy(false);
    }
  }
  async function refresh() {
    if (busy || accessLost) return;
    setPolling(true);
    const current = await configuration.refetch();
    if (!mounted.current || current.isError || !current.data) return;
    setGenerationReview({
      sourceGeneration: resource.generation,
      generation: current.data.generation,
    });
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
  return {
    accessLost,
    destinationProps,
    owner,
    remoteTarget,
    setRemoteTarget,
    destinations,
    blocked,
    attempt,
    key,
    scope,
    resource,
    retireArchiveAccess,
    setSelected,
    busy,
    queries,
    refresh,
    reviewedGeneration,
    configuration,
    polling,
    readFailed,
    history,
    setRestoreID,
    setRecovery,
    setPolling,
    setConfirmed,
    offset,
    setOffset,
    eligibility,
    confirmed,
    submit,
    generation,
    backup,
    paused,
    actor,
    selected,
    transfers,
    remoteBusy,
    remoteRetry,
    retryRemote,
    operation,
    name,
    remoteRestore,
    remoteSource,
    remoteUpload,
    setRemoteRestore,
    id,
    setName,
    restoreDraft,
    setRestoreDraft,
    readRestore,
    restored,
    recovered,
    error,
    setAttempt,
    setError,
  };
}

export function OperationDetails({
  operation: o,
}: {
  operation: ArchiveOperation;
}) {
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

function ArchiveHistory({
  history,
  busy,
  setSelected,
  setRestoreID,
  setRecovery,
  setPolling,
  setConfirmed,
  offset,
  setOffset,
}: {
  history: ReturnType<typeof useStorageArchives>["history"];
  busy: boolean;
  setSelected: (value: string) => void;
  setRestoreID: React.Dispatch<React.SetStateAction<string>>;
  setRecovery: ReturnType<typeof useStorageArchives>["setRecovery"];
  setPolling: React.Dispatch<React.SetStateAction<boolean>>;
  setConfirmed: React.Dispatch<React.SetStateAction<boolean>>;
  offset: number;
  setOffset: React.Dispatch<React.SetStateAction<number>>;
}) {
  return (
    <>
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
    </>
  );
}

function ArchiveBackupRequest({
  owner,
  eligibility,
  confirmed,
  blocked,
  attempt,
  setConfirmed,
  remoteTarget,
  destinations,
  submit,
  generation,
}: {
  owner: boolean;
  eligibility: UseQueryResult<boolean, Error>;
  confirmed: boolean;
  blocked: boolean;
  attempt: Attempt | undefined;
  setConfirmed: React.Dispatch<React.SetStateAction<boolean>>;
  remoteTarget: RemoteBackupTarget | undefined;
  destinations: ReturnType<typeof useDatabaseBackupDestinations>;
  submit: (next: Attempt) => Promise<void>;
  generation: number;
}) {
  return (
    <>
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
    </>
  );
}

function ArchiveRestoreForm({
  attempt,
  name,
  remoteRestore,
  remoteSource,
  submit,
  selected,
  remoteUpload,
  blocked,
  setRemoteRestore,
  id,
  busy,
  setName,
}: {
  attempt: Attempt | undefined;
  name: string;
  remoteRestore: boolean;
  remoteSource:
    | { transfer_id: string; expected_target_revision: number }
    | undefined;
  submit: (next: Attempt) => Promise<void>;
  selected: string;
  remoteUpload: ReturnType<typeof useStorageArchives>["remoteUpload"];
  blocked: boolean;
  setRemoteRestore: (value: boolean) => void;
  id: string;
  busy: boolean;
  setName: React.Dispatch<React.SetStateAction<string>>;
}) {
  return (
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
        The source is preserved. The restored resource starts under a hold while
        bounded file validation completes.
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
  );
}

function ArchiveRecoveryForm({
  operation,
  blocked,
  attempt,
  submit,
}: {
  operation: NonNullable<ReturnType<typeof useStorageArchives>["operation"]>;
  blocked: boolean;
  attempt: Attempt | undefined;
  submit: (next: Attempt) => Promise<void>;
}) {
  const [confirmed, setConfirmed] = useState(false);
  return (
    <div className="min-w-0 space-y-2">
      <p>
        Recovery requests fresh verification and writer resumption authority. It
        does not repeat archive creation or extraction. Review the held
        operation and plan before requesting it.
      </p>
      <p className="break-all">Recovery plan {operation.planHash}</p>
      <label className="flex items-start gap-2">
        <input
          type="checkbox"
          checked={confirmed}
          disabled={blocked || !!attempt}
          onChange={(e) => setConfirmed(e.target.checked)}
        />
        <span>I reviewed this operation's hold and exact recovery plan.</span>
      </label>
      <Button
        type="button"
        className={button}
        disabled={
          blocked ||
          !!attempt ||
          !confirmed ||
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
  );
}

function ArchiveMetadata({
  backup,
  scope,
}: {
  backup:
    | QueryObserverSuccessResult<Backup, Error>
    | QueryObserverPlaceholderResult<Backup, Error>;
  scope: ArchiveScope;
}) {
  return (
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
  );
}

function ArchiveRestoreLookup({
  id,
  restoreDraft,
  busy,
  setRestoreDraft,
  readRestore,
}: {
  id: string;
  restoreDraft: string;
  busy: boolean;
  setRestoreDraft: React.Dispatch<React.SetStateAction<string>>;
  readRestore: () => void;
}) {
  return (
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
  );
}

function ArchiveSelection({
  backup,
  scope,
  owner,
  paused,
  accessLost,
  retireArchiveAccess,
  actor,
  selected,
  resource,
  blocked,
  transfers,
  remoteBusy,
  remoteRetry,
  retryRemote,
  operation,
  attempt,
  name,
  remoteRestore,
  remoteSource,
  submit,
  remoteUpload,
  setRemoteRestore,
  id,
  busy,
  setName,
  readFailed,
  history,
  restoreDraft,
  setRestoreDraft,
  readRestore,
  restored,
  recovered,
}: {
  backup: ReturnType<typeof useStorageArchives>["backup"];
  scope: ArchiveScope;
  owner: boolean;
  paused: boolean;
  accessLost: false;
  retireArchiveAccess: () => void;
  actor: string;
  selected: string;
  resource: ReturnType<typeof useStorageArchives>["resource"];
  blocked: boolean;
  transfers: ReturnType<typeof useStorageArchives>["transfers"];
  remoteBusy: boolean;
  remoteRetry: ReturnType<typeof useStorageArchives>["remoteRetry"];
  retryRemote: ReturnType<typeof useStorageArchives>["retryRemote"];
  operation: ReturnType<typeof useStorageArchives>["operation"];
  attempt: Attempt | undefined;
  name: string;
  remoteRestore: boolean;
  remoteSource:
    | { transfer_id: string; expected_target_revision: number }
    | undefined;
  submit: (next: Attempt) => Promise<void>;
  remoteUpload: ReturnType<typeof useStorageArchives>["remoteUpload"];
  setRemoteRestore: (value: boolean) => void;
  id: string;
  busy: boolean;
  setName: React.Dispatch<React.SetStateAction<string>>;
  readFailed: boolean;
  history: ReturnType<typeof useStorageArchives>["history"];
  restoreDraft: string;
  setRestoreDraft: React.Dispatch<React.SetStateAction<string>>;
  readRestore: () => void;
  restored: ReturnType<typeof useStorageArchives>["restored"];
  recovered: ReturnType<typeof useStorageArchives>["recovered"];
}) {
  return (
    <>
      <SelectedArchiveEvidence
        backup={backup}
        scope={scope}
        owner={owner}
        paused={paused}
        accessLost={accessLost}
        retireArchiveAccess={retireArchiveAccess}
        actor={actor}
        selected={selected}
        resource={resource}
        blocked={blocked}
        transfers={transfers}
        remoteBusy={remoteBusy}
        remoteRetry={remoteRetry}
        retryRemote={retryRemote}
      />
      {operation ? <OperationDetails operation={operation} /> : null}
      <SelectedArchiveRestore
        owner={owner}
        backup={backup}
        attempt={attempt}
        name={name}
        remoteRestore={remoteRestore}
        remoteSource={remoteSource}
        submit={submit}
        selected={selected}
        remoteUpload={remoteUpload}
        blocked={blocked}
        setRemoteRestore={setRemoteRestore}
        id={id}
        busy={busy}
        setName={setName}
        readFailed={readFailed}
        history={history}
        restoreDraft={restoreDraft}
        setRestoreDraft={setRestoreDraft}
        readRestore={readRestore}
        restored={restored}
      />
      <SelectedArchiveRecovery
        owner={owner}
        operation={operation}
        blocked={blocked}
        attempt={attempt}
        submit={submit}
      />
      {!recovered.isError && recovered.data ? (
        <OperationDetails operation={recovered.data} />
      ) : null}
    </>
  );
}

function archiveReadStatus(
  queries: { error: unknown; isError: boolean; isFetching: boolean }[],
  {
    lost,
    paused,
    polling,
    busy,
    remoteBusy,
  }: {
    lost: boolean;
    paused: boolean;
    polling: boolean;
    busy: boolean;
    remoteBusy: boolean;
  },
) {
  const accessLost = lost || queries.some((query) => denied(query.error));
  const readFailed = queries.some((q) => q.isError);
  const blocked =
    paused ||
    !polling ||
    busy ||
    remoteBusy ||
    accessLost ||
    readFailed ||
    queries.some((q) => q.isFetching);
  return { accessLost, readFailed, blocked };
}

function useArchiveEvidence({
  scope,
  key,
  storage,
  generation,
  owner,
  lost,
  selected,
  polling,
  restoreID,
  recovery,
  offset,
  onDenied,
}: {
  offset: number;
  scope: ArchiveScope;
  key: readonly unknown[];
  storage: HostingStorage;
  generation: number;
  owner: boolean;
  lost: boolean;
  selected: string;
  polling: boolean;
  restoreID: string;
  recovery: ArchiveOperation | undefined;
  onDenied: () => void;
}) {
  const history = useQuery({
    ...archiveReadOptions,
    queryKey: [...key, "history", offset],
    enabled: !lost,
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => listArchiveBackups(scope, offset, signal),
        onDenied,
        [401, 403],
      ),
  });
  const eligibility = useQuery({
    ...archiveReadOptions,
    queryKey: [...key, "eligible", generation, storage.hash],
    enabled: owner && !lost,
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => archiveBackupEligible(scope, storage, generation, signal),
        onDenied,
        [401, 403],
      ),
  });
  const configuration = useQuery({
    ...archiveReadOptions,
    queryKey: [...key, "configuration", storage.hash],
    enabled: false,
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => refreshArchiveConfiguration(scope, storage, signal),
        onDenied,
        [401, 403],
      ),
  });
  const backup = useQuery({
    ...archiveReadOptions,
    queryKey: [...key, "backup", selected],
    enabled: !!selected && !lost,
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => getArchiveBackup(scope, selected, signal),
        onDenied,
        [401, 403],
      ),
    refetchInterval: (q) =>
      polling &&
      !q.state.error &&
      !["succeeded", "failed", "canceled"].includes(q.state.data?.state ?? "")
        ? 5000
        : false,
  });
  const detail = useQuery({
    ...archiveReadOptions,
    queryKey: [...key, "operation", backup.data?.operation_id],
    enabled: !!backup.data && !backup.isError && !lost,
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () =>
          getArchiveOperation(
            scope,
            backup.data!.operation_id,
            selected,
            signal,
          ),
        onDenied,
        [401, 403],
      ),
    refetchInterval: (q) =>
      polling &&
      !q.state.error &&
      !["succeeded", "failed", "canceled"].includes(q.state.data?.state ?? "")
        ? 5000
        : false,
  });
  const restored = useQuery({
    ...archiveReadOptions,
    queryKey: [...key, "restore", restoreID],
    enabled: !!restoreID && !lost,
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => getArchiveRestore(scope, restoreID, signal),
        onDenied,
        [401, 403],
      ),
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
    ...archiveReadOptions,
    queryKey: [...key, "recovery", recovery?.id],
    enabled: !!recovery && !lost,
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () =>
          getArchiveOperation(scope, recovery!.id, recovery!.backup, signal),
        onDenied,
        [401, 403],
      ),
    refetchInterval: (q) =>
      polling &&
      !q.state.error &&
      !["succeeded", "failed", "canceled"].includes(q.state.data?.state ?? "")
        ? 5000
        : false,
  });
  return {
    history,
    eligibility,
    configuration,
    backup,
    detail,
    restored,
    recovered,
  };
}

function SelectedArchiveEvidence({
  backup,
  scope,
  owner,
  paused,
  accessLost,
  retireArchiveAccess,
  actor,
  selected,
  resource,
  blocked,
  transfers,
  remoteBusy,
  remoteRetry,
  retryRemote,
}: {
  backup: ReturnType<typeof useStorageArchives>["backup"];
  scope: ArchiveScope;
  owner: boolean;
  paused: boolean;
  accessLost: false;
  retireArchiveAccess: () => void;
  actor: string;
  selected: string;
  resource: ReturnType<typeof useStorageArchives>["resource"];
  blocked: boolean;
  transfers: ReturnType<typeof useStorageArchives>["transfers"];
  remoteBusy: boolean;
  remoteRetry: ReturnType<typeof useStorageArchives>["remoteRetry"];
  retryRemote: ReturnType<typeof useStorageArchives>["retryRemote"];
}) {
  return (
    <>
      {!backup.isError && backup.data ? (
        <StorageArchiveDownload
          scope={scope}
          backup={backup.data}
          owner={owner}
          blocked={paused || accessLost}
          onDenied={retireArchiveAccess}
        />
      ) : null}
      {!backup.isError && backup.data ? (
        <ArchiveMetadata backup={backup} scope={scope} />
      ) : null}
      {!backup.isError && backup.data ? (
        <StorageBackupDeletion
          key={[
            actor,
            scope.project,
            scope.resource,
            scope.binding,
            scope.destination,
            selected,
            resource.generation,
          ].join(":")}
          scope={scope}
          resource={resource}
          backup={selected}
          copies={backup.data.copies ?? []}
          owner={owner}
          blocked={blocked}
          onDenied={retireArchiveAccess}
        />
      ) : null}
      {selected ? (
        <StorageArchiveTransfers
          transfers={transfers.data}
          selected={selected}
          owner={owner}
          blocked={blocked}
          remoteBusy={remoteBusy}
          remoteRetry={remoteRetry}
          onRetry={(v) => void retryRemote(v)}
          allowDownload
        />
      ) : null}
    </>
  );
}

function SelectedArchiveRestore({
  owner,
  backup,
  attempt,
  name,
  remoteRestore,
  remoteSource,
  submit,
  selected,
  remoteUpload,
  blocked,
  setRemoteRestore,
  id,
  busy,
  setName,
  readFailed,
  history,
  restoreDraft,
  setRestoreDraft,
  readRestore,
  restored,
}: {
  owner: boolean;
  backup: ReturnType<typeof useStorageArchives>["backup"];
  attempt: Attempt | undefined;
  name: string;
  remoteRestore: boolean;
  remoteSource:
    | { transfer_id: string; expected_target_revision: number }
    | undefined;
  submit: (next: Attempt) => Promise<void>;
  selected: string;
  remoteUpload: ReturnType<typeof useStorageArchives>["remoteUpload"];
  blocked: boolean;
  setRemoteRestore: (value: boolean) => void;
  id: string;
  busy: boolean;
  setName: React.Dispatch<React.SetStateAction<string>>;
  readFailed: boolean;
  history: ReturnType<typeof useStorageArchives>["history"];
  restoreDraft: string;
  setRestoreDraft: React.Dispatch<React.SetStateAction<string>>;
  readRestore: () => void;
  restored: ReturnType<typeof useStorageArchives>["restored"];
}) {
  return (
    <>
      {owner &&
      !backup.isError &&
      backup.data?.state === "succeeded" &&
      backup.data.blob ? (
        <ArchiveRestoreForm
          attempt={attempt}
          name={name}
          remoteRestore={remoteRestore}
          remoteSource={remoteSource}
          submit={submit}
          selected={selected}
          remoteUpload={remoteUpload}
          blocked={blocked}
          setRemoteRestore={setRemoteRestore}
          id={id}
          busy={busy}
          setName={setName}
        />
      ) : null}
      {!readFailed && history.data ? (
        <ArchiveRestoreLookup
          id={id}
          restoreDraft={restoreDraft}
          busy={busy}
          setRestoreDraft={setRestoreDraft}
          readRestore={readRestore}
        />
      ) : null}
      {!restored.isError && restored.data ? (
        <RestoreDetails value={restored.data} />
      ) : null}
    </>
  );
}

function SelectedArchiveRecovery({
  owner,
  operation,
  blocked,
  attempt,
  submit,
}: {
  owner: boolean;
  operation: ReturnType<typeof useStorageArchives>["operation"];
  blocked: boolean;
  attempt: Attempt | undefined;
  submit: (next: Attempt) => Promise<void>;
}) {
  return (
    <>
      {owner &&
      operation &&
      operation.mode !== "recover" &&
      (operation.mutationHold ||
        operation.ownershipHold ||
        operation.result?.uncertain ||
        operation.state === "reconciliation_required") ? (
        <ArchiveRecoveryForm
          key={JSON.stringify([
            operation.id,
            operation.planHash,
            attempt?.body.request_id,
          ])}
          operation={operation}
          blocked={blocked}
          attempt={attempt}
          submit={submit}
        />
      ) : null}
    </>
  );
}

function useArchiveTransfers({
  targetScope,
  key,
  selected,
  owner,
  blocked,
  polling,
  onDenied,
  onError,
}: {
  targetScope: DatabaseScope;
  key: readonly unknown[];
  selected: string;
  owner: boolean;
  blocked: boolean;
  polling: boolean;
  onDenied: () => void;
  onError: (message: string) => void;
}) {
  const [remoteRestoreBackup, setRemoteRestoreBackup] = useState<string>();
  const [remoteRetry, setRemoteRetry] = useState<{
    backup: string;
    input: { request_id: string; expected_transfer_id: string };
  }>();
  const [remoteBusy, setRemoteBusy] = useState(false);
  const remoteRestore = remoteRestoreBackup === selected;
  function setRemoteRestore(value: boolean) {
    setRemoteRestoreBackup(value ? selected : undefined);
  }
  const request = useRef<AbortController | null>(null);
  useEffect(() => () => request.current?.abort(), []);
  useEffect(() => {
    if (blocked || !owner) request.current?.abort();
  }, [blocked, owner]);
  const transfers = useQuery({
    ...archiveReadOptions,
    queryKey: [...key, "remote", selected],
    enabled: !!selected && !blocked,
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => listStorageTransfers(targetScope, selected, signal),
        onDenied,
        [401, 403],
      ),
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
    if (!owner || remoteBusy || blocked || !transfer || request.current) return;
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
    const controller = new AbortController();
    request.current = controller;
    try {
      await retryStorageTransfer(
        targetScope,
        selected,
        input,
        controller.signal,
      );
      if (controller.signal.aborted) return;
      setRemoteRetry(undefined);
      await transfers.refetch();
    } catch (cause) {
      if (controller.signal.aborted) return;
      onError(message(cause));
      if (denied(cause)) onDenied();
    } finally {
      request.current = null;
      setRemoteBusy(false);
    }
  }
  return {
    transfers,
    remoteUpload,
    remoteSource,
    remoteRetry,
    remoteBusy,
    remoteRestore,
    setRemoteRestore,
    retryRemote,
  };
}
