import {
  BackupDestinationSelect,
  DatabaseBackupDestinations,
  useDatabaseBackupDestinations,
  validSelectedBackupDestination,
} from "./DatabaseBackupDestinations";
import {
  databaseBackupTransfersKey,
  listDatabaseBackupTransfers,
  retryDatabaseBackupUpload,
  type RemoteBackupTarget,
  type DatabaseBackupTransfer,
} from "../api/hosting-database-backup-targets";
import { useEffect, useRef, useState } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError } from "../api/errors";
import { DatabaseBackupSchedules } from "./DatabaseBackupSchedules";
import { Button } from "../components/ui/button";
import {
  databaseDenied,
  databaseError,
  databaseRequestID,
  type DatabaseScope,
  type HostingDatabase,
} from "../api/hosting-databases";
import {
  databaseBackupsKey,
  listDatabaseBackups,
  requestDatabaseBackup,
  restoreDatabaseBackup,
  type DatabaseBackupInput,
  type DatabaseRestoreInput,
} from "../api/hosting-database-backups";

type Attempt =
  | { mode: "backup"; input: DatabaseBackupInput }
  | { mode: "restore"; backup: string; input: DatabaseRestoreInput };
type Props = {
  database: HostingDatabase;
  scope: DatabaseScope;
  owner: boolean;
  blocked: boolean;
  onDenied: () => void;
};
export function DatabaseBackups(props: Props) {
  return (
    <DatabaseBackupsPanel
      key={JSON.stringify([
        props.scope.actor,
        props.scope.project,
        props.scope.environment,
        props.database.resource.id,
      ])}
      {...props}
    />
  );
}
function DatabaseBackupsPanel({
  database,
  scope,
  owner,
  blocked,
  onDenied,
}: Props) {
  const client = useQueryClient();
  const [accessRevoked, setAccessRevoked] = useState(false);
  const destinations = useDatabaseBackupDestinations({
    database,
    scope,
    owner,
    blocked,
    onDenied,
  });
  const [remoteTarget, setRemoteTarget] = useState<RemoteBackupTarget>();
  const [restoreSource, setRestoreSource] = useState("");
  const [name, setName] = useState("");
  const [selected, setSelected] = useState("");
  const [attempt, setAttempt] = useState<Attempt>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  const [accepted, setAccepted] = useState("");
  const lifetime = useRef<AbortController | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    return () => {
      controller.abort();
      if (lifetime.current === controller) lifetime.current = null;
    };
  }, []);
  const key = databaseBackupsKey(scope, database.resource.id);
  const history = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => listDatabaseBackups(scope, database, signal),
    enabled: !blocked && !accessRevoked && database.engine === "postgresql",
    retry: false,
    refetchInterval: (q) =>
      q.state.data?.some(
        (v) => v.ownership_held && !["uncertain", "failed"].includes(v.state),
      )
        ? 3000
        : false,
  });
  const transfers = useQueries({
    queries: (history.data ?? [])
      .filter((v) => v.mode === "backup")
      .map((v) => ({
        queryKey: databaseBackupTransfersKey(scope, v.id),
        queryFn: ({ signal }: { signal: AbortSignal }) =>
          listDatabaseBackupTransfers(scope, v.id, signal),
        enabled: !blocked && !accessRevoked,
        retry: false,
        refetchInterval: (query: {
          state: { data?: DatabaseBackupTransfer[] };
        }) =>
          (v.remote_target &&
            !query.state.data?.some((t) => t.mode === "upload")) ||
          query.state.data?.some((t) =>
            ["queued", "dispatched", "applying"].includes(t.state),
          )
            ? 3000
            : false,
      })),
  });
  const uploads = transfers
    .flatMap((v) => v.data ?? [])
    .filter((v) => v.mode === "upload");
  const restoreTransfers = uploads.filter(
    (v) => v.backup_id === selected && v.state === "succeeded" && v.blob,
  );
  const remoteRestore = restoreTransfers.find((v) => v.id === restoreSource);
  const denied =
    accessRevoked ||
    destinations.denied ||
    databaseDenied(history.error) ||
    databaseDenied(error) ||
    transfers.some((v) => databaseDenied(v.error));
  useEffect(() => {
    if (denied) {
      setAccessRevoked(true);
      lifetime.current?.abort();
      client.removeQueries({ queryKey: key });
      client.removeQueries({
        queryKey: ["database-backup-transfers", scope.actor, scope.project],
      });
      onDenied();
    }
  }, [denied]);
  if (database.engine !== "postgresql")
    return (
      <section aria-label="Database backups">
        <h4>Logical backups</h4>
        <p>
          Immediate logical backup and new-database restore currently support
          PostgreSQL17.11. Redis backups are not available.
        </p>
      </section>
    );
  if (denied) return <p role="alert">Database backup access is unavailable.</p>;
  async function submit(next: Attempt) {
    const controller = lifetime.current;
    if (
      !owner ||
      blocked ||
      pending ||
      !controller ||
      controller.signal.aborted
    )
      return;
    setAttempt(next);
    setPending(true);
    setError(undefined);
    try {
      const out =
        next.mode === "backup"
          ? await requestDatabaseBackup(
              scope,
              database,
              next.input,
              controller.signal,
            )
          : await restoreDatabaseBackup(
              scope,
              database,
              next.backup,
              next.input,
              controller.signal,
            );
      if (controller.signal.aborted) return;
      setAccepted(out.id);
      setAttempt(undefined);
      await history.refetch();
    } catch (cause) {
      if (!controller.signal.aborted) {
        setError(cause);
        if (cause instanceof ApiError && [400, 404, 409].includes(cause.status))
          setAttempt(undefined);
      }
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }
  const disabled =
    blocked ||
    pending ||
    history.isPending ||
    history.isError ||
    history.isFetching;
  const backups =
    history.data?.filter(
      (v) => v.mode === "backup" && v.state === "succeeded" && v.blob,
    ) ?? [];
  return (
    <section
      className="min-w-0 space-y-3 rounded-xl border p-3"
      aria-label="Database backups"
    >
      <h4 className="font-medium">PostgreSQL logical backups</h4>
      <p>
        A backup captures a consistent logical snapshot of the app database
        while source writes continue. The operator sets byte and execution
        limits; each accepted operation records its exact limits. Schedules and
        S3-compatible destinations are configured below. Upload status is
        separate from dump completion.
      </p>
      <p>
        Restore creates a new private database with distinct credentials and
        volume. It stays unavailable for application connections until import
        succeeds. Source roles and passwords are excluded.
      </p>
      {!owner ? (
        <p>
          Project owners can request backups and restore a new database. You can
          inspect history.
        </p>
      ) : (
        <div className="space-y-3">
          <BackupDestinationSelect
            label="Immediate backup destination"
            value={remoteTarget}
            onChange={setRemoteTarget}
            destinations={destinations}
            disabled={disabled || Boolean(attempt)}
          />
          <Button
            type="button"
            className="h-auto max-w-full whitespace-normal"
            disabled={
              disabled ||
              Boolean(attempt) ||
              !validSelectedBackupDestination(remoteTarget, destinations)
            }
            onClick={() =>
              void submit({
                mode: "backup",
                input: {
                  request_id: databaseRequestID(),
                  expected_resource_generation: database.resource.generation,
                  ...(remoteTarget ? { remote_target: remoteTarget } : {}),
                },
              })
            }
          >
            Back up now
          </Button>
          <div className="space-y-2">
            <label className="block">
              Completed backup
              <select
                aria-label="Completed backup"
                className="block w-full min-w-0 rounded border p-2"
                value={selected}
                disabled={disabled || Boolean(attempt)}
                onChange={(e) => {
                  setSelected(e.target.value);
                  setRestoreSource("");
                }}
              >
                <option value="">Select a completed backup</option>
                {backups.map((b) => (
                  <option key={b.id} value={b.id}>
                    {new Date(b.created_at).toLocaleString()} · {b.id}
                  </option>
                ))}
              </select>
            </label>
            <label className="block">
              Restore archive source
              <select
                aria-label="Restore archive source"
                className="block w-full min-w-0 rounded border p-2"
                value={restoreSource}
                disabled={disabled || Boolean(attempt)}
                onChange={(e) => setRestoreSource(e.target.value)}
              >
                <option value="">Local archive on the backup runner</option>
                {restoreSource && !remoteRestore ? (
                  <option value={restoreSource} disabled>
                    Selected remote object is no longer eligible
                  </option>
                ) : null}
                {restoreTransfers.map((v) => (
                  <option key={v.id} value={v.id}>
                    Saved remote object · {v.target_id} · upload {v.id}
                  </option>
                ))}
              </select>
            </label>
            <p>
              Remote restore uses only a successfully uploaded, recorded object
              at its saved destination revision. It downloads and verifies the
              archive before importing into a new database.
            </p>
            {!restoreSource ? (
              <p>
                Local restore requires the archive to remain available on the
                original backup runner. Remote upload success does not make a
                local restore independent of that runner.
              </p>
            ) : null}
            {restoreSource && !remoteRestore ? (
              <p role="alert">
                Refresh history and explicitly choose an eligible remote object
                before restoring.
              </p>
            ) : null}
            <label className="block">
              New database name
              <input
                aria-label="New database name"
                className="block w-full min-w-0 rounded border p-2"
                maxLength={128}
                value={name}
                disabled={disabled || Boolean(attempt)}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
            <Button
              type="button"
              className="h-auto max-w-full whitespace-normal"
              disabled={
                disabled ||
                Boolean(attempt) ||
                !backups.some((b) => b.id === selected) ||
                !name.trim() ||
                (Boolean(restoreSource) && !remoteRestore) ||
                /[\x00-\x1f\x7f]/.test(name)
              }
              onClick={() =>
                void submit({
                  mode: "restore",
                  backup: selected,
                  input: {
                    request_id: databaseRequestID(),
                    name: name.trim(),
                    ...(remoteRestore
                      ? {
                          remote_source: {
                            transfer_id: remoteRestore.id,
                            expected_target_revision:
                              remoteRestore.target_revision,
                          },
                        }
                      : {}),
                  },
                })
              }
            >
              Restore into new database
            </Button>
          </div>
        </div>
      )}
      {pending ? <p role="status">Submitting the saved request…</p> : null}
      {error ? (
        <p role="alert">
          {databaseError(error)}{" "}
          {attempt ? `Retained request ${attempt.input.request_id}.` : ""}
        </p>
      ) : null}
      {attempt && !pending && error && owner ? (
        <Button
          type="button"
          disabled={blocked}
          onClick={() => void submit(attempt)}
        >
          Retry the same request
        </Button>
      ) : null}
      {accepted ? (
        <p role="status" className="break-all">
          Accepted operation {accepted}. Completion is shown by server history
          below.
        </p>
      ) : null}
      {history.isError ? (
        <p role="alert">
          Backup history is unavailable. {databaseError(history.error)}
        </p>
      ) : null}
      {history.isPending ? <p role="status">Loading backup history…</p> : null}
      {history.data?.length === 0 ? (
        <p>No logical backup or restore requests yet.</p>
      ) : null}
      <ul className="space-y-3">
        {history.data?.map((v) => (
          <li key={v.id} className="min-w-0 space-y-1 border-t pt-2 break-all">
            <p>
              {v.mode === "backup" ? "Backup" : "New-database restore"} ·{" "}
              {v.state} · {v.id}
            </p>
            <p>{new Date(v.created_at).toLocaleString()}</p>
            <p>
              Limit: {v.max_bytes.toLocaleString()} bytes · {v.timeout_seconds}{" "}
              seconds
            </p>
            {v.blob ? (
              <p>
                {v.blob.bytes.toLocaleString()} bytes · PostgreSQL custom ·{" "}
                {v.blob.digest}
              </p>
            ) : null}
            {v.target_resource_id ? (
              <p>Target resource: {v.target_resource_id}</p>
            ) : null}
            {v.state === "succeeded" ? (
              <p>
                {v.mode === "restore"
                  ? "Import and readiness succeeded. Refresh project resources to select the restored database for a distinct application."
                  : "Local logical snapshot published."}
              </p>
            ) : null}
            {v.ownership_held ? (
              <p>
                {v.state === "uncertain"
                  ? "Execution is uncertain. Ownership remains held; no automatic import retry is authorized."
                  : "Operation owns maintenance or restore authority until settlement."}
              </p>
            ) : null}
            {v.failure_code ? <p>Recorded reason: {v.failure_code}</p> : null}
            {v.mode === "backup" ? (
              <BackupUploadStatus
                database={database}
                scope={scope}
                owner={owner}
                blocked={blocked}
                onDenied={onDenied}
                backup={v.id}
                dumpState={v.state}
                transfers={uploads.filter((t) => t.backup_id === v.id)}
                unavailable={
                  transfers[
                    (history.data ?? [])
                      .filter((t) => t.mode === "backup")
                      .findIndex((t) => t.id === v.id)
                  ]?.isError ?? false
                }
              />
            ) : null}
          </li>
        ))}
      </ul>
      <Button
        type="button"
        variant="outline"
        disabled={blocked || history.isFetching}
        onClick={() => {
          void history.refetch();
          transfers.forEach((v) => void v.refetch());
        }}
      >
        Refresh backup history
      </Button>
      <DatabaseBackupDestinations
        database={database}
        scope={scope}
        owner={owner}
        blocked={blocked}
        onDenied={onDenied}
      />
      <DatabaseBackupSchedules
        database={database}
        scope={scope}
        owner={owner}
        blocked={blocked}
        onDenied={onDenied}
      />
    </section>
  );
}

