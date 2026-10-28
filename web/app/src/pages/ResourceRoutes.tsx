import { principalQuery } from "@/api";
import { ApiError } from "@/api/errors";
import type { Resource } from "@/api/hosting";
import { hostingDeploymentsQuery } from "@/api/hosting-deployments";
import {
  createRoute,
  deleteRoute,
  proxyError,
  routeOperationQuery,
  routesQuery,
  updateRoute,
  type HostingRoute,
  type RouteDeletion,
  type RouteInput,
} from "@/api/hosting-proxy";
import { PageNavigation } from "@/components/common/PageNavigation";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { maximumPageOffset } from "@/router/search";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { ResourceRouteSwitch } from "./ResourceRouteSwitch";

type Attempt =
  | { kind: "create" | "update"; target?: string; body: RouteInput }
  | { kind: "delete"; target: string; body: RouteDeletion };
export function ResourceRoutes(props: {
  resource: Resource;
  writable: boolean;
  blocked: boolean;
}) {
  const principal = useQuery(principalQuery());
  if (
    principal.isPending ||
    principal.isFetching ||
    principal.isError ||
    !principal.data
  )
    return <p role="status">Loading route identity...</p>;
  const actor = JSON.stringify([principal.data.provider, principal.data.id]);
  return (
    <Routes
      key={JSON.stringify([
        actor,
        props.resource.project_id,
        props.resource.id,
        props.writable,
      ])}
      {...props}
      actor={actor}
      blocked={props.blocked || principal.isError}
    />
  );
}
function RouteOperation({
  actor,
  resource,
  id,
}: {
  actor: string;
  resource: Resource;
  id: string;
}) {
  const operation = useQuery({
    ...routeOperationQuery(actor, resource.project_id, resource.id, id),
    refetchInterval: 5000,
  });
  return (
    <section
      aria-label="Route operation status"
      className="space-y-2 rounded-lg border p-2"
    >
      <p className="break-all">Proxy operation {id}</p>
      <Button
        className="h-auto max-w-full whitespace-normal break-all"
        type="button"
        variant="outline"
        onClick={() => void operation.refetch()}
      >
        Refresh route operation
      </Button>
      {operation.isError ? (
        <p role="alert">
          Unable to refresh route operation; loaded and HTTPS state are
          unconfirmed.
        </p>
      ) : null}
      {operation.data ? (
        <>
          <p>Operation state: {operation.data.state}</p>
          {operation.data.engine_outcome ? (
            <p>Historical engine outcome: {operation.data.engine_outcome}</p>
          ) : null}
          {operation.data.metadata_conflict ? (
            <p role="alert">
              Route metadata conflict. Keep ownership held; this operator
              workflow has no reconciliation or unlock action. The original
              engine outcome is historical.
            </p>
          ) : null}
          {operation.data.mutation_hold ? (
            <p role="status">
              Proxy mutation ownership remains held. Refresh status; do not
              assume cancellation releases ownership.
            </p>
          ) : null}
          {operation.data.restore_ownership_held ? (
            <p role="alert">
              Proxy ownership held after restore. Recovery proof is pending; no
              hold-clearing API is available.
            </p>
          ) : null}
        </>
      ) : null}
    </section>
  );
}
function Routes(input: {
  resource: Resource;
  writable: boolean;
  blocked: boolean;
  actor: string;
}) {
  const viewModel = useRoutes(input);
  const {
    resource,
    writable,
    blocked,
    actor,
    routes,
    history,
    busy,
    denied,
    error,
    selected,
    admissionHeld,
    refresh,
  } = viewModel;
  return (
    <section
      className="min-w-0 space-y-3 border-t pt-3"
      aria-label={`Routes for ${resource.name}`}
    >
      <h5 className="font-medium">Resource routes</h5>
      <p className="text-xs text-muted-foreground">
        A saved route queues desired routing to a settled healthy release. The
        server derives the upstream from that release; no URL or upstream port
        is supplied here. Execution requires supported proxy_apply runner
        execution. Queued work is not loaded routing or trusted HTTPS.
      </p>
      <Button
        className="h-auto max-w-full whitespace-normal break-all"
        type="button"
        variant="outline"
        disabled={busy}
        onClick={() => void refresh()}
      >
        Refresh resource routes
      </Button>
      {routes.isPending ? (
        <p role="status">Loading resource routes...</p>
      ) : null}
      {routes.isError || history.isError ? (
        <p role="alert">
          Unable to refresh routes or deployment prerequisites. Drafts are
          preserved; actions are paused.
        </p>
      ) : null}
      {routes.data?.length === 0 ? (
        <p>No desired resource routes recorded.</p>
      ) : null}
      <SavedRoutes {...viewModel} />
      {admissionHeld ? (
        <p role="status">
          New route requests are paused while proxy mutation ownership is held.
          An exact frozen request may recover its existing receipt; refreshing
          does not authorize a separate mutation.
        </p>
      ) : null}
      {selected ? (
        <RouteOperation
          key={selected}
          actor={actor}
          resource={resource}
          id={selected}
        />
      ) : null}
      <RouteEditor {...viewModel} />
      {denied ? (
        <p role="alert">
          Route mutations paused after permission denial. Reload to refresh
          project permissions.
        </p>
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
      <ResourceRouteSwitch
        resource={resource}
        writable={writable}
        blocked={blocked || denied}
      />
    </section>
  );
}

function useRoutes({
  resource,
  writable,
  blocked,
  actor,
}: {
  resource: Resource;
  writable: boolean;
  blocked: boolean;
  actor: string;
}) {
  const id = useId();
  const routes = useQuery({
    ...routesQuery(actor, resource.project_id, resource.id),
    refetchInterval: 5000,
  });
  const [offset, setOffset] = useState(0);
  const history = useQuery(
    hostingDeploymentsQuery(resource.project_id, resource.id, offset, actor),
  );
  const [target, setTarget] = useState<HostingRoute>();
  const [hostname, setHostname] = useState("");
  const [path, setPath] = useState("/");
  const [healthy, setHealthy] = useState("");
  const [generated, setGenerated] = useState(false);
  const [withdraw, setWithdraw] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [attempt, setAttempt] = useState<Attempt>();
  const [busy, setBusy] = useState(false);
  const [denied, setDenied] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState("");
  const active = useRef(true);
  const submitting = useRef(false);
  useEffect(
    () => () => {
      active.current = false;
    },
    [],
  );
  const candidates =
    history.data?.items.filter(
      (op) =>
        op.state === "succeeded" &&
        op.result?.ready &&
        !op.result.uncertain &&
        !op.mutation_hold &&
        !op.restore_ownership_held &&
        !op.cleanup_plan,
    ) ?? [];
  const operation = useQuery({
    ...routeOperationQuery(actor, resource.project_id, resource.id, selected),
    enabled: Boolean(selected),
  });
  const restoreHeld = Boolean(operation.data?.restore_ownership_held);
  const admissionHeld = Boolean(operation.data?.mutation_hold && !attempt);
  const paused =
    restoreHeld ||
    Boolean(selected && operation.isError) ||
    denied ||
    blocked ||
    routes.isPending ||
    routes.isError ||
    history.isPending ||
    history.isError;
  async function refresh() {
    const results = await Promise.all([routes.refetch(), history.refetch()]);
    return results.every((result) => !result.isError);
  }
  function clear() {
    setTarget(undefined);
    setAttempt(undefined);
    setWithdraw(false);
    setConfirmed(false);
    setConflict(false);
    setError("");
    setHostname("");
    setPath("/");
    setHealthy("");
    setGenerated(false);
  }
  function edit(route: HostingRoute, deletion: boolean) {
    setTarget(route);
    setHostname(route.hostname);
    setPath(route.path_prefix);
    setHealthy(route.selected_release_operation_id);
    setGenerated(false);
    setWithdraw(deletion);
    setConfirmed(false);
    setConflict(false);
    setError("");
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (
      !writable ||
      paused ||
      admissionHeld ||
      submitting.current ||
      conflict ||
      (withdraw && !confirmed) ||
      (!withdraw && !attempt && !candidates.some((op) => op.id === healthy))
    )
      return;
    const frozen: Attempt =
      attempt ??
      (withdraw && target
        ? {
            kind: "delete",
            target: target.id,
            body: {
              expected_route_version: target.version,
              idempotency_key: crypto.randomUUID(),
            },
          }
        : {
            kind: target ? "update" : "create",
            target: target?.id,
            body: {
              expected_route_version: target?.version ?? 0,
              idempotency_key: crypto.randomUUID(),
              hostname: generated ? "" : hostname,
              path_prefix: path,
              expected_healthy_operation_id: healthy,
              ...(generated ? { generate_hostname: true } : {}),
            },
          });
    submitting.current = true;
    setAttempt(frozen);
    setBusy(true);
    setError("");
    try {
      const result =
        frozen.kind === "delete"
          ? await deleteRoute(frozen.target, frozen.body)
          : frozen.kind === "update"
            ? await updateRoute(frozen.target!, frozen.body)
            : await createRoute(resource.id, frozen.body);
      if (!active.current) return;
      if (
        result.resource_id !== resource.id ||
        (frozen.target && result.id !== frozen.target)
      )
        throw new Error("Route scope mismatch");
      clear();
      setSelected(result.operation_id ?? "");
      await routes.refetch();
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
  return {
    resource,
    writable,
    blocked,
    actor,
    id,
    routes,
    offset,
    setOffset,
    history,
    target,
    hostname,
    setHostname,
    path,
    setPath,
    healthy,
    setHealthy,
    generated,
    setGenerated,
    withdraw,
    confirmed,
    setConfirmed,
    attempt,
    busy,
    denied,
    conflict,
    error,
    selected,
    setSelected,
    active,
    candidates,
    operation,
    admissionHeld,
    paused,
    refresh,
    clear,
    edit,
    submit,
  };
}

type SavedRoutesModel = Pick<
  ReturnType<typeof useRoutes>,
  "routes" | "setSelected" | "writable" | "attempt" | "paused" | "busy" | "edit"
>;
function SavedRoutes(viewModel: SavedRoutesModel) {
  const { routes, setSelected, writable, attempt, paused, busy, edit } =
    viewModel;

  return (routes.data ?? []).map((route) => (
    <article
      key={route.id}
      className="min-w-0 space-y-2 rounded-lg border p-2"
      aria-label={`Resource route ${route.id}`}
    >
      <dl className="space-y-1 break-all text-sm">
        <dt>Desired hostname and literal path</dt>
        <dd>
          {route.hostname}
          {route.path_prefix}
        </dd>
        <dt>Route ID / version</dt>
        <dd>
          {route.id} / {route.version}
        </dd>
        <dt>Desired route enabled</dt>
        <dd>{route.enabled ? "yes" : "no — withdrawal requested"}</dd>
        <dt>Selected healthy release</dt>
        <dd>{route.selected_release_operation_id}</dd>
        <dt>Route state</dt>
        <dd>{route.state}</dd>
        <dt>HTTP readiness recorded</dt>
        <dd>{route.http_ready ? "ready" : "not confirmed"}</dd>
        <dt>Trusted HTTPS readiness recorded</dt>
        <dd>{route.https_ready ? "ready" : "not confirmed"}</dd>
        <dt>Certificate status recorded</dt>
        <dd>{route.certificate.status || "not observed"}</dd>
        {route.certificate.issuer ? (
          <>
            <dt>Issuer recorded</dt>
            <dd>{route.certificate.issuer}</dd>
          </>
        ) : null}
        {route.certificate.expires_at ? (
          <>
            <dt>Certificate expiry recorded</dt>
            <dd>{route.certificate.expires_at}</dd>
          </>
        ) : null}
      </dl>
      <p className="text-xs">
        Desired changes do not prove loaded routing. Prior serving releases
        remain protected until a verified proxy generation commits. Readiness is
        recorded observation, not continuous monitoring.
      </p>
      {route.operation_id ? (
        <Button
          className="h-auto max-w-full whitespace-normal break-all"
          type="button"
          variant="outline"
          onClick={() => setSelected(route.operation_id!)}
        >
          View route operation {route.operation_id}
        </Button>
      ) : null}
      {writable && !attempt ? (
        <div className="flex flex-wrap gap-2">
          <Button
            className="h-auto max-w-full whitespace-normal break-all"
            type="button"
            variant="outline"
            disabled={paused || busy}
            onClick={() => edit(route, false)}
          >
            Edit route {route.id}
          </Button>
          <Button
            className="h-auto max-w-full whitespace-normal break-all"
            type="button"
            variant="outline"
            disabled={paused || busy || !route.enabled}
            onClick={() => edit(route, true)}
          >
            Review withdrawal {route.id}
          </Button>
        </div>
      ) : null}
    </article>
  ));
}

type RouteEditorModel = Pick<
  ReturnType<typeof useRoutes>,
  | "writable"
  | "resource"
  | "submit"
  | "withdraw"
  | "target"
  | "confirmed"
  | "busy"
  | "attempt"
  | "setConfirmed"
  | "generated"
  | "setGenerated"
  | "id"
  | "hostname"
  | "setHostname"
  | "path"
  | "setPath"
  | "healthy"
  | "setHealthy"
  | "candidates"
  | "history"
  | "setOffset"
  | "offset"
  | "paused"
  | "admissionHeld"
  | "conflict"
  | "refresh"
  | "active"
  | "clear"
>;
function RouteEditor(viewModel: RouteEditorModel) {
  const {
    writable,
    resource,
    submit,
    withdraw,
    target,
    confirmed,
    busy,
    attempt,
    healthy,
    candidates,
    paused,
    admissionHeld,
    conflict,
    refresh,
    active,
    clear,
  } = viewModel;

  if (!writable)
    return (
      <p className="text-xs">
        Project maintainer access is required to change routes.
      </p>
    );
  return (
    <form
      className="space-y-2 rounded-lg border p-3"
      aria-label={`Configure route for ${resource.name}`}
      onSubmit={(e) => void submit(e)}
    >
      <p className="break-all">
        {withdraw
          ? `Withdraw route ${target?.id} at ${target?.hostname}${target?.path_prefix}, version ${target?.version}. Prior routing may remain loaded until withdrawal completes.`
          : target
            ? `Update route ${target.id} at expected version ${target.version}.`
            : "Create a route at expected version 0."}
      </p>
      <RouteTargetSelection {...viewModel} />
      <Button
        className="h-auto max-w-full whitespace-normal break-all"
        type="submit"
        disabled={
          paused ||
          admissionHeld ||
          busy ||
          conflict ||
          (withdraw
            ? !confirmed
            : !attempt && !candidates.some((op) => op.id === healthy))
        }
      >
        {attempt
          ? "Retry identical route request"
          : withdraw
            ? "Request route withdrawal"
            : "Save desired route"}
      </Button>
      {attempt ? (
        <>
          <p className="break-all text-xs">
            Frozen {attempt.kind} request key: {attempt.body.idempotency_key} ·
            expected route version {attempt.body.expected_route_version}
          </p>
          <Button
            className="h-auto max-w-full whitespace-normal break-all"
            type="button"
            variant="outline"
            disabled={busy || paused}
            onClick={async () => {
              const refreshed = await refresh();
              if (active.current && refreshed) clear();
            }}
          >
            Review refreshed routes for a separate request
          </Button>
        </>
      ) : target ? (
        <Button
          className="h-auto max-w-full whitespace-normal break-all"
          type="button"
          variant="outline"
          disabled={busy}
          onClick={clear}
        >
          Cancel route edit
        </Button>
      ) : null}
    </form>
  );
}

type RouteTargetSelectionModel = Pick<
  ReturnType<typeof useRoutes>,
  | "withdraw"
  | "confirmed"
  | "busy"
  | "attempt"
  | "setConfirmed"
  | "generated"
  | "setGenerated"
  | "id"
  | "hostname"
  | "setHostname"
  | "path"
  | "setPath"
  | "healthy"
  | "setHealthy"
  | "candidates"
  | "history"
  | "setOffset"
  | "offset"
>;
function RouteTargetSelection(viewModel: RouteTargetSelectionModel) {
  const {
    withdraw,
    confirmed,
    busy,
    attempt,
    setConfirmed,
    generated,
    setGenerated,
    id,
    hostname,
    setHostname,
    path,
    setPath,
    healthy,
    setHealthy,
    candidates,
    history,
    setOffset,
    offset,
  } = viewModel;

  if (!withdraw)
    return (
      <>
        <label className="flex gap-2">
          <input
            type="checkbox"
            checked={generated}
            disabled={busy || Boolean(attempt)}
            onChange={(e) => setGenerated(e.target.checked)}
          />
          Request generated hostname
        </label>
        <p className="text-xs">
          Generated names require the administrator's configured domain and an
          active wildcard namespace grant for this project. Grants and generated
          names do not prove DNS ownership or TLS readiness.
        </p>
        {!generated ? (
          <>
            <label htmlFor={`${id}-host`}>Route hostname</label>
            <Input
              id={`${id}-host`}
              required
              value={hostname}
              disabled={busy || Boolean(attempt)}
              onChange={(e) => setHostname(e.target.value)}
            />
          </>
        ) : null}
        <label htmlFor={`${id}-path`}>Canonical literal path prefix</label>
        <Input
          id={`${id}-path`}
          required
          value={path}
          disabled={busy || Boolean(attempt)}
          onChange={(e) => setPath(e.target.value)}
        />
        <label htmlFor={`${id}-healthy`}>
          Expected settled healthy deployment
        </label>
        <select
          id={`${id}-healthy`}
          className="h-10 w-full min-w-0 rounded-xl border bg-card px-2 text-sm"
          required
          value={healthy}
          disabled={busy || Boolean(attempt)}
          onChange={(e) => setHealthy(e.target.value)}
        >
          <option value="">Select a settled healthy release</option>
          {candidates.map((op) => (
            <option key={op.id} value={op.id}>
              {op.id} ·{" "}
              {op.snapshot.local_build
                ? `Local build ${op.snapshot.local_build.artifact.build_id} · source generation ${op.snapshot.local_build.source_generation} · ${op.snapshot.local_build.artifact.image_id}`
                : op.snapshot.image_reference}
            </option>
          ))}
        </select>
        {candidates.length === 0 ? (
          <p role="status">
            No settled healthy release on this deployment page. Deploy and
            verify an image first, or browse deployment history. A saved
            application or queued deployment is insufficient.
          </p>
        ) : null}
        <PageNavigation
          label="route deployment prerequisites"
          page={history.data}
          loading={history.isFetching || busy}
          onFirst={() => setOffset(0)}
          onPrevious={() => setOffset(Math.max(0, offset - 20))}
          onNext={() => setOffset(Math.min(maximumPageOffset, offset + 20))}
        />
      </>
    );
  return (
    <label className="flex gap-2">
      <input
        type="checkbox"
        checked={confirmed}
        disabled={busy || Boolean(attempt)}
        onChange={(e) => setConfirmed(e.target.checked)}
      />
      I confirm withdrawal of this exact route.
    </label>
  );
}
