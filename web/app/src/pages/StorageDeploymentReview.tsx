import { allProjectMembersQuery, principalQuery } from "@/api";
import { ApiError } from "@/api/errors";
import type { Resource } from "@/api/hosting";
import { getCatalogInstallation } from "@/api/hosting-catalog";
import {
  getHostingOperation,
  requestHostingDeployment,
  type HostingDeploymentInput,
  type HostingOperation,
} from "@/api/hosting-deployments";
import { getHostingRuntimeConfiguration } from "@/api/hosting-runtime-config";
import type { HostingStorage } from "@/api/hosting-storage";
import {
  boundedStorageRequest,
  getStorageContinuityStatus,
  getStorageDeploymentOptions,
  getStorageDeploymentStorage,
  recoverStorageRouteContinuity,
  validStorageReadiness,
  type StorageContinuityStatus,
  type StorageDeploymentOptions,
  type StorageDeploymentScope,
  type StorageRouteRecoveryInput,
} from "@/api/hosting-storage-deployment";
import { BackupAccessBoundary } from "@/components/hosting/BackupAccessBoundary";
import {
  ImageRegistryCredential,
  type ImageRegistrySelection,
} from "@/components/hosting/ImageRegistryCredential";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { canMaintainProject } from "@/lib/permissions";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";

