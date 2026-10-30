import { readBackupMetadata } from "@/components/hosting/backup-access";
import type { UseQueryResult } from "@tanstack/react-query";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { ApiError } from "../api/errors";
import { hostingBindingQuery } from "../api/hosting-deployments";
import { listResources, type Resource } from "../api/hosting";
import type { BackupRecoveryLocator } from "../api/hosting-backup-notifications";
import {
  databaseBackupTransfersKey,
  listDatabaseBackupTransfers,
  retryDatabaseBackupUpload,
  type DatabaseBackupTransfer,
  type RemoteBackupTarget,
} from "../api/hosting-database-backup-targets";
import {
  databaseBackupsKey,
  getDatabaseBackupComponents,
  listDatabaseBackups,
  requestDatabaseBackup,
  restoreDatabaseBackup,
  type DatabaseBackup,
  type DatabaseBackupInput,
  type DatabaseRestoreInput,
} from "../api/hosting-database-backups";
import {
  databaseDenied,
  databaseError,
  databaseRequestID,
  type DatabaseScope,
  type HostingDatabase,
} from "../api/hosting-databases";
import { currentGitStackQuery } from "../api/hosting-stack-lifecycle";
import { Button } from "../components/ui/button";
import { ComposeDatabaseExternalImport } from "./ComposeDatabaseExternalImport";
import { ComposeDatabaseBackupSchedules } from "./ComposeDatabaseBackupSchedules";
import {
  BackupDestinationSelect,
  DatabaseBackupDestinations,
} from "./DatabaseBackupDestinations";
import { DatabaseBackupRetention } from "./DatabaseBackupRetention";
import { DatabaseBackupSchedules } from "./DatabaseBackupSchedules";
import { useDatabaseAccess } from "./ResourceDatabase";
import {
  useDatabaseBackupDestinations,
  validSelectedBackupDestination,
} from "./database-backup-destinations";

type Attempt =
  | { mode: "backup"; input: DatabaseBackupInput }
  | { mode: "restore"; backup: string; input: DatabaseRestoreInput };
