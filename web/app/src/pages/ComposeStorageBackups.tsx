import { ApiError } from "@/api/errors";
import {
  getComposeBackupVolumes,
  requestComposeBackup,
  type ComposeBackupInput,
} from "@/api/hosting-compose-storage-backups";
import type { RemoteBackupTarget } from "@/api/hosting-database-backup-targets";
import { storageRequestID } from "@/api/hosting-storage";
import {
  getArchiveBackup,
  getArchiveOperation,
  listArchiveBackups,
  requestArchiveRecovery,
  type ArchiveScope,
  type RecoveryInput,
} from "@/api/hosting-storage-archives";
import {
  listStorageTransfers,
  retryStorageTransfer,
  type StorageBackupTransfer,
} from "@/api/hosting-storage-backup-remote";
import { readBackupMetadata } from "@/components/hosting/backup-access";
import { BackupAccessBoundary } from "@/components/hosting/BackupAccessBoundary";
import { StorageArchiveDownload } from "@/components/hosting/StorageArchiveDownload";
import { OperationDetails } from "@/components/hosting/StorageArchives";
import {
  StorageArchiveTransfers,
  type StorageTransferRetry,
} from "@/components/hosting/StorageArchiveTransfers";
import { StorageBackupSchedules } from "@/components/hosting/StorageBackupSchedules";
import { Button } from "@/components/ui/button";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import {
  useDatabaseBackupDestinations,
  validSelectedBackupDestination,
} from "./database-backup-destinations";
import { BackupDestinationSelect } from "./DatabaseBackupDestinations";

const button = "h-auto max-w-full whitespace-normal break-all";
const terminal = (state?: string) =>
  ["succeeded", "failed", "canceled"].includes(state ?? "");
const denied = (e: unknown) =>
  e instanceof ApiError && [401, 403, 404].includes(e.status);
