import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError } from "../api/errors";
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
export function DatabaseBackups({
  database,
  scope,
  owner,
  blocked,
  onDenied,
}: {
  database: HostingDatabase;
  scope: DatabaseScope;
  owner: boolean;
  blocked: boolean;
  onDenied: () => void;
}) {
  const client = useQueryClient();
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
    enabled: !blocked && database.engine === "postgresql",
    retry: false,
    refetchInterval: (q) =>
      q.state.data?.some(
        (v) => v.ownership_held && !["uncertain", "failed"].includes(v.state),
      )
        ? 3000
        : false,
  });
  const denied = databaseDenied(history.error) || databaseDenied(error);
  useEffect(() => {
    if (denied) {
      lifetime.current?.abort();
      client.removeQueries({ queryKey: key });
      onDenied();
    }
  }, [denied]);
  if (database.engine !== "postgresql")
    return (
      <section aria-label="Database backups">
        <h4>Logical backups</h4>
        <p>
          Immediate logical backup and new-database restore currently support
          PostgreSQL17.11. Redis backups, schedules and S3 are not available.
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
        if (cause instanceof ApiError && cause.status === 400)
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
        while source writes continue. Local storage only. The operator sets byte
        and execution limits; each accepted operation records its exact limits.
        Schedules and S3 are not available.
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
          <Button
            type="button"
            className="h-auto max-w-full whitespace-normal"
            disabled={disabled || Boolean(attempt)}
            onClick={() =>
              void submit({
                mode: "backup",
                input: {
                  request_id: databaseRequestID(),
                  expected_resource_generation: database.resource.generation,
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
                onChange={(e) => setSelected(e.target.value)}
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
                /[\x00-\x1f\x7f]/.test(name)
              }
              onClick={() =>
                void submit({
                  mode: "restore",
                  backup: selected,
                  input: { request_id: databaseRequestID(), name: name.trim() },
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
          </li>
        ))}
      </ul>
      <Button
        type="button"
        variant="outline"
        disabled={blocked || history.isFetching}
        onClick={() => void history.refetch()}
      >
        Refresh backup history
      </Button>
    </section>
  );
}