type Props = {
  database: HostingDatabase;
  scope: DatabaseScope;
  owner: boolean;
  blocked: boolean;
  onDenied: () => void;
  recovery?: BackupRecoveryLocator;
  recoveryBackup?: DatabaseBackup;
  recoveryTransfer?: DatabaseBackupTransfer;
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
function DatabaseBackupsPanel(props: Parameters<typeof useDatabaseBackups>[0]) {
  const {
    database,
    denied,
    owner,
    remoteTarget,
    setRemoteTarget,
    destinations,
    disabled,
    attempt,
    submit,
    selected,
    setSelected,
    setRestoreSource,
    backups,
    restoreSource,
    remoteRestore,
    restoreTransfers,
    name,
    setName,
    pending,
    error,
    blocked,
    accepted,
    history,
    records,
    recovery,
    scope,
    onDenied,
    uploads,
    transfers,
  } = useDatabaseBackups(props);

  if (!["postgresql", "mysql"].includes(database.engine))
    return (
      <section aria-label="Database backups">
        <h4>Logical backups</h4>
        <p>
          Immediate logical backup and new-database restore currently support
          PostgreSQL17.11 and MySQL8.4.13. Redis backups are not available.
        </p>
      </section>
    );
  if (denied) return <p role="alert">Database backup access is unavailable.</p>;
  return (
    <section
      className="min-w-0 space-y-3 rounded-xl border p-3"
      aria-label="Database backups"
    >
      <h4 className="font-medium">{database.engine === "mysql" ? "MySQL" : "PostgreSQL"} logical backups</h4>
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
        <DatabaseBackupRequests
          remoteTarget={remoteTarget}
          setRemoteTarget={setRemoteTarget}
          destinations={destinations}
          disabled={disabled}
          attempt={attempt}
          submit={submit}
          database={database}
          selected={selected}
          setSelected={setSelected}
          setRestoreSource={setRestoreSource}
          backups={backups}
          restoreSource={restoreSource}
          remoteRestore={remoteRestore}
          restoreTransfers={restoreTransfers}
          name={name}
          setName={setName}
        />
      )}
      <DatabaseBackupSubmission
        pending={pending}
        error={error}
        attempt={attempt}
        owner={owner}
        blocked={blocked}
        submit={submit}
        accepted={accepted}
      />
      {history.isError ? (
        <p role="alert">
          Backup history is unavailable. {databaseError(history.error)}
        </p>
      ) : null}
      {history.isPending ? <p role="status">Loading backup history…</p> : null}
      {history.data && records.length === 0 ? (
        <p>No logical backup or restore requests yet.</p>
      ) : null}
      <ul className="space-y-3">
        {records.map((v) => (
          <DatabaseBackupRecord
            key={v.id}
            v={v}
            recovery={recovery}
            scope={scope}
            owner={owner}
            blocked={blocked}
            onDenied={onDenied}
            uploads={uploads}
            transfers={transfers}
            records={records}
          />
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
      <DatabaseBackupRetention
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
function useDatabaseBackups({
  database,
  scope,
  owner,
  blocked,
  onDenied,
  recovery,
  recoveryBackup,
  recoveryTransfer,
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
  const [selected, setSelected] = useState(recovery?.backup_id ?? "");
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
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => listDatabaseBackups(scope, database, signal),
        retireBackupAccess,
        [401, 403],
      ),
    enabled: !blocked && !accessRevoked && ["postgresql", "mysql"].includes(database.engine),
    retry: false,
    refetchInterval: (q) =>
      q.state.data?.some(
        (v) => v.ownership_held && !["uncertain", "failed"].includes(v.state),
      )
        ? 3000
        : false,
  });
  // The authenticated exact-ID lookup can resolve a retained backup outside
  // the latest 100 operations. Prefer fresh list metadata when it is present.
  const records = includeRecoveryRecord(history.data, recoveryBackup);
  const transfers = useQueries({
    queries: records
      .filter((v) => v.mode === "backup")
      .map((v) => ({
        queryKey: databaseBackupTransfersKey(scope, v.id),
        queryFn: ({ signal }: { signal: AbortSignal }) =>
          readBackupMetadata(
            signal,
            () => listDatabaseBackupTransfers(scope, v.id, signal),
            retireBackupAccess,
            [401, 403],
          ),
        enabled: !blocked && !accessRevoked,
        retry: false,
        refetchInterval: (query: {
          state: { data?: DatabaseBackupTransfer[] };
        }) =>
          (v.remote_target &&
            v.state === "succeeded" &&
            v.blob &&
            !query.state.data?.some((t) => t.mode === "upload")) ||
          query.state.data?.some((t) =>
            ["queued", "dispatched", "applying"].includes(t.state),
          )
            ? 3000
            : false,
      })),
  });
  const recentUploads = transfers
    .flatMap((v) => v.data ?? [])
    .filter((v) => v.mode === "upload");
  const uploads = includeRecoveryRecord(recentUploads, recoveryTransfer);
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
  function retireBackupAccess() {
    setAccessRevoked(true);
    lifetime.current?.abort();
    client.removeQueries({ queryKey: key });
    client.removeQueries({
      queryKey: ["database-backup-transfers", scope.actor, scope.project],
    });
    onDenied();
  }
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
        if (databaseDenied(cause)) retireBackupAccess();
        if (cause instanceof ApiError && [400, 404, 409].includes(cause.status))
          setAttempt(undefined);
      }
    } finally {
      setPending(false);
    }
  }
  const disabled =
    blocked ||
    pending ||
    history.isPending ||
    history.isError ||
    history.isFetching;
  const backups =
    records.filter(
      (v) => v.mode === "backup" && v.state === "succeeded" && v.blob,
    ) ?? [];
  return {
    database,
    denied,
    owner,
    remoteTarget,
    setRemoteTarget,
    destinations,
    disabled,
    attempt,
    submit,
    selected,
    setSelected,
    setRestoreSource,
    backups,
    restoreSource,
    remoteRestore,
    restoreTransfers,
    name,
    setName,
    pending,
    error,
    blocked,
    accepted,
    history,
    records,
    recovery,
    scope,
    onDenied,
    uploads,
    transfers,
  };
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
  recovery,
}: Pick<Props, "scope" | "owner" | "blocked" | "onDenied" | "recovery"> & {
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
        if (databaseDenied(cause)) {
          controller.abort();
          client.removeQueries({
            queryKey: databaseBackupTransfersKey(scope, backup),
          });
          onDenied();
        }
        if (cause instanceof ApiError && [400, 404, 409].includes(cause.status))
          setAttempt(undefined);
      }
    } finally {
      setPending(false);
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
        <BackupUploadHistory
          dumpState={dumpState}
          latest={latest}
          owner={owner}
          attempt={attempt}
          blocked={blocked}
          pending={pending}
          unavailable={unavailable}
          retry={retry}
          backup={backup}
          transfers={transfers}
          recovery={recovery}
        />
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

// Compose databases keep their owning application identity; they are never
// represented as fabricated standalone database resources.
export function ResourceComposeDatabaseBackups({
  resource,
  blocked,
}: {
  resource: Resource;
  blocked: boolean;
}) {
  const access = useDatabaseAccess(
    resource.project_id,
    resource.project_environment_id,
  );
  if (resource.source.kind !== "compose" && resource.source.kind !== "git")
    return null;
  if (blocked || !access.ready || access.refreshing || !access.viewer)
    return (
      <p role="status">
        Component database backup access is pending or unavailable.
      </p>
    );
  return resource.source.kind === "git" ? (
    <GitComposeDatabaseBackups
      key={JSON.stringify([access.scope, resource.id])}
      resource={resource}
      scope={access.scope}
      owner={access.owner}
    />
  ) : (
    <ComposeDatabaseBackups
      key={JSON.stringify([access.scope, resource.id, resource.generation])}
      resource={resource}
      scope={access.scope}
      owner={access.owner}
    />
  );
}
function GitComposeDatabaseBackups({
  resource,
  scope,
  owner,
}: {
  resource: Resource;
  scope: DatabaseScope;
  owner: boolean;
}) {
  const current = useQuery(
    currentGitStackQuery({
      actor: scope.actor,
      project: scope.project,
      resource: resource.id,
    }),
  );
  if (current.isPending || current.isFetching || current.isError)
    return (
      <div>
        <p role={current.isError ? "alert" : "status"}>
          Current stack evidence is pending or unavailable. Component database
          backups are paused.
        </p>
        <Button
          disabled={current.isFetching}
          onClick={() => void current.refetch()}
        >
          Refresh database stack evidence
        </Button>
      </div>
    );
  return current.data.operation ? (
    <ComposeDatabaseBackups
      key={current.data.operation.id}
      resource={resource}
      scope={scope}
      owner={owner}
    />
  ) : null;
}
function ComposeDatabaseBackups(
  props: Parameters<typeof useComposeDatabaseBackups>[0],
) {
  const [externalSource, setExternalSource] = useState(false);
  const [externalBusy, setExternalBusy] = useState(false);
  const {
    denied,
    revoked,
    component,
    locked: operationLocked,
    setComponent,
    source,
    ready: operationReady,
    submit,
    targets,
    backup,
    setBackup,
    history,
    target,
    setTarget,
    setTargetComponent,
    applications,
    resource,
    scope,
    targetComponent,
    destination,
    targetEvidenceRevision,
    ack,
    setAck,
    selected,
    chosen,
    error,
    attempt,
    pending,
    owner,
    retireComposeAccess,
    destinations,
    remoteTarget,
    setRemoteTarget,
    transfers,
    destinationReady,
    restoreSource,
    setRestoreSource,
    restoreTransfers,
    remoteRestore,
  } = useComposeDatabaseBackups(props);

  const locked = operationLocked || externalBusy;
  const ready = operationReady && !externalBusy;
  if (denied || revoked)
    return <p role="alert">Component database backup access is unavailable.</p>;
  return (
    <section
      aria-label="Component database backups"
      className="min-w-0 space-y-3 rounded-xl border p-3"
    >
      <h5 className="font-medium">Compose database logical backups</h5>
      <p>
        Qualified official PostgreSQL16 and pinned MySQL8.4.13 components only. Backups can upload to a validated destination. MySQL imports can use a local archive, a recorded successful upload or an inspected configured S3 object. Retention for the qualified mysql component is configured below. Archives contain
        database records, not uploaded files or volume contents. The runner
        verifies the live profile before execution. Source writes may continue
        during backup.
      </p>
      <label className="block">
        Database backup component
        <select
          value={component}
          disabled={locked}
          onChange={(e) => {
            if (!locked) {
              setAck(false);
              setComponent(e.target.value);
            }
          }}
        >
          <option value="">Select a database component</option>
          {source?.components
            .filter((c) => c.eligible)
            .map((c) => (
              <option key={c.component} value={c.component}>
                {c.component} · {c.engine} {c.major_version}
              </option>
            ))}
        </select>
      </label>
      <BackupDestinationSelect
        label="Immediate backup destination"
        value={remoteTarget}
        onChange={(value) => {
          if (!locked) setRemoteTarget(value);
        }}
        destinations={destinations}
        disabled={!owner || locked || !destinationReady}
      />
      <Button
        disabled={
          !ready ||
          !validSelectedBackupDestination(remoteTarget, destinations) ||
          !source?.operation_id ||
          !source.components.some(
            (c) => c.component === component && c.eligible,
          )
        }
        onClick={() => {
          if (source?.operation_id)
            void submit({
              mode: "backup",
              input: {
                request_id: databaseRequestID(),
                expected_resource_generation: source.resource_generation,
                component,
                expected_deployed_operation_id: source.operation_id,
                ...(remoteTarget ? { remote_target: remoteTarget } : {}),
              },
            });
        }}
      >
        Create logical backup
      </Button>
      <Button
        variant="outline"
        disabled={locked || targets.isFetching}
        onClick={() => {
          if (!locked) {
            setAck(false);
            setComponent("");
            void targets.refetch();
          }
        }}
      >
        Refresh database components
      </Button>
      {!source ? (
        <p role="status">
          A settled supported database component is unavailable. Refresh after
          deployment settles.
        </p>
      ) : null}
      <label className="block">Database import source<select className="block w-full min-w-0 rounded border p-2" value={externalSource ? "s3" : "backup"} disabled={locked} onChange={e => { if (!locked) { setAck(false); setExternalSource(e.target.value === "s3"); } }}><option value="backup">Recorded database backup</option><option value="s3">Configured S3 object · MySQL SQL</option></select></label>
      <ComposeDatabaseRestore
        externalSource={externalSource}
        targetEvidenceRevision={targetEvidenceRevision}
        externalBlocked={operationLocked}
        onExternalBusy={setExternalBusy}
        owner={owner}
        onDenied={retireComposeAccess}
        backup={backup}
        locked={locked}
        setAck={setAck}
        setBackup={setBackup}
        history={history}
        target={target}
        setTarget={setTarget}
        setTargetComponent={setTargetComponent}
        applications={applications}
        resource={resource}
        scope={scope}
        targetComponent={targetComponent}
        destination={destination}
        ack={ack}
        ready={ready}
        selected={selected}
        chosen={chosen}
        submit={submit}
        restoreSource={restoreSource}
        setRestoreSource={setRestoreSource}
        restoreTransfers={restoreTransfers}
        remoteRestore={remoteRestore}
      />
      <Button
        variant="outline"
        disabled={locked || history.isFetching}
        onClick={() => {
          if (!locked) void history.refetch();
        }}
      >
        Refresh database backup history
      </Button>
      {history.data?.map((v) => (
        <div key={v.id} className="min-w-0 space-y-1 break-all">
          <p role="status">
            {v.id} · {v.mode} · {v.state}
            {v.ownership_held ? " · mutation ownership retained" : ""}
            {v.failure_code ? ` · ${v.failure_code}` : ""}
          </p>
          {v.mode === "restore" ? transfers.flatMap(t => t.data ?? [])
            .filter(t => t.mode === "download" && t.restore_operation_id === v.id)
            .map(t => <p key={t.id}>Remote archive download: {t.state}. Restore operation: {v.state}. Download success does not prove import completion.</p>) : null}
          {v.mode === "backup" ? (
            <BackupUploadStatus
              scope={scope}
              owner={owner}
              blocked={locked}
              onDenied={retireComposeAccess}
              backup={v.id}
              dumpState={v.state}
              transfers={transfers.flatMap(t => t.data ?? []).filter(t => t.mode === "upload" && t.backup_id === v.id)}
              unavailable={transfers[(history.data ?? []).filter(t => t.mode === "backup").findIndex(t => t.id === v.id)]?.isError ?? false}
            />
          ) : null}
        </div>
      ))}
      {error ? <p role="alert">{databaseError(error)}</p> : null}
      {error && attempt ? (
        <p role="status">
          The response is unconfirmed. Request {attempt.input.request_id} is
          retained; retry the identical request before changing selections or
          starting another database action.
        </p>
      ) : null}
      {error && attempt ? (
        <Button disabled={pending} onClick={() => void submit(attempt)}>
          Retry exact database request
        </Button>
      ) : null}
      <DatabaseBackupRetention
        database={{ resource, engine: "mysql", source_scope: { backup_version: "mysql.compose-logical-local.v1", component: "mysql" } }}
        sourceEligible={source?.components.some(c => c.component === "mysql" && c.engine === "mysql" && c.major_version === 8 && c.eligible) === true}
        scope={scope}
        owner={owner}
        blocked={locked}
        onDenied={retireComposeAccess}
      />
      <ComposeDatabaseBackupSchedules
        resource={resource}
        scope={scope}
        owner={owner}
        onDenied={retireComposeAccess}
      />
    </section>
  );
}
function useComposeDatabaseBackups({
  resource,
  scope,
  owner,
}: {
  resource: Resource;
  scope: DatabaseScope;
  owner: boolean;
}) {
  const client = useQueryClient();
  const context = { resource };
  const [component, setComponent] = useState("");
  const [remoteTarget, setRemoteTarget] = useState<RemoteBackupTarget>();
  const [backup, setBackup] = useState("");
  const [restoreSource, setRestoreSource] = useState("");
  const [target, setTarget] = useState("");
  const [targetComponent, setTargetComponent] = useState("");
  const [targetEvidenceRevision, setTargetEvidenceRevision] = useState(0);
  const [confirmation, setConfirmation] = useState({ identity: "", accepted: false });
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  const [denied, setDenied] = useState(false);
  const bindingOptions = hostingBindingQuery(scope.project, resource.id, scope.actor);
  const binding = useQuery({
    ...bindingOptions,
    enabled: !denied,
    queryFn: (query) => readBackupMetadata(
      query.signal,
      () => bindingOptions.queryFn!(query),
      retireComposeAccess,
      [401, 403],
    ),
  });
  const destinations = useDatabaseBackupDestinations({
    component: binding.data && !binding.isFetching && !binding.isError ? { destination_id: binding.data.destination_id } : undefined,
    scope,
    owner,
    blocked: denied || binding.isFetching || binding.isError || !binding.data,
    onDenied: retireComposeAccess,
  });
  const [attempt, setAttempt] = useState<Attempt>();
  const lifetime = useRef<AbortController | null>(null);
  useEffect(() => {
    const c = new AbortController();
    lifetime.current = c;
    return () => {
      c.abort();
      lifetime.current = null;
    };
  }, []);
  const targets = useQuery({
    queryKey: ["database-backup-components", scope, resource.id],
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => getDatabaseBackupComponents(scope, resource.id, signal),
        retireComposeAccess,
        [401, 403],
      ),
    retry: false,
    enabled: !denied,
  });
  const history = useQuery({
    queryKey: databaseBackupsKey(scope, resource.id),
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => listDatabaseBackups(scope, context, signal),
        retireComposeAccess,
        [401, 403],
      ),
    retry: false,
    enabled: !denied,
    refetchInterval: (q) =>
      q.state.data?.some(
        (v) => v.ownership_held && !["uncertain", "failed"].includes(v.state),
      )
        ? 3000
        : false,
  });
  const transfers = useQueries({
    queries: (history.data ?? []).filter(v => v.mode === "backup").map(v => ({
      queryKey: databaseBackupTransfersKey(scope, v.id),
      queryFn: ({ signal }: { signal: AbortSignal }) => readBackupMetadata(
        signal,
        () => listDatabaseBackupTransfers(scope, v.id, signal),
        retireComposeAccess,
        [401, 403],
      ),
      enabled: !denied,
      retry: false,
      refetchInterval: (query: { state: { data?: DatabaseBackupTransfer[] } }) =>
        (v.remote_target && v.state === "succeeded" && v.blob && !query.state.data?.some(t => t.mode === "upload")) ||
        query.state.data?.some(t => ["queued", "dispatched", "applying"].includes(t.state)) ? 3000 : false,
    })),
  });
  const applications = useQuery({
    queryKey: ["database-import-applications", scope],
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () =>
          listResources(
            { project_id: scope.project, limit: 100, offset: 0 },
            { signal },
          ),
        retireComposeAccess,
        [401, 403],
      ),
    retry: false,
    enabled: !denied,
  });
  const chosen = applications.data?.items.find(
    (r) =>
      r.id === target &&
      r.id !== resource.id &&
      (r.source.kind === "compose" || r.source.kind === "git") &&
      r.project_id === scope.project,
  );
  const targetEvidence = useQuery({
    queryKey: ["database-backup-components", scope, chosen?.id],
    queryFn: async ({ signal }) => {
      const value = await readBackupMetadata(
        signal,
        () => getDatabaseBackupComponents(scope, chosen!.id, signal),
        retireComposeAccess,
        [401, 403],
      );
      if (!signal.aborted) setTargetEvidenceRevision(revision => revision + 1);
      return value;
    },
    retry: false,
    enabled: Boolean(chosen) && !denied,
  });
  const revoked =
    databaseDenied(error) ||
    databaseDenied(targets.error) ||
    databaseDenied(history.error) ||
    databaseDenied(applications.error) ||
    databaseDenied(targetEvidence.error) ||
    destinations.denied ||
    databaseDenied(binding.error) ||
    transfers.some(t => databaseDenied(t.error));
  function retireComposeAccess() {
    setDenied(true);
    lifetime.current?.abort();
    setAttempt(undefined);
    setRemoteTarget(undefined);
    setRestoreSource("");
    client.removeQueries({ queryKey: databaseBackupsKey(scope, resource.id) });
    client.removeQueries({ queryKey: ["database-backup-transfers", scope.actor, scope.project] });
  }
  const source = settledDatabaseComponents(targets);
  const destination = settledDatabaseComponents(targetEvidence);
  const locked = pending || Boolean(attempt);
  const ready =
    owner &&
    !locked &&
    !history.isPending &&
    !history.isError &&
    !history.isFetching;
  const selected = history.data?.find(
    (v) =>
      v.id === backup &&
      v.mode === "backup" &&
      v.state === "succeeded" &&
      v.blob &&
      (v.version === "postgresql.compose-logical-local.v1" || v.version === "mysql.compose-logical-local.v1"),
  );
  const selectedTransfers = transfers[(history.data ?? []).filter(v => v.mode === "backup").findIndex(v => v.id === selected?.id)];
  const restoreTransfers = selected?.version === "mysql.compose-logical-local.v1" && !selectedTransfers?.isFetching && !selectedTransfers?.isError
    ? (selectedTransfers?.data ?? []).filter(v =>
      v.mode === "upload" && v.state === "succeeded" && v.backup_id === selected.id &&
      v.version === "mysql.logical-s3-transfer.v1" && v.blob?.format === "mysql_sql" &&
      v.blob.digest === selected.blob?.digest && v.blob.bytes === selected.blob?.bytes,
    ) : [];
  const remoteRestore = restoreTransfers.find(v => v.id === restoreSource);
  const confirmationIdentity = JSON.stringify([
    restoreSource,
    remoteRestore?.id,
    remoteRestore?.target_id,
    remoteRestore?.target_revision,
    remoteRestore?.blob?.digest,
    remoteRestore?.blob?.bytes,
    selected?.id,
    selected?.version,
    selected?.blob?.digest,
    chosen?.id,
    targetComponent,
    targetEvidence.data?.operation_id,
    targetEvidence.data?.resource_generation,
  ]);
  // Retire unsubmitted consent, rather than hiding it until old evidence returns.
  // An attempted request retains its own immutable target and retry authority.
  if (!attempt && confirmation.identity !== confirmationIdentity) {
    setConfirmation({ identity: confirmationIdentity, accepted: false });
  }
  const ack = confirmation.identity === confirmationIdentity && confirmation.accepted;
  const setAck: React.Dispatch<React.SetStateAction<boolean>> = (value) => {
    setConfirmation(current => ({
      identity: confirmationIdentity,
      accepted: typeof value === "function"
        ? value(current.identity === confirmationIdentity && current.accepted)
        : value,
    }));
  };
  async function submit(next: Attempt) {
    const c = lifetime.current;
    if (
      !owner ||
      pending ||
      (attempt && next !== attempt) ||
      !c ||
      c.signal.aborted
    )
      return;
    setPending(true);
    setError(undefined);
    setAttempt(next);
    try {
      if (next.mode === "backup")
        await requestDatabaseBackup(scope, context, next.input, c.signal);
      else
        await restoreDatabaseBackup(
          scope,
          context,
          next.backup,
          next.input,
          c.signal,
        );
      if (c.signal.aborted) return;
      setAttempt(undefined);
      await history.refetch();
    } catch (cause) {
      if (!c.signal.aborted) {
        setError(cause);
        if (databaseDenied(cause)) retireComposeAccess();
        if (cause instanceof ApiError && [400, 404, 409].includes(cause.status))
          setAttempt(undefined);
      }
    } finally {
      setPending(false);
    }
  }
  return {
    denied,
    revoked,
    component,
    locked,
    setComponent,
    source,
    ready,
    submit,
    targets,
    backup,
    setBackup,
    history,
    target,
    setTarget,
    setTargetComponent,
    applications,
    resource,
    scope,
    targetComponent,
    destination,
    targetEvidenceRevision,
    ack,
    setAck,
    selected,
    chosen,
    error,
    attempt,
    pending,
    owner,
    retireComposeAccess,
    destinations,
    remoteTarget,
    setRemoteTarget,
    transfers,
    destinationReady: Boolean(binding.data) && !binding.isFetching && !binding.isError,
    restoreSource,
    setRestoreSource,
    restoreTransfers,
    remoteRestore,
  };
}