type ComposeStorageBackupsProps = {
  scope: ArchiveScope;
  generation: number;
  owner: boolean;
  blocked: boolean;
  onDenied: () => void;
};
export function ComposeStorageBackups(props: ComposeStorageBackupsProps) {
  return (
    <BackupAccessBoundary
      key={JSON.stringify([props.scope])}
      onDenied={props.onDenied}
      message="Compose backup access is unavailable. Refresh access before continuing."
    >
      {(onDenied) => (
        <ComposeStorageBackupsSession {...props} onDenied={onDenied} />
      )}
    </BackupAccessBoundary>
  );
}
function ComposeStorageBackupsSession(
  props: Parameters<typeof useComposeArchive>[0],
) {
  const {
    busy,
    sent,
    setReview,
    setConflict,
    choices,
    history,
    selected,
    transfers,
    id,
    disabled,
    review,
    remoteRetry,
    volume,
    setVolume,
    owner,
    prepare,
    remoteTarget,
    setRemoteTarget,
    destinations,
    scope,
    generation,
    blocked,
    scheduleRemoteTarget,
    setScheduleRemoteTarget,
    setSelected,
    setRecovered,
    lose,
    ack,
    setAck,
    conflict,
    submit,
    setSent,
    error,
    recovery,
    setRecovery,
    setResumeAck,
    offset,
    setOffset,
    backup,
    detail,
    retryRemote,
    resumeAck,
    resume,
  } = useComposeArchive(props);

  return (
    <section
      aria-label="Compose volume backups"
      className="min-w-0 space-y-3 rounded-lg border p-3"
    >
      <h6 className="font-medium">Compose volume backups</h6>
      <p>
        Back up one local named volume. Its running writers stop while the
        archive is created, then resume. This interrupts service. Other volumes
        are excluded; this is not a coordinated database and uploads snapshot.
        No automatic rollback runs.
      </p>
      <Button
        className={button}
        variant="outline"
        disabled={busy}
        onClick={() => {
          if (!sent) {
            setReview(undefined);
            setConflict(false);
          }
          void choices.refetch();
          void history.refetch();
          if (selected) void transfers.refetch();
        }}
      >
        Refresh Compose backup evidence and history
      </Button>
      {choices.isError ? (
        <p role="alert">
          Current volume evidence is unavailable. Backup history remains
          accessible.
        </p>
      ) : null}
      <ComposeVolumeSelection
        choices={choices}
        id={id}
        disabled={disabled}
        review={review}
        remoteRetry={remoteRetry}
        volume={volume}
        setVolume={setVolume}
        owner={owner}
        sent={sent}
        prepare={prepare}
      />
      {owner ? (
        <>
          <BackupDestinationSelect
            label="Remote Compose backup target"
            value={remoteTarget}
            onChange={setRemoteTarget}
            destinations={destinations}
            disabled={disabled || !!review}
          />
          <Button
            className={button}
            type="button"
            variant="outline"
            disabled={busy || !!review}
            onClick={() => void destinations.refresh()}
          >
            Refresh remote Compose backup targets
          </Button>
        </>
      ) : null}
      <StorageBackupSchedules
        key={JSON.stringify([scope, generation, owner])}
        scope={scope}
        resource={{ id: scope.resource, generation }}
        owner={owner}
        blocked={blocked}
        composeVolume={volume}
        remoteTarget={scheduleRemoteTarget}
        onRemoteTarget={setScheduleRemoteTarget}
        remoteDestinations={destinations}
        onSelectBackup={(value) => {
          if (!remoteRetry) {
            setSelected(value);
            setRecovered("");
          }
        }}
        onDenied={lose}
      />
      {review ? (
        <ComposeArchiveReview
          review={review}
          ack={ack}
          disabled={disabled}
          sent={sent}
          setAck={setAck}
          owner={owner}
          conflict={conflict}
          submit={submit}
          busy={busy}
          setReview={setReview}
          setSent={setSent}
          setConflict={setConflict}
        />
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
      {history.isError ? (
        <p role="alert">Backup history is unavailable.</p>
      ) : null}
      <ComposeArchiveHistory
        history={history}
        busy={busy}
        recovery={recovery}
        remoteRetry={remoteRetry}
        setSelected={setSelected}
        setRecovered={setRecovered}
        setRecovery={setRecovery}
        setResumeAck={setResumeAck}
      />
      <ComposeArchivePagination
        offset={offset}
        history={history}
        setOffset={setOffset}
      />
      <ComposeArchiveEvidence
        selected={selected}
        backup={backup}
        detail={detail}
        scope={scope}
        owner={owner}
        blocked={blocked}
        lose={lose}
        transfers={transfers}
        remoteRetry={remoteRetry}
        busy={busy}
        retryRemote={retryRemote}
      />
      <ComposeArchiveRecoveryReview
        owner={owner}
        detail={detail}
        resumeAck={resumeAck}
        disabled={disabled}
        recovery={recovery}
        setResumeAck={setResumeAck}
        resume={resume}
      />
    </section>
  );
}
function useComposeArchive({
  scope,
  generation,
  owner,
  blocked,
  onDenied,
}: ComposeStorageBackupsProps) {
  const id = useId();
  const [offset, setOffset] = useState(0),
    [selected, setSelected] = useState(""),
    [volume, setVolume] = useState("");
  const [remoteTarget, setRemoteTarget] = useState<RemoteBackupTarget>();
  const [scheduleRemoteTarget, setScheduleRemoteTarget] =
    useState<RemoteBackupTarget>();
  const [remoteRetry, setRemoteRetry] = useState<StorageTransferRetry>();
  const [review, setReview] = useState<ComposeBackupInput>(),
    [sent, setSent] = useState(false),
    [ack, setAck] = useState(false),
    [conflict, setConflict] = useState(false);
  const [recovery, setRecovery] = useState<{
      operation: string;
      backup: string;
      body: RecoveryInput;
    }>(),
    [recovered, setRecovered] = useState(""),
    [resumeAck, setResumeAck] = useState(false);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const active = useRef(true),
    controller = useRef<AbortController | null>(null);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      controller.current?.abort();
    };
  }, []);
  useEffect(() => {
    if (blocked) controller.current?.abort();
  }, [blocked]);
  const key = [
    "composeStorageBackups",
    scope.actor,
    scope.project,
    scope.resource,
    scope.binding,
    scope.destination,
  ];
  const base = {
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  } as const;
  const choices = useQuery({
    ...base,
    queryKey: [...key, "volumes"],
    enabled: true,
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => getComposeBackupVolumes(scope, signal),
        retireComposeAccess,
        [401, 403, 404],
      ),
  });
  const history = useQuery({
    ...base,
    queryKey: [...key, "history", offset],
    enabled: true,
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => listArchiveBackups(scope, offset, signal),
        retireComposeAccess,
        [401, 403, 404],
      ),
  });
  const backup = useQuery({
    ...base,
    queryKey: [...key, "backup", selected],
    enabled: !!selected,
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => getArchiveBackup(scope, selected, signal),
        retireComposeAccess,
        [401, 403, 404],
      ),
    refetchInterval: (q) =>
      !q.state.error && !terminal(q.state.data?.state) ? 2000 : false,
  });
  const operationID = recovered || backup.data?.operation_id;
  const detail = useQuery({
    ...base,
    queryKey: [...key, "operation", operationID],
    enabled: !!operationID,
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => getArchiveOperation(scope, operationID!, selected, signal),
        retireComposeAccess,
        [401, 403, 404],
      ),
    refetchInterval: (q) =>
      !q.state.error && !terminal(q.state.data?.state) ? 2000 : false,
  });
  const targetScope = {
    actor: scope.actor,
    project: scope.project,
    environment: "",
  };
  const destinations = useDatabaseBackupDestinations({
    storage: {
      resource: { id: scope.resource },
      destination_id: scope.destination,
    },
    scope: targetScope,
    owner,
    blocked: blocked || !owner,
    onDenied: lose,
  });
  const transfers = useQuery({
    ...base,
    queryKey: [...key, "transfers", selected],
    enabled: !!selected,
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => listStorageTransfers(targetScope, selected, signal),
        retireComposeAccess,
        [401, 403, 404],
      ),
    refetchInterval: (q) =>
      !q.state.error &&
      (!terminal(backup.data?.state) ||
        (!detail.data && !detail.isError) ||
        (backup.data?.state === "succeeded" &&
          detail.data?.remoteTarget &&
          !q.state.data?.some((v) => v.mode === "upload")) ||
        q.state.data?.some((v) =>
          ["queued", "dispatched", "applying"].includes(v.state),
        ))
        ? 2000
        : false,
  });
  function lose() {
    controller.current?.abort();
    onDenied();
  }
  function retireComposeAccess() {
    lose();
  }

  async function run(work: (signal: AbortSignal) => Promise<void>) {
    if (controller.current || blocked) return;
    const c = new AbortController();
    controller.current = c;
    setBusy(true);
    setError("");
    try {
      await work(c.signal);
    } catch (e) {
      if (active.current) {
        if (denied(e)) lose();
        else {
          if (e instanceof ApiError && [400, 409].includes(e.status))
            setConflict(true);
          setError(
            e instanceof ApiError && [400, 409].includes(e.status)
              ? "Backup evidence changed or the request was rejected. Refresh and review a separate request. Check history first."
              : "Delivery or status is unconfirmed. Retry the identical saved request to recover its result. No automatic recovery will run.",
          );
        }
      }
    } finally {
      if (controller.current === c) controller.current = null;
      if (active.current) setBusy(false);
    }
  }
  async function prepare() {
    if (!owner || sent || blocked) return;
    if (!validSelectedBackupDestination(remoteTarget, destinations)) {
      setError(
        "Refresh the remote target validation and select its current revision before review.",
      );
      return;
    }
    const selectedTarget = remoteTarget
      ? Object.freeze({ ...remoteTarget })
      : undefined;
    await run(async (signal) => {
      const current = await getComposeBackupVolumes(scope, signal);
      if (!active.current || signal.aborted) return;
      const chosen = current.volumes.find(
        (v) => v.volume === volume && v.eligible,
      );
      if (
        !current.available ||
        !chosen ||
        !current.operation_id ||
        current.resource_generation !== generation
      ) {
        setReview(undefined);
        throw new ApiError(409, "Compose changed", null);
      }
      setConflict(false);
      setAck(false);
      setReview(
        Object.freeze({
          request_id: storageRequestID(),
          expected_resource_generation: current.resource_generation,
          expected_deployed_operation_id: current.operation_id,
          volume,
          acknowledge_interruption: true,
          ...(selectedTarget ? { remote_target: selectedTarget } : {}),
        }),
      );
    });
  }
  async function submit() {
    if (!review || !owner || !ack || conflict || blocked) return;
    await run(async (signal) => {
      setSent(true);
      const value = await requestComposeBackup(scope, review, signal);
      if (!active.current || signal.aborted) return;
      setSelected(value.id);
      setRecovered("");
      setReview(undefined);
      setSent(false);
      setAck(false);
      await history.refetch();
    });
  }
  async function resume() {
    const op = detail.data;
    if (!owner || !op || !resumeAck || blocked) return;
    const attempt = recovery ?? {
      operation: op.id,
      backup: selected,
      body: { request_id: storageRequestID(), expected_plan_hash: op.planHash },
    };
    setRecovery(attempt);
    await run(async (signal) => {
      const value = await requestArchiveRecovery(
        scope,
        attempt.operation,
        attempt.backup,
        attempt.body,
        signal,
      );
      if (!active.current || signal.aborted) return;
      setRecovered(value.id);
      setRecovery(undefined);
      setResumeAck(false);
    });
  }
  async function retryRemote(transfer?: StorageBackupTransfer) {
    if (
      !owner ||
      blocked ||
      (!remoteRetry && !transfer) ||
      (remoteRetry &&
        (remoteRetry.backup !== selected ||
          (transfer && remoteRetry.input.expected_transfer_id !== transfer.id)))
    )
      return;
    const attempt = remoteRetry ?? {
      backup: selected,
      input: {
        request_id: storageRequestID(),
        expected_transfer_id: transfer!.id,
      },
    };
    setRemoteRetry(attempt);
    await run(async (signal) => {
      await retryStorageTransfer(
        targetScope,
        attempt.backup,
        attempt.input,
        signal,
      );
      if (!active.current || signal.aborted) return;
      setRemoteRetry(undefined);
      await transfers.refetch();
    });
  }
  const disabled = busy || blocked;
  return {
    busy,
    sent,
    setReview,
    setConflict,
    choices,
    history,
    selected,
    transfers,
    id,
    disabled,
    review,
    remoteRetry,
    volume,
    setVolume,
    owner,
    prepare,
    remoteTarget,
    setRemoteTarget,
    destinations,
    scope,
    generation,
    blocked,
    scheduleRemoteTarget,
    setScheduleRemoteTarget,
    setSelected,
    setRecovered,
    lose,
    ack,
    setAck,
    conflict,
    submit,
    setSent,
    error,
    recovery,
    setRecovery,
    setResumeAck,
    offset,
    setOffset,
    backup,
    detail,
    retryRemote,
    resumeAck,
    resume,
  };
}

