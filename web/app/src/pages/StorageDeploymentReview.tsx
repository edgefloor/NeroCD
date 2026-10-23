import { useEffect, useId, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ApiError } from "@/api/errors";
import { getHostingRuntimeConfiguration } from "@/api/hosting-runtime-config";
import {
  requestHostingDeployment,
  type HostingDeploymentInput,
} from "@/api/hosting-deployments";
import {
  boundedStorageRequest,
  getStorageContinuityStatus,
  getStorageDeploymentOptions,
  getStorageDeploymentStorage,
  validStorageReadiness,
  type StorageDeploymentOptions,
  type StorageDeploymentScope,
} from "@/api/hosting-storage-deployment";
import type { HostingStorage } from "@/api/hosting-storage";
import { Input } from "@/components/ui/input";
import type { Resource } from "@/api/hosting";
import { Button } from "@/components/ui/button";

export function StorageContinuityDetail({
  scope,
  operation,
}: {
  scope: StorageDeploymentScope;
  operation: string;
}) {
  const detail = useQuery({
    queryKey: [
      "storageContinuity",
      scope.actor,
      scope.project,
      scope.resource,
      operation,
    ],
    retry: false,
    gcTime: 0,
    queryFn: ({ signal }) =>
      getStorageContinuityStatus(scope, operation, signal),
    refetchInterval: (query) =>
      query.state.data &&
      ["reserved", "proxy_pending"].includes(query.state.data.state)
        ? 5000
        : false,
  });
  const status = detail.data;
  return (
    <section
      className="min-w-0 space-y-2 break-all rounded-lg border p-3"
      aria-label="Storage route continuity"
    >
      <h6 className="font-medium">Public route continuity</h6>
      <p>Workload completion and proxy continuity are separate results.</p>
      {detail.isPending ? (
        <p role="status">Loading continuity evidence...</p>
      ) : null}
      {detail.isError ? (
        <p role="alert">
          Continuity evidence is unavailable. Public traffic is unconfirmed; any
          earlier receipt may be stale.
        </p>
      ) : null}
      {status ? (
        <>
          <p>Recorded continuity state: {status.state}</p>
          {status.state === "loaded" ? (
            <p>
              Historical loaded receipt for generation{" "}
              {status.loaded_generation}. This does not confirm current routes,
              certificate freshness, or HTTPS health.
            </p>
          ) : status.state === "proxy_failed" ? (
            <p role="alert">
              Proxy continuity failed. Workload success does not confirm public
              traffic.
            </p>
          ) : status.state === "unresolved" ? (
            <p role="alert">
              Continuity is unresolved. Public traffic remains unconfirmed.
            </p>
          ) : status.state === "released" ? (
            <p>
              Continuity reservation released. This does not confirm public
              traffic.
            </p>
          ) : (
            <p role="status">
              Proxy continuity is pending. Public traffic remains unconfirmed.
            </p>
          )}
          {status.successor_proxy_operation_id ? (
            <p>Proxy operation: {status.successor_proxy_operation_id}</p>
          ) : null}
        </>
      ) : null}
      <Button
        type="button"
        variant="outline"
        className="h-auto max-w-full whitespace-normal"
        disabled={detail.isFetching}
        onClick={() => void detail.refetch()}
      >
        Refresh continuity evidence
      </Button>
    </section>
  );
}

