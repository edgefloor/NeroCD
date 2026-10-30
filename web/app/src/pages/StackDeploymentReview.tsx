import { ApiError } from "@/api/errors";
import type { Resource } from "@/api/hosting";
import { getCatalogInstallation } from "@/api/hosting-catalog";
import {
  HostingDeploymentError,
  requestHostingDeployment,
  type HostingDeploymentInput,
  type HostingOperation,
} from "@/api/hosting-deployments";
import { getHostingRuntimeConfiguration } from "@/api/hosting-runtime-config";
import {
  boundedStorageRequest,
  getStorageDeploymentOptions,
  validStorageReadiness,
  type StorageDeploymentOptions,
  type StorageDeploymentScope,
} from "@/api/hosting-storage-deployment";
import {
  ImageRegistryCredential,
  type ImageRegistrySelection,
} from "@/components/hosting/ImageRegistryCredential";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";

type Review = {
  input: HostingDeploymentInput;
  options: StorageDeploymentOptions;
  runtimeNames: string[];
};

export function StackDeploymentReview(input: {
  resource: Resource;
  scope: StorageDeploymentScope;
  bindingID: string;
  options: StorageDeploymentOptions;
  blocked: boolean;
  mutationHeld: boolean;
  onDenied: () => void;
  onSelected: (id: string) => void;
  onRefresh: (signal?: AbortSignal) => Promise<void>;
}) {
  const viewModel = useStackDeploymentReview(input);
  const {
    scope,
    options,
    onDenied,
    source,
    setRegistrySelection,
    review,
    busy,
    error,
    accepted,
    starting,
    paused,
  } = viewModel;
  if (!source) return null;
  return (
    <section
      className="min-w-0 space-y-3 rounded-lg border p-3"
      aria-label="Saved Compose deployment review"
    >
      <h6 className="font-medium">
        {starting ? "Start saved Compose" : "Deploy saved Compose"}
      </h6>
      <p>
        Deploy all saved components together. Public component:{" "}
        {source.public_component}, container port {source.container_port}.
      </p>
      <p className="text-sm">
        Save required variables and secret references in Runtime configuration
        first. They are checked before the current stack stops.
      </p>
      {starting ? (
        <p className="text-sm">
          Start recreates containers using saved settings and retained volumes.
          Review runtime settings and routes before starting.
        </p>
      ) : null}
      <InitialStackDeploymentSettings {...viewModel} />
      <ImageRegistryCredential
        actor={scope.actor}
        project={scope.project}
        image=""
        compose
        disabled={paused || busy || !!review}
        onChange={setRegistrySelection}
        onDenied={onDenied}
      />
      {!options.available ? (
        <p role="status">
          Compose deployment unavailable: {options.reason}. Refresh deployment
          information after resolving this condition.
        </p>
      ) : null}
      <StackDeploymentPreparation {...viewModel} />
      <ReviewedStackDeployment {...viewModel} />
      {accepted ? (
        <p role="status">
          Deployment requested. Check progress below. Request: {accepted}.
        </p>
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
    </section>
  );
}

export function StackComponentStatus({
  operation,
}: {
  operation: HostingOperation;
}) {
  const stack = operation.result?.stack;
  if (!stack) return null;
  const definitions =
    operation.plan?.stack?.definition.services ??
    operation.stack_source?.services ??
    [];
  const init = new Set(
    definitions.flatMap((service) =>
      Object.entries(service.depends_on ?? {})
        .filter(
          ([, condition]) => condition === "service_completed_successfully",
        )
        .map(([name]) => name),
    ),
  );
  return (
    <section
      className="min-w-0 space-y-2 break-all rounded-lg border p-3"
      aria-label="Recorded Compose components"
    >
      <h6 className="font-medium">Recorded Compose components</h6>
      <p>
        Observation {stack.observation_complete ? "complete" : "incomplete"} ·
        public component {stack.public_component}
      </p>
      {stack.components.map((component) => (
        <article
          className="space-y-1"
          key={component.component}
          aria-label={`Component ${component.component}`}
        >
          <h6 className="font-medium">{component.component}</h6>
          <p>
            {component.running
              ? "Running"
              : init.has(component.component) && component.exit_code === 0
                ? "Initialization completed successfully"
                : `Stopped · exit code ${component.exit_code}`}{" "}
            · health {component.health || "not recorded"}
          </p>
          <p className="text-xs">
            Container {component.container_id} · image {component.image_id} ·
            operation {component.operation_id}
          </p>
        </article>
      ))}
      <p className="text-xs">
        Recorded network {stack.network_id}. This operation evidence is not
        continuous health monitoring.
      </p>
    </section>
  );
}

function useStackDeploymentReview({
  resource,
  scope,
  bindingID,
  options,
  blocked,
  mutationHeld,
  onDenied,
  onSelected,
  onRefresh,
}: {
  resource: Resource;
  scope: StorageDeploymentScope;
  bindingID: string;
  options: StorageDeploymentOptions;
  blocked: boolean;
  mutationHeld: boolean;
  onDenied: () => void;
  onSelected: (id: string) => void;
  onRefresh: (signal?: AbortSignal) => Promise<void>;
}) {
  const id = useId();
  const source =
    resource.source.kind === "compose" ? resource.source.compose : undefined;
  const [hostPort, setHostPort] = useState("");
  const [path, setPath] = useState("/");
  const [timeout, setTimeout] = useState("30");
  const readinessEdited = useRef(false);
  const catalogDefaults = useRef(false);
  const catalog = useQuery({
    queryKey: ["catalogInstallation", scope.actor, resource.id],
    queryFn: ({ signal }) => getCatalogInstallation(resource.id, signal),
    retry: false,
    gcTime: 0,
  });
  const [registrySelection, setRegistrySelection] =
    useState<ImageRegistrySelection>({ valid: true });
  const [review, setReview] = useState<Review>();
  const [confirmed, setConfirmed] = useState(false);
  const [sent, setSent] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!catalog.data || catalogDefaults.current) return;
    catalogDefaults.current = true;
    if (
      catalog.data.resource.project_id !== scope.project ||
      !catalog.data.template.definition.compose ||
      readinessEdited.current ||
      review ||
      busy
    )
      return;
    setPath(catalog.data.template.definition.readiness.path);
    setTimeout(
      String(catalog.data.template.definition.readiness.timeout_seconds),
    );
  }, [catalog.data, scope.project, review, busy]);
  const [conflict, setConflict] = useState(false);
  const [restoreHeld, setRestoreHeld] = useState(false);
  const [error, setError] = useState("");
  const [accepted, setAccepted] = useState("");
  const active = useRef(true);
  const request = useRef<AbortController | null>(null);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      request.current?.abort();
    };
  }, []);
  const shown = review?.options ?? options;
  const starting = Boolean(shown.stopped_operation_id);
  const initial = shown.reason === "initial_deployment";
  const paused = blocked || restoreHeld;
  async function prepare() {
    if (
      !source ||
      request.current ||
      paused ||
      mutationHeld ||
      !bindingID ||
      !registrySelection.valid
    )
      return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    setReview(undefined);
    setConfirmed(false);
    setSent(false);
    setConflict(false);
    setAccepted("");
    try {
      const current = await getStorageDeploymentOptions(
        scope,
        controller.signal,
      );
      if (
        !current.available ||
        !current.stack_ref ||
        current.resource_generation !== resource.generation
      )
        throw new Error(
          `Compose deployment cannot be reviewed: ${current.reason}. Refresh saved configuration and deployment history.`,
        );
      const { runtime_configuration: runtime } = await boundedStorageRequest(
        (signal) => getHostingRuntimeConfiguration(resource.id, { signal }),
        controller.signal,
      );
      if (
        runtime.resource_id !== scope.resource ||
        runtime.project_id !== scope.project ||
        !Number.isSafeInteger(runtime.runtime_generation) ||
        runtime.runtime_generation < 0
      )
        throw new Error(
          "Saved runtime configuration does not match this application.",
        );
      if (!active.current || controller.signal.aborted) return;
      const first = current.reason === "initial_deployment";
      const readiness = first
        ? { path, timeout_seconds: Number(timeout) }
        : { ...current.readiness! };
      if (
        !validStorageReadiness(readiness.path, readiness.timeout_seconds) ||
        (first &&
          hostPort !== "" &&
          (!Number.isInteger(Number(hostPort)) ||
            Number(hostPort) < 1024 ||
            Number(hostPort) > 65535))
      )
        throw new Error(
          "Enter a valid readiness path, timeout, and optional host port between 1024 and 65535.",
        );
      const input: HostingDeploymentInput = {
        expected_generation: current.resource_generation,
        ...(registrySelection.credential
          ? { registry_credential: { ...registrySelection.credential } }
          : {}),
        ...(runtime.runtime_generation > 0
          ? {
              expected_runtime_configuration_generation:
                runtime.runtime_generation,
            }
          : {}),
        idempotency_key: crypto.randomUUID(),
        managed_stack: {
          acknowledge_interruption: true,
          ...(current.stopped_operation_id
            ? { expected_stopped_operation_id: current.stopped_operation_id }
            : {}),
        },
        container_port: first ? source.container_port : current.container_port!,
        ...(first
          ? hostPort === ""
            ? {}
            : { host_port: Number(hostPort) }
          : { host_port: current.host_port! }),
        readiness,
        ...(current.storage_route_continuity
          ? {
              storage_route_continuity: structuredClone(
                current.storage_route_continuity,
              ),
            }
          : {}),
      };
      setReview({
        input,
        options: structuredClone(current),
        runtimeNames: runtime.entries.map(
          (entry) =>
            `${entry.name} (${entry.kind === "managed_secret" ? "managed secret reference" : "saved variable"})`,
        ),
      });
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      if (cause instanceof ApiError && [401, 403, 404].includes(cause.status))
        onDenied();
      else
        setError(
          cause instanceof Error
            ? cause.message
            : "Unable to review saved Compose configuration.",
        );
    } finally {
      if (request.current === controller) request.current = null;
      if (active.current) setBusy(false);
    }
  }
  async function submit() {
    if (
      !review ||
      !confirmed ||
      request.current ||
      paused ||
      conflict ||
      (mutationHeld && !sent)
    )
      return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setSent(true);
    setError("");
    try {
      const operation = await boundedStorageRequest(
        (signal) =>
          requestHostingDeployment(resource.id, review.input, { signal }),
        controller.signal,
      );
      if (!active.current || controller.signal.aborted) return;
      const stackRef =
        "stack" in operation.snapshot ? operation.snapshot.stack : undefined;
      const registryCredential =
        "registry_credential" in operation.snapshot
          ? operation.snapshot.registry_credential
          : undefined;
      if (
        registryCredential?.secret_id !==
          review.input.registry_credential?.secret_id ||
        registryCredential?.version !==
          review.input.registry_credential?.version ||
        operation.snapshot.stack_stopped_operation_id !==
          review.input.managed_stack?.expected_stopped_operation_id ||
        (operation.plan &&
          review.input.managed_stack?.expected_stopped_operation_id &&
          operation.plan.previous_stopped?.stop_operation_id !==
            review.input.managed_stack.expected_stopped_operation_id) ||
        !operation.id ||
        operation.resource_id !== scope.resource ||
        operation.project_id !== scope.project ||
        operation.binding_id !== bindingID ||
        operation.snapshot.resource_generation !==
          review.input.expected_generation ||
        stackRef?.generation !== review.options.stack_ref?.generation ||
        stackRef?.hash !== review.options.stack_ref?.hash ||
        (operation.snapshot.runtime_configuration?.generation ?? 0) !==
          (review.input.expected_runtime_configuration_generation ?? 0) ||
        (review.input.host_port !== undefined &&
          operation.snapshot.host_port !== review.input.host_port) ||
        operation.snapshot.container_port !== review.input.container_port ||
        operation.snapshot.readiness.path !== review.input.readiness.path ||
        operation.snapshot.readiness.timeout_seconds !==
          review.input.readiness.timeout_seconds ||
        Boolean(operation.snapshot.storage_route_continuity) !==
          Boolean(review.input.storage_route_continuity)
      )
        throw new Error("Deployment receipt mismatch");
      setAccepted(operation.id);
      setReview(undefined);
      setConfirmed(false);
      setSent(false);
      onSelected(operation.id);
      try {
        await boundedStorageRequest(
          (signal) => onRefresh(signal),
          controller.signal,
        );
      } catch (cause) {
        if (!active.current || controller.signal.aborted) return;
        if (cause instanceof ApiError && [401, 403, 404].includes(cause.status))
          onDenied();
        else
          setError(
            `Deployment ${operation.id} was accepted, but history could not be refreshed. Refresh deployment history to inspect completion.`,
          );
      }
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      if (cause instanceof ApiError && [401, 403, 404].includes(cause.status)) {
        onDenied();
        return;
      }
      if (
        cause instanceof HostingDeploymentError &&
        cause.code === "hosting_restore_held"
      ) {
        setRestoreHeld(true);
        setError(
          "Deployment is blocked after restore. Recovery proof is required.",
        );
      } else if (
        cause instanceof ApiError &&
        [400, 409].includes(cause.status)
      ) {
        setConflict(true);
        setError(
          `${cause.message}. Review saved Compose variables, managed secret references, placement and routes before a separate request.`,
        );
      } else
        setError(
          "Delivery is unconfirmed. The operation may already exist. Retry the identical reviewed request to recover it.",
        );
    } finally {
      if (request.current === controller) request.current = null;
      if (active.current) setBusy(false);
    }
  }
  return {
    scope,
    bindingID,
    options,
    mutationHeld,
    onDenied,
    id,
    source,
    hostPort,
    setHostPort,
    path,
    setPath,
    timeout,
    setTimeout,
    readinessEdited,
    registrySelection,
    setRegistrySelection,
    review,
    confirmed,
    setConfirmed,
    sent,
    busy,
    conflict,
    error,
    accepted,
    request,
    shown,
    starting,
    initial,
    paused,
    prepare,
    submit,
  };
}

