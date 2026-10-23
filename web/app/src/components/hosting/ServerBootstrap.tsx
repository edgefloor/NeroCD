import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, listProjects, principalQuery } from "@/api";
import {
  listManagedSecrets,
  listManagedSecretVersions,
} from "@/api/managed-secrets";
import {
  bootstrapKeys,
  bootstrapListQuery,
  bootstrapOperationQuery,
  bootstrapOptionsQuery,
  commandServerBootstrap,
  createServerBootstrap,
  type BootstrapAction,
  type BootstrapCommand,
  type BootstrapCreate,
  type ServerBootstrap as Operation,
} from "@/api/server-bootstrap";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ServerReadiness } from "@/pages/ServerReadiness";

const selectClass =
  "h-10 w-full min-w-0 rounded-xl border bg-card px-2 text-sm";
const actionNames: Record<BootstrapAction, string> = {
  probe: "Probe host prerequisites",
  review: "Review planned install",
  install: "Install and start runner",
  inspect: "Inspect installation and enrollment",
  register: "Register enrolled server",
  grant: "Apply chosen project grant",
  cancel: "Cancel onboarding",
};
const unavailable = {
  disabled: "SSH onboarding is disabled on this controller.",
  durable_store_required: "SSH onboarding requires durable PostgreSQL storage.",
  managed_secrets_unavailable:
    "Managed SSH secrets are unavailable on this controller.",
  "": "SSH onboarding is unavailable.",
};

export function ServerBootstrap({ blocked }: { blocked: boolean }) {
  const principal = useQuery(principalQuery());
  if (blocked || principal.isPending || principal.isError || !principal.data)
    return (
      <p role="status">SSH onboarding access is pending or unavailable.</p>
    );
  if (
    !Array.isArray(principal.data.roles) ||
    typeof principal.data.id !== "string" ||
    typeof principal.data.provider !== "string" ||
    !principal.data.roles.some((role) =>
      ["system_admin", "runner_admin"].includes(role),
    )
  )
    return (
      <p>
        SSH onboarding requires runner administrator access and project
        ownership.
      </p>
    );
  return (
    <ProjectPicker
      key={`${principal.data.provider}:${principal.data.id}`}
      actor={`${principal.data.provider}:${principal.data.id}`}
      authorityPending={principal.isFetching}
    />
  );
}

function ProjectPicker({
  actor,
  authorityPending,
}: {
  actor: string;
  authorityPending: boolean;
}) {
  const id = useId();
  const [offset, setOffset] = useState(0),
    [project, setProject] = useState("");
  const [requestHeld, setRequestHeld] = useState(false);
  const projects = useQuery({
    queryKey: ["bootstrapProjects", actor, offset],
    queryFn: ({ signal }) => listProjects({ limit: 20, offset }, { signal }),
    retry: false,
  });
  return (
    <section
      aria-label="Add server over SSH"
      className="min-w-0 space-y-4 border-t pt-4"
    >
      <h3 className="font-semibold">Add server over SSH</h3>
      <p className="text-sm">
        Use an existing managed SSH key to install and start a dedicated hosting
        runner. Choose a project you own. No Task is required.
      </p>
      {authorityPending ? (
        <p role="status">
          Current access is being checked. Onboarding actions are paused.
        </p>
      ) : null}
      <label htmlFor={`${id}-project`}>Project for server onboarding</label>
      <select
        id={`${id}-project`}
        className={selectClass}
        value={project}
        disabled={
          requestHeld ||
          authorityPending ||
          projects.isError ||
          projects.isPending
        }
        onChange={(event) => setProject(event.target.value)}
      >
        <option value="">Select project</option>
        {projects.data?.items.map((item) => (
          <option key={item.id} value={item.id}>
            {item.name}
          </option>
        ))}
      </select>
      {projects.isError ? (
        <>
          <p role="alert">Unable to load projects.</p>
          <Button onClick={() => void projects.refetch()}>
            Retry onboarding projects
          </Button>
        </>
      ) : null}
      <div className="flex flex-wrap gap-2">
        <Button
          variant="outline"
          disabled={
            requestHeld ||
            authorityPending ||
            offset === 0 ||
            projects.isFetching
          }
          onClick={() => {
            setProject("");
            setOffset(Math.max(0, offset - 20));
          }}
        >
          Previous projects
        </Button>
        <Button
          variant="outline"
          disabled={
            requestHeld ||
            authorityPending ||
            !projects.data ||
            offset + 20 >= projects.data.total ||
            projects.isFetching
          }
          onClick={() => {
            setProject("");
            setOffset(offset + 20);
          }}
        >
          Next projects
        </Button>
      </div>
      {project && projects.data?.items.some((item) => item.id === project) ? (
        <ProjectBootstrap
          key={`${actor}:${project}`}
          actor={actor}
          project={project}
          projectName={
            projects.data.items.find((item) => item.id === project)!.name
          }
          authorityPending={authorityPending}
          onRequestHeld={setRequestHeld}
        />
      ) : null}
    </section>
  );
}