function DatabaseBackupRequests({
  remoteTarget,
  setRemoteTarget,
  destinations,
  disabled,
  attempt,
  submit,
  database,
  selected,
  setSelected,
  setRestoreSource,
  backups,
  restoreSource,
  remoteRestore,
  restoreTransfers,
  name,
  setName,
}: {
  remoteTarget:
    | import("@/api/hosting-database-backup-targets").RemoteBackupTarget
    | undefined;
  setRemoteTarget: ReturnType<typeof useDatabaseBackups>["setRemoteTarget"];
  destinations: ReturnType<typeof useDatabaseBackupDestinations>;
  disabled: boolean;
  attempt: Attempt | undefined;
  submit: (next: Attempt) => Promise<void>;
  database: import("@/api/hosting-databases").HostingDatabase;
  selected: string;
  setSelected: React.Dispatch<React.SetStateAction<string>>;
  setRestoreSource: React.Dispatch<React.SetStateAction<string>>;
  backups: import("@/api/hosting-database-backups").DatabaseBackup[];
  restoreSource: string;
  remoteRestore:
    | import("@/api/hosting-database-backup-targets").DatabaseBackupTransfer
    | undefined;
  restoreTransfers: import("@/api/hosting-database-backup-targets").DatabaseBackupTransfer[];
  name: string;
  setName: React.Dispatch<React.SetStateAction<string>>;
}) {
  return (
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
      <DatabaseRestoreRequest
        selected={selected}
        disabled={disabled}
        attempt={attempt}
        setSelected={setSelected}
        setRestoreSource={setRestoreSource}
        backups={backups}
        restoreSource={restoreSource}
        remoteRestore={remoteRestore}
        restoreTransfers={restoreTransfers}
        name={name}
        setName={setName}
        submit={submit}
      />
    </div>
  );
}