export function StorageDeploymentReview({
  scope,
  resource,
  bindingID,
  options,
  blocked,
  mutationHeld,
  onDenied,
  onSelected,
  onRefresh,
}: {
  scope: StorageDeploymentScope;
  resource: Resource;
  bindingID: string;
  options: StorageDeploymentOptions;
  blocked: boolean;
  mutationHeld: boolean;
  onDenied: () => void;
  onSelected: (id: string) => void;
  onRefresh: (signal: AbortSignal) => Promise<void>;
}) {
  const [review, setReview] = useState<{
    options: StorageDeploymentOptions;
    input: HostingDeploymentInput;
    storage: HostingStorage;
  }>();
  const formID = useId();
  const [hostPort, setHostPort] = useState("");
  const [containerPort, setContainerPort] = useState("");
  const [readinessPath, setReadinessPath] = useState("");
  const [readinessTimeout, setReadinessTimeout] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [error, setError] = useState("");
  const [acceptedID, setAcceptedID] = useState("");
  const [refreshError, setRefreshError] = useState("");
  const [refreshing, setRefreshing] = useState(false);
  const refreshRequest = useRef<AbortController | null>(null);
  const request = useRef<AbortController | null>(null);
  const active = useRef(true);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      request.current?.abort();
      refreshRequest.current?.abort();
    };
  }, []);
  async function prepare() {
    if (request.current || blocked || mutationHeld || refreshing) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    setAcceptedID("");
    setRefreshError("");
    setReview(undefined);
    setConfirmed(false);
    setSent(false);
    setConflict(false);
    try {
      const current = await getStorageDeploymentOptions(
        scope,
        controller.signal,
      );
      if (!current.available)
        throw new Error(
          `Storage deployment unavailable: ${current.reason}. Refresh after the server resolves this condition.`,
        );
      if (current.resource_generation !== resource.generation)
        throw new Error(
          "Saved resource changed. Refresh the application before reviewing again.",
        );
      const { runtime_configuration: runtime } = await boundedStorageRequest(
        (signal) => getHostingRuntimeConfiguration(resource.id, { signal }),
        controller.signal,
      );
      if (
        runtime.project_id !== scope.project ||
        runtime.resource_id !== scope.resource ||
        !Number.isSafeInteger(runtime.runtime_generation) ||
        runtime.runtime_generation !== 0
      )
        throw new Error(
          "Storage deployment requires the supported untouched runtime configuration.",
        );
      if (!active.current || controller.signal.aborted) return;
      const initial = current.reason === "initial_deployment";
      const settings = initial
        ? {
            host_port: Number(hostPort),
            container_port: Number(containerPort),
            readiness: {
              path: readinessPath,
              timeout_seconds: Number(readinessTimeout),
            },
          }
        : {
            host_port: current.host_port!,
            container_port: current.container_port!,
            readiness: { ...current.readiness! },
          };
      if (
        initial &&
        (!Number.isInteger(settings.host_port) ||
          settings.host_port < 1024 ||
          settings.host_port > 65535 ||
          !Number.isInteger(settings.container_port) ||
          settings.container_port < 1 ||
          settings.container_port > 65535 ||
          !validStorageReadiness(
            settings.readiness.path,
            settings.readiness.timeout_seconds,
          ))
      )
        throw new Error(
          "Enter an explicit host port, container port, origin-relative readiness path and timeout before reviewing the first deployment.",
        );
      const storage = await getStorageDeploymentStorage(
        scope,
        current,
        bindingID,
        controller.signal,
      );
      if (!active.current || controller.signal.aborted) return;
      const input: HostingDeploymentInput = {
        expected_generation: current.resource_generation,
        idempotency_key: crypto.randomUUID(),
        ...settings,
        ...(current.storage_route_continuity
          ? {
              storage_route_continuity: structuredClone(
                current.storage_route_continuity,
              ),
            }
          : {}),
      };
      setReview({
        options: structuredClone(current),
        input,
        storage: structuredClone(storage),
      });
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      if (cause instanceof ApiError && [401, 403, 404].includes(cause.status))
        onDenied();
      else
        setError(
          cause instanceof Error
            ? cause.message
            : "Unable to load deployment review.",
        );
    } finally {
      if (request.current === controller) request.current = null;
      if (active.current) setBusy(false);
    }
  }
  async function refreshAccepted(id: string) {
    refreshRequest.current?.abort();
    const controller = new AbortController();
    refreshRequest.current = controller;
    setRefreshing(true);
    setRefreshError("");
    try {
      await boundedStorageRequest(
        (signal) => onRefresh(signal),
        controller.signal,
      );
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      if (cause instanceof ApiError && [401, 403, 404].includes(cause.status)) {
        onDenied();
        return;
      }
      setRefreshError(
        `Deployment request ${id} was accepted, but history could not be refreshed. Refresh history to inspect its recorded state.`,
      );
    } finally {
      if (refreshRequest.current === controller) {
        refreshRequest.current = null;
        if (active.current) setRefreshing(false);
      }
    }
  }
  async function submit() {
    if (
      !review ||
      !confirmed ||
      blocked ||
      (mutationHeld && !sent) ||
      conflict ||
      request.current
    )
      return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    setSent(true);
    let accepted = "";
    try {
      const operation = await boundedStorageRequest(
        (signal) =>
          requestHostingDeployment(resource.id, review.input, { signal }),
        controller.signal,
      );
      if (!active.current || controller.signal.aborted) return;
      if (
        !operation.id ||
        operation.project_id !== scope.project ||
        operation.resource_id !== scope.resource ||
        operation.binding_id !== bindingID ||
        operation.snapshot.resource_generation !==
          review.input.expected_generation ||
        operation.snapshot.host_port !== review.input.host_port ||
        operation.snapshot.container_port !== review.input.container_port ||
        operation.snapshot.readiness.path !== review.input.readiness.path ||
        operation.snapshot.readiness.timeout_seconds !==
          review.input.readiness.timeout_seconds ||
        operation.snapshot.storage?.storage_id !==
          review.options.storage_ref?.storage_id ||
        operation.snapshot.storage?.generation !==
          review.options.storage_ref?.generation ||
        operation.snapshot.storage?.hash !== review.options.storage_ref?.hash ||
        Boolean(operation.snapshot.storage_route_continuity) !==
          Boolean(review.input.storage_route_continuity)
      )
        throw new Error(
          "Deployment receipt does not match reviewed storage. Delivery remains unconfirmed.",
        );
      accepted = operation.id;
      setAcceptedID(operation.id);
      setReview(undefined);
      setConfirmed(false);
      setSent(false);
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      if (cause instanceof ApiError && [401, 403, 404].includes(cause.status)) {
        onDenied();
        return;
      }
      if (cause instanceof ApiError && cause.status === 409) {
        setConflict(true);
        setError(
          "The reviewed plan is stale or conflicts with server authority. Nothing was resubmitted. Explicitly review current storage and routes for a separate request.",
        );
      } else
        setError(
          "Delivery is unconfirmed. The deployment may already exist. Retry the identical reviewed request to recover its operation.",
        );
    } finally {
      if (request.current === controller) request.current = null;
      if (active.current) setBusy(false);
    }
    if (accepted && active.current && !controller.signal.aborted) {
      onSelected(accepted);
      void refreshAccepted(accepted);
    }
  }
  const shown = review?.options ?? options;
  const initial = shown.reason === "initial_deployment";
  return (
    <section
      className="min-w-0 space-y-3 rounded-lg border p-3"
      aria-label="Persistent upload deployment review"
    >
      <h6 className="font-medium">Deploy image with persistent uploads</h6>
      {initial ? (
        <p>
          This first deployment creates the configured platform-owned upload
          volume. Data retention is retain. Existing uploads and image files are
          not imported. Image rollback does not undo upload writes. Public
          routes and HTTPS are not created by this request.
        </p>
      ) : (
        <p>
          The previous writer must stop before replacement readiness,
          interrupting service. The retained upload volume is reused. Image
          rollback does not undo writes made to uploads.
        </p>
      )}
      {initial ? (
        <form
          id={formID}
          className="grid min-w-0 gap-3 sm:grid-cols-2"
          aria-label="Configure first storage deployment"
          onSubmit={(event) => {
            event.preventDefault();
            void prepare();
          }}
        >
          <label className="space-y-1">
            Runner-local host port
            <Input
              type="number"
              min={1024}
              max={65535}
              required
              value={hostPort}
              disabled={busy || Boolean(review)}
              onChange={(event) => setHostPort(event.target.value)}
            />
          </label>
          <label className="space-y-1">
            Container port
            <Input
              type="number"
              min={1}
              max={65535}
              required
              value={containerPort}
              disabled={busy || Boolean(review)}
              onChange={(event) => setContainerPort(event.target.value)}
            />
          </label>
          <label className="space-y-1">
            Readiness path
            <Input
              required
              maxLength={1024}
              value={readinessPath}
              disabled={busy || Boolean(review)}
              onChange={(event) => setReadinessPath(event.target.value)}
            />
          </label>
          <label className="space-y-1">
            Readiness timeout in seconds
            <Input
              type="number"
              min={1}
              max={120}
              required
              value={readinessTimeout}
              disabled={busy || Boolean(review)}
              onChange={(event) => setReadinessTimeout(event.target.value)}
            />
          </label>
        </form>
      ) : null}
      {!shown.available ? (
        <p role="status">
          Storage deployment unavailable: {shown.reason}. Server authority must
          resolve this condition before a new review.
        </p>
      ) : null}
      <Button
        type={initial ? "submit" : "button"}
        form={initial ? formID : undefined}
        variant="outline"
        className="h-auto max-w-full whitespace-normal"
        disabled={
          blocked || mutationHeld || busy || refreshing || (sent && !conflict)
        }
        onClick={initial ? undefined : () => void prepare()}
      >
        {conflict
          ? "Review current storage and routes for a new request"
          : "Review current storage deployment"}
      </Button>
      {review ? (
        <>
          <dl className="space-y-1 break-all text-sm">
            <dt>Saved image / generation</dt>
            <dd>
              {resource.source.kind === "image"
                ? resource.source.image.reference
                : "unavailable"}{" "}
              / {review.input.expected_generation}
            </dd>
            <dt>Retained storage identity / generation / hash</dt>
            <dd>
              {shown.storage_ref?.storage_id} / {shown.storage_ref?.generation}{" "}
              / {shown.storage_ref?.hash}
            </dd>
            {review.storage ? (
              <>
                <dt>Platform-owned upload volume</dt>
                <dd>{review.storage.volume_name}</dd>
                <dt>Container mount path</dt>
                <dd>{review.storage.mount_path}</dd>
                <dt>Data retention</dt>
                <dd>
                  {review.storage.retention}. Application data remains after
                  deployment failure, image rollback and retained-container
                  cleanup.
                </dd>
              </>
            ) : null}
            {initial ? (
              <>
                <dt>Deployment stage</dt>
                <dd>
                  First deployment; no previous healthy writer is selected.
                </dd>
              </>
            ) : (
              <>
                <dt>Previous healthy writer</dt>
                <dd>{shown.previous_healthy_operation_id}</dd>
              </>
            )}
            <dt>Fixed runner-local endpoint</dt>
            <dd>
              127.0.0.1:{String(review.input.host_port)} → container{" "}
              {review.input.container_port}
            </dd>
            <dt>Readiness</dt>
            <dd>
              {review.input.readiness.path} /{" "}
              {review.input.readiness.timeout_seconds}s
            </dd>
            <dt>Request key</dt>
            <dd>{review.input.idempotency_key}</dd>
          </dl>
          <p>
            Complete owned route selection{" "}
            {shown.storage_route_continuity
              ? `from loaded generation ${shown.storage_route_continuity.expected_loaded_generation}, proxy operation ${shown.storage_route_continuity.expected_loaded_proxy_operation_id}`
              : "contains no public routes"}
            :
          </p>
          <ul className="space-y-1 break-all text-sm">
            {shown.routes.map((route) => (
              <li key={route.id}>
                {route.hostname}
                {route.path_prefix} · {route.id} · version {route.version} ·
                release {route.selected_release_operation_id}
              </li>
            ))}
          </ul>
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              checked={confirmed}
              disabled={busy || sent}
              onChange={(event) => setConfirmed(event.target.checked)}
            />
            <span>
              {initial
                ? "I reviewed this exact image, platform-owned upload volume, mount path, retain policy, ports and readiness. I understand existing uploads are not imported and image rollback does not undo upload writes."
                : "I reviewed this exact image, retained upload volume, mount path, retain policy, fixed port and complete route selection. I accept interruption and understand image rollback does not undo upload writes."}
            </span>
          </label>
          <Button
            type="button"
            className="h-auto whitespace-normal"
            disabled={
              blocked ||
              (mutationHeld && !sent) ||
              busy ||
              conflict ||
              !confirmed
            }
            onClick={() => void submit()}
          >
            {busy
              ? "Requesting storage deployment..."
              : sent
                ? "Retry identical storage deployment request"
                : "Deploy reviewed image with uploads"}
          </Button>
          {sent ? (
            <p>
              The exact request body and key are retained. A separate request
              requires an explicit fresh review after conflict.
            </p>
          ) : null}
        </>
      ) : null}
      {acceptedID ? (
        <p role="status" className="break-all">
          Deployment request accepted: {acceptedID}. Acceptance does not confirm
          workload completion or public traffic.
        </p>
      ) : null}
      {refreshing ? (
        <p role="status">Refreshing accepted deployment history...</p>
      ) : null}
      {refreshError ? <p role="alert">{refreshError}</p> : null}
      {acceptedID && refreshError ? (
        <Button
          type="button"
          variant="outline"
          className="h-auto max-w-full whitespace-normal"
          disabled={refreshing}
          onClick={() => void refreshAccepted(acceptedID)}
        >
          Refresh accepted deployment history
        </Button>
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
    </section>
  );
}