function ComposeArchiveReview({
  review,
  ack,
  disabled,
  sent,
  setAck,
  owner,
  conflict,
  submit,
  busy,
  setReview,
  setSent,
  setConflict,
}: {
  review: ComposeBackupInput;
  ack: boolean;
  disabled: boolean;
  sent: boolean;
  setAck: React.Dispatch<React.SetStateAction<boolean>>;
  owner: boolean;
  conflict: boolean;
  submit: () => Promise<void>;
  busy: boolean;
  setReview: ReturnType<typeof useComposeArchive>["setReview"];
  setSent: React.Dispatch<React.SetStateAction<boolean>>;
  setConflict: React.Dispatch<React.SetStateAction<boolean>>;
}) {
  return (
    <section
      aria-label="Reviewed Compose volume backup"
      className="min-w-0 space-y-2 break-all"
    >
      <p>
        Volume {review.volume} · generation{" "}
        {review.expected_resource_generation} · deployed operation{" "}
        {review.expected_deployed_operation_id}
      </p>
      <p>
        Remote target for this immediate backup:{" "}
        {review.remote_target
          ? `${review.remote_target.target_id} revision ${review.remote_target.revision}`
          : "local only"}
        . Remote upload begins after the local archive completes and writers
        resume. Local and remote outcomes are separate.
      </p>
      <label className="flex items-start gap-2">
        <input
          type="checkbox"
          checked={ack}
          disabled={disabled || sent}
          onChange={(e) => setAck(e.target.checked)}
        />
        <span>
          I acknowledge interruption of this volume's writers while the local
          archive is created.
        </span>
      </label>
      <Button
        className={button}
        disabled={disabled || !owner || !ack || conflict}
        onClick={() => void submit()}
      >
        {sent
          ? "Retry identical Compose backup request"
          : "Confirm Compose volume backup"}
      </Button>
      <p className="text-xs">Saved request {review.request_id}</p>
      {!sent || conflict ? (
        <Button
          className={button}
          variant="outline"
          disabled={busy}
          onClick={() => {
            setReview(undefined);
            setSent(false);
            setConflict(false);
            setAck(false);
          }}
        >
          Discard review for a separate request
        </Button>
      ) : null}
    </section>
  );
}

