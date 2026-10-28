import { BackupAccessBoundary } from "@/components/hosting/BackupAccessBoundary";
import { useEffect, useRef, useState } from "react";
import { ApiError } from "../api/errors";
import {
  DatabaseBackupTargetInputError,
  saveDatabaseBackupTarget,
  validateDatabaseBackupTarget,
  validBackupEndpoint,
  validDatabaseBackupTargetCredentials,
  type DatabaseBackupTarget,
  type DatabaseBackupTargetInput,
  type RemoteBackupTarget,
} from "../api/hosting-database-backup-targets";
import {
  databaseDenied,
  databaseError,
  databaseRequestID,
  type DatabaseScope,
  type HostingDatabase,
} from "../api/hosting-databases";
import { validateStorageTarget } from "../api/hosting-storage-backup-remote";
import { Button } from "../components/ui/button";
import { useDatabaseBackupDestinations } from "./database-backup-destinations";

type Props = {
  database?: HostingDatabase;
  storage?: { resource: { id: string }; destination_id: string };
  scope: DatabaseScope;
  owner: boolean;
  blocked: boolean;
  onDenied: () => void;
};
export function BackupDestinationSelect({
  label,
  value,
  onChange,
  destinations,
  disabled,
}: {
  label: string;
  value?: RemoteBackupTarget;
  onChange: (v?: RemoteBackupTarget) => void;
  destinations: ReturnType<typeof useDatabaseBackupDestinations>;
  disabled: boolean;
}) {
  const eligible = destinations.eligible;
  const current = value
    ? eligible.find(
        (v) => v.id === value.target_id && v.revision === value.revision,
      )
    : undefined;
  return (
    <label className="block">
      {label}
      <select
        aria-label={label}
        className="block w-full min-w-0 rounded border p-2"
        value={value ? `${value.target_id}:${value.revision}` : ""}
        disabled={disabled}
        onChange={(e) => {
          const target = eligible.find(
            (v) => `${v.id}:${v.revision}` === e.target.value,
          );
          onChange(
            target
              ? { target_id: target.id, revision: target.revision }
              : undefined,
          );
        }}
      >
        <option value="">Local archive only</option>
        {value && !current ? (
          <option value={`${value.target_id}:${value.revision}`} disabled>
            Selected destination requires current validation
          </option>
        ) : null}
        {eligible.map((v) => (
          <option key={v.id} value={`${v.id}:${v.revision}`}>
            {v.name} · revision {v.revision}
          </option>
        ))}
      </select>
      <span className="block text-sm">
        Only enabled destinations validated from this destination's runner at
        the current revision are selectable. ListObjects validation does not
        prove upload permission.
      </span>
      {value && !current ? (
        <span role="alert">
          Refresh destinations, validate the current revision and select it
          again before submitting.
        </span>
      ) : null}
      {destinations.targets.isError ||
      destinations.validations.some((v) => v.isError) ? (
        <span role="alert">
          Remote destination status is unavailable. Local backups remain
          available.
        </span>
      ) : null}
    </label>
  );
}
type Attempt =
  | { mode: "save"; target: string; input: DatabaseBackupTargetInput }
  | {
      mode: "validate";
      target: string;
      input: {
        request_id: string;
        expected_revision: number;
        destination_id: string;
      };
    };