export function StorageContinuityDetail({
  scope,
  operation,
  recovery,
}: {
  scope: StorageDeploymentScope;
  operation: string;
  recovery?: { record: HostingOperation; writable: boolean; blocked: boolean };
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
      {status ? <ContinuityReceipt status={status} /> : null}
      {recovery && status ? (
        <StorageContinuityRecovery
          key={JSON.stringify([
            scope.actor,
            scope.project,
            scope.resource,
            operation,
          ])}
          scope={scope}
          operation={operation}
          record={recovery.record}
          status={status}
          blocked={recovery.blocked || detail.isError || detail.isFetching}
          writable={recovery.writable}
          onUpdated={() => {
            void detail.refetch();
          }}
        />
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

function continuityRecoveryInput(
  record: HostingOperation,
  scope: StorageDeploymentScope,
  operation: string,
): StorageRouteRecoveryInput | undefined {
  const descriptor = record.snapshot.storage_route_continuity;
  const receipt = record.complete_receipt;
  if (
    record.id !== operation ||
    record.resource_id !== scope.resource ||
    record.project_id !== scope.project ||
    record.snapshot.resource_id !== scope.resource ||
    record.state !== "succeeded" ||
    record.mutation_hold !== false ||
    record.restore_ownership_held !== false ||
    record.reconcile_requested !== false ||
    record.cleanup_plan ||
    record.stack_stop_plan ||
    record.result?.outcome !== "succeeded" ||
    record.result.ready !== true ||
    record.result.uncertain !== false ||
    record.result.restored !== false ||
    !descriptor ||
    !/^sha256:[a-f0-9]{64}$/.test(descriptor.hash) ||
    !receipt ||
    !/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(receipt.id) ||
    !/^sha256:[a-f0-9]{64}$/.test(receipt.body_hash)
  )
    return undefined;
  return {
    expected_descriptor_hash: descriptor.hash,
    expected_complete_receipt_id: receipt.id,
    expected_complete_receipt_hash: receipt.body_hash,
  };
}
function StorageContinuityRecovery(
  props: Omit<Parameters<typeof useContinuityRecovery>[0], "onDenied">,
) {
  const principal = useQuery({ ...principalQuery(), enabled: false });
  const members = useQuery({ ...allProjectMembersQuery(), enabled: false });
  const access =
    props.writable &&
    !principal.isPending &&
    !principal.isFetching &&
    !principal.isError &&
    !members.isPending &&
    !members.isFetching &&
    !members.isError &&
    principal.data &&
    JSON.stringify([principal.data.provider, principal.data.id]) ===
      props.scope.actor &&
    canMaintainProject(principal.data, members.data ?? [], props.scope.project);
  return (
    <BackupAccessBoundary message="Recovery permission is unavailable. Refresh access before another action.">
      {(onDenied) =>
        access ? (
          <ContinuityRecoverySession {...props} onDenied={onDenied} />
        ) : null
      }
    </BackupAccessBoundary>
  );
}
function ContinuityRecoverySession(
  props: Parameters<typeof useContinuityRecovery>[0],
) {
  const {
    denied,
    blocked,
    eligible,
    busy,
    sent,
    conflict,
    prepare,
    review,
    confirmed,
    setConfirmed,
    evidence,
    submit,
    message,
  } = useContinuityRecovery(props);

  if (denied)
    return (
      <p role="alert">
        Recovery permission is unavailable. Refresh access before another
        action.
      </p>
    );
  if (blocked || !eligible) return null;
  return (
    <section
      className="min-w-0 space-y-2 break-all"
      aria-label="Proxy continuity recovery"
    >
      <p>
        Recovery retries only the proxy queue for this completed deployment. It
        does not deploy containers or recreate data volumes.
      </p>
      <Button
        type="button"
        variant="outline"
        disabled={busy || (sent && !conflict)}
        onClick={() => void prepare()}
      >
        Review proxy continuity recovery
      </Button>
      {review ? (
        <>
          <p>
            Deployment receipt {review.expected_complete_receipt_id} ·
            descriptor {review.expected_descriptor_hash}.
          </p>
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              checked={confirmed}
              disabled={busy || sent}
              onChange={(event) => setConfirmed(event.target.checked)}
            />
            <span>
              I reviewed this completed deployment and want to retry its proxy
              continuity queue.
            </span>
          </label>
          <Button
            type="button"
            disabled={
              busy ||
              conflict ||
              !confirmed ||
              JSON.stringify(evidence) !== JSON.stringify(review)
            }
            onClick={() => void submit()}
          >
            {sent
              ? "Retry identical proxy continuity recovery"
              : "Retry proxy continuity queue"}
          </Button>
        </>
      ) : null}
      {message ? <p role="status">{message}</p> : null}
    </section>
  );
}
function useContinuityRecovery({
  scope,
  operation,
  record,
  status,
  blocked,
  writable,
  onUpdated,
  onDenied,
}: {
  scope: StorageDeploymentScope;
  operation: string;
  record: HostingOperation;
  status: StorageContinuityStatus;
  blocked: boolean;
  writable: boolean;
  onUpdated: () => void;
  onDenied: () => void;
}) {
  const [review, setReview] = useState<StorageRouteRecoveryInput>();
  const [confirmed, setConfirmed] = useState(false),
    [sent, setSent] = useState(false),
    [busy, setBusy] = useState(false),
    [conflict, setConflict] = useState(false),
    [denied, setDenied] = useState(false);
  const [message, setMessage] = useState("");
  const active = useRef(true),
    request = useRef<AbortController | null>(null);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      request.current?.abort();
    };
  }, []);
  const evidence = continuityRecoveryInput(record, scope, operation);
  const eligible =
    Boolean(evidence) &&
    (sent ||
      (status.state === "unresolved" && !status.successor_proxy_operation_id));
  useEffect(() => {
    if (blocked || denied) request.current?.abort();
  }, [blocked, denied]);
  async function prepare() {
    if (blocked || denied || busy || !eligible || (sent && !conflict)) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setMessage("");
    try {
      const current = await getHostingOperation(operation, {
        signal: controller.signal,
      });
      const currentStatus = await getStorageContinuityStatus(
        scope,
        operation,
        controller.signal,
      );
      const input = continuityRecoveryInput(current, scope, operation);
      if (!active.current || controller.signal.aborted) return;
      if (
        !input ||
        currentStatus.state !== "unresolved" ||
        currentStatus.successor_proxy_operation_id ||
        JSON.stringify(input) !== JSON.stringify(evidence)
      )
        throw new Error(
          "Recovery evidence changed. Refresh operation and continuity evidence before reviewing again.",
        );
      setReview(input);
      setConfirmed(false);
      setSent(false);
      setConflict(false);
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      if (cause instanceof ApiError && [401, 403, 404].includes(cause.status)) {
        setDenied(true);
        onDenied();
      }
      setMessage(
        "Unable to review current recovery evidence. Refresh operation and continuity evidence before another request.",
      );
    } finally {
      if (request.current === controller) request.current = null;
      if (active.current) setBusy(false);
    }
  }
  async function submit() {
    if (
      blocked ||
      denied ||
      busy ||
      conflict ||
      !review ||
      !confirmed ||
      !eligible ||
      JSON.stringify(evidence) !== JSON.stringify(review)
    )
      return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setSent(true);
    setMessage("");
    try {
      const result = await recoverStorageRouteContinuity(
        scope,
        operation,
        review,
        controller.signal,
      );
      if (!active.current || controller.signal.aborted) return;
      if (
        !["proxy_pending", "loaded", "proxy_failed"].includes(result.state) ||
        result.successor_proxy_operation_id !== `pop_storage_${operation}`
      )
        throw new Error(
          "Recovery response did not confirm a linked proxy operation",
        );
      setReview(undefined);
      setConfirmed(false);
      setSent(false);
      setMessage(
        "Proxy continuity queue request confirmed. Review its recorded status; public traffic may still be pending.",
      );
      onUpdated();
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      if (cause instanceof ApiError && [401, 403, 404].includes(cause.status)) {
        setDenied(true);
        onDenied();
        setMessage(
          "Recovery permission is unavailable. Refresh access before another action.",
        );
      } else if (
        cause instanceof ApiError &&
        [400, 409].includes(cause.status)
      ) {
        setConflict(true);
        setMessage(
          "Recovery conflicts with current evidence. Refresh operation and continuity evidence and explicitly review again.",
        );
      } else
        setMessage(
          "Recovery delivery is unconfirmed. Retry the identical reviewed recovery request; no deployment is repeated.",
        );
    } finally {
      if (request.current === controller) request.current = null;
      if (active.current) setBusy(false);
    }
  }
  return {
    denied,
    blocked,
    eligible,
    busy,
    sent,
    conflict,
    prepare,
    review,
    confirmed,
    setConfirmed,
    evidence,
    submit,
    message,
  };
}

export function StorageDeploymentReview(
  props: Parameters<typeof useStorageDeployment>[0],
) {
  const {
    initial,
    scope,
    resource,
    blocked,
    busy,
    review,
    refreshing,
    setRegistrySelection,
    onDenied,
    formID,
    prepare,
    hostPort,
    setHostPort,
    containerPort,
    setContainerPort,
    readinessPath,
    setReadinessPath,
    readinessTimeout,
    setReadinessTimeout,
    shown,
    mutationHeld,
    registrySelection,
    sent,
    conflict,
    confirmed,
    setConfirmed,
    submit,
    acceptedID,
    refreshError,
    refreshAccepted,
    error,
  } = useStorageDeployment(props);

  return (
    <section
      className="min-w-0 space-y-3 rounded-lg border p-3"
      aria-label="Persistent upload deployment review"
    >
      <h6 className="font-medium">Deploy image with persistent uploads</h6>
      {initial ? (
        <p>
          This first deployment creates the configured platform-owned upload
          volume when needed and preserves existing or restored data. Data
          retention is retain. Image rollback does not undo upload writes.
          Public routes and HTTPS are not created by this request.
        </p>
      ) : (
        <p>
          The previous writer must stop before replacement readiness,
          interrupting service. The retained upload volume is reused. Image
          rollback does not undo writes made to uploads.
        </p>
      )}
      <StorageDeploymentAdmission
        scope={scope}
        resource={resource}
        blocked={blocked}
        busy={busy}
        review={review}
        refreshing={refreshing}
        setRegistrySelection={setRegistrySelection}
        onDenied={onDenied}
        initial={initial}
        formID={formID}
        prepare={prepare}
        hostPort={hostPort}
        setHostPort={setHostPort}
        containerPort={containerPort}
        setContainerPort={setContainerPort}
        readinessPath={readinessPath}
        setReadinessPath={setReadinessPath}
        readinessTimeout={readinessTimeout}
        setReadinessTimeout={setReadinessTimeout}
        shown={shown}
        mutationHeld={mutationHeld}
        registrySelection={registrySelection}
        sent={sent}
        conflict={conflict}
      />
      {review ? (
        <StorageDeploymentEvidence
          resource={resource}
          review={review}
          shown={shown}
          initial={initial}
          confirmed={confirmed}
          busy={busy}
          sent={sent}
          setConfirmed={setConfirmed}
          blocked={blocked}
          mutationHeld={mutationHeld}
          conflict={conflict}
          submit={submit}
        />
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
function useStorageDeployment({
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
  const [registrySelection, setRegistrySelection] =
    useState<ImageRegistrySelection>({ valid: true });
  const [hostPort, setHostPort] = useState("");
  const [readinessDraft, setReadinessDraft] = useState<{
    containerPort: string;
    readinessPath: string;
    readinessTimeout: string;
  }>();
  const catalog = useQuery({
    queryKey: ["catalogInstallation", scope.actor, resource.id],
    queryFn: ({ signal }) => getCatalogInstallation(resource.id, signal),
    retry: false,
    gcTime: 0,
  });
  const definition =
    catalog.data?.resource.project_id === scope.project
      ? catalog.data.template.definition
      : undefined;
  const readiness = readinessDraft ?? {
    containerPort: definition ? String(definition.container_port) : "",
    readinessPath: definition?.readiness.path ?? "",
    readinessTimeout: definition
      ? String(definition.readiness.timeout_seconds)
      : "",
  };
  const { containerPort, readinessPath, readinessTimeout } = readiness;
  const setContainerPort = (value: string) =>
    setReadinessDraft({ ...readiness, containerPort: value });
  const setReadinessPath = (value: string) =>
    setReadinessDraft({ ...readiness, readinessPath: value });
  const setReadinessTimeout = (value: string) =>
    setReadinessDraft({ ...readiness, readinessTimeout: value });

  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [error, setError] = useState("");
  const [acceptedID, setAcceptedID] = useState("");
  const [refreshError, setRefreshError] = useState("");
  const [refreshingRequest, setRefreshingRequest] =
    useState<AbortController | null>(null);
  const refreshing = refreshingRequest !== null;
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
    if (
      request.current ||
      blocked ||
      mutationHeld ||
      refreshing ||
      !registrySelection.valid
    )
      return;
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
        runtime.runtime_generation < 0
      )
        throw new Error("Saved runtime configuration identity is invalid.");
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
        ...(registrySelection.credential
          ? { registry_credential: { ...registrySelection.credential } }
          : {}),
        expected_generation: current.resource_generation,
        ...(runtime.runtime_generation > 0
          ? {
              expected_runtime_configuration_generation:
                runtime.runtime_generation,
            }
          : {}),
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
    setRefreshingRequest(controller);
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
      }
      setRefreshingRequest((current) =>
        current === controller ? null : current,
      );
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
        (review.input.expected_runtime_configuration_generation !== undefined &&
          operation.snapshot.runtime_configuration?.generation !==
            review.input.expected_runtime_configuration_generation) ||
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
  return {
    initial,
    scope,
    resource,
    blocked,
    busy,
    review,
    refreshing,
    setRegistrySelection,
    onDenied,
    formID,
    prepare,
    hostPort,
    setHostPort,
    containerPort,
    setContainerPort,
    readinessPath,
    setReadinessPath,
    readinessTimeout,
    setReadinessTimeout,
    shown,
    mutationHeld,
    registrySelection,
    sent,
    conflict,
    confirmed,
    setConfirmed,
    submit,
    acceptedID,
    refreshError,
    refreshAccepted,
    error,
  };
}

function ContinuityReceipt({ status }: { status: StorageContinuityStatus }) {
  return (
    <>
      <p>Recorded continuity state: {status.state}</p>
      {status.state === "loaded" ? (
        <p>
          Historical loaded receipt for generation {status.loaded_generation}.
          This does not confirm current routes, certificate freshness, or HTTPS
          health.
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
          Continuity reservation released. This does not confirm public traffic.
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
  );
}

function StorageDeploymentConfiguration({
  formID,
  prepare,
  hostPort,
  busy,
  review,
  setHostPort,
  containerPort,
  setContainerPort,
  readinessPath,
  setReadinessPath,
  readinessTimeout,
  setReadinessTimeout,
}: {
  formID: string;
  prepare: () => Promise<void>;
  hostPort: string;
  busy: boolean;
  review: ReturnType<typeof useStorageDeployment>["review"];
  setHostPort: React.Dispatch<React.SetStateAction<string>>;
  containerPort: string;
  setContainerPort: (value: string) => void;
  readinessPath: string;
  setReadinessPath: (value: string) => void;
  readinessTimeout: string;
  setReadinessTimeout: (value: string) => void;
}) {
  return (
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
  );
}

function StorageDeploymentEvidence({
  resource,
  review,
  shown,
  initial,
  confirmed,
  busy,
  sent,
  setConfirmed,
  blocked,
  mutationHeld,
  conflict,
  submit,
}: {
  resource: Resource;
  review: NonNullable<ReturnType<typeof useStorageDeployment>["review"]>;
  shown: StorageDeploymentOptions;
  initial: boolean;
  confirmed: boolean;
  busy: boolean;
  sent: boolean;
  setConfirmed: React.Dispatch<React.SetStateAction<boolean>>;
  blocked: boolean;
  mutationHeld: boolean;
  conflict: boolean;
  submit: () => Promise<void>;
}) {
  return (
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
          {shown.storage_ref?.storage_id} / {shown.storage_ref?.generation} /{" "}
          {shown.storage_ref?.hash}
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
              deployment failure, image rollback and retained-container cleanup.
            </dd>
          </>
        ) : null}
        {initial ? (
          <>
            <dt>Deployment stage</dt>
            <dd>First deployment; no previous healthy writer is selected.</dd>
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
            {route.path_prefix} · {route.id} · version {route.version} · release{" "}
            {route.selected_release_operation_id}
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
          blocked || (mutationHeld && !sent) || busy || conflict || !confirmed
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
  );
}

function StorageDeploymentAdmission({
  scope,
  resource,
  blocked,
  busy,
  review,
  refreshing,
  setRegistrySelection,
  onDenied,
  initial,
  formID,
  prepare,
  hostPort,
  setHostPort,
  containerPort,
  setContainerPort,
  readinessPath,
  setReadinessPath,
  readinessTimeout,
  setReadinessTimeout,
  shown,
  mutationHeld,
  registrySelection,
  sent,
  conflict,
}: {
  scope: StorageDeploymentScope;
  resource: ReturnType<typeof useStorageDeployment>["resource"];
  blocked: boolean;
  busy: boolean;
  review: ReturnType<typeof useStorageDeployment>["review"];
  refreshing: boolean;
  setRegistrySelection: ReturnType<
    typeof useStorageDeployment
  >["setRegistrySelection"];
  onDenied: () => void;
  initial: boolean;
  formID: string;
  prepare: () => Promise<void>;
  hostPort: string;
  setHostPort: React.Dispatch<React.SetStateAction<string>>;
  containerPort: string;
  setContainerPort: (value: string) => void;
  readinessPath: string;
  setReadinessPath: (value: string) => void;
  readinessTimeout: string;
  setReadinessTimeout: (value: string) => void;
  shown: StorageDeploymentOptions;
  mutationHeld: boolean;
  registrySelection: ImageRegistrySelection;
  sent: boolean;
  conflict: boolean;
}) {
  return (
    <>
      <ImageRegistryCredential
        actor={scope.actor}
        project={scope.project}
        image={
          resource.source.kind === "image"
            ? resource.source.image.reference
            : ""
        }
        disabled={blocked || busy || !!review || refreshing}
        onChange={setRegistrySelection}
        onDenied={onDenied}
      />
      {initial ? (
        <StorageDeploymentConfiguration
          formID={formID}
          prepare={prepare}
          hostPort={hostPort}
          busy={busy}
          review={review}
          setHostPort={setHostPort}
          containerPort={containerPort}
          setContainerPort={setContainerPort}
          readinessPath={readinessPath}
          setReadinessPath={setReadinessPath}
          readinessTimeout={readinessTimeout}
          setReadinessTimeout={setReadinessTimeout}
        />
      ) : null}
      {!shown.available ? (
        <p role="status">
          Storage deployment unavailable: {shown.reason}. Server authority must
          resolve this condition before a new review.
        </p>
      ) : null}
      <StorageDeploymentReviewAction
        initial={initial}
        formID={formID}
        blocked={blocked}
        mutationHeld={mutationHeld}
        busy={busy}
        refreshing={refreshing}
        registrySelection={registrySelection}
        sent={sent}
        conflict={conflict}
        prepare={prepare}
      />
    </>
  );
}

function StorageDeploymentReviewAction({
  initial,
  formID,
  blocked,
  mutationHeld,
  busy,
  refreshing,
  registrySelection,
  sent,
  conflict,
  prepare,
}: {
  initial: boolean;
  formID: string;
  blocked: boolean;
  mutationHeld: boolean;
  busy: boolean;
  refreshing: boolean;
  registrySelection: ImageRegistrySelection;
  sent: boolean;
  conflict: boolean;
  prepare: () => Promise<void>;
}) {
  return (
    <Button
      type={initial ? "submit" : "button"}
      form={initial ? formID : undefined}
      variant="outline"
      className="h-auto max-w-full whitespace-normal"
      disabled={
        blocked ||
        mutationHeld ||
        busy ||
        refreshing ||
        !registrySelection.valid ||
        (sent && !conflict)
      }
      onClick={initial ? undefined : () => void prepare()}
    >
      {conflict
        ? "Review current storage and routes for a new request"
        : "Review current storage deployment"}
    </Button>
  );
}