type InitialStackDeploymentSettingsModel = Pick<
  ReturnType<typeof useStackDeploymentReview>,
  | "initial"
  | "id"
  | "hostPort"
  | "busy"
  | "review"
  | "setHostPort"
  | "path"
  | "readinessEdited"
  | "setPath"
  | "timeout"
  | "setTimeout"
>;
function InitialStackDeploymentSettings(
  viewModel: InitialStackDeploymentSettingsModel,
) {
  const {
    initial,
    id,
    hostPort,
    busy,
    review,
    setHostPort,
    path,
    readinessEdited,
    setPath,
    timeout,
    setTimeout,
  } = viewModel;

  if (!initial)
    return (
      <p className="text-sm">
        Redeployment stops the previous stack before replacement readiness and
        reuses its port and retained volumes. Existing selected HTTPS routes are
        carried forward through the server's continuity plan. Data writes are
        not rolled back.
      </p>
    );
  return (
    <>
      <p className="text-sm">
        The first deployment creates owned named volumes as needed and retains
        existing data. Configure the existing HTTPS route after the stack
        settles healthy.
      </p>
      <div className="grid min-w-0 gap-3 sm:grid-cols-2">
        <label htmlFor={`${id}-host`}>
          Runner-local host port (optional)
          <Input
            id={`${id}-host`}
            type="number"
            min={1024}
            max={65535}
            value={hostPort}
            disabled={busy || Boolean(review)}
            onChange={(e) => setHostPort(e.target.value)}
            placeholder="Automatic allocation"
          />
        </label>
        <label htmlFor={`${id}-path`}>
          Readiness path
          <Input
            id={`${id}-path`}
            required
            value={path}
            disabled={busy || Boolean(review)}
            onChange={(e) => {
              readinessEdited.current = true;
              setPath(e.target.value);
            }}
          />
        </label>
        <label htmlFor={`${id}-timeout`}>
          Readiness timeout in seconds
          <Input
            id={`${id}-timeout`}
            type="number"
            min={1}
            max={120}
            value={timeout}
            disabled={busy || Boolean(review)}
            onChange={(e) => {
              readinessEdited.current = true;
              setTimeout(e.target.value);
            }}
          />
        </label>
      </div>
    </>
  );
}