export function DatabaseBackupDestinations(props: Props) {
  return (
    <BackupAccessBoundary
      key={JSON.stringify([
        props.scope.actor,
        props.scope.project,
        props.storage?.resource.id ?? props.database?.resource.id,
      ])}
      onDenied={props.onDenied}
      message="Backup destination access is unavailable."
    >
      {(onDenied) => <DestinationPanel {...props} onDenied={onDenied} />}
    </BackupAccessBoundary>
  );
}
function DestinationPanel(
  props: Parameters<typeof useBackupDestinationEditor>[0],
) {
  const {
    storage,
    database,
    denied,
    owner,
    editing,
    name,
    setName,
    endpoint,
    setEndpoint,
    bucket,
    setBucket,
    region,
    setRegion,
    unavailable,
    s3AccessKey,
    setS3AccessKey,
    secretKey,
    setSecretKey,
    credentials,
    credentialsValid,
    enabled,
    setEnabled,
    stale,
    submit,
    review,
    pending,
    accepted,
    error,
    attempt,
    blocked,
    destinations,
  } = useBackupDestinationEditor(props);

  if (!storage && database?.engine !== "postgresql") return null;
  if (denied)
    return <p role="alert">Backup destination access is unavailable.</p>;
  return (
    <section
      aria-label={storage ? "Backup targets" : "Database backup destinations"}
      className="min-w-0 space-y-3 rounded-xl border p-3"
    >
      <h4 className="font-medium">S3-compatible backup destinations</h4>
      <p>
        Configure project backup targets here, then select a validated target.
        Each backup retains its local archive; remote upload has its own status.
        Validation runs from this destination's runner and checks ListObjects
        reachability, not PutObject permission.
      </p>
      {!owner ? (
        <p>
          Project owners can configure and validate backup destinations. You can
          inspect safe storage settings and transfer status.
        </p>
      ) : (
        <BackupDestinationEditor
          editing={editing}
          name={name}
          setName={setName}
          endpoint={endpoint}
          setEndpoint={setEndpoint}
          bucket={bucket}
          setBucket={setBucket}
          region={region}
          setRegion={setRegion}
          unavailable={unavailable}
          s3AccessKey={s3AccessKey}
          setS3AccessKey={setS3AccessKey}
          secretKey={secretKey}
          setSecretKey={setSecretKey}
          credentials={credentials}
          credentialsValid={credentialsValid}
          enabled={enabled}
          setEnabled={setEnabled}
          stale={stale}
          submit={submit}
          review={review}
        />
      )}
      <DestinationSubmission
        pending={pending}
        accepted={accepted}
        error={error}
        attempt={attempt}
        owner={owner}
        blocked={blocked}
        submit={submit}
      />
      {destinations.targets.isPending ? (
        <p role="status">Loading backup destinations…</p>
      ) : null}
      {destinations.targets.isError ? (
        <p role="alert">
          Backup destinations are unavailable.{" "}
          {databaseError(destinations.targets.error)}
        </p>
      ) : null}
      {destinations.targets.data?.length === 0 ? (
        <p>No remote backup destinations yet.</p>
      ) : null}
      <ul className="space-y-3">
        <DestinationHistory
          destinations={destinations}
          owner={owner}
          unavailable={unavailable}
          review={review}
          storage={storage}
          database={database}
          submit={submit}
        />
      </ul>
      <Button
        type="button"
        variant="outline"
        disabled={blocked || pending || destinations.targets.isFetching}
        onClick={() => void destinations.refresh()}
      >
        Refresh destinations
      </Button>
    </section>
  );
}
function useBackupDestinationEditor(props: Props) {
  const { database, storage, scope, owner, blocked, onDenied } = props;
  const destinations = useDatabaseBackupDestinations(props);
  const [editing, setEditing] = useState<{ id: string; revision: number }>({
    id: "",
    revision: 0,
  });
  const [name, setName] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [bucket, setBucket] = useState("");
  const [region, setRegion] = useState("us-east-1");
  const [enabled, setEnabled] = useState(true);
  const [s3AccessKey, setS3AccessKey] = useState("");
  const [secretKey, setSecretKey] = useState("");
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
  const denied = destinations.denied || databaseDenied(error);
  function retireDestinationDraft() {
    lifetime.current?.abort();
    onDenied();
  }

  const unavailable =
    blocked ||
    pending ||
    Boolean(attempt) ||
    destinations.targets.isPending ||
    destinations.targets.isError ||
    destinations.targets.isFetching;
  const stale =
    Boolean(editing.id) &&
    !destinations.targets.data?.some(
      (v) => v.id === editing.id && v.revision === editing.revision,
    );
  function review(target?: DatabaseBackupTarget) {
    setEditing({ id: target?.id ?? "", revision: target?.revision ?? 0 });
    setName(target?.name ?? "");
    setEndpoint(target?.endpoint ?? "");
    setBucket(target?.bucket ?? "");
    setRegion(target?.region ?? "us-east-1");
    setEnabled(target?.enabled ?? true);
    setS3AccessKey("");
    setSecretKey("");
    setError(undefined);
    setAccepted("");
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
      if (next.mode === "save") {
        const out = await saveDatabaseBackupTarget(
          scope,
          next.target,
          next.input,
          controller.signal,
        );
        if (controller.signal.aborted) return;
        setEditing({ id: out.id, revision: out.revision });
        setS3AccessKey("");
        setSecretKey("");
        setAccepted(
          `Saved destination ${out.name}, revision ${out.revision}. Validate this revision from this destination's runner before selecting it.`,
        );
      } else {
        const out = await (
          storage ? validateStorageTarget : validateDatabaseBackupTarget
        )(scope, next.target, next.input, controller.signal);
        if (controller.signal.aborted) return;
        setAccepted(
          `Accepted destination validation ${out.id}. Wait for its server status.`,
        );
      }
      setAttempt(undefined);
      await destinations.refresh();
    } catch (cause) {
      if (!controller.signal.aborted) {
        setError(cause);
        if (databaseDenied(cause)) retireDestinationDraft();
        if (
          cause instanceof DatabaseBackupTargetInputError ||
          (cause instanceof ApiError && [400, 404, 409].includes(cause.status))
        )
          setAttempt(undefined);
      }
    } finally {
      setPending(false);
    }
  }
  const credentials =
    s3AccessKey || secretKey
      ? { access_key: s3AccessKey, secret_key: secretKey }
      : undefined;
  const credentialsValid = validDatabaseBackupTargetCredentials(
    credentials,
    !editing.id,
  );
  return {
    storage,
    database,
    denied,
    owner,
    editing,
    name,
    setName,
    endpoint,
    setEndpoint,
    bucket,
    setBucket,
    region,
    setRegion,
    unavailable,
    s3AccessKey,
    setS3AccessKey,
    secretKey,
    setSecretKey,
    credentials,
    credentialsValid,
    enabled,
    setEnabled,
    stale,
    submit,
    review,
    pending,
    accepted,
    error,
    attempt,
    blocked,
    destinations,
  };
}