function ComposeArchiveHistory({
  history,
  busy,
  recovery,
  remoteRetry,
  setSelected,
  setRecovered,
  setRecovery,
  setResumeAck,
}: {
  history: ReturnType<typeof useComposeArchive>["history"];
  busy: boolean;
  recovery: ReturnType<typeof useComposeArchive>["recovery"];
  remoteRetry: ReturnType<typeof useComposeArchive>["remoteRetry"];
  setSelected: React.Dispatch<React.SetStateAction<string>>;
  setRecovered: React.Dispatch<React.SetStateAction<string>>;
  setRecovery: ReturnType<typeof useComposeArchive>["setRecovery"];
  setResumeAck: React.Dispatch<React.SetStateAction<boolean>>;
}) {
  return (
    <ol
      aria-label="Compose volume backup history"
      className="min-w-0 space-y-2 break-all"
    >
      {history.data?.items.map((b) => (
        <li key={b.id}>
          <p>
            {b.volume || "Volume unavailable"} · {b.state} · {b.created_at}
          </p>
          <Button
            className={button}
            variant="outline"
            disabled={busy || !!recovery || !!remoteRetry}
            onClick={() => {
              setSelected(b.id);
              setRecovered("");
              setRecovery(undefined);
              setResumeAck(false);
            }}
          >
            View volume backup {b.id}
          </Button>
        </li>
      ))}
    </ol>
  );
}