type ReviewedStackDeploymentModel = Pick<
  ReturnType<typeof useStackDeploymentReview>,
  | "review"
  | "confirmed"
  | "busy"
  | "sent"
  | "setConfirmed"
  | "paused"
  | "conflict"
  | "mutationHeld"
  | "submit"
  | "starting"
>;
function ReviewedStackDeployment(viewModel: ReviewedStackDeploymentModel) {
  const {
    review,
    confirmed,
    busy,
    sent,
    paused,
    conflict,
    mutationHeld,
    submit,
    starting,
  } = viewModel;

  if (!review) return null;
  return (
    <section
      className="min-w-0 space-y-2 break-all"
      aria-label="Reviewed Compose deployment"
    >
      <p>
        Saved generation {review.input.expected_generation} · Compose generation{" "}
        {review.options.stack_ref?.generation} · runtime generation{" "}
        {review.input.expected_runtime_configuration_generation ?? 0}
      </p>
      <p>
        Image access:{" "}
        {review.input.registry_credential
          ? `Registry credential ${review.input.registry_credential.secret_id}, version ${review.input.registry_credential.version}`
          : "Public images"}
        .
      </p>
      <p>Compose hash: {review.options.stack_ref?.hash}</p>
      <p>
        Runtime configuration:{" "}
        {review.runtimeNames.length
          ? review.runtimeNames.join(", ")
          : "No saved variables or managed secret references. Compose required values must be supplied before deployment."}
      </p>
      <p>
        Runner-local port{" "}
        {typeof review.input.host_port === "number"
          ? review.input.host_port
          : "automatically allocated"}{" "}
        → {review.input.container_port}, readiness {review.input.readiness.path}
        .
      </p>
      {review.options.stopped_operation_id ? (
        <p>
          Starts from recorded stop operation{" "}
          {review.options.stopped_operation_id}.
        </p>
      ) : null}
      {review.options.previous_healthy_operation_id ? (
        <p>
          Replaces healthy operation{" "}
          {review.options.previous_healthy_operation_id}.
        </p>
      ) : null}
      {review.options.routes.map((route) => (
        <p key={route.id}>
          Keep HTTPS route {route.hostname}
          {route.path_prefix} · version {route.version}.
        </p>
      ))}
      <StackDeploymentApproval {...viewModel} />
      <Button
        type="button"
        disabled={
          busy || paused || conflict || !confirmed || (mutationHeld && !sent)
        }
        onClick={() => void submit()}
      >
        {sent
          ? "Retry identical Compose deployment request"
          : starting
            ? "Start reviewed Compose"
            : "Deploy reviewed Compose"}
      </Button>
      <StackDeploymentRequestReceipt {...viewModel} />
    </section>
  );
}