function BackupDestinationEditor({
  editing,
  name,
  setName,
  endpoint,
  setEndpoint,
  bucket,
  setBucket,
  region,
  setRegion,
  unavailable,
  s3AccessKey,
  setS3AccessKey,
  secretKey,
  setSecretKey,
  credentials,
  credentialsValid,
  enabled,
  setEnabled,
  stale,
  submit,
  review,
}: {
  editing: { id: string; revision: number };
  name: string;
  setName: React.Dispatch<React.SetStateAction<string>>;
  endpoint: string;
  setEndpoint: React.Dispatch<React.SetStateAction<string>>;
  bucket: string;
  setBucket: React.Dispatch<React.SetStateAction<string>>;
  region: string;
  setRegion: React.Dispatch<React.SetStateAction<string>>;
  unavailable: boolean;
  s3AccessKey: string;
  setS3AccessKey: React.Dispatch<React.SetStateAction<string>>;
  secretKey: string;
  setSecretKey: React.Dispatch<React.SetStateAction<string>>;
  credentials: { access_key: string; secret_key: string } | undefined;
  credentialsValid: boolean;
  enabled: boolean;
  setEnabled: React.Dispatch<React.SetStateAction<boolean>>;
  stale: boolean;
  submit: (next: Attempt) => Promise<void>;
  review: ReturnType<typeof useBackupDestinationEditor>["review"];
}) {
  return (
    <div className="space-y-2">
      <p>
        {editing.id
          ? `Editing ${editing.id}, reviewed revision ${editing.revision}`
          : "New backup destination"}
      </p>
      {(
        [
          ["Destination name", name, setName],
          ["S3 endpoint", endpoint, setEndpoint],
          ["S3 bucket", bucket, setBucket],
          ["S3 region", region, setRegion],
        ] as const
      ).map(([label, value, update]) => (
        <label className="block" key={label}>
          {label}
          <input
            aria-label={label}
            className="block w-full min-w-0 rounded border p-2"
            value={value}
            maxLength={label === "S3 endpoint" ? 2048 : 128}
            disabled={unavailable}
            onChange={(e) => update(e.target.value)}
          />
        </label>
      ))}
      <p>
        Use an HTTPS endpoint without a path, URL credentials, query or
        fragment. The server enforces network policy. HTTP is reserved for an
        operator-approved local qualification fixture.
      </p>
      <p>
        Credentials are write-only. Leave both key fields empty while editing to
        keep the existing credentials.
      </p>
      {editing.id ? (
        <p>
          Replacing keys revokes the previous credentials. Older backups cannot
          use the replacement keys, so retrying their upload or restoring them
          remotely may fail. Local backups remain available.
        </p>
      ) : null}
      <label className="block">
        S3 access key
        <input
          type="password"
          autoComplete="new-password"
          aria-label="S3 access key"
          className="block w-full min-w-0 rounded border p-2"
          maxLength={4096}
          value={s3AccessKey}
          disabled={unavailable}
          onChange={(e) => setS3AccessKey(e.target.value)}
        />
      </label>
      <label className="block">
        S3 secret key
        <input
          type="password"
          autoComplete="new-password"
          aria-label="S3 secret key"
          className="block w-full min-w-0 rounded border p-2"
          maxLength={4096}
          value={secretKey}
          disabled={unavailable}
          onChange={(e) => setSecretKey(e.target.value)}
        />
      </label>
      {credentials && !credentialsValid ? (
        <p role="alert">
          Enter both keys without surrounding whitespace or control characters.
        </p>
      ) : null}
      <label className="flex items-center gap-2">
        <input
          type="checkbox"
          aria-label="Destination enabled"
          checked={enabled}
          disabled={unavailable}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        Destination enabled
      </label>
      {stale ? (
        <p role="alert">
          The reviewed destination revision changed. Refresh and explicitly edit
          its current revision before saving.
        </p>
      ) : null}
      <div className="flex flex-wrap gap-2">
        <Button
          type="button"
          className="h-auto max-w-full whitespace-normal"
          disabled={
            unavailable ||
            stale ||
            !name.trim() ||
            !bucket.trim() ||
            !region.trim() ||
            !validBackupEndpoint(endpoint) ||
            !credentialsValid
          }
          onClick={() =>
            void submit({
              mode: "save",
              target: editing.id,
              input: {
                request_id: databaseRequestID(),
                expected_revision: editing.revision,
                name: name.trim(),
                endpoint,
                bucket: bucket.trim(),
                region: region.trim(),
                enabled,
                ...(credentials ? { credentials } : {}),
              },
            })
          }
        >
          {editing.id
            ? "Save destination changes"
            : "Create backup destination"}
        </Button>
        <Button
          type="button"
          variant="outline"
          disabled={unavailable}
          onClick={() => review()}
        >
          New destination
        </Button>
      </div>
    </div>
  );
}