function ComposeVolumeSelection({
  choices,
  id,
  disabled,
  review,
  remoteRetry,
  volume,
  setVolume,
  owner,
  sent,
  prepare,
}: {
  choices: ReturnType<typeof useComposeArchive>["choices"];
  id: string;
  disabled: boolean;
  review: ComposeBackupInput | undefined;
  remoteRetry: ReturnType<typeof useComposeArchive>["remoteRetry"];
  volume: string;
  setVolume: React.Dispatch<React.SetStateAction<string>>;
  owner: boolean;
  sent: boolean;
  prepare: () => Promise<void>;
}) {
  return (
    <>
      {choices.data && !choices.isError ? (
        <>
          <p>
            Backup availability:{" "}
            {choices.data.available
              ? "available"
              : choices.data.reason || "unavailable"}
          </p>
          <label htmlFor={`${id}-volume`}>Named volume to back up</label>
          <select
            id={`${id}-volume`}
            className="w-full min-w-0 rounded border bg-card p-2"
            disabled={disabled || !!review || !!remoteRetry}
            value={volume}
            onChange={(e) => setVolume(e.target.value)}
          >
            <option value="">Select a named volume</option>
            {choices.data.volumes.map((v) => (
              <option key={v.volume} value={v.volume} disabled={!v.eligible}>
                {v.volume}
                {!v.eligible ? ` · ${v.reason || "ineligible"}` : ""}
              </option>
            ))}
          </select>
          <ul className="min-w-0 space-y-1 break-all text-sm">
            {choices.data.volumes
              .filter((v) => v.volume === volume)
              .flatMap((v) =>
                v.mounts.map((m) => (
                  <li
                    key={JSON.stringify([v.volume, m.component, m.mount_path])}
                  >
                    {m.component} · {m.mount_path} ·{" "}
                    {m.read_only ? "read only" : "writer"}
                  </li>
                )),
              )}
          </ul>
          {owner ? (
            <Button
              className={button}
              disabled={
                disabled ||
                sent ||
                !!remoteRetry ||
                !!review ||
                !volume ||
                !choices.data.available ||
                choices.isFetching
              }
              onClick={() => void prepare()}
            >
              Review Compose volume backup
            </Button>
          ) : (
            <p>
              Project owner access is required to request a backup or resume
              recovery.
            </p>
          )}
        </>
      ) : null}
    </>
  );
}

