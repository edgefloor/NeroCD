import { listResources, type Resource } from "../api/hosting";
import { useDatabaseAccess } from "./ResourceDatabase";
import type { BackupRecoveryLocator } from "../api/hosting-backup-notifications";
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
import { DatabaseBackupRetention } from "./DatabaseBackupRetention";
import { ComposeDatabaseBackupSchedules } from "./ComposeDatabaseBackupSchedules";
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
  getDatabaseBackupComponents,
  databaseBackupsKey,
  listDatabaseBackups,
  requestDatabaseBackup,
  restoreDatabaseBackup,
  type DatabaseBackup,
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
function DatabaseBackupsPanel({
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
  // The authenticated exact-ID lookup can resolve a retained backup outside
  // the latest 100 operations. Prefer fresh list metadata when it is present.
  const records = recoveryBackup && !history.data?.some(v => v.id === recoveryBackup.id)
    ? [recoveryBackup, ...(history.data ?? [])]
    : history.data ?? [];
  const transfers = useQueries({
    queries: records
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
  const uploads = recoveryTransfer && !recentUploads.some(v => v.id === recoveryTransfer.id)
    ? [recoveryTransfer, ...recentUploads]
    : recentUploads;
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
    records.filter(
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
      {history.data && records.length === 0 ? (
        <p>No logical backup or restore requests yet.</p>
      ) : null}
      <ul className="space-y-3">
        {records.map((v) => (
          <li key={v.id} aria-label={recovery?.backup_id === v.id ? "Linked backup history" : undefined} className="min-w-0 space-y-1 border-t pt-2 break-all">
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
              <li key={v.id} aria-label={recovery?.transfer_id === v.id ? "Linked upload history" : undefined}>
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


// Compose databases keep their owning application identity; they are never
// represented as fabricated standalone database resources.
export function ResourceComposeDatabaseBackups({resource,blocked}: {resource: Resource; blocked: boolean}) {
 const access = useDatabaseAccess(resource.project_id,resource.project_environment_id);
 if (resource.source.kind !== "compose") return null;
 if (blocked || !access.ready || access.refreshing || !access.viewer) return <p role="status">Component database backup access is pending or unavailable.</p>;
 return <ComposeDatabaseBackups key={JSON.stringify([access.scope,resource.id,resource.generation])} resource={resource} scope={access.scope} owner={access.owner} />;
}
function ComposeDatabaseBackups({resource,scope,owner}:{resource:Resource;scope:DatabaseScope;owner:boolean}) {
 const client=useQueryClient();
 const context={resource};
 const [component,setComponent]=useState("");
 const [backup,setBackup]=useState("");
 const [target,setTarget]=useState("");
 const [targetComponent,setTargetComponent]=useState("");
 const [ack,setAck]=useState(false);
 const [pending,setPending]=useState(false);
 const [error,setError]=useState<unknown>();
 const [denied,setDenied]=useState(false);
 const [attempt,setAttempt]=useState<Attempt>();
 const lifetime=useRef<AbortController|null>(null);
 useEffect(()=>{const c=new AbortController();lifetime.current=c;return()=>{c.abort();lifetime.current=null}},[]);
 const targets=useQuery({queryKey:["database-backup-components",scope,resource.id],queryFn:({signal})=>getDatabaseBackupComponents(scope,resource.id,signal),retry:false,enabled:!denied});
 const history=useQuery({queryKey:databaseBackupsKey(scope,resource.id),queryFn:({signal})=>listDatabaseBackups(scope,context,signal),retry:false,enabled:!denied,refetchInterval:q=>q.state.data?.some(v=>v.ownership_held && !["uncertain","failed"].includes(v.state))?3000:false});
 const applications=useQuery({queryKey:["database-import-applications",scope],queryFn:({signal})=>listResources({project_id:scope.project,limit:100,offset:0},{signal}),retry:false,enabled:!denied});
 const chosen=applications.data?.items.find(r=>r.id===target && r.id!==resource.id && r.source.kind==="compose" && r.project_id===scope.project);
 const targetEvidence=useQuery({queryKey:["database-backup-components",scope,chosen?.id],queryFn:({signal})=>getDatabaseBackupComponents(scope,chosen!.id,signal),retry:false,enabled:Boolean(chosen)&&!denied});
 const revoked=databaseDenied(error)||databaseDenied(targets.error)||databaseDenied(history.error)||databaseDenied(applications.error)||databaseDenied(targetEvidence.error);
 useEffect(()=>{if(revoked){setDenied(true);lifetime.current?.abort();setAttempt(undefined);client.removeQueries({queryKey:databaseBackupsKey(scope,resource.id)});}},[revoked]);
 useEffect(()=>{setAck(false)},[component,backup,target,targetComponent]);
 if(denied||revoked) return <p role="alert">Component database backup access is unavailable.</p>;
 const source=targets.isSuccess&&!targets.isFetching&&targets.data.available?targets.data:undefined;
 const destination=targetEvidence.isSuccess&&!targetEvidence.isFetching&&targetEvidence.data.available?targetEvidence.data:undefined;
 const locked=pending||Boolean(attempt);
 const ready=owner&&!locked&&!history.isPending&&!history.isError&&!history.isFetching;
 const selected=history.data?.find(v=>v.id===backup&&v.mode==="backup"&&v.state==="succeeded"&&v.blob&&v.version==="postgresql.compose-logical-local.v1");
 async function submit(next:Attempt){
  const c=lifetime.current;if(!owner||pending||(attempt&&next!==attempt)||!c||c.signal.aborted)return;
  setPending(true);setError(undefined);setAttempt(next);
  try{
   if(next.mode==="backup")await requestDatabaseBackup(scope,context,next.input,c.signal);
   else await restoreDatabaseBackup(scope,context,next.backup,next.input,c.signal);
   if(c.signal.aborted)return;setAttempt(undefined);await history.refetch();
  }catch(cause){if(!c.signal.aborted){setError(cause);if(cause instanceof ApiError&&[400,404,409].includes(cause.status))setAttempt(undefined)}}finally{if(!c.signal.aborted)setPending(false)}
 }
 return <section aria-label="Component database backups" className="min-w-0 space-y-3 rounded-xl border p-3">
  <h5 className="font-medium">Compose PostgreSQL logical backups</h5>
  <p>Qualified official PostgreSQL16 components only. Archives contain database records, not uploaded files or volume contents. The runner verifies the live profile before execution. Source writes may continue during backup.</p>
  <label className="block">Database backup component<select value={component} disabled={locked} onChange={e=>{if(!locked)setComponent(e.target.value)}}><option value="">Select a database component</option>{source?.components.filter(c=>c.eligible).map(c=><option key={c.component} value={c.component}>{c.component}</option>)}</select></label>
  <Button disabled={!ready||!source?.operation_id||!source.components.some(c=>c.component===component&&c.eligible)} onClick={()=>{if(source?.operation_id)void submit({mode:"backup",input:{request_id:databaseRequestID(),expected_resource_generation:source.resource_generation,component,expected_deployed_operation_id:source.operation_id}})}}>Create logical backup</Button>
  <Button variant="outline" disabled={locked||targets.isFetching} onClick={()=>{if(!locked){setComponent("");void targets.refetch();}}}>Refresh database components</Button>
  {!source?<p role="status">A settled supported database component is unavailable. Refresh after deployment settles.</p>:null}
  <label className="block">Backup to restore<select value={backup} disabled={locked} onChange={e=>{if(!locked)setBackup(e.target.value)}}><option value="">Select a completed backup</option>{history.data?.filter(v=>v.mode==="backup"&&v.state==="succeeded"&&v.blob&&v.version==="postgresql.compose-logical-local.v1").map(v=><option key={v.id} value={v.id}>{v.source_component} · {v.id}</option>)}</select></label>
  <label className="block">Restore target application<select value={target} disabled={locked} onChange={e=>{if(!locked){setTarget(e.target.value);setTargetComponent("")}}}><option value="">Select a distinct deployed Compose application</option>{applications.data?.items.filter(r=>r.id!==resource.id&&r.source.kind==="compose"&&r.project_id===scope.project).map(r=><option key={r.id} value={r.id}>{r.name}</option>)}</select></label>
  <p>Targets are drawn from the first 100 project applications. The server requires the same authorized destination. Existing target tables are replaced; target credentials and uploaded files remain unchanged.</p>
  <label className="block">Restore target database component<select value={targetComponent} disabled={locked} onChange={e=>{if(!locked)setTargetComponent(e.target.value)}}><option value="">Select a target database component</option>{destination?.components.filter(c=>c.eligible).map(c=><option key={c.component} value={c.component}>{c.component}</option>)}</select></label>
  <label className="block"><input type="checkbox" checked={ack} disabled={locked} onChange={e=>{if(!locked)setAck(e.target.checked)}}/>Replace target database and interrupt its writers</label>
  <p>Only target components that were running are stopped and resumed. Failed or uncertain import/resume keeps a mutation hold; do not repeat an uncertain import. Ambiguous launch, lease loss or unconfirmed writer resume requires operator reconciliation; this view cannot release that hold or resume writers manually.</p>
  <Button disabled={!ready||!selected||!chosen||!destination?.operation_id||!destination.components.some(c=>c.component===targetComponent&&c.eligible)||!ack} onClick={()=>{if(chosen&&destination?.operation_id&&selected)void submit({mode:"restore",backup:selected.id,input:{request_id:databaseRequestID(),component_target:{resource_id:chosen.id,component:targetComponent,expected_deployed_operation_id:destination.operation_id,expected_resource_generation:destination.resource_generation,acknowledge_replacement:true}}})}}>Restore database backup</Button>
  <Button variant="outline" disabled={locked||history.isFetching} onClick={()=>{if(!locked)void history.refetch()}}>Refresh database backup history</Button>
  {history.data?.map(v=><p key={v.id} role="status">{v.id} · {v.mode} · {v.state}{v.ownership_held?" · mutation ownership retained":""}{v.failure_code?` · ${v.failure_code}`:""}</p>)}
  {error?<p role="alert">{databaseError(error)}</p>:null}
  {error&&attempt?<p role="status">The response is unconfirmed. Request {attempt.input.request_id} is retained; retry the identical request before changing selections or starting another database action.</p>:null}
  {error&&attempt?<Button disabled={pending} onClick={()=>void submit(attempt)}>Retry exact database request</Button>:null}
  <ComposeDatabaseBackupSchedules resource={resource} scope={scope} owner={owner} onDenied={()=>setDenied(true)} />
 </section>;
}