function BackupUploadStatus({
  scope,
  owner,
  blocked,
  onDenied,
  backup,
  dumpState,
  transfers,
  unavailable,
}: Props & {
  backup: string;
  dumpState: string;
  transfers: DatabaseBackupTransfer[];
  unavailable: boolean;
}) {
  const client = useQueryClient();
  const [attempt, setAttempt] = useState<{
    request_id: string;
    expected_transfer_id: string;
  }>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  const lifetime = useRef<AbortController | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    return () => {
      controller.abort();
      if (lifetime.current === controller) lifetime.current = null;
    };
  }, []);
  const denied = databaseDenied(error);
  useEffect(() => {
    if (denied) {
      lifetime.current?.abort();
      client.removeQueries({
        queryKey: databaseBackupTransfersKey(scope, backup),
      });
      onDenied();
    }
  }, [denied]);
  const latest = [...transfers].sort(
    (a, b) =>
      Date.parse(b.created_at) - Date.parse(a.created_at) ||
      Date.parse(b.updated_at) - Date.parse(a.updated_at),
  )[0];
  async function retry(input: {
    request_id: string;
    expected_transfer_id: string;
  }) {
    const controller = lifetime.current;
    if (
      !owner ||
      blocked ||
      pending ||
      !controller ||
      controller.signal.aborted
    )
      return;
    setAttempt(input);
    setPending(true);
    setError(undefined);
    try {
      await retryDatabaseBackupUpload(scope, backup, input, controller.signal);
      if (controller.signal.aborted) return;
      setAttempt(undefined);
      await client.invalidateQueries({
        queryKey: databaseBackupTransfersKey(scope, backup),
      });
    } catch (cause) {
      if (!controller.signal.aborted) {
        setError(cause);
        if (cause instanceof ApiError && [400, 404, 409].includes(cause.status))
          setAttempt(undefined);
      }
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }
  if (denied) return <p role="alert">Upload access is unavailable.</p>;
  return (
    <div className="space-y-2" aria-label={`Remote uploads for ${backup}`}>
      {unavailable ? (
        <p role="alert">
          Upload history is unavailable. Dump completion does not prove remote
          upload success.
        </p>
      ) : null}
      {latest ? (
        <>
          <p>
            Local dump: {dumpState}. Latest remote upload: {latest.state}.
          </p>
          {latest.state === "failed" ? (
            <p role="alert">
              Remote upload failed
              {latest.failure_code ? `: ${latest.failure_code}` : ""}. The local
              dump result is unchanged. Local restore still requires its archive
              on the original runner. Retry uploads the same archive and object
              key without another dump.
            </p>
          ) : null}
          {latest.state === "uncertain" ? (
            <p role="alert">
              Remote upload is uncertain. Refresh status before taking further
              action.
            </p>
          ) : null}
          {owner && latest.state === "failed" && !attempt ? (
            <Button
              type="button"
              variant="outline"
              className="h-auto max-w-full whitespace-normal"
              disabled={blocked || pending || unavailable}
              onClick={() =>
                void retry({
                  request_id: databaseRequestID(),
                  expected_transfer_id: latest.id,
                })
              }
            >
              Retry upload for {backup}
            </Button>
          ) : null}
          <ul>
            {transfers.map((v) => (
              <li key={v.id}>
                Upload {v.id} · {v.state} · target {v.target_id} revision{" "}
                {v.target_revision}
                {v.failure_code ? ` · ${v.failure_code}` : ""}
              </li>
            ))}
          </ul>
        </>
      ) : null}
      {pending ? <p role="status">Submitting the saved upload retry…</p> : null}
      {error ? (
        <p role="alert">
          {databaseError(error)}{" "}
          {attempt
            ? `Retained request ${attempt.request_id}.`
            : "Refresh upload history and review the current attempt before retrying."}
        </p>
      ) : null}
      {attempt && error && !pending && owner ? (
        <Button
          type="button"
          disabled={blocked}
          onClick={() => void retry(attempt)}
        >
          Retry the same upload request
        </Button>
      ) : null}
    </div>
  );
}