function DestinationHistory({
  destinations,
  owner,
  unavailable,
  review,
  storage,
  database,
  submit,
}: {
  destinations: ReturnType<typeof useBackupDestinationEditor>["destinations"];
  owner: boolean;
  unavailable: boolean;
  review: ReturnType<typeof useBackupDestinationEditor>["review"];
  storage: { resource: { id: string }; destination_id: string } | undefined;
  database: ReturnType<typeof useBackupDestinationEditor>["database"];
  submit: (next: Attempt) => Promise<void>;
}) {
  return (
    <>
      {destinations.targets.data?.map((target, i) => (
        <li
          className="min-w-0 space-y-1 border-t pt-2 break-all"
          key={target.id}
        >
          <p>
            {target.name} · revision {target.revision} ·{" "}
            {target.enabled ? "Enabled" : "Disabled"}
          </p>
          <p>
            {target.endpoint} · {target.bucket} · {target.region}
          </p>
          {owner ? (
            <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={unavailable}
                onClick={() => review(target)}
              >
                Edit destination {target.name}
              </Button>
              <Button
                type="button"
                variant="outline"
                className="h-auto max-w-full whitespace-normal"
                disabled={
                  unavailable ||
                  !target.enabled ||
                  !(
                    storage?.destination_id ?? database?.binding?.destination_id
                  )
                }
                onClick={() =>
                  void submit({
                    mode: "validate",
                    target: target.id,
                    input: {
                      request_id: databaseRequestID(),
                      expected_revision: target.revision,
                      destination_id: (storage?.destination_id ??
                        database?.binding?.destination_id)!,
                    },
                  })
                }
              >
                Validate {target.name} from this runner
              </Button>
            </div>
          ) : null}
          {destinations.validations[i]?.isError ? (
            <p role="alert">Validation history is unavailable.</p>
          ) : null}
          <ul>
            {destinations.validations[i]?.data?.map((v) => (
              <li key={v.id}>
                Validation {v.state} · revision {v.target_revision} · runner
                destination {v.destination_id}
                {v.failure_code ? ` · ${v.failure_code}` : ""}
                {v.state === "succeeded"
                  ? " · ListObjects reachable; upload permission unproven."
                  : ""}
              </li>
            ))}
          </ul>
        </li>
      ))}
    </>
  );
}

function DestinationSubmission({
  pending,
  accepted,
  error,
  attempt,
  owner,
  blocked,
  submit,
}: {
  pending: boolean;
  accepted: string;
  error: unknown;
  attempt: Attempt | undefined;
  owner: boolean;
  blocked: boolean;
  submit: (next: Attempt) => Promise<void>;
}) {
  return (
    <>
      {pending ? (
        <p role="status">Submitting the saved destination request…</p>
      ) : null}
      {accepted ? (
        <p role="status" className="break-all">
          {accepted}
        </p>
      ) : null}
      {error ? (
        <p role="alert">
          {error instanceof DatabaseBackupTargetInputError
            ? "Correct the destination fields. This request was not sent."
            : databaseError(error)}{" "}
          {attempt
            ? `Retained request ${attempt.input.request_id}.`
            : error instanceof DatabaseBackupTargetInputError
              ? ""
              : "Refresh destinations and explicitly review the current revision before correcting a rejected request."}
        </p>
      ) : null}
      {attempt && error && !pending && owner ? (
        <Button
          type="button"
          disabled={blocked}
          onClick={() => void submit(attempt)}
        >
          Retry the same destination request
        </Button>
      ) : null}
    </>
  );
}