type Attempt =
  | { kind: "create"; body: BootstrapCreate }
  | {
      kind: "command";
      id: string;
      action: BootstrapAction;
      body: BootstrapCommand;
    };
function ProjectBootstrap({
  actor,
  project,
  projectName,
  authorityPending,
  onRequestHeld,
}: {
  actor: string;
  project: string;
  projectName: string;
  authorityPending: boolean;
  onRequestHeld: (held: boolean) => void;
}) {
  const client = useQueryClient(),
    id = useId(),
    active = useRef(true),
    lock = useRef(false);
  const [secretOffset, setSecretOffset] = useState(0),
    [operationOffset, setOperationOffset] = useState(0);
  const [secret, setSecret] = useState(""),
    [version, setVersion] = useState(""),
    [versionOffset, setVersionOffset] = useState(0);
  const [name, setName] = useState(""),
    [ip, setIP] = useState(""),
    [port, setPort] = useState("22"),
    [fingerprint, setFingerprint] = useState(""),
    [artifact, setArtifact] = useState("");
  const [selected, setSelected] = useState(""),
    [attempt, setAttempt] = useState<Attempt>(),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [denied, setDenied] = useState(false);
  const [grantChoice, setGrantChoice] = useState({ review: "", value: "" }),
    [acknowledgedReview, setAcknowledgedReview] = useState(""),
    [needsReview, setNeedsReview] = useState(false),
    [now, setNow] = useState(Date.now());
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);
  const options = useQuery(bootstrapOptionsQuery(actor, project));
  const operations = useQuery(
    bootstrapListQuery(actor, project, operationOffset),
  );
  const secrets = useQuery({
    queryKey: ["bootstrapSecrets", actor, project, secretOffset],
    retry: false,
    queryFn: async ({ signal }) => {
      const page = await listManagedSecrets(
        { project_id: project, limit: 20, offset: secretOffset },
        { signal },
      );
      if (page.items.some((item) => item.project_id !== project))
        throw new Error("Secret project mismatch");
      return page;
    },
  });
  const versions = useQuery({
    queryKey: [
      "bootstrapSecretVersions",
      actor,
      project,
      secret,
      versionOffset,
    ],
    enabled: Boolean(secret),
    retry: false,
    queryFn: async ({ signal }) => {
      const page = await listManagedSecretVersions(
        secret,
        { limit: 20, offset: versionOffset },
        { signal },
      );
      if (page.items.some((item) => item.secret_id !== secret))
        throw new Error("Secret version mismatch");
      return page;
    },
  });
  const operation = useQuery({
    ...bootstrapOperationQuery(actor, project, selected),
    enabled: Boolean(selected) && !denied,
    refetchInterval: (query) =>
      query.state.status === "error" ||
      !query.state.data ||
      ["completed", "canceled", "refused"].includes(query.state.data.state)
        ? false
        : 2000,
  });
  const current = operation.data;
  const reviewKey = current?.review
    ? JSON.stringify([
        current.id,
        current.version,
        current.review.manifest_sha256,
        current.review.probe_receipt_id,
        current.review.expires_at,
      ])
    : "";
  const grant = grantChoice.review === reviewKey ? grantChoice.value : "";
  const acknowledge = Boolean(reviewKey && acknowledgedReview === reviewKey);
  function setGrant(value: string) {
    setGrantChoice({ review: reviewKey, value });
  }
  function setAcknowledge(value: boolean) {
    setAcknowledgedReview(value ? reviewKey : "");
  }
  const accessError = [
    options.error,
    operations.error,
    secrets.error,
    versions.error,
    operation.error,
  ].some(
    (cause) =>
      cause instanceof ApiError && [401, 403, 404].includes(cause.status),
  );
  const blocked = busy || denied || accessError || authorityPending;
  const artifactChoice = options.data?.artifacts.find(
    (item) => item.artifact_id === artifact,
  );
  const secretChoice = secrets.data?.items.find(
    (item) => item.id === secret && item.status === "active",
  );
  const freshProbe = Boolean(
    current?.probe && Date.parse(current.probe.expires_at) > now,
  );
  const freshReview = Boolean(
    current?.review && Date.parse(current.review.expires_at) > now,
  );
  const savedArtifactAvailable = Boolean(
    current?.artifact && current.artifact.artifact_id === current.artifact_id,
  );
  async function execute(frozen: Attempt) {
    if (lock.current || denied || accessError || authorityPending) return;
    lock.current = true;
    setBusy(true);
    setError("");
    setAttempt(frozen);
    onRequestHeld(true);
    try {
      const result =
        frozen.kind === "create"
          ? await createServerBootstrap(frozen.body)
          : await commandServerBootstrap(frozen.id, frozen.action, frozen.body);
      if (!active.current) return;
      if (
        result.project_id !== project ||
        (frozen.kind === "command" && result.id !== frozen.id)
      ) {
        setDenied(true);
        setAttempt(undefined);
        onRequestHeld(false);
        return;
      }
      await client.cancelQueries({
        queryKey: bootstrapKeys.operation(actor, project, result.id),
        exact: true,
      });
      if (!active.current) return;
      client.setQueryData(
        bootstrapKeys.operation(actor, project, result.id),
        result,
      );
      setSelected(result.id);
      setAttempt(undefined);
      onRequestHeld(false);
      if (frozen.kind === "command" && frozen.action === "review") {
        setNeedsReview(false);
        setGrant("");
        setAcknowledge(false);
      }
      if (frozen.kind === "create") {
        setGrant("");
        setAcknowledge(false);
        setNeedsReview(false);
      }
      void client.invalidateQueries({
        queryKey: ["serverBootstraps", actor, project],
      });
    } catch (cause) {
      if (!active.current) return;
      if (cause instanceof ApiError && cause.status === 409) {
        setAttempt(undefined);
        onRequestHeld(false);
        setNeedsReview(true);
        setAcknowledge(false);
        setGrant("");
        setError(
          "The operation changed or the probe/review expired. Current state was refreshed. Probe and review again before installing.",
        );
        if (frozen.kind === "command") {
          try {
            await client.fetchQuery(
              bootstrapOperationQuery(actor, project, frozen.id),
            );
          } catch {
            setError(
              "Conflict detected. Unable to refresh current operation; actions are paused until refresh succeeds.",
            );
          }
        }
      } else if (
        cause instanceof ApiError &&
        [401, 403, 404].includes(cause.status)
      ) {
        setDenied(true);
        setAttempt(undefined);
        onRequestHeld(false);
        setError(
          "Onboarding permission is unavailable. Verify runner administration and project ownership.",
        );
      } else if (cause instanceof ApiError && cause.status === 400) {
        setAttempt(undefined);
        onRequestHeld(false);
        setError(
          "The server rejected this request. Review the literal IP address, port, fingerprint, selected SSH key version and artifact.",
        );
      } else
        setError(
          "The response was not confirmed. Retry the exact saved request to recover its result. Its selections and request identity are frozen.",
        );
    } finally {
      lock.current = false;
      if (active.current) setBusy(false);
    }
  }
  function create(event: FormEvent) {
    event.preventDefault();
    if (
      blocked ||
      attempt ||
      !options.data?.available ||
      !artifactChoice ||
      !secretChoice ||
      !versions.data?.items.some((item) => String(item.version) === version)
    )
      return;
    void execute({
      kind: "create",
      body: {
        request_id: crypto.randomUUID(),
        project_id: project,
        name,
        target: {
          ip,
          port: Number(port),
          user: "root",
          host_fingerprint: fingerprint,
        },
        ssh_secret_id: secret,
        ssh_secret_version: Number(version),
        artifact_id: artifact,
      },
    });
  }
  function command(action: BootstrapAction) {
    if (
      !current ||
      blocked ||
      attempt ||
      operation.isError ||
      operation.isFetching ||
      !current.available_actions.includes(action)
    )
      return;
    const base = {
      command_id: crypto.randomUUID(),
      expected_version: current.version,
    };
    if (action === "review" && (!freshProbe || !current.probe)) return;
    if (
      action === "install" &&
      (!savedArtifactAvailable ||
        !freshReview ||
        needsReview ||
        !acknowledge ||
        !grant)
    )
      return;
    const body: BootstrapCommand =
      action === "review"
        ? { ...base, probe_receipt_id: current.probe!.receipt_id }
        : action === "install"
          ? {
              ...base,
              manifest_sha256: current.review!.manifest_sha256,
              acknowledge_install_and_start: true,
              grant_project: grant === "yes",
            }
          : base;
    void execute({ kind: "command", id: current.id, action, body });
  }
  if (denied || accessError)
    return (
      <p role="alert">
        Onboarding access is unavailable. Runner administrator access and
        ownership of this project are required. Operation evidence is cleared.
      </p>
    );
  return (
    <div className="min-w-0 space-y-5">
      <p className="text-sm">
        Qualified host profile: prepared Debian 12, native arm64, systemd,
        Docker API 1.54 and Docker Compose. Prepare these prerequisites before
        probing. The hosting_image_v1 preset supports image deployment and
        readiness. Builds, proxy management and runtime logs require separate
        qualification.
      </p>
      {options.data && !options.data.available ? (
        <p role="status">{unavailable[options.data.unavailable_reason]}</p>
      ) : null}
      {options.isError ||
      secrets.isError ||
      versions.isError ||
      operations.isError ? (
        <div>
          <p role="alert">
            Unable to load onboarding choices. Changes are paused.
          </p>
          <Button
            onClick={() => {
              if (options.isError) void options.refetch();
              if (secrets.isError) void secrets.refetch();
              if (versions.isError) void versions.refetch();
              if (operations.isError) void operations.refetch();
            }}
          >
            Retry onboarding choices
          </Button>
        </div>
      ) : null}
      <form
        aria-label="Prepare SSH server onboarding"
        className="min-w-0 space-y-3"
        onSubmit={create}
      >
        <fieldset
          disabled={
            blocked ||
            Boolean(attempt) ||
            !options.data?.available ||
            options.isError ||
            secrets.isError ||
            versions.isError ||
            operations.isError
          }
          className="min-w-0 space-y-3"
        >
          <legend className="font-medium">New server for {projectName}</legend>
          <label htmlFor={`${id}-name`}>Server name</label>
          <Input
            id={`${id}-name`}
            required
            value={name}
            onChange={(event) => setName(event.target.value)}
          />
          <label htmlFor={`${id}-ip`}>Literal server IP address</label>
          <Input
            id={`${id}-ip`}
            required
            value={ip}
            onChange={(event) => setIP(event.target.value)}
          />
          <p className="text-xs">
            Use the server's IPv4 or IPv6 address. SSH connects as root.
          </p>
          <label htmlFor={`${id}-port`}>SSH port</label>
          <Input
            id={`${id}-port`}
            type="number"
            min="1"
            max="65535"
            required
            value={port}
            onChange={(event) => setPort(event.target.value)}
          />
          <label htmlFor={`${id}-fingerprint`}>
            Verified SSH host fingerprint
          </label>
          <Input
            id={`${id}-fingerprint`}
            required
            placeholder="SHA256:..."
            value={fingerprint}
            onChange={(event) => setFingerprint(event.target.value)}
          />
          <p className="text-xs">
            Verify this fingerprint through a trusted channel before connecting.
          </p>
          <label htmlFor={`${id}-secret`}>Managed SSH key</label>
          <select
            id={`${id}-secret`}
            className={selectClass}
            value={secret}
            onChange={(event) => {
              setSecret(event.target.value);
              setVersion("");
              setVersionOffset(0);
            }}
          >
            <option value="">Select managed SSH key</option>
            {secrets.data?.items.map((item) => (
              <option
                key={item.id}
                value={item.id}
                disabled={item.status !== "active"}
              >
                {item.name}
                {item.status !== "active" ? " · revoked" : ""}
              </option>
            ))}
          </select>
          <p className="text-xs">
            Choose an existing secret containing the SSH private key. This flow
            uses it without revealing its value. Manage keys in Settings.
          </p>
          {secrets.data &&
          !secrets.data.items.some((item) => item.status === "active") ? (
            <p role="status">
              No active managed SSH key is available on this page. Choose
              another page or create a managed SSH key in Settings for this
              project.
            </p>
          ) : null}
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              variant="outline"
              disabled={secretOffset === 0 || secrets.isFetching}
              onClick={() => {
                setSecret("");
                setVersion("");
                setSecretOffset(Math.max(0, secretOffset - 20));
              }}
            >
              Previous SSH keys
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={
                !secrets.data ||
                secretOffset + 20 >= secrets.data.total ||
                secrets.isFetching
              }
              onClick={() => {
                setSecret("");
                setVersion("");
                setSecretOffset(secretOffset + 20);
              }}
            >
              Next SSH keys
            </Button>
          </div>
          <label htmlFor={`${id}-version`}>SSH key version</label>
          <select
            id={`${id}-version`}
            className={selectClass}
            disabled={!secret || versions.isPending || versions.isError}
            value={version}
            onChange={(event) => setVersion(event.target.value)}
          >
            <option value="">Select version</option>
            {versions.data?.items.map((item) => (
              <option key={item.version} value={item.version}>
                Version {item.version}
                {item.version === secretChoice?.current_version
                  ? " · current"
                  : ""}
              </option>
            ))}
          </select>
          {secret && versions.data && !versions.data.items.length ? (
            <p role="status">
              No SSH key version is available on this page. Choose another page
              or check the managed secret in Settings.
            </p>
          ) : null}
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              variant="outline"
              disabled={!secret || versionOffset === 0 || versions.isFetching}
              onClick={() => {
                setVersion("");
                setVersionOffset(Math.max(0, versionOffset - 20));
              }}
            >
              Previous key versions
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={
                !versions.data ||
                versionOffset + 20 >= versions.data.total ||
                versions.isFetching
              }
              onClick={() => {
                setVersion("");
                setVersionOffset(versionOffset + 20);
              }}
            >
              Next key versions
            </Button>
          </div>
          <label htmlFor={`${id}-artifact`}>Qualified runner release</label>
          <select
            id={`${id}-artifact`}
            className={selectClass}
            value={artifact}
            onChange={(event) => setArtifact(event.target.value)}
          >
            <option value="">Select qualified release</option>
            {options.data?.artifacts.map((item) => (
              <option key={item.artifact_id} value={item.artifact_id}>
                {item.version} · {item.architecture} · Debian 12
              </option>
            ))}
          </select>
          {options.data?.available && !options.data.artifacts.length ? (
            <p role="status">
              No qualified arm64 release is available. Configure a
              prepared_debian12_v1 artifact on this controller.
            </p>
          ) : null}
          {artifactChoice ? (
            <p className="break-all text-xs">
              Release SHA-256: {artifactChoice.sha256}. Download size:{" "}
              {artifactChoice.size} bytes.
            </p>
          ) : null}
          <Button
            type="submit"
            disabled={busy || !artifactChoice || !secretChoice || !version}
          >
            Prepare onboarding
          </Button>
        </fieldset>
      </form>
      {attempt ? (
        <section aria-label="Saved onboarding request" className="space-y-2">
          <p>The saved request is frozen until its result is confirmed.</p>
          <Button disabled={blocked} onClick={() => void execute(attempt)}>
            Retry exact onboarding request
          </Button>
        </section>
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
      <section aria-label="Server onboarding operations" className="space-y-2">
        <h4 className="font-medium">Onboarding operations</h4>
        {operations.data?.items.map((item) => (
          <Button
            key={item.id}
            variant="outline"
            className="h-auto max-w-full whitespace-normal"
            disabled={busy || Boolean(attempt)}
            onClick={() => {
              setSelected(item.id);
              setGrant("");
              setAcknowledge(false);
              setNeedsReview(false);
            }}
          >
            {item.name} · {item.target.ip} · {item.state.replaceAll("_", " ")}
          </Button>
        ))}
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            disabled={
              operationOffset === 0 || operations.isFetching || Boolean(attempt)
            }
            onClick={() =>
              setOperationOffset(Math.max(0, operationOffset - 20))
            }
          >
            Previous onboarding operations
          </Button>
          <Button
            variant="outline"
            disabled={
              !operations.data?.has_more ||
              operations.isFetching ||
              Boolean(attempt)
            }
            onClick={() => setOperationOffset(operationOffset + 20)}
          >
            Next onboarding operations
          </Button>
        </div>
      </section>
      {selected && operation.isError ? (
        <>
          <p role="alert">
            Unable to read current onboarding. Actions are paused.
          </p>
          <Button onClick={() => void operation.refetch()}>
            Refresh current onboarding
          </Button>
        </>
      ) : null}
      {current ? (
        <section
          aria-label="Current server onboarding"
          className="min-w-0 space-y-3 rounded-lg border p-3"
        >
          <h4 className="break-words font-semibold">{current.name}</h4>
          <p role="status">
            Onboarding state: {current.state.replaceAll("_", " ")}
          </p>
          <dl className="grid min-w-0 gap-1 break-all text-sm">
            <dt>SSH target</dt>
            <dd>
              root@{current.target.ip}:{current.target.port}
            </dd>
            <dt>Pinned host fingerprint</dt>
            <dd>{current.target.host_fingerprint}</dd>
            <dt>Qualified artifact</dt>
            <dd>
              {current.artifact?.version ??
                "Saved release metadata unavailable"}
            </dd>
            <dt>Saved release SHA-256</dt>
            <dd>{current.artifact?.sha256 ?? "Unavailable"}</dd>
            <dt>Saved release profile</dt>
            <dd>
              {current.artifact?.profile ?? "Unavailable"} ·{" "}
              {current.artifact?.architecture}
            </dd>
          </dl>
          {current.remote_hold || current.restore_held ? (
            <p role="alert">
              Remote or restore ownership is held. Only the actions offered by
              the controller are available.
            </p>
          ) : null}
          {current.probe ? (
            <section
              aria-label="Recorded host prerequisites"
              className="space-y-2"
            >
              <h5 className="font-medium">Recorded host prerequisites</h5>
              <dl className="grid min-w-0 gap-1 break-all text-sm">
                {Object.entries(current.probe.prerequisites).map(
                  ([key, value]) => (
                    <div key={key}>
                      <dt>{key.replaceAll("_", " ")}</dt>
                      <dd>{value}</dd>
                    </div>
                  ),
                )}
              </dl>
              <p>
                {freshProbe
                  ? "Probe is within its reported validity window."
                  : "Probe expired. Probe the host again before review."}
              </p>
            </section>
          ) : null}
          {current.review ? (
            <section
              aria-label="Reviewed install plan"
              className="min-w-0 space-y-2"
            >
              <h5 className="font-medium">Reviewed install plan</h5>
              <p>
                Install the selected verified runner binary, write its owned
                agent configuration and credential, and create and start its
                persistent systemd service as root on this pinned SSH target.
                This requires a prepared host; it does not install the operating
                system or Docker. Enrollment is automatic; server registration
                follows observed enrollment.
              </p>
              <p className="break-all text-xs">
                Manifest SHA-256: {current.review.manifest_sha256}
              </p>
              <p className="break-all text-xs">
                Review expires: {current.review.expires_at}
              </p>
              {!freshReview || needsReview ? (
                <p role="alert">
                  Review expired or changed. Probe and review again before
                  installing.
                </p>
              ) : null}
              <label htmlFor={`${id}-grant`}>
                Project access after registration
              </label>
              <select
                id={`${id}-grant`}
                className={selectClass}
                value={grant}
                disabled={blocked || Boolean(attempt)}
                onChange={(event) => setGrant(event.target.value)}
              >
                <option value="">Choose project access explicitly</option>
                <option value="no">Keep server ungranted</option>
                <option value="yes">
                  Grant {projectName} access to the default destination
                </option>
              </select>
              <label className="flex items-start gap-2">
                <input
                  type="checkbox"
                  checked={acknowledge}
                  disabled={blocked || Boolean(attempt)}
                  onChange={(event) => setAcknowledge(event.target.checked)}
                />
                <span>
                  I approve installing and starting this runner on the reviewed
                  host.
                </span>
              </label>
            </section>
          ) : null}
          {!savedArtifactAvailable ? (
            <p role="alert">
              Saved release metadata is unavailable. Refresh onboarding before
              installing.
            </p>
          ) : null}
          <div className="flex flex-wrap gap-2">
            {current.available_actions.map((action) => (
              <Button
                key={action}
                variant={action === "install" ? "default" : "outline"}
                className="h-auto max-w-full whitespace-normal"
                disabled={
                  blocked ||
                  Boolean(attempt) ||
                  operation.isError ||
                  operation.isFetching ||
                  (action === "review" && !freshProbe) ||
                  (action === "install" &&
                    (!savedArtifactAvailable ||
                      !freshReview ||
                      needsReview ||
                      !grant ||
                      !acknowledge))
                }
                onClick={() => command(action)}
              >
                {actionNames[action]}
              </Button>
            ))}
            <Button
              variant="outline"
              disabled={busy || Boolean(attempt) || operation.isFetching}
              onClick={() => void operation.refetch()}
            >
              Refresh onboarding status
            </Button>
          </div>
          <ol className="space-y-1 text-sm">
            {current.progress.map((item) => (
              <li key={item.command_id}>
                {actionNames[item.action]}: {item.status},{" "}
                {item.outcome || "awaiting result"}
              </li>
            ))}
          </ol>
          {current.state === "installed_waiting_enrollment" ? (
            <p>
              Runner installation is recorded. Wait for enrollment, then inspect
              and register when the controller offers those actions.
            </p>
          ) : null}
          {current.state === "uncertain" ? (
            <p role="alert">
              The remote outcome is uncertain. Inspect the installation before
              taking another action.
            </p>
          ) : null}
          {current.server_id &&
          current.readiness_state === "check_server_readiness" ? (
            <ServerReadiness
              key={current.server_id}
              serverID={current.server_id}
              blocked={blocked || operation.isError}
            />
          ) : null}
        </section>
      ) : null}
    </div>
  );
}