type StackDeploymentApprovalModel = Pick<
  ReturnType<typeof useStackDeploymentReview>,
  "confirmed" | "busy" | "sent" | "setConfirmed"
>;
function StackDeploymentApproval(viewModel: StackDeploymentApprovalModel) {
  const { confirmed, busy, sent, setConfirmed } = viewModel;

  return (
    <label className="flex items-start gap-2 text-sm">
      <input
        type="checkbox"
        checked={confirmed}
        disabled={busy || sent}
        onChange={(e) => setConfirmed(e.target.checked)}
      />
      <span>
        I reviewed this saved Compose configuration and acknowledge service
        interruption during replacement. Named volumes are retained.
      </span>
    </label>
  );
}

type StackDeploymentPreparationModel = Pick<
  ReturnType<typeof useStackDeploymentReview>,
  | "busy"
  | "paused"
  | "mutationHeld"
  | "bindingID"
  | "registrySelection"
  | "sent"
  | "conflict"
  | "prepare"
  | "starting"
>;
function StackDeploymentPreparation(
  viewModel: StackDeploymentPreparationModel,
) {
  const {
    busy,
    paused,
    mutationHeld,
    bindingID,
    registrySelection,
    sent,
    conflict,
    prepare,
    starting,
  } = viewModel;

  return (
    <Button
      type="button"
      variant="outline"
      disabled={
        busy ||
        paused ||
        mutationHeld ||
        !bindingID ||
        !registrySelection.valid ||
        (sent && !conflict)
      }
      onClick={() => void prepare()}
    >
      {starting
        ? "Review current Compose start"
        : "Review current Compose deployment"}
    </Button>
  );
}

type StackDeploymentRequestReceiptModel = Pick<
  ReturnType<typeof useStackDeploymentReview>,
  "sent" | "review"
>;
function StackDeploymentRequestReceipt(
  viewModel: StackDeploymentRequestReceiptModel,
) {
  const { sent, review } = viewModel;

  if (!review) return null;
  if (!sent) return null;
  return (
    <p className="text-xs">
      Request key {review.input.idempotency_key}. The reviewed body is retained
      for an identical retry.
    </p>
  );
}
