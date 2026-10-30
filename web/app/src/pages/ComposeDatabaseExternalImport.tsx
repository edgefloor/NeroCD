import { useEffect, useRef, useState } from "react";
import { ApiError } from "@/api/errors";
import { useQuery } from "@tanstack/react-query";
import { hostingBindingQuery } from "@/api/hosting-deployments";
import {
  admitDatabaseImportUpload,
  getDatabaseImportUploadStage,
  sendDatabaseImportFile,
  validImportFile,
  type DatabaseImportUploadInput,
  type DatabaseImportUploadStage,
  getDatabaseImport,
  getDatabaseImportStage,
  inspectDatabaseImport,
  requestDatabaseImport,
  validImportObjectKey,
  type DatabaseImportInput,
  type DatabaseImportOperation,
  type DatabaseImportStage,
  type DatabaseImportStageInput,
  type DatabaseImportTarget,
} from "@/api/hosting-database-imports";
import type { RemoteBackupTarget } from "@/api/hosting-database-backup-targets";
import {
  databaseDenied,
  databaseError,
  databaseRequestID,
  type DatabaseScope,
} from "@/api/hosting-databases";
import { readBackupMetadata } from "@/components/hosting/backup-access";
import { Button } from "@/components/ui/button";
import {
  useDatabaseBackupDestinations,
  validSelectedBackupDestination,
} from "./database-backup-destinations";

type Props = {
  sourceKind?: "s3" | "upload";
  scope: DatabaseScope;
  target?: DatabaseImportTarget;
  targetResourceID?: string;
  targetEvidenceRevision?: number;
  owner: boolean;
  blocked: boolean;
  onBusy: (busy: boolean) => void;
  onDenied: () => void;
};
type Inspection =
  | {
      kind: "s3";
      target: DatabaseImportTarget;
      input: DatabaseImportStageInput;
      stage: DatabaseImportStage;
    }
  | {
      kind: "upload";
      target: DatabaseImportTarget;
      input: DatabaseImportUploadInput;
      file: File;
      stage: DatabaseImportUploadStage;
    };
type Attempt =
  | {
      mode: "inspect";
      target: DatabaseImportTarget;
      input: DatabaseImportStageInput;
    }
  | {
      mode: "admit_upload";
      target: DatabaseImportTarget;
      input: DatabaseImportUploadInput;
      file: File;
    }
  | { mode: "import"; input: DatabaseImportInput };
