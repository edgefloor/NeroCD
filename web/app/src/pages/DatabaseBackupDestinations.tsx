import { useEffect, useRef, useState } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
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
  DatabaseBackupTargetInputError,
  validDatabaseBackupTargetCredentials,
  databaseBackupTargetsKey,
  databaseBackupValidationsKey,
  listDatabaseBackupTargets,
  listDatabaseBackupValidations,
  saveDatabaseBackupTarget,
  validateDatabaseBackupTarget,
  validBackupEndpoint,
  type DatabaseBackupTransfer,
  type DatabaseBackupTarget,
  type DatabaseBackupTargetInput,
  type RemoteBackupTarget,
} from "../api/hosting-database-backup-targets";

type Props = {
  database: HostingDatabase;
  scope: DatabaseScope;
  owner: boolean;
  blocked: boolean;
  onDenied: () => void;
};
export function useDatabaseBackupDestinations({
  database,
  scope,
  blocked,
  onDenied,
}: Props) {
  const client = useQueryClient();
  const [accessRevoked, setAccessRevoked] = useState(false);
  const targets = useQuery({
    queryKey: databaseBackupTargetsKey(scope),
    queryFn: ({ signal }) => listDatabaseBackupTargets(scope, signal),
    enabled: !blocked && !accessRevoked && database.engine === "postgresql",
    retry: false,
  });
  const validations = useQueries({
    queries: (targets.data ?? []).map((target) => ({
      queryKey: databaseBackupValidationsKey(scope, target.id),
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        listDatabaseBackupValidations(scope, target.id, signal),
      enabled: !blocked && !accessRevoked,
      retry: false,
      refetchInterval: (query: {
        state: {
          data?: DatabaseBackupTransfer[];
        };
      }) =>
        query.state.data?.some((v) =>
          ["queued", "dispatched", "applying"].includes(v.state),
        )
          ? 3000
          : false,
    })),
  });
  const denied =
    accessRevoked ||
    databaseDenied(targets.error) ||
    validations.some((v) => databaseDenied(v.error));
  useEffect(() => {
    if (denied) {
      setAccessRevoked(true);
      client.removeQueries({ queryKey: databaseBackupTargetsKey(scope) });
      onDenied();
    }
  }, [denied]);
  const eligible = (targets.data ?? []).filter(
    (target, i) =>
      target.enabled &&
      !validations[i]?.isError &&
      validations[i]?.data?.some(
        (v) =>
          v.mode === "validate" &&
          v.state === "succeeded" &&
          v.target_revision === target.revision &&
          v.destination_id === database.binding?.destination_id,
      ),
  );
  return {
    targets,
    validations,
    eligible,
    denied,
    refresh: async () => {
      await targets.refetch();
      await Promise.all(validations.map((v) => v.refetch()));
    },
  };
}
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
        Only enabled destinations validated from this database's runner at the
        current revision are selectable. ListObjects validation does not prove
        upload permission.
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
export function validSelectedBackupDestination(
  value: RemoteBackupTarget | undefined,
  destinations: ReturnType<typeof useDatabaseBackupDestinations>,
) {
  return (
    !value ||
    destinations.eligible.some(
      (v) => v.id === value.target_id && v.revision === value.revision,
    )
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
    <DestinationPanel
      key={JSON.stringify([
        props.scope.actor,
        props.scope.project,
        props.database.resource.id,
      ])}
      {...props}
    />
  );
}
function DestinationPanel(props: Props) {
  const { database, scope, owner, blocked, onDenied } = props;
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
  const [accessKey, setAccessKey] = useState("");
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
  useEffect(() => {
    if (denied) {
      lifetime.current?.abort();
      setAccessKey("");
      setSecretKey("");
      setAttempt(undefined);
      onDenied();
    }
  }, [denied]);
  if (database.engine !== "postgresql") return null;
  if (denied)
    return <p role="alert">Backup destination access is unavailable.</p>;
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
    setAccessKey("");
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
        setAccessKey("");
        setSecretKey("");
        setAccepted(
          `Saved destination ${out.name}, revision ${out.revision}. Validate this revision from this database's runner before selecting it.`,
        );
      } else {
        const out = await validateDatabaseBackupTarget(
          scope,
          next.target,
          next.input,
          controller.signal,
        );
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
        if (
          cause instanceof DatabaseBackupTargetInputError ||
          (cause instanceof ApiError && [400, 404, 409].includes(cause.status))
        )
          setAttempt(undefined);
      }
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }
  const credentials =
    accessKey || secretKey
      ? { access_key: accessKey, secret_key: secretKey }
      : undefined;
  const credentialsValid = validDatabaseBackupTargetCredentials(
    credentials,
    !editing.id,
  );
  return (
    <section
      aria-label="Database backup destinations"
      className="min-w-0 space-y-3 rounded-xl border p-3"
    >
      <h4 className="font-medium">S3-compatible backup destinations</h4>
      <p>
        Configure project storage here, then choose it for immediate or
        scheduled backups. Each dump remains a local archive; remote upload has
        its own status. Validation runs from this database's destination runner
        and checks ListObjects reachability, not PutObject permission.
      </p>
      {!owner ? (
        <p>
          Project owners can configure and validate backup destinations. You can
          inspect safe storage settings and transfer status.
        </p>
      ) : (
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
            fragment. The server enforces network policy. HTTP is reserved for
            an operator-approved local qualification fixture.
          </p>
          <p>
            Credentials are write-only. Leave both key fields empty while
            editing to keep the existing credentials.
          </p>
          {editing.id ? (
            <p>
              Replacing keys revokes the previous credentials. Older backups
              cannot use the replacement keys, so retrying their upload or
              restoring them remotely may fail. Local backups remain available.
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
              value={accessKey}
              disabled={unavailable}
              onChange={(e) => setAccessKey(e.target.value)}
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
              Enter both keys without surrounding whitespace or control
              characters.
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
              The reviewed destination revision changed. Refresh and explicitly
              edit its current revision before saving.
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
      )}
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
                    !database.binding?.destination_id
                  }
                  onClick={() =>
                    void submit({
                      mode: "validate",
                      target: target.id,
                      input: {
                        request_id: databaseRequestID(),
                        expected_revision: target.revision,
                        destination_id: database.binding.destination_id,
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
