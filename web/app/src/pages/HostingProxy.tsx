import { allProjectMembersQuery, principalQuery } from "@/api";
import { ApiError } from "@/api/errors";
import {
  cancelProxyOperation,
  configureProxy,
  disableProxy,
  getProxyOperation,
  grantHostname,
  grantsQuery,
  proxyError,
  proxyKeys,
  proxyQuery,
  revokeHostname,
  type ProxyInput,
} from "@/api/hosting-proxy";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { HostingProxyRecovery } from "./HostingProxyRecovery";

type Action =
  | { kind: "configure"; body: ProxyInput }
  | {
      kind: "disable" | "revoke";
      body: { idempotency_key: string };
      grant?: string;
    };
export function HostingProxy({
  serverID,
  blocked,
}: {
  serverID: string;
  blocked: boolean;
}) {
  const principal = useQuery(principalQuery());
  const members = useQuery(allProjectMembersQuery());
  const p = principal.data;
  if (
    !p ||
    !p.roles.some((role) => role === "system_admin" || role === "runner_admin")
  )
    return null;
  const ownership =
    members.data
      ?.filter((m) => m.user_id === p.id && m.role === "owner")
      .map((m) => m.project_id)
      .sort() ?? [];
  const actor = JSON.stringify([p.provider, p.id]);
  return (
    <ProxyAdministration
      key={JSON.stringify([actor, serverID, p.roles, ownership])}
      serverID={serverID}
      actor={actor}
      owns={(project) =>
        p.roles.includes("system_admin") || ownership.includes(project)
      }
      blocked={
        blocked || principal.isError || members.isError || members.isPending
      }
    />
  );
}
function ProxyOperationStatus(input: {
  actor: string;
  serverID: string;
  operationID: string;
  writable: boolean;
  blocked: boolean;
}) {
  const viewModel = useProxyOperationStatus(input);
  const { operationID, operation, denied, error } = viewModel;
  return (
    <section
      aria-label="Administrator proxy operation"
      className="space-y-2 rounded-lg border p-2"
    >
      <p className="break-all">Operation {operationID}</p>
      {operation.isError ? (
        <p role="alert">Unable to refresh administrator operation status.</p>
      ) : null}
      {operation.data ? (
        <>
          <p>
            Operation state: {operation.data.state} · generation{" "}
            {operation.data.generation}
          </p>
          {operation.data.result?.outcome ? (
            <p>Historical engine outcome: {operation.data.result.outcome}</p>
          ) : null}
          {operation.data.metadata_conflict ? (
            <p role="alert">
              Route metadata conflict. Keep ownership held until a supported
              reviewed recovery succeeds. The original engine outcome is
              preserved independently of current state.
            </p>
          ) : null}
          {operation.data.mutation_hold ? (
            <p role="status">
              Proxy mutation ownership remains held; cancellation does not
              release it.
            </p>
          ) : null}
          {operation.data.restore_ownership_held ? (
            <p role="alert">
              Restored proxy ownership is held; recovery proof is pending and no
              unlock API exists.
            </p>
          ) : null}
          <p>
            Loaded generation recorded by this operation:{" "}
            {operation.data.result?.loaded_generation ?? "not confirmed"}
          </p>
          <ProxyCancellation {...viewModel} />
        </>
      ) : null}
      {denied ? (
        <p role="alert">
          Mutations paused after permission denial. Reload to refresh identity
          and project permissions before a separate request.
        </p>
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
    </section>
  );
}
function ProxyAdministration(props: {
  serverID: string;
  actor: string;
  owns: (project: string) => boolean;
  blocked: boolean;
}) {
  const viewModel = useProxyAdministration(props);
  const { proxy, grants, busy, denied, error, writable, refresh } = viewModel;
  return (
    <section
      className="min-w-0 space-y-3 border-t pt-3"
      aria-label="Managed proxy"
    >
      <h4 className="font-medium">Managed proxy</h4>
      <p className="text-xs text-muted-foreground">
        Configuration admission requires the enrolled runner to advertise
        proxy_apply. Verify supported proxy execution and enrollment with the
        server operator before configuring; an image-only runner is not
        sufficient. Saved configuration queues work. Execution requires a runner
        with supported proxy_apply execution; this UI does not enable that
        capability. Queued operations do not prove loaded routing, DNS ownership
        or trusted HTTPS.
      </p>
      <Button
        className="h-auto max-w-full whitespace-normal break-all"
        type="button"
        variant="outline"
        disabled={busy}
        onClick={() => void refresh()}
      >
        Refresh proxy information
      </Button>
      {proxy.isPending ? (
        <p role="status">Loading proxy configuration...</p>
      ) : null}
      {proxy.isError || grants.isError ? (
        <p role="alert">
          Unable to refresh proxy information. Drafts are preserved; mutations
          are paused.
        </p>
      ) : null}
      <SavedProxyConfiguration {...viewModel} />
      {!writable ? (
        <p className="text-xs">
          Server administration and owner access in the control project are both
          required for mutations. System administrators retain their existing
          override.
        </p>
      ) : null}
      <ProxyWithdrawal {...viewModel} />
      <ProxyRequestRecovery {...viewModel} />
      {denied ? (
        <p role="alert">
          Mutations paused after permission denial. Reload to refresh identity
          and project permissions before a separate request.
        </p>
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
    </section>
  );
}

function useProxyAdministration({
  serverID,
  actor,
  owns,
  blocked,
}: {
  serverID: string;
  actor: string;
  owns: (project: string) => boolean;
  blocked: boolean;
}) {
  const id = useId();
  const proxy = useQuery(proxyQuery(actor, serverID));
  const grants = useQuery({
    ...grantsQuery(actor, serverID),
    enabled: Boolean(proxy.data),
  });
  const [project, setProject] = useState("");
  const [email, setEmail] = useState("");
  const [ca, setCA] = useState<"letsencrypt" | "letsencrypt-staging">(
    "letsencrypt-staging",
  );
  const [port, setPort] = useState("18080");
  const [domain, setDomain] = useState("");
  const [grantProject, setGrantProject] = useState("");
  const [hostname, setHostname] = useState("");
  const [wildcard, setWildcard] = useState(false);
  const [grantNeedsReview, setGrantNeedsReview] = useState(false);
  const [attempt, setAttempt] = useState<Action>();
  const [confirmation, setConfirmation] = useState<"disable" | string>();
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [denied, setDenied] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [error, setError] = useState("");
  const active = useRef(true);
  const submitting = useRef(false);
  useEffect(
    () => () => {
      active.current = false;
    },
    [],
  );
  const control = proxy.data?.control_project_id ?? project;
  const writable = owns(control);
  const paused =
    Boolean(proxy.data?.hold_operation_id && !attempt) ||
    denied ||
    blocked ||
    proxy.isPending ||
    proxy.isError ||
    Boolean(proxy.data && (grants.isError || grants.isPending)) ||
    Boolean(proxy.data?.restore_ownership_held);
  async function refresh() {
    const p = await proxy.refetch();
    if (p.isError) return false;
    if (p.data) {
      const g = await grants.refetch();
      if (g.isError) return false;
    }
    if (active.current) setGrantNeedsReview(false);
    return true;
  }
  async function mutate(action: Action) {
    if (!writable || paused || submitting.current || conflict) return;
    const frozen = attempt ?? action;
    submitting.current = true;
    setAttempt(frozen);
    setBusy(true);
    setError("");
    try {
      const result =
        frozen.kind === "configure"
          ? await configureProxy(serverID, frozen.body)
          : frozen.kind === "disable"
            ? await disableProxy(serverID, frozen.body)
            : await revokeHostname(serverID, frozen.grant!, frozen.body);
      if (!active.current) return;
      if (result.server_id !== serverID)
        throw new Error("Proxy target mismatch");
      setAttempt(undefined);
      setConfirmation(undefined);
      setConfirmed(false);
      await refresh();
    } catch (cause) {
      if (active.current) {
        setError(proxyError(cause));
        if (cause instanceof ApiError && [401, 403].includes(cause.status))
          setDenied(true);
        if (cause instanceof ApiError && cause.status === 409) {
          setConflict(true);
          await refresh();
        }
      }
    } finally {
      submitting.current = false;
      if (active.current) setBusy(false);
    }
  }
  async function grant(event: FormEvent) {
    event.preventDefault();
    if (!writable || paused || busy || attempt || grantNeedsReview) return;
    setBusy(true);
    setError("");
    try {
      await grantHostname(serverID, {
        project_id: grantProject,
        hostname,
        wildcard,
      });
      if (!active.current) return;
      setHostname("");
      setGrantProject("");
      setWildcard(false);
      await grants.refetch();
    } catch (cause) {
      if (active.current) {
        setGrantNeedsReview(true);
        if (cause instanceof ApiError && [401, 403].includes(cause.status))
          setDenied(true);
        setError(
          `${proxyError(cause)} Refresh grants before submitting again; hostname grants do not accept an idempotency key.`,
        );
      }
    } finally {
      if (active.current) setBusy(false);
    }
  }
  return {
    serverID,
    actor,
    blocked,
    id,
    proxy,
    grants,
    project,
    setProject,
    email,
    setEmail,
    ca,
    setCA,
    port,
    setPort,
    domain,
    setDomain,
    grantProject,
    setGrantProject,
    hostname,
    setHostname,
    wildcard,
    setWildcard,
    grantNeedsReview,
    attempt,
    setAttempt,
    confirmation,
    setConfirmation,
    confirmed,
    setConfirmed,
    busy,
    denied,
    conflict,
    setConflict,
    error,
    setError,
    active,
    control,
    writable,
    paused,
    refresh,
    mutate,
    grant,
  };
}

type ProxyConfigurationModel = Pick<
  ReturnType<typeof useProxyAdministration>,
  | "mutate"
  | "project"
  | "email"
  | "ca"
  | "port"
  | "domain"
  | "id"
  | "busy"
  | "attempt"
  | "setProject"
  | "setEmail"
  | "setCA"
  | "setPort"
  | "setDomain"
  | "paused"
  | "writable"
>;
function ProxyConfiguration(viewModel: ProxyConfigurationModel) {
  const {
    mutate,
    project,
    email,
    ca,
    port,
    domain,
    id,
    busy,
    attempt,
    setProject,
    setEmail,
    setCA,
    setPort,
    setDomain,
    paused,
    writable,
  } = viewModel;

  return (
    <form
      aria-label="Configure managed proxy"
      className="space-y-2"
      onSubmit={(e) => {
        e.preventDefault();
        void mutate({
          kind: "configure",
          body: {
            control_project_id: project,
            configuration: {
              email,
              ca,
              management_port: Number(port),
              ...(domain ? { generated_domain: domain } : {}),
            },
            idempotency_key: crypto.randomUUID(),
          },
        });
      }}
    >
      <label htmlFor={`${id}-project`}>Immutable control project ID</label>
      <Input
        id={`${id}-project`}
        required
        value={project}
        disabled={busy || Boolean(attempt)}
        onChange={(e) => setProject(e.target.value)}
      />
      <label htmlFor={`${id}-email`}>ACME email</label>
      <Input
        id={`${id}-email`}
        type="email"
        required
        value={email}
        disabled={busy || Boolean(attempt)}
        onChange={(e) => setEmail(e.target.value)}
      />
      <label htmlFor={`${id}-ca`}>Certificate authority</label>
      <select
        id={`${id}-ca`}
        className="h-10 w-full min-w-0 rounded-xl border bg-card px-2"
        disabled={busy || Boolean(attempt)}
        value={ca}
        onChange={(e) => setCA(e.target.value as typeof ca)}
      >
        <option value="letsencrypt-staging">Let's Encrypt staging</option>
        <option value="letsencrypt">Let's Encrypt production</option>
      </select>
      <label htmlFor={`${id}-port`}>Loopback management port</label>
      <Input
        id={`${id}-port`}
        type="number"
        min={1024}
        max={65535}
        required
        value={port}
        disabled={busy || Boolean(attempt)}
        onChange={(e) => setPort(e.target.value)}
      />
      <label htmlFor={`${id}-domain`}>Optional generated hostname domain</label>
      <Input
        id={`${id}-domain`}
        value={domain}
        disabled={busy || Boolean(attempt)}
        onChange={(e) => setDomain(e.target.value)}
      />
      <p className="text-xs">
        Generated names also require an active wildcard namespace grant. You
        must arrange DNS separately. The configured control project cannot be
        archived, including while disabled.
      </p>
      <Button
        className="h-auto max-w-full whitespace-normal break-all"
        type="submit"
        disabled={paused || busy || !writable || Boolean(attempt)}
      >
        Configure managed proxy
      </Button>
    </form>
  );
}

type ProxyHostnameGrantModel = Pick<
  ReturnType<typeof useProxyAdministration>,
  | "writable"
  | "grant"
  | "id"
  | "grantProject"
  | "busy"
  | "attempt"
  | "setGrantProject"
  | "hostname"
  | "setHostname"
  | "wildcard"
  | "setWildcard"
  | "paused"
  | "grantNeedsReview"
>;
function ProxyHostnameGrant(viewModel: ProxyHostnameGrantModel) {
  const {
    writable,
    grant,
    id,
    grantProject,
    busy,
    attempt,
    setGrantProject,
    hostname,
    setHostname,
    wildcard,
    setWildcard,
    paused,
    grantNeedsReview,
  } = viewModel;

  if (!writable) return null;
  return (
    <form
      aria-label="Grant proxy hostname"
      className="space-y-2"
      onSubmit={(e) => void grant(e)}
    >
      <label htmlFor={`${id}-grant-project`}>Hostname project ID</label>
      <Input
        id={`${id}-grant-project`}
        required
        value={grantProject}
        disabled={busy || Boolean(attempt)}
        onChange={(e) => setGrantProject(e.target.value)}
      />
      <label htmlFor={`${id}-hostname`}>Hostname or namespace root</label>
      <Input
        id={`${id}-hostname`}
        required
        value={hostname}
        disabled={busy || Boolean(attempt)}
        onChange={(e) => setHostname(e.target.value)}
      />
      <label className="flex gap-2">
        <input
          type="checkbox"
          checked={wildcard}
          disabled={busy || Boolean(attempt)}
          onChange={(e) => setWildcard(e.target.checked)}
        />
        Authorize descendants of this namespace
      </label>
      <Button
        className="h-auto max-w-full whitespace-normal break-all"
        type="submit"
        disabled={paused || busy || Boolean(attempt) || grantNeedsReview}
      >
        Grant hostname authority
      </Button>
    </form>
  );
}

type ProxyWithdrawalModel = Pick<
  ReturnType<typeof useProxyAdministration>,
  | "confirmation"
  | "proxy"
  | "serverID"
  | "confirmed"
  | "busy"
  | "attempt"
  | "setConfirmed"
  | "paused"
  | "mutate"
>;
function ProxyWithdrawal(viewModel: ProxyWithdrawalModel) {
  const {
    confirmation,
    proxy,
    serverID,
    confirmed,
    busy,
    attempt,
    setConfirmed,
    paused,
    mutate,
  } = viewModel;

  if (!confirmation) return null;
  return (
    <div className="space-y-2 rounded-lg border p-2">
      <p className="break-all">
        {confirmation === "disable"
          ? `Withdraw all routes from proxy ${proxy.data?.id} on server ${serverID}?`
          : `Revoke grant ${confirmation} and queue withdrawal of its routes?`}{" "}
        Existing routing may remain loaded until withdrawal completes.
        Certificate data is retained.
      </p>
      <label className="flex gap-2">
        <input
          type="checkbox"
          checked={confirmed}
          disabled={busy || Boolean(attempt)}
          onChange={(e) => setConfirmed(e.target.checked)}
        />
        I confirm this exact withdrawal.
      </label>
      <Button
        className="h-auto max-w-full whitespace-normal break-all"
        type="button"
        disabled={!confirmed || paused || busy || Boolean(attempt)}
        onClick={() =>
          void mutate({
            kind: confirmation === "disable" ? "disable" : "revoke",
            grant: confirmation === "disable" ? undefined : confirmation,
            body: { idempotency_key: crypto.randomUUID() },
          })
        }
      >
        Request proxy withdrawal
      </Button>
    </div>
  );
}

type ProxyRequestRecoveryModel = Pick<
  ReturnType<typeof useProxyAdministration>,
  | "attempt"
  | "paused"
  | "busy"
  | "conflict"
  | "writable"
  | "mutate"
  | "refresh"
  | "active"
  | "setAttempt"
  | "setConflict"
  | "setConfirmed"
  | "setConfirmation"
  | "setError"
>;
function ProxyRequestRecovery(viewModel: ProxyRequestRecoveryModel) {
  const {
    attempt,
    paused,
    busy,
    conflict,
    writable,
    mutate,
    refresh,
    active,
    setAttempt,
    setConflict,
    setConfirmed,
    setConfirmation,
    setError,
  } = viewModel;

  if (!attempt) return null;
  return (
    <div className="space-y-2">
      <p className="break-all text-xs">
        Frozen {attempt.kind} request key: {attempt.body.idempotency_key}
      </p>
      <Button
        className="h-auto max-w-full whitespace-normal break-all"
        type="button"
        disabled={paused || busy || conflict || !writable}
        onClick={() => void mutate(attempt)}
      >
        Retry identical proxy request
      </Button>
      <Button
        className="h-auto max-w-full whitespace-normal break-all"
        type="button"
        variant="outline"
        disabled={busy || paused}
        onClick={async () => {
          const refreshed = await refresh();
          if (active.current && refreshed) {
            setAttempt(undefined);
            setConflict(false);
            setConfirmed(false);
            setConfirmation(undefined);
            setError("");
          }
        }}
      >
        Review refreshed proxy for a separate request
      </Button>
    </div>
  );
}

type SavedProxyConfigurationModel = Pick<
  ReturnType<typeof useProxyAdministration>,
  | "proxy"
  | "control"
  | "writable"
  | "attempt"
  | "paused"
  | "busy"
  | "mutate"
  | "setConfirmation"
  | "setConfirmed"
  | "actor"
  | "serverID"
  | "blocked"
  | "grants"
  | "denied"
  | "grant"
  | "id"
  | "grantProject"
  | "setGrantProject"
  | "hostname"
  | "setHostname"
  | "wildcard"
  | "setWildcard"
  | "grantNeedsReview"
  | "project"
  | "email"
  | "ca"
  | "port"
  | "domain"
  | "setProject"
  | "setEmail"
  | "setCA"
  | "setPort"
  | "setDomain"
>;
function SavedProxyConfiguration(viewModel: SavedProxyConfigurationModel) {
  const { proxy } = viewModel;

  if (!proxy.data)
    return !proxy.isPending && !proxy.isError ? (
      <ProxyConfiguration {...viewModel} />
    ) : null;
  return (
    <>
      <ProxyConfigurationMetadata {...viewModel} />
      <p className="text-xs">
        Placement and settings are immutable. Disable queues withdrawal; it does
        not destroy certificate data. Loaded generation can lag desired
        generation while prior routing remains loaded.
      </p>
      {proxy.data.hold_operation_id ? (
        <p role="status">
          New proxy mutations are paused while an operation retains ownership.
          An existing frozen request can recover its exact receipt.
        </p>
      ) : null}
      {proxy.data.restore_ownership_held ? (
        <p role="alert">
          Proxy ownership is held after restore. Recovery proof is pending;
          there is no hold-clearing control.
        </p>
      ) : null}
      <SavedProxyControls {...viewModel} />{" "}
      <h5 className="font-medium">Hostname grants</h5>
      <p className="text-xs">
        Exact names and explicit descendant namespaces cannot overlap across
        projects. A wildcard namespace authorizes individual names, not wildcard
        certificates or DNS ownership.
      </p>
      <ProxyHostnameGrants {...viewModel} />
      <ProxyHostnameGrant {...viewModel} />
    </>
  );
}

type ProxyHostnameGrantsModel = Pick<
  ReturnType<typeof useProxyAdministration>,
  | "grants"
  | "writable"
  | "attempt"
  | "paused"
  | "busy"
  | "setConfirmation"
  | "setConfirmed"
>;
function ProxyHostnameGrants(viewModel: ProxyHostnameGrantsModel) {
  const {
    grants,
    writable,
    attempt,
    paused,
    busy,
    setConfirmation,
    setConfirmed,
  } = viewModel;

  return (grants.data ?? []).map((g) => (
    <article key={g.id} className="min-w-0 space-y-2 rounded-lg border p-2">
      <p className="break-all">
        {g.wildcard ? `Descendants of ${g.hostname}` : g.hostname} · project{" "}
        {g.project_id} · {g.revoked_at ? "revoked" : "active"}
      </p>
      {writable && !g.revoked_at && !attempt ? (
        <Button
          className="h-auto max-w-full whitespace-normal break-all"
          type="button"
          variant="outline"
          disabled={paused || busy}
          onClick={() => {
            setConfirmation(g.id);
            setConfirmed(false);
          }}
        >
          Review revoke {g.hostname}
        </Button>
      ) : null}
    </article>
  ));
}

type ProxyConfigurationMetadataModel = Pick<
  ReturnType<typeof useProxyAdministration>,
  "control" | "proxy"
>;
function ProxyConfigurationMetadata(
  viewModel: ProxyConfigurationMetadataModel,
) {
  const { control, proxy } = viewModel;

  if (!proxy.data) return null;
  return (
    <dl className="space-y-1 break-all text-sm">
      <dt>Control project</dt>
      <dd>{control}</dd>
      <dt>Desired proxy enabled</dt>
      <dd>{proxy.data.enabled ? "yes" : "no — withdrawal requested"}</dd>
      <dt>Desired generation</dt>
      <dd>{proxy.data.desired_generation}</dd>
      <dt>Loaded generation</dt>
      <dd>{proxy.data.loaded_generation}</dd>
      <dt>Active operation</dt>
      <dd>{proxy.data.active_operation_id ?? "none recorded"}</dd>
      <dt>Held operation</dt>
      <dd>{proxy.data.hold_operation_id ?? "none recorded"}</dd>
      <dt>ACME configuration</dt>
      <dd>
        {proxy.data.configuration.email} · {proxy.data.configuration.ca} ·
        management port {proxy.data.configuration.management_port}
      </dd>
      <dt>Generated domain</dt>
      <dd>{proxy.data.configuration.generated_domain ?? "not configured"}</dd>
    </dl>
  );
}

function useProxyOperationStatus({
  actor,
  serverID,
  operationID,
  writable,
  blocked,
}: {
  actor: string;
  serverID: string;
  operationID: string;
  writable: boolean;
  blocked: boolean;
}) {
  const operation = useQuery({
    queryKey: proxyKeys.operation(actor, `admin:${serverID}`, operationID),
    retry: false,
    refetchInterval: 5000,
    queryFn: async ({ signal }) => {
      const op = await getProxyOperation(operationID, { signal });
      if (op.id !== operationID || op.snapshot.server_id !== serverID)
        throw new Error("Proxy operation target mismatch");
      return op;
    },
  });
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [denied, setDenied] = useState(false);
  const [error, setError] = useState("");
  const active = useRef(true);
  useEffect(
    () => () => {
      active.current = false;
    },
    [],
  );
  async function cancel() {
    if (
      denied ||
      !writable ||
      blocked ||
      !confirmed ||
      busy ||
      operation.isError ||
      operation.isPending ||
      operation.data?.restore_ownership_held
    )
      return;
    setBusy(true);
    setError("");
    try {
      await cancelProxyOperation(operationID);
      if (active.current) {
        setConfirmed(false);
        await operation.refetch();
      }
    } catch (cause) {
      if (active.current) {
        setError(proxyError(cause));
        if (cause instanceof ApiError && [401, 403].includes(cause.status))
          setDenied(true);
      }
    } finally {
      if (active.current) setBusy(false);
    }
  }
  return {
    operationID,
    writable,
    blocked,
    operation,
    confirmed,
    setConfirmed,
    busy,
    denied,
    error,
    cancel,
  };
}

type SavedProxyControlsModel = Pick<
  ReturnType<typeof useProxyAdministration>,
  | "writable"
  | "attempt"
  | "proxy"
  | "paused"
  | "busy"
  | "mutate"
  | "setConfirmation"
  | "setConfirmed"
  | "actor"
  | "serverID"
  | "blocked"
  | "grants"
  | "control"
  | "denied"
>;
function SavedProxyControls(viewModel: SavedProxyControlsModel) {
  const {
    writable,
    attempt,
    proxy,
    paused,
    busy,
    setConfirmation,
    setConfirmed,
    actor,
    serverID,
    blocked,
    grants,
    control,
    denied,
  } = viewModel;

  if (!proxy.data) return null;

  return (
    <>
      <ProxySavedEnable {...viewModel} />
      {writable && !attempt ? (
        <Button
          className="h-auto max-w-full whitespace-normal break-all"
          type="button"
          variant="outline"
          disabled={paused || busy}
          onClick={() => {
            setConfirmation("disable");
            setConfirmed(false);
          }}
        >
          Review proxy withdrawal
        </Button>
      ) : null}
      {proxy.data.active_operation_id ? (
        <ProxyOperationStatus
          key={JSON.stringify([
            actor,
            serverID,
            proxy.data.active_operation_id,
            writable,
          ])}
          actor={actor}
          serverID={serverID}
          operationID={proxy.data.active_operation_id}
          writable={writable}
          blocked={
            blocked ||
            proxy.isError ||
            grants.isError ||
            Boolean(proxy.data.restore_ownership_held)
          }
        />
      ) : null}
      <HostingProxyRecovery
        serverID={serverID}
        projectID={control}
        proxyID={proxy.data.id}
        operationID={
          proxy.data.hold_operation_id ?? proxy.data.active_operation_id ?? ""
        }
        blocked={blocked || proxy.isError || grants.isError || denied}
      />
    </>
  );
}

type ProxyCancellationModel = Pick<
  ReturnType<typeof useProxyOperationStatus>,
  | "writable"
  | "confirmed"
  | "busy"
  | "setConfirmed"
  | "operationID"
  | "denied"
  | "blocked"
  | "operation"
  | "cancel"
>;
function ProxyCancellation(viewModel: ProxyCancellationModel) {
  const {
    writable,
    confirmed,
    busy,
    setConfirmed,
    operationID,
    denied,
    blocked,
    operation,
    cancel,
  } = viewModel;

  if (!operation.data) return null;
  if (!writable) return null;
  return (
    <>
      <label className="flex gap-2">
        <input
          type="checkbox"
          checked={confirmed}
          disabled={busy}
          onChange={(e) => setConfirmed(e.target.checked)}
        />
        I confirm cancellation of proxy operation {operationID}.
      </label>
      <Button
        className="h-auto max-w-full whitespace-normal break-all"
        type="button"
        disabled={
          denied ||
          busy ||
          blocked ||
          !confirmed ||
          operation.isError ||
          operation.data.restore_ownership_held
        }
        onClick={() => void cancel()}
      >
        Request proxy operation cancellation
      </Button>
    </>
  );
}

type ProxySavedEnableModel = Pick<
  ReturnType<typeof useProxyAdministration>,
  "writable" | "attempt" | "proxy" | "paused" | "busy" | "mutate"
>;
function ProxySavedEnable(viewModel: ProxySavedEnableModel) {
  const { writable, attempt, proxy, paused, busy, mutate } = viewModel;

  if (!proxy.data) return null;
  if (!(writable && !attempt && !proxy.data.enabled)) return null;
  return (
    <Button
      className="h-auto max-w-full whitespace-normal break-all"
      type="button"
      disabled={paused || busy}
      onClick={() =>
        void mutate({
          kind: "configure",
          body: {
            control_project_id: proxy.data!.control_project_id,
            configuration: proxy.data!.configuration,
            idempotency_key: crypto.randomUUID(),
          },
        })
      }
    >
      Enable saved proxy configuration
    </Button>
  );
}