function DatabaseBackupRecord({
  v,
  recovery,
  scope,
  owner,
  blocked,
  onDenied,
  uploads,
  transfers,
  records,
}: {
  v: import("@/api/hosting-database-backups").DatabaseBackup;
  recovery:
    | import("@/api/hosting-backup-notifications").BackupRecoveryLocator
    | undefined;
  scope: import("@/api/hosting-databases").DatabaseScope;
  owner: boolean;
  blocked: boolean;
  onDenied: () => void;
  uploads: import("@/api/hosting-database-backup-targets").DatabaseBackupTransfer[];
  transfers: ReturnType<typeof useDatabaseBackups>["transfers"];
  records: import("@/api/hosting-database-backups").DatabaseBackup[];
}) {
  return (
    <li
      key={v.id}
      aria-label={
        recovery?.backup_id === v.id ? "Linked backup history" : undefined
      }
      className="min-w-0 space-y-1 border-t pt-2 break-all"
    >
      <p>
        {v.mode === "backup" ? "Backup" : "New-database restore"} · {v.state} ·{" "}
        {v.id}
      </p>
      <p>{new Date(v.created_at).toLocaleString()}</p>
      <p>
        Limit: {v.max_bytes.toLocaleString()} bytes · {v.timeout_seconds}{" "}
        seconds
      </p>
      {v.blob ? (
        <p>
          {v.blob.bytes.toLocaleString()} bytes · {v.blob.format === "mysql_sql" ? "MySQL SQL dump" : "PostgreSQL custom"} ·{" "}
          {v.blob.digest}
        </p>
      ) : null}
      {v.mode === "restore" ? transfers
        .flatMap(t => t.data ?? [])
        .filter(t => t.mode === "download" && t.restore_operation_id === v.id)
        .map(t => <p key={t.id}>Remote archive download: {t.state}. Restore import: {v.state}. Download success does not prove import completion.</p>) : null}
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
          scope={scope}
          owner={owner}
          blocked={blocked}
          onDenied={onDenied}
          recovery={recovery}
          backup={v.id}
          dumpState={v.state}
          transfers={uploads.filter((t) => t.backup_id === v.id)}
          unavailable={
            transfers[
              records
                .filter((t) => t.mode === "backup")
                .findIndex((t) => t.id === v.id)
            ]?.isError ?? false
          }
        />
      ) : null}
    </li>
  );
}