function ComposeArchiveEvidence({
  selected,
  backup,
  detail,
  scope,
  owner,
  blocked,
  lose,
  transfers,
  remoteRetry,
  busy,
  retryRemote,
}: {
  selected: string;
  backup: ReturnType<typeof useComposeArchive>["backup"];
  detail: ReturnType<typeof useComposeArchive>["detail"];
  scope: ArchiveScope;
  owner: boolean;
  blocked: boolean;
  lose: () => void;
  transfers: ReturnType<typeof useComposeArchive>["transfers"];
  remoteRetry: ReturnType<typeof useComposeArchive>["remoteRetry"];
  busy: boolean;
  retryRemote: ReturnType<typeof useComposeArchive>["retryRemote"];
}) {
  return (
    <>
      {selected ? (
        <p role="status" className="break-all">
          Volume backup {selected}. Acceptance alone does not prove an archive
          or writer resumption.
        </p>
      ) : null}
      {backup.isError || detail.isError ? (
        <p role="alert">
          Selected backup evidence is unavailable. Refresh before recovery.
        </p>
      ) : null}
      {backup.data && !backup.isError ? (
        <StorageArchiveDownload
          scope={scope}
          backup={backup.data}
          owner={owner}
          blocked={blocked}
          onDenied={lose}
        />
      ) : null}
      {backup.data && !backup.isError ? (
        <div className="break-all text-sm">
          <p>
            Backup state: {backup.data.state}. Volume: {backup.data.volume}.
            Source deployment: {backup.data.source_deployed_operation_id}
          </p>
          {backup.data.blob ? (
            <p>
              Local artifact: {backup.data.blob.size_bytes} bytes ·{" "}
              {backup.data.blob.sha256} · {backup.data.blob.format}
            </p>
          ) : (
            <p>No local archive artifact is confirmed.</p>
          )}
        </div>
      ) : null}
      {transfers.isError ? (
        <p role="alert">
          Remote transfer history is unavailable. Local archive evidence remains
          separate.
        </p>
      ) : null}
      {selected && (!transfers.isError || remoteRetry) ? (
        <StorageArchiveTransfers
          transfers={transfers.data}
          selected={selected}
          owner={owner}
          blocked={blocked}
          remoteBusy={busy}
          remoteRetry={remoteRetry}
          onRetry={(v) => void retryRemote(v)}
          onRetrySaved={() => void retryRemote()}
        />
      ) : null}
      {detail.data && !detail.isError ? (
        <OperationDetails operation={detail.data} />
      ) : null}
    </>
  );
}

function ComposeArchivePagination({
  offset,
  history,
  setOffset,
}: {
  offset: number;
  history: ReturnType<typeof useComposeArchive>["history"];
  setOffset: React.Dispatch<React.SetStateAction<number>>;
}) {
  return (
    <div className="flex flex-wrap gap-2">
      <Button
        className={button}
        variant="outline"
        disabled={!offset || history.isFetching}
        onClick={() => setOffset(Math.max(0, offset - 20))}
      >
        Previous volume backups
      </Button>
      <Button
        className={button}
        variant="outline"
        disabled={
          !history.data ||
          offset + history.data.count >= history.data.total ||
          history.isFetching
        }
        onClick={() => setOffset(offset + 20)}
      >
        Next volume backups
      </Button>
    </div>
  );
}

function ComposeArchiveRecoveryReview({
  owner,
  detail,
  resumeAck,
  disabled,
  recovery,
  setResumeAck,
  resume,
}: {
  owner: boolean;
  detail: ReturnType<typeof useComposeArchive>["detail"];
  resumeAck: boolean;
  disabled: boolean;
  recovery: ReturnType<typeof useComposeArchive>["recovery"];
  setResumeAck: React.Dispatch<React.SetStateAction<boolean>>;
  resume: () => Promise<void>;
}) {
  return (
    <>
      {owner &&
      detail.data &&
      !detail.isError &&
      (detail.data.mutationHold ||
        detail.data.ownershipHold ||
        detail.data.result?.uncertain ||
        detail.data.state === "reconciliation_required") &&
      detail.data.mode !== "recover" ? (
        <div className="space-y-2 break-all">
          <p>
            Recovery verifies the held plan and resumes its original writers. It
            does not repeat archive creation. Plan {detail.data.planHash}
          </p>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={resumeAck}
              disabled={disabled || !!recovery}
              onChange={(e) => setResumeAck(e.target.checked)}
            />
            <span>
              I reviewed the held operation and request writer resumption.
            </span>
          </label>
          <Button
            className={button}
            disabled={disabled || !resumeAck}
            onClick={() => void resume()}
          >
            {recovery
              ? "Retry identical Compose resume recovery"
              : "Request Compose writer resume recovery"}
          </Button>
        </div>
      ) : null}
    </>
  );
}
