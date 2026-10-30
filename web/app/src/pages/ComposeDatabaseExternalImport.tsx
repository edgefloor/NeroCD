import { useEffect, useRef, useState } from "react";
import { ApiError } from "@/api/errors";
import { useQuery } from "@tanstack/react-query";
import { hostingBindingQuery } from "@/api/hosting-deployments";
import {
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
  scope: DatabaseScope;
  target?: DatabaseImportTarget;
  targetResourceID?: string;
  targetEvidenceRevision?: number;
  owner: boolean;
  blocked: boolean;
  onBusy: (busy: boolean) => void;
  onDenied: () => void;
};
type Inspection = {
  target: DatabaseImportTarget;
  input: DatabaseImportStageInput;
  stage: DatabaseImportStage;
};
type Attempt =
  | {
      mode: "inspect";
      target: DatabaseImportTarget;
      input: DatabaseImportStageInput;
    }
  | { mode: "import"; input: DatabaseImportInput };
export function ComposeDatabaseExternalImport(props: Props) {
  return (
    <ExternalImportPanel
      key={JSON.stringify([
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
    blocked: blocked || denied || !currentBinding,
    onDenied: retireAccess,
  });
  const stageQuery = useQuery({
    queryKey: ["database-import-stage", scope, inspection?.stage.id],
    enabled: Boolean(inspection) && !denied,
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () =>
          getDatabaseImportStage(
            inspection!.stage.id,
            inspection!.target,
            inspection!.input.s3_source,
            signal,
          ),
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
    Boolean(attempt) ||
    ["queued", "running"].includes(stage?.state ?? "") ||
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
    remote &&
    inspection.input.s3_source.target_id === remote.target_id &&
    inspection.input.s3_source.revision === remote.revision &&
    inspection.input.s3_source.object_key === objectKey,
  );
  const currentSource = Boolean(
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
    remote,
    objectKey,
    currentSource,
    destinations.evidenceRevision,
    stage?.id,
    stage?.state,
    stage?.stage_hash,
    stage?.staged_blob,
    stage?.source_identity,
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
        setInspection({ target: next.target, input: next.input, stage: value });
        setOperation(undefined);
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
  if (denied)
    return <p role="alert">External database import access is unavailable.</p>;
  return (
    <section
      aria-label="External S3 database import"
      className="min-w-0 space-y-3 rounded border p-3"
    >
      <p>
        Inspect a plain or gzip MySQL SQL object from an existing configured S3
        destination. Inspection stages and verifies the exact object without
        stopping target writers. Encoded and decoded SQL are each limited to 10
        GiB. This does not create a backup record.
      </p>
      {!target ? (
        <p role="status">
          Select a distinct deployed MySQL 8 target component before inspecting
          an object.
        </p>
      ) : null}
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
            <option value={`${remote.target_id}:${remote.revision}`} disabled>
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
          Current S3 destination authority is unavailable. Inspection and import
          require current validation.
        </p>
      ) : null}
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
          <p>
            S3 destination {stage.s3_source.target_id} revision{" "}
            {stage.s3_source.revision} · object {stage.s3_source.object_key}
          </p>
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
              <p>
                Object version{" "}
                {stage.source_identity?.version_id ?? "unversioned"} · ETag{" "}
                {stage.source_identity?.etag ?? "unavailable"}
              </p>
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
              Missing or expired staged SQL is never downloaded again silently.
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