function BackupUploadHistory({
  dumpState,
  latest,
  owner,
  attempt,
  blocked,
  pending,
  unavailable,
  retry,
  backup,
  transfers,
  recovery,
}: {
  dumpState: string;
  latest: import("@/api/hosting-database-backup-targets").DatabaseBackupTransfer;
  owner: boolean;
  attempt: { request_id: string; expected_transfer_id: string } | undefined;
  blocked: boolean;
  pending: boolean;
  unavailable: boolean;
  retry: (input: {
    request_id: string;
    expected_transfer_id: string;
  }) => Promise<void>;
  backup: string;
  transfers: import("@/api/hosting-database-backup-targets").DatabaseBackupTransfer[];
  recovery:
    | import("@/api/hosting-backup-notifications").BackupRecoveryLocator
    | undefined;
}) {
  return (
    <>
      <p>
        Local dump: {dumpState}. Latest remote upload: {latest.state}.
      </p>
      {latest.state === "failed" ? (
        <p role="alert">
          Remote upload failed
          {latest.failure_code ? `: ${latest.failure_code}` : ""}. The local
          dump result is unchanged. Local restore still requires its archive on
          the original runner. Retry uploads the same archive and object key
          without another dump.
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
          <li
            key={v.id}
            aria-label={
              recovery?.transfer_id === v.id
                ? "Linked upload history"
                : undefined
            }
          >
            Upload {v.id} · {v.state} · target {v.target_id} revision{" "}
            {v.target_revision}
            {v.failure_code ? ` · ${v.failure_code}` : ""}
          </li>
        ))}
      </ul>
    </>
  );
}