export function ComposeDatabaseExternalImport(props: Props) {
  return (
    <ExternalImportPanel
      key={JSON.stringify([
        props.sourceKind ?? "s3",
        props.scope.actor,
        props.scope.project,
        props.scope.environment,
        props.targetResourceID ?? props.target?.resource_id,
      ])}
      {...props}
    />
  );
}
function ExternalImportPanel({
  sourceKind = "s3",
  scope,
  target,
  targetEvidenceRevision,
  owner,
  blocked,
  onBusy,
  onDenied,
}: Props) {
  const [remote, setRemote] = useState<RemoteBackupTarget>();
  const [objectKey, setObjectKey] = useState("");
  const [file, setFile] = useState<File>();
  const [fileRevision, setFileRevision] = useState(0);
  const [sending, setSending] = useState(false);
  const [sentBytes, setSentBytes] = useState(0);
  const [bodyError, setBodyError] = useState<unknown>();
  const bodyAttempts = useRef(new Set<string>());
  const bodyController = useRef<AbortController | null>(null);
  const [inspection, setInspection] = useState<Inspection>();
  const [operation, setOperation] = useState<{
    input: DatabaseImportInput;
    value: DatabaseImportOperation;
  }>();
  const [attempt, setAttempt] = useState<Attempt>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  const [denied, setDenied] = useState(false);
  const [confirmation, setConfirmation] = useState({
    identity: "",
    accepted: false,
  });
  const lifetime = useRef<AbortController | null>(null);
  useEffect(() => {
    const c = new AbortController();
    lifetime.current = c;
    return () => {
      c.abort();
      bodyController.current?.abort();
      lifetime.current = null;
    };
  }, []);
  const bindingOptions = hostingBindingQuery(
    scope.project,
    target?.resource_id ?? "",
    scope.actor,
  );
  const binding = useQuery({
    ...bindingOptions,
    enabled: Boolean(target) && !denied,
    retry: false,
    queryFn: async (q) => {
      const value = await readBackupMetadata(
        q.signal,
        () => bindingOptions.queryFn!(q),
        retireAccess,
      );
      if (!q.signal.aborted) setConfirmation({ identity: "", accepted: false });
      return value;
    },
  });
  const currentBinding =
    target &&
    binding.data?.resource_id === target.resource_id &&
    !binding.isFetching &&
    !binding.isError
      ? binding.data
      : undefined;
  const destinations = useDatabaseBackupDestinations({
    scope,
    component: currentBinding
      ? { destination_id: currentBinding.destination_id }
      : undefined,
    owner,
    blocked: blocked || denied || !currentBinding || sourceKind === "upload",
    onDenied: retireAccess,
  });
  const stageQuery = useQuery({
    queryKey: ["database-import-stage", scope, inspection?.stage.id],
    enabled: Boolean(inspection) && !denied,
    queryFn: async ({ signal }) =>
      readBackupMetadata(
        signal,
        () => {
          const current = inspection!;
          return current.kind === "upload"
            ? getDatabaseImportUploadStage(
                current.stage.id,
                current.target,
                {
                  ...current.stage.upload_source,
                  upload_hash: current.stage.upload_hash,
                },
                signal,
              )
            : getDatabaseImportStage(
                current.stage.id,
                current.target,
                current.input.s3_source,
                signal,
              );
        },
        retireAccess,
      ),
    retry: false,
    refetchInterval: (q) =>
      ["queued", "running"].includes(
        q.state.data?.state ?? inspection?.stage.state ?? "",
      )
        ? 1500
        : false,
  });
  const stage = stageQuery.data ?? inspection?.stage;
  const importQuery = useQuery({
    queryKey: ["database-import", scope, operation?.value.id],
    enabled: Boolean(operation) && !denied,
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () => getDatabaseImport(operation!.value.id, operation!.input, signal),
        retireAccess,
      ),
    retry: false,
    refetchInterval: (q) =>
      ["queued", "running"].includes(
        q.state.data?.state ?? operation?.value.state ?? "",
      )
        ? 1500
        : false,
  });
  const imported = importQuery.data ?? operation?.value;
  const busy =
    pending ||
    sending ||
    Boolean(attempt) ||
    ((sourceKind === "s3" ||
      Boolean(stage && bodyAttempts.current.has(stage.id))) &&
      ["queued", "running"].includes(stage?.state ?? "")) ||
    Boolean(
      imported &&
      (["queued", "running", "uncertain"].includes(imported.state) ||
        imported.ownership_held),
    );
  useEffect(() => {
    onBusy(busy);
    return () => onBusy(false);
  }, [busy, onBusy]);
  const sameTarget = Boolean(
    inspection &&
    target &&
    JSON.stringify(inspection.target) === JSON.stringify(target),
  );
  const sameSource = Boolean(
    inspection &&
    (inspection.kind === "upload"
      ? sourceKind === "upload" && file === inspection.file
      : remote &&
        inspection.input.s3_source.target_id === remote.target_id &&
        inspection.input.s3_source.revision === remote.revision &&
        inspection.input.s3_source.object_key === objectKey),
  );
  const currentSource =
    sourceKind === "upload"
      ? Boolean(file && validImportFile(file) && currentBinding)
      : Boolean(
          remote &&
          currentBinding &&
          !destinations.denied &&
          validSelectedBackupDestination(remote, destinations),
        );
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (stage?.state !== "ready") return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [stage?.state, stage?.expires_at]);
  const expired = stage?.expires_at
    ? Date.parse(stage.expires_at) <= now
    : false;
  const identity = JSON.stringify([
    target,
    targetEvidenceRevision,
    sourceKind,
    fileRevision,
    remote,
    objectKey,
    currentSource,
    sourceKind === "s3" ? destinations.evidenceRevision : undefined,
    stage?.id,
    stage?.state,
    stage?.stage_hash,
    stage?.staged_blob,
    stage?.version === "mysql.compose-external-s3-stage.v1"
      ? stage.source_identity
      : undefined,
    stage?.source_bytes,
    stage?.expires_at,
    expired,
  ]);
  if (!attempt && confirmation.identity !== identity)
    setConfirmation({ identity, accepted: false });
  const ack = confirmation.identity === identity && confirmation.accepted;
  const ready =
    !blocked &&
    owner &&
    !busy &&
    sameTarget &&
    sameSource &&
    currentSource &&
    stage?.state === "ready" &&
    !expired &&
    !stageQuery.isFetching &&
    !stageQuery.isError &&
    !operation;
  function retireAccess() {
    setDenied(true);
    lifetime.current?.abort();
    setAttempt(undefined);
    setInspection(undefined);
    setOperation(undefined);
    setRemote(undefined);
    setFile(undefined);
    bodyController.current?.abort();
    setConfirmation({ identity: "", accepted: false });
    onDenied();
  }
  async function submit(next: Attempt) {
    const c = lifetime.current;
    if (
      !owner ||
      blocked ||
      denied ||
      pending ||
      !c ||
      c.signal.aborted ||
      (attempt && attempt !== next)
    )
      return;
    setAttempt(next);
    setPending(true);
    setError(undefined);
    try {
      if (next.mode === "inspect") {
        const value = await inspectDatabaseImport(
          next.target.resource_id,
          next.input,
          c.signal,
        );
        if (c.signal.aborted) return;
        setInspection({
          kind: "s3",
          target: next.target,
          input: next.input,
          stage: value,
        });
        setOperation(undefined);
      } else if (next.mode === "admit_upload") {
        const value = await admitDatabaseImportUpload(
          next.target.resource_id,
          next.input,
          c.signal,
        );
        if (c.signal.aborted) return;
        setInspection({
          kind: "upload",
          target: next.target,
          input: next.input,
          file: next.file,
          stage: value,
        });
        setOperation(undefined);
        setBodyError(undefined);
        setSentBytes(0);
      } else {
        const value = await requestDatabaseImport(next.input, c.signal);
        if (c.signal.aborted) return;
        setOperation({ input: next.input, value });
      }
      setAttempt(undefined);
      setConfirmation({ identity: "", accepted: false });
    } catch (cause) {
      if (!c.signal.aborted) {
        if (databaseDenied(cause)) retireAccess();
        else {
          if (
            cause instanceof ApiError &&
            [400, 404, 409].includes(cause.status)
          )
            setAttempt(undefined);
          setError(cause);
        }
      }
    } finally {
      if (!c.signal.aborted) setPending(false);
    }
  }
  async function sendFile() {
    const c = lifetime.current;
    if (
      !owner ||
      blocked ||
      denied ||
      pending ||
      sending ||
      !c ||
      c.signal.aborted ||
      !sameTarget ||
      !sameSource ||
      !currentSource ||
      inspection?.kind !== "upload" ||
      stage?.version !== "mysql.compose-local-upload-stage.v1" ||
      bodyAttempts.current.has(stage.id) ||
      !["queued", "running"].includes(stage.state)
    )
      return;
    bodyAttempts.current.add(stage.id);
    const transfer = new AbortController();
    bodyController.current = transfer;
    const retire = () => transfer.abort();
    c.signal.addEventListener("abort", retire, { once: true });
    setSending(true);
    setSentBytes(0);
    setBodyError(undefined);
    setConfirmation({ identity: "", accepted: false });
    try {
      await sendDatabaseImportFile(
        stage,
        inspection.file,
        transfer.signal,
        (sent) => {
          if (!c.signal.aborted) setSentBytes(sent);
        },
      );
    } catch (cause) {
      if (!c.signal.aborted) {
        if (databaseDenied(cause)) retireAccess();
        else setBodyError(cause);
      }
    } finally {
      c.signal.removeEventListener("abort", retire);
      if (!c.signal.aborted) {
        setSending(false);
        void stageQuery.refetch();
      }
      if (bodyController.current === transfer) bodyController.current = null;
    }
  }
  if (denied)
    return <p role="alert">External database import access is unavailable.</p>;
  return (
    <section
      aria-label={
        sourceKind === "upload"
          ? "Local SQL file import"
          : "External S3 database import"
      }
      className="min-w-0 space-y-3 rounded border p-3"
    >
      <p>
        {sourceKind === "upload"
          ? "Select a local plain or gzip MySQL SQL file. Admission records the intended target before one native file transfer; only a ready inspection confirms persisted and verified SQL."
          : "Inspect a plain or gzip MySQL SQL object from an existing configured S3 destination."}{" "}
        Inspection never stops target writers. Encoded and decoded SQL are each
        limited to 10 GiB. This does not create a backup record.
      </p>
      {!target ? (
        <p role="status">
          Select a distinct deployed MySQL 8 target component before inspecting
          SQL from the selected source.
        </p>
      ) : null}
      {sourceKind === "upload" ? (
        <>
          <label className="block">
            Local SQL file
            <input
              type="file"
              className="block w-full min-w-0"
              disabled={!owner || blocked || busy}
              onChange={(event) => {
                const next = event.currentTarget.files?.[0];
                event.currentTarget.value = "";
                if (!busy) {
                  setFile(next);
                  setFileRevision((revision) => revision + 1);
                  setConfirmation({ identity: "", accepted: false });
                }
              }}
            />
          </label>
          {file ? (
            <p className="break-all">
              Selected file {file.name} · {file.size} bytes
            </p>
          ) : null}
          {file && !validImportFile(file) ? (
            <p role="alert">
              Choose a nonempty file up to 10 GiB with a filename of 1–255 UTF-8
              bytes and no controls or path separators. Plain or gzip content is
              verified after transfer.
            </p>
          ) : null}
          <Button
            className="h-auto max-w-full whitespace-normal"
            disabled={!owner || blocked || busy || !target || !currentSource}
            onClick={() => {
              if (target && file)
                void submit({
                  mode: "admit_upload",
                  target: { ...target },
                  file,
                  input: {
                    request_id: databaseRequestID(),
                    component: "mysql",
                    expected_deployed_operation_id:
                      target.expected_deployed_operation_id,
                    expected_resource_generation:
                      target.expected_resource_generation,
                    filename: file.name,
                    source_bytes: file.size,
                  },
                });
            }}
          >
            Prepare local file upload
          </Button>
          <Button
            className="h-auto max-w-full whitespace-normal"
            disabled={
              !owner ||
              blocked ||
              busy ||
              !sameTarget ||
              !sameSource ||
              !currentSource ||
              stage?.version !== "mysql.compose-local-upload-stage.v1" ||
              bodyAttempts.current.has(stage.id) ||
              !["queued", "running"].includes(stage.state) ||
              Date.parse(stage.upload_deadline) <= Date.now()
            }
            onClick={() => void sendFile()}
          >
            Send file for inspection
          </Button>
          {sending ? (
            <>
              <p role="status">
                Sending file bytes: {sentBytes} of {file?.size}. This does not
                confirm persisted or inspected bytes.
              </p>
              <Button
                className="h-auto max-w-full whitespace-normal"
                variant="outline"
                onClick={() => bodyController.current?.abort()}
              >
                Stop sending file
              </Button>
            </>
          ) : stage &&
            bodyAttempts.current.has(stage.id) &&
            ["queued", "running"].includes(stage.state) ? (
            <p role="status">
              File transfer attempted. Waiting for authoritative inspection
              status; the file will not be sent again automatically.
            </p>
          ) : null}
          {bodyError ? (
            <p role="alert">
              {bodyError instanceof ApiError && bodyError.status === 412
                ? "File upload admission was rejected because its receipt no longer matches. The body will not be sent again; prepare a fresh upload."
                : "File transfer result requires inspection status review. Refresh this stage; do not resend the body. A failed or interrupted stage requires a fresh upload."}
            </p>
          ) : null}
          <p>
            File content is streamed once without loading it all into
            JavaScript. Resumable chunks and server paths are unavailable.
          </p>
        </>
      ) : (
        <>
          <label className="block">
            S3 import destination
            <select
              aria-label="S3 import destination"
              className="block w-full min-w-0 rounded border p-2"
              value={remote ? `${remote.target_id}:${remote.revision}` : ""}
              disabled={!owner || blocked || busy || !currentBinding}
              onChange={(e) => {
                if (!busy) {
                  const value = destinations.eligible.find(
                    (t) => `${t.id}:${t.revision}` === e.target.value,
                  );
                  setRemote(
                    value
                      ? { target_id: value.id, revision: value.revision }
                      : undefined,
                  );
                  setConfirmation({ identity: "", accepted: false });
                }
              }}
            >
              <option value="">Select a configured S3 destination</option>
              {remote && !currentSource ? (
                <option
                  value={`${remote.target_id}:${remote.revision}`}
                  disabled
                >
                  Selected destination requires current validation
                </option>
              ) : null}
              {destinations.eligible.map((t) => (
                <option key={t.id} value={`${t.id}:${t.revision}`}>
                  {t.name} · revision {t.revision}
                </option>
              ))}
            </select>
          </label>
          <label className="block">
            S3 object key
            <input
              className="block w-full min-w-0 rounded border p-2"
              value={objectKey}
              disabled={!owner || blocked || busy}
              maxLength={1024}
              onChange={(e) => {
                if (!busy) {
                  setObjectKey(e.target.value);
                  setConfirmation({ identity: "", accepted: false });
                }
              }}
            />
          </label>
          <p>
            Enter the exact key inside the configured bucket. The configured
            destination supplies the bucket and endpoint.
          </p>
          <Button
            className="h-auto max-w-full whitespace-normal"
            disabled={
              !owner ||
              blocked ||
              busy ||
              !target ||
              !currentSource ||
              !validImportObjectKey(objectKey)
            }
            onClick={() => {
              if (target && remote)
                void submit({
                  mode: "inspect",
                  target: { ...target },
                  input: {
                    request_id: databaseRequestID(),
                    component: "mysql",
                    expected_deployed_operation_id:
                      target.expected_deployed_operation_id,
                    expected_resource_generation:
                      target.expected_resource_generation,
                    s3_source: { ...remote, object_key: objectKey },
                  },
                });
            }}
          >
            Inspect S3 object
          </Button>
          <Button
            className="h-auto max-w-full whitespace-normal"
            variant="outline"
            disabled={blocked || busy || destinations.targets.isFetching}
            onClick={() => {
              setConfirmation({ identity: "", accepted: false });
              void binding.refetch();
              void destinations.refresh();
            }}
          >
            Refresh S3 import destinations
          </Button>
          {binding.isError ||
          destinations.targets.isError ||
          destinations.validations.some((v) => v.isError) ? (
            <p role="alert">
              Current S3 destination authority is unavailable. Inspection and
              import require current validation.
            </p>
          ) : null}
        </>
      )}
      {stage ? (
        <div
          className="min-w-0 space-y-2 break-all"
          role="region"
          aria-label="Inspected SQL object"
        >
          <p role="status">
            Inspection {stage.id} · {stage.state}
            {stage.failure_code ? ` · ${stage.failure_code}` : ""}
          </p>
          {stage.version === "mysql.compose-local-upload-stage.v1" ? (
            <p>
              Local file {stage.upload_source.filename} · admitted bytes{" "}
              {stage.upload_source.bytes} · upload deadline{" "}
              {stage.upload_deadline}
            </p>
          ) : (
            <p>
              S3 destination {stage.s3_source.target_id} revision{" "}
              {stage.s3_source.revision} · object {stage.s3_source.object_key}
            </p>
          )}
          <p>
            Intended target {stage.target_resource_id} · component{" "}
            {stage.component} · deployment{" "}
            {stage.expected_deployed_operation_id} · generation{" "}
            {stage.expected_resource_generation}
          </p>
          {stage.staged_blob ? (
            <>
              <p>
                Source bytes {stage.source_bytes} · compression{" "}
                {stage.compression}. Verified SQL bytes{" "}
                {stage.staged_blob.bytes} · digest {stage.staged_blob.digest}
              </p>
              {stage.version === "mysql.compose-external-s3-stage.v1" ? (
                <p>
                  Object version{" "}
                  {stage.source_identity?.version_id ?? "unversioned"} · ETag{" "}
                  {stage.source_identity?.etag ?? "unavailable"}
                </p>
              ) : null}
              <p>
                Stage hash {stage.stage_hash} · expires {stage.expires_at}
              </p>
            </>
          ) : null}
          {!sameTarget ||
          !sameSource ||
          expired ||
          ["failed", "expired", "consumed"].includes(stage.state) ? (
            <p role="alert">
              Fresh inspection is required before importing this selection.
              Missing or expired staged SQL requires fresh inspection or a fresh
              local upload.
            </p>
          ) : null}
          <Button
            className="h-auto max-w-full whitespace-normal"
            variant="outline"
            disabled={stageQuery.isFetching}
            onClick={() => void stageQuery.refetch()}
          >
            Refresh inspection status
          </Button>
        </div>
      ) : null}
      {stageQuery.isError ? (
        <p role="alert">{databaseError(stageQuery.error)}</p>
      ) : null}
      <label className="block">
        <input
          type="checkbox"
          checked={ack}
          disabled={!ready}
          onChange={(e) =>
            setConfirmation({ identity, accepted: e.target.checked })
          }
        />
        Replace target database with this inspected SQL and interrupt its
        writers
      </label>
      <p>
        Import replaces only the selected target database using its application
        database account. Uploaded files and target credentials remain
        unchanged. Uncertain import or unconfirmed writer resume retains
        ownership and requires reconciliation.
      </p>
      <Button
        className="h-auto max-w-full whitespace-normal"
        disabled={!ready || !ack}
        onClick={() => {
          if (target && stage?.stage_hash)
            void submit({
              mode: "import",
              input: {
                request_id: databaseRequestID(),
                stage_id: stage.id,
                expected_stage_hash: stage.stage_hash,
                component_target: { ...target, acknowledge_replacement: true },
              },
            });
        }}
      >
        Import inspected SQL
      </Button>
      {pending ? (
        <p role="status">Submitting the saved external import request…</p>
      ) : null}
      {error ? <p role="alert">{databaseError(error)}</p> : null}
      {attempt && error && !pending ? (
        <>
          <p>
            Unconfirmed {attempt.mode} request {attempt.input.request_id}. Retry
            preserves the exact source and target authority.
          </p>
          <Button
            className="h-auto max-w-full whitespace-normal"
            disabled={!owner || blocked}
            onClick={() => void submit(attempt)}
          >
            Retry exact external import request
          </Button>
        </>
      ) : null}
      {imported ? (
        <div className="break-all">
          <p role="status">
            External import {imported.id} · {imported.state}
            {imported.failure_code ? ` · ${imported.failure_code}` : ""}.
            Mutation ownership{" "}
            {imported.ownership_held ? "retained" : "released"}. Writer resume{" "}
            {imported.writers_resumed ? "confirmed" : "not confirmed"}.
          </p>
          <Button
            className="h-auto max-w-full whitespace-normal"
            variant="outline"
            disabled={importQuery.isFetching}
            onClick={() => void importQuery.refetch()}
          >
            Refresh external import status
          </Button>
        </div>
      ) : null}
      {importQuery.isError ? (
        <p role="alert">{databaseError(importQuery.error)}</p>
      ) : null}
    </section>
  );
}