function DatabaseRestoreRequest({
  selected,
  disabled,
  attempt,
  setSelected,
  setRestoreSource,
  backups,
  restoreSource,
  remoteRestore,
  restoreTransfers,
  name,
  setName,
  submit,
}: {
  selected: string;
  disabled: boolean;
  attempt: Attempt | undefined;
  setSelected: React.Dispatch<React.SetStateAction<string>>;
  setRestoreSource: React.Dispatch<React.SetStateAction<string>>;
  backups: import("@/api/hosting-database-backups").DatabaseBackup[];
  restoreSource: string;
  remoteRestore:
    | import("@/api/hosting-database-backup-targets").DatabaseBackupTransfer
    | undefined;
  restoreTransfers: import("@/api/hosting-database-backup-targets").DatabaseBackupTransfer[];
  name: string;
  setName: React.Dispatch<React.SetStateAction<string>>;
  submit: (next: Attempt) => Promise<void>;
}) {
  return (
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
        Remote restore uses only a successfully uploaded, recorded object at its
        saved destination revision. It downloads and verifies the archive before
        importing into a new database.
      </p>
      {!restoreSource ? (
        <p>
          Local restore requires the archive to remain available on the original
          backup runner. Remote upload success does not make a local restore
          independent of that runner.
        </p>
      ) : null}
      {restoreSource && !remoteRestore ? (
        <p role="alert">
          Refresh history and explicitly choose an eligible remote object before
          restoring.
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
                      expected_target_revision: remoteRestore.target_revision,
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
  );
}

function DatabaseBackupSubmission({
  pending,
  error,
  attempt,
  owner,
  blocked,
  submit,
  accepted,
}: {
  pending: boolean;
  error: unknown;
  attempt: Attempt | undefined;
  owner: boolean;
  blocked: boolean;
  submit: (next: Attempt) => Promise<void>;
  accepted: string;
}) {
  return (
    <>
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
    </>
  );
}

function includeRecoveryRecord<T extends { id: string }>(
  records: T[] | undefined,
  recovery: T | undefined,
): T[] {
  return recovery && !records?.some((value) => value.id === recovery.id)
    ? [recovery, ...(records ?? [])]
    : (records ?? []);
}
function settledDatabaseComponents(
  query: UseQueryResult<
    Awaited<ReturnType<typeof getDatabaseBackupComponents>>
  >,
) {
  return query.isSuccess && !query.isFetching && query.data.available
    ? query.data
    : undefined;
}

function ComposeDatabaseRestore({
  externalSource,
  targetEvidenceRevision,
  externalBlocked,
  onExternalBusy,
  owner,
  onDenied,
  backup,
  locked,
  setAck,
  setBackup,
  history,
  target,
  setTarget,
  setTargetComponent,
  applications,
  resource,
  scope,
  targetComponent,
  destination,
  ack,
  ready,
  selected,
  chosen,
  submit,
  restoreSource,
  setRestoreSource,
  restoreTransfers,
  remoteRestore,
}: {
  externalSource: boolean;
  targetEvidenceRevision: number;
  externalBlocked: boolean;
  onExternalBusy: (busy: boolean) => void;
  owner: boolean;
  onDenied: () => void;
  restoreSource: string;
  setRestoreSource: React.Dispatch<React.SetStateAction<string>>;
  restoreTransfers: DatabaseBackupTransfer[];
  remoteRestore: DatabaseBackupTransfer | undefined;
  backup: string;
  locked: boolean;
  setAck: React.Dispatch<React.SetStateAction<boolean>>;
  setBackup: React.Dispatch<React.SetStateAction<string>>;
  history: ReturnType<typeof useDatabaseBackups>["history"];
  target: string;
  setTarget: React.Dispatch<React.SetStateAction<string>>;
  setTargetComponent: React.Dispatch<React.SetStateAction<string>>;
  applications: ReturnType<typeof useComposeDatabaseBackups>["applications"];
  resource: ReturnType<typeof useComposeDatabaseBackups>["resource"];
  scope: import("@/api/hosting-databases").DatabaseScope;
  targetComponent: string;
  destination: ReturnType<typeof useComposeDatabaseBackups>["destination"];
  ack: boolean;
  ready: boolean;
  selected: import("@/api/hosting-database-backups").DatabaseBackup | undefined;
  chosen: ReturnType<typeof useComposeDatabaseBackups>["chosen"];
  submit: (next: Attempt) => Promise<void>;
}) {
  const matchesArchive = (c: { component: string; eligible: boolean; engine: string; major_version: number }) =>
    (!externalSource || c.component === "mysql") && c.eligible && (externalSource || Boolean(selected)) &&
    (externalSource || selected?.version === "mysql.compose-logical-local.v1"
      ? c.engine === "mysql" && c.major_version === 8
      : c.engine === "postgresql" && c.major_version === 16);
  return (
    <>
      {!externalSource ? <>
      <label className="block">
        Backup to restore
        <select
          value={backup}
          disabled={locked}
          onChange={(e) => {
            if (!locked) {
              setAck(false);
              setBackup(e.target.value);
              setRestoreSource("");
            }
          }}
        >
          <option value="">Select a completed backup</option>
          {history.data
            ?.filter(
              (v) =>
                v.mode === "backup" &&
                v.state === "succeeded" &&
                v.blob &&
                (v.version === "postgresql.compose-logical-local.v1" || v.version === "mysql.compose-logical-local.v1"),
            )
            .map((v) => (
              <option key={v.id} value={v.id}>
                {v.source_component} · {v.version === "mysql.compose-logical-local.v1" ? "MySQL" : "PostgreSQL"} · {v.id}
              </option>
            ))}
        </select>
      </label>
      {selected?.version === "mysql.compose-logical-local.v1" ? (
        <>
          <label className="block">
            Restore archive source
            <select
              aria-label="Restore archive source"
              className="block w-full min-w-0 rounded border p-2"
              value={restoreSource}
              disabled={locked}
              onChange={e => {
                if (!locked) {
                  setAck(false);
                  setRestoreSource(e.target.value);
                }
              }}
            >
              <option value="">Local archive on the backup runner</option>
              {restoreSource && !remoteRestore ? <option value={restoreSource} disabled>Selected remote object is no longer eligible</option> : null}
              {restoreTransfers.map(v => <option key={v.id} value={v.id}>Saved remote object · {v.target_id} · upload {v.id}</option>)}
            </select>
          </label>
          <p>Remote import downloads and verifies the recorded upload at its saved destination revision before starting target database replacement. A download failure can retain ownership before any target writer is stopped. Local import requires the archive on the original backup runner.</p>
          {restoreSource && !remoteRestore ? <p role="alert">Refresh history and explicitly choose an eligible remote object before restoring.</p> : null}
        </>
      ) : null}
      </> : null}
      <label className="block">
        Restore target application
        <select
          value={target}
          disabled={locked}
          onChange={(e) => {
            if (!locked) {
              setAck(false);
              setTarget(e.target.value);
              setTargetComponent("");
            }
          }}
        >
          <option value="">
            Select a distinct deployed Compose application
          </option>
          {applications.data?.items
            .filter(
              (r) =>
                r.id !== resource.id &&
                (r.source.kind === "compose" || r.source.kind === "git") &&
                r.project_id === scope.project,
            )
            .map((r) => (
              <option key={r.id} value={r.id}>
                {r.name}
              </option>
            ))}
        </select>
      </label>
      <p>
        Targets are drawn from the first 100 project applications. Git
        candidates must have a committed Compose database, verified after
        selection. The server requires the same authorized destination. Existing
        target tables are replaced; target credentials and uploaded files remain
        unchanged. Import does not restore media or rewrite WordPress URLs.
      </p>
      <label className="block">
        Restore target database component
        <select
          value={targetComponent}
          disabled={locked}
          onChange={(e) => {
            if (!locked) {
              setAck(false);
              setTargetComponent(e.target.value);
            }
          }}
        >
          <option value="">Select a target database component</option>
          {destination?.components
            .filter(matchesArchive)
            .map((c) => (
              <option key={c.component} value={c.component}>
                {c.component} · {c.engine} {c.major_version}
              </option>
            ))}
        </select>
      </label>
      {destination ? <p>Reviewed target deployment {destination.operation_id}, generation {destination.resource_generation}.</p> : null}
      {externalSource ? <ComposeDatabaseExternalImport targetResourceID={target} targetEvidenceRevision={targetEvidenceRevision} scope={scope} owner={owner} blocked={externalBlocked} onBusy={onExternalBusy} onDenied={onDenied} target={chosen && destination?.operation_id && destination.components.some(c => c.component === targetComponent && matchesArchive(c)) ? {resource_id:chosen.id,component:targetComponent,expected_deployed_operation_id:destination.operation_id,expected_resource_generation:destination.resource_generation} : undefined} /> : <>
      <label className="block">
        <input
          type="checkbox"
          checked={ack}
          disabled={locked}
          onChange={(e) => {
            if (!locked) setAck(e.target.checked);
          }}
        />
        Replace target database and interrupt its writers
      </label>
      <p>
        Only target components that were running are stopped and resumed. Failed
        or uncertain import/resume keeps a mutation hold; do not repeat an
        uncertain import. Ambiguous launch, lease loss or unconfirmed writer
        resume requires operator reconciliation; this view cannot release that
        hold or resume writers manually.
      </p>
      <Button
        disabled={
          !ready ||
          !selected ||
          (Boolean(restoreSource) && !remoteRestore) ||
          !chosen ||
          !destination?.operation_id ||
          !destination.components.some(
            (c) => c.component === targetComponent && matchesArchive(c),
          ) ||
          !ack
        }
        onClick={() => {
          if (chosen && destination?.operation_id && selected)
            void submit({
              mode: "restore",
              backup: selected.id,
              input: {
                request_id: databaseRequestID(),
                ...(remoteRestore ? { remote_source: { transfer_id: remoteRestore.id, expected_target_revision: remoteRestore.target_revision } } : {}),
                component_target: {
                  resource_id: chosen.id,
                  component: targetComponent,
                  expected_deployed_operation_id: destination.operation_id,
                  expected_resource_generation: destination.resource_generation,
                  acknowledge_replacement: true,
                },
              },
            });
        }}
      >
        Restore database backup
      </Button>
      </>}
    </>
  );
}
