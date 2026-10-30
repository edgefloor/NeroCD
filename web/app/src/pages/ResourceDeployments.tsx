import { allProjectMembersQuery, ApiError, principalQuery } from "@/api";
import { getResource, type Resource } from "@/api/hosting";
import {
  cancelHostingOperation,
  hostingBindingQuery,
  HostingDeploymentError,
  hostingDeploymentKeys,
  hostingDeploymentsQuery,
  hostingOperationQuery,
  hostingRetainedQuery,
  reconcileHostingOperation,
  requestHostingDeployment,
  requestHostingReleaseCleanup,
  type HostingCleanupInput,
  type HostingDeploymentInput,
  type HostingRetainedRelease,
} from "@/api/hosting-deployments";
import { getHostingRuntimeConfiguration } from "@/api/hosting-runtime-config";
import {
  currentGitStackQuery,
  stackLifecycleKey,
} from "@/api/hosting-stack-lifecycle";
import {
  boundedStorageRequest,
  getStorageDeploymentOptions,
  storageDeploymentOptionsQuery,
} from "@/api/hosting-storage-deployment";
import { PageNavigation } from "@/components/common/PageNavigation";
import {
  ImageRegistryCredential,
  type ImageRegistrySelection,
} from "@/components/hosting/ImageRegistryCredential";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { canMaintainProject, canOwnProject } from "@/lib/permissions";
import { maximumPageOffset } from "@/router/search";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { ComposeStorageBackups } from "./ComposeStorageBackups";
import {
  StackComponentStatus,
  StackDeploymentReview,
} from "./StackDeploymentReview";
import { StackLifecycle } from "./StackLifecycle";
import {
  StorageContinuityDetail,
  StorageDeploymentReview,
} from "./StorageDeploymentReview";

const terminalStates = new Set(["succeeded", "failed", "canceled"]);
const stateText: Record<string, string> = {
  queued: "Queued",
  dispatched: "Dispatched to runner",
  preparing: "Preparing image",
  prepared: "Image prepared",
  applying: "Applying workload",
  verifying: "Verifying readiness",
  succeeded: "Succeeded",
  failed: "Failed",
  canceled: "Canceled",
  cancel_requested: "Cancellation requested after admission",
  reconciliation_required: "Reconciliation required",
};
function deploymentError(cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.status === 503)
      return "Deployment service unavailable. Delivery may be uncertain; retry the identical request when the service returns.";
    if (cause.status === 401 || cause.status === 403)
      return "Project permission is unavailable for this action. Refresh access before retrying.";
    if (cause.status === 409)
      return "The request conflicts with saved configuration, placement, an existing operation, or a previously used request key. Nothing was resubmitted automatically.";
    if (cause.status === 400)
      return "The server rejected the deployment settings. Review the ports and readiness path.";
    if (cause.status === 404)
      return "The requested application or operation is unavailable.";
  }
  return "The connection ended without a confirmed response. The request may have been accepted. Retry the identical request to recover its operation.";
}

export function OperationDetail(props: {
  actor: string;
  projectID: string;
  resourceID: string;
  operationID: string;
  writable: boolean;
  owner: boolean;
  blocked: boolean;
  onChanged: () => Promise<void>;
}) {
  const viewModel = useOperationDetail(props);
  const { busy, error, notice, detail } = viewModel;
  return (
    <section
      className="min-w-0 space-y-3 rounded-lg border p-3"
      aria-label="Deployment operation detail"
    >
      {detail.isPending ? <p role="status">Loading operation...</p> : null}
      {detail.isError ? (
        <p role="alert">
          Unable to refresh operation. Its last recorded state may be stale;
          controls are paused.
        </p>
      ) : null}
      <Button
        type="button"
        variant="outline"
        disabled={detail.isFetching || busy}
        onClick={() => void detail.refetch()}
      >
        Refresh operation
      </Button>
      <OperationEvidence {...viewModel} />
      {error ? <p role="alert">{error}</p> : null}
      {notice ? <p role="status">{notice}</p> : null}
    </section>
  );
}

function RetainedReleases(props: {
  actor: string;
  resource: Resource;
  writable: boolean;
  blocked: boolean;
  mutationHeld: boolean;
  restoreHeld: boolean;
  onSelected: (id: string) => void;
  onChanged: () => Promise<void>;
}) {
  const viewModel = useRetainedReleases(props);
  const {
    resource,
    writable,
    mutationHeld,
    onSelected,
    releases,
    target,
    setTarget,
    setConfirmed,
    setAttempt,
    setExpected,
    busy,
    setConflict,
    error,
    setError,
    paused,
  } = viewModel;
  return (
    <section
      className="min-w-0 space-y-3 border-t pt-3"
      aria-label="Retained releases"
    >
      <h6 className="font-medium">Retained releases</h6>
      <p className="text-xs text-muted-foreground">
        Earlier release containers are retained until explicit cleanup. The
        current healthy release is excluded from this list. The server rejects
        cleanup of current, route-protected, held, or restored targets. Recorded
        running state is not continuous health monitoring.
      </p>
      <Button
        type="button"
        variant="outline"
        disabled={releases.isFetching || busy}
        onClick={() => void releases.refetch()}
      >
        Refresh retained releases
      </Button>
      {releases.isPending ? (
        <p role="status">Loading retained releases...</p>
      ) : null}
      {releases.isError ? (
        <p role="alert">
          Unable to refresh retained releases. Cleanup is paused; the selected
          target is preserved.
        </p>
      ) : null}
      {releases.data?.length === 0 ? (
        <p>No retained releases recorded.</p>
      ) : null}
      {releases.data?.map((release) => (
        <article
          key={release.healthy.operation_id}
          className="min-w-0 space-y-2 rounded-lg border p-3"
          aria-label={`Retained release ${release.healthy.operation_id}`}
        >
          <dl className="space-y-1 break-all text-sm">
            <dt>Release operation</dt>
            <dd>{release.healthy.operation_id}</dd>
            <dt>Container</dt>
            <dd>{release.healthy.container_id}</dd>
            <dt>Release state</dt>
            <dd>{release.state}</dd>
            <dt>Recorded retained running</dt>
            <dd>{release.retained_running ? "yes" : "no"}</dd>
            <dt>Image</dt>
            <dd>
              {release.healthy.release.local_build
                ? `Local build ${release.healthy.release.local_build.build_id} · ${release.healthy.release.image_id}`
                : release.healthy.release.canonical_image}
            </dd>
            <dt>Target binding</dt>
            <dd>{release.healthy.snapshot.binding_id}</dd>
            <dt>Destination / runner</dt>
            <dd>
              {release.healthy.snapshot.destination_id} /{" "}
              {release.healthy.snapshot.runner_id}
            </dd>
            <dt>Retained local port</dt>
            <dd>127.0.0.1:{release.healthy.snapshot.host_port}</dd>
            <dt>Original readiness request</dt>
            <dd>
              {release.healthy.snapshot.readiness.path} ·{" "}
              {release.healthy.snapshot.readiness.timeout_seconds}s
            </dd>
          </dl>
          {release.cleanup_operation_id ? (
            <Button
              type="button"
              variant="outline"
              className="h-auto max-w-full whitespace-normal break-all text-left"
              onClick={() => onSelected(release.cleanup_operation_id!)}
            >
              View cleanup operation {release.cleanup_operation_id}
            </Button>
          ) : null}
          {writable && release.state === "retained" ? (
            <Button
              type="button"
              variant="outline"
              className="h-auto max-w-full whitespace-normal break-all text-left"
              disabled={paused || mutationHeld || busy || Boolean(target)}
              onClick={() => {
                setTarget(release);
                setExpected(resource.generation);
                setAttempt(undefined);
                setConfirmed(false);
                setConflict(false);
                setError("");
              }}
            >
              Review cleanup {release.healthy.operation_id}
            </Button>
          ) : null}
        </article>
      ))}
      <ReleaseCleanupReview {...viewModel} />
      {error ? <p role="alert">{error}</p> : null}
      {!writable ? (
        <p className="text-xs text-muted-foreground">
          Project maintainer access is required to request retained release
          cleanup.
        </p>
      ) : null}
    </section>
  );
}

export function ResourceDeployments(props: {
  resource: Resource;
  writable: boolean;
  blocked: boolean;
}) {
  const [deniedScope, setDeniedScope] = useState("");
  const identity = useQuery(principalQuery());
  const members = useQuery({ ...allProjectMembersQuery(), staleTime: 10_000 });
  const accessFailed = identity.isError || members.isError;
  if (
    props.blocked ||
    identity.isFetching ||
    accessFailed ||
    members.isPending ||
    members.isFetching
  )
    return (
      <p role={accessFailed ? "alert" : "status"}>
        Deployment access is pending or unavailable. Drafts and retained
        requests are cleared.
      </p>
    );
  if (!identity.data)
    return (
      <p role={identity.isError ? "alert" : "status"}>
        {identity.isError
          ? "Unable to load deployment identity. Reload to refresh access."
          : "Loading deployment identity..."}
      </p>
    );
  const actor = JSON.stringify([identity.data.provider, identity.data.id]);
  const scope = JSON.stringify([
    actor,
    props.resource.project_id,
    props.resource.id,
  ]);
  if (deniedScope === scope)
    return (
      <p role="alert">
        Deployment permission is unavailable. Reload to verify current project
        access. Drafts and retained requests are cleared.
      </p>
    );
  const writable =
    props.writable &&
    canMaintainProject(
      identity.data,
      members.data ?? [],
      props.resource.project_id,
    );
  return (
    <Deployments
      key={JSON.stringify([
        actor,
        props.resource.project_id,
        props.resource.id,
        props.resource.generation,
        writable,
      ])}
      {...props}
      writable={writable}
      actor={actor}
      onDenied={() => setDeniedScope(scope)}
      blocked={props.blocked || identity.isError}
    />
  );
}
function Deployments(props: {
  actor: string;
  resource: Resource;
  writable: boolean;
  blocked: boolean;
  onDenied: () => void;
}) {
  const viewModel = useDeployments(props);
  const {
    actor,
    resource,
    writable,
    principal,
    members,
    owner,
    selected,
    acceptedID,
    acceptedRefreshError,
    binding,
    history,
    storageOptions,
    restoreHeld,
    mutationHeld,
    failed,
    unavailable,
    refreshHistory,
    deployedGitCompose,
  } = viewModel;
  return (
    <section
      className="min-w-0 space-y-3 border-t pt-3"
      aria-label={`Image deployments for ${resource.name}`}
    >
      <h5 className="font-medium">
        {resource.source.kind === "image"
          ? "Image deployments"
          : resource.source.kind === "compose" || deployedGitCompose
            ? "Compose deployments"
            : "Deployment history"}
      </h5>
      {acceptedID ? (
        <p role="status" className="break-all">
          Deployment requested. Check progress below. Request: {acceptedID}.
        </p>
      ) : null}
      {acceptedRefreshError ? <p role="alert">{acceptedRefreshError}</p> : null}
      <GitDeploymentControls {...viewModel} />{" "}
      {restoreHeld ? (
        <p role="alert">
          Deployment admission blocked after platform restore. Recovery proof is
          pending; refreshing configuration, retrying, or requesting
          reconciliation cannot release restored ownership. Your settings are
          preserved.
        </p>
      ) : null}
      {mutationHeld ? (
        <p role="status">
          New deployment requests are paused while a recorded mutation hold is
          retained. An exact retained request can still recover its existing
          operation. Refresh history to review settlement; cancellation alone
          does not release the hold.
        </p>
      ) : null}
      {failed ? (
        <div className="space-y-2">
          <p role="alert">
            Unable to refresh deployment information. Draft settings are
            preserved; actions are paused.
          </p>
          <Button
            type="button"
            variant="outline"
            onClick={() => {
              void binding.refetch();
              void history.refetch();
              if (principal.isError) void principal.refetch();
              if (members.isError) void members.refetch();
            }}
          >
            Retry deployment information
          </Button>
        </div>
      ) : null}
      <DeploymentHistory {...viewModel} />{" "}
      {selected ? (
        <OperationDetail
          actor={actor}
          key={selected}
          projectID={resource.project_id}
          resourceID={resource.id}
          operationID={selected}
          writable={writable}
          owner={owner}
          blocked={unavailable}
          onChanged={refreshHistory}
        />
      ) : null}
      <RetainedDeploymentReleases {...viewModel} />
      <ComposeDeploymentControls {...viewModel} />{" "}
      {resource.source.kind === "image" && storageOptions.isPending ? (
        <p role="status">
          Checking authoritative storage deployment options...
        </p>
      ) : null}
      {resource.source.kind === "image" && storageOptions.isError ? (
        <>
          <p role="alert">
            Storage deployment options are unavailable. Deployment is paused;
            refresh authority before reviewing.
          </p>
          <Button
            type="button"
            variant="outline"
            disabled={storageOptions.isFetching}
            onClick={() => void storageOptions.refetch()}
          >
            Retry storage deployment options
          </Button>
        </>
      ) : null}
      <ImageStorageDeployment {...viewModel} />
      <ImageDeploymentForm {...viewModel} />
    </section>
  );
}

function useOperationDetail({
  actor,
  projectID,
  resourceID,
  operationID,
  writable,
  owner,
  blocked,
  onChanged,
}: {
  actor: string;
  projectID: string;
  resourceID: string;
  operationID: string;
  writable: boolean;
  owner: boolean;
  blocked: boolean;
  onChanged: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const active = useRef(true);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);
  const detail = useQuery({
    ...hostingOperationQuery(projectID, resourceID, operationID, actor),
    refetchInterval: (query) => {
      const operation = query.state.data;
      return operation &&
        (!terminalStates.has(operation.state) ||
          operation.reconcile_requested ||
          operation.mutation_hold)
        ? 5000
        : false;
    },
  });
  const operation = detail.data;
  const stackRef =
    operation && "stack" in operation.snapshot
      ? operation.snapshot.stack
      : undefined;
  const gitCompose =
    operation && "git_compose" in operation.snapshot
      ? operation.snapshot.git_compose
      : undefined;
  async function control(kind: "cancel" | "reconcile") {
    if (
      busy ||
      blocked ||
      detail.isError ||
      !operation ||
      (kind === "cancel" ? !writable : !owner)
    )
      return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      if (kind === "cancel") await cancelHostingOperation(operationID);
      else await reconcileHostingOperation(operationID);
      if (!active.current) return;
      await detail.refetch();
      await onChanged();
      if (active.current)
        setNotice(
          kind === "cancel"
            ? "Cancellation requested. Review the persisted operation state; this does not guarantee the workload stopped."
            : "Reconciliation requested. Observation does not authorize replaying a deployment.",
        );
    } catch (cause) {
      if (!active.current) return;
      setError(deploymentError(cause));
      if (cause instanceof ApiError && cause.status === 409) {
        await detail.refetch();
        await onChanged();
      }
    } finally {
      if (active.current) setBusy(false);
    }
  }
  return {
    actor,
    projectID,
    resourceID,
    operationID,
    writable,
    owner,
    blocked,
    busy,
    error,
    notice,
    detail,
    operation,
    stackRef,
    gitCompose,
    control,
  };
}

function useDeployments({
  actor,
  resource,
  writable,
  blocked,
  onDenied,
}: {
  actor: string;
  resource: Resource;
  writable: boolean;
  blocked: boolean;
  onDenied: () => void;
}) {
  const id = useId();
  const client = useQueryClient();
  const active = useRef(true);
  const submitting = useRef(false);
  const request = useRef<AbortController | null>(null);
  const refreshRequests = useRef(new Set<AbortController>());
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      request.current?.abort();
      for (const controller of refreshRequests.current) controller.abort();
      refreshRequests.current.clear();
    };
  }, []);
  const { principal, members, owner } = useDeploymentIdentity(
    resource.project_id,
  );
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState("");
  const [containerPort, setContainerPort] = useState("80");
  const [hostPort, setHostPort] = useState("8080");
  const [portMode, setPortMode] = useState("automatic");
  const [path, setPath] = useState("/");
  const [timeout, setTimeout] = useState("30");
  const [acknowledged, setAcknowledged] = useState(false);
  const [busy, setBusy] = useState(false);
  const [attempt, setAttempt] = useState<HostingDeploymentInput>();
  const [registrySelection, setRegistrySelection] =
    useState<ImageRegistrySelection>({ valid: true });
  const [conflict, setConflict] = useState(false);
  const [current, setCurrent] = useState<Resource>();
  const [reviewed, setReviewed] = useState<Resource>();
  const [error, setError] = useState("");
  const [refreshError, setRefreshError] = useState("");
  const [differentIntent, setDifferentIntent] = useState(false);
  const [restoreRejected, setRestoreRejected] = useState(false);
  const [acceptedID, setAcceptedID] = useState("");
  const acceptedReceipt = useRef("");
  const [acceptedRefreshError, setAcceptedRefreshError] = useState("");
  const binding = useQuery(
    hostingBindingQuery(resource.project_id, resource.id, actor),
  );
  const history = useQuery(
    hostingDeploymentsQuery(resource.project_id, resource.id, offset, actor),
  );
  const currentGitStack = useQuery({
    ...currentGitStackQuery({
      actor,
      project: resource.project_id,
      resource: resource.id,
    }),
    enabled: resource.source.kind === "git",
  });
  const storageOptions = useQuery({
    ...storageDeploymentOptionsQuery({
      actor,
      project: resource.project_id,
      resource: resource.id,
    }),
    enabled:
      resource.source.kind === "image" ||
      (resource.source.kind === "compose" && Boolean(binding.data)),
  });
  const stateless =
    !storageOptions.isError &&
    storageOptions.data?.reason === "storage_unconfigured" &&
    storageOptions.data.storage_ref === null;
  const base = reviewed ?? resource;
  const restoreHeld =
    restoreRejected ||
    Boolean(
      history.data?.items.some((operation) => operation.restore_ownership_held),
    );
  const mutationHeld = Boolean(
    history.data?.items.some((operation) => operation.mutation_hold),
  );
  const reads = [binding, history, principal, members];
  const failed = reads.some((read) => read.isError);
  const unavailable = blocked || failed || reads.some((read) => read.isPending);
  const canWrite = writable && !unavailable;
  const newAdmissionHeld = mutationHeld && !attempt;
  const refreshHistory = async (retired?: AbortSignal) => {
    if (!active.current)
      throw new DOMException("Deployment scope retired", "AbortError");
    const controller = new AbortController();
    const abort = () => controller.abort();
    if (retired?.aborted) controller.abort();
    retired?.addEventListener("abort", abort, { once: true });
    refreshRequests.current.add(controller);
    const keys = [
      hostingDeploymentKeys.history(resource.project_id, resource.id, actor),
      hostingDeploymentKeys.retained(resource.project_id, resource.id, actor),
      ...(resource.source.kind === "compose"
        ? [
            stackLifecycleKey({
              actor,
              project: resource.project_id,
              resource: resource.id,
            }),
            storageDeploymentOptionsQuery({
              actor,
              project: resource.project_id,
              resource: resource.id,
            }).queryKey,
          ]
        : []),
    ];
    try {
      await boundedStorageRequest(async (signal) => {
        const cancel = () => {
          for (const queryKey of keys) void client.cancelQueries({ queryKey });
        };
        signal.addEventListener("abort", cancel, { once: true });
        try {
          const denied = (cause: unknown) =>
            cause instanceof ApiError && [401, 403, 404].includes(cause.status);
          // Inspect each current read while siblings are pending. An earlier
          // ordinary failure must not consume a later permission denial.
          const results = await Promise.allSettled(
            keys.map(async (queryKey) => {
              try {
                await client.invalidateQueries(
                  { queryKey },
                  { throwOnError: true },
                );
              } catch (cause) {
                if (active.current && !signal.aborted && denied(cause))
                  onDenied();
                throw cause;
              }
            }),
          );
          for (const result of results)
            if (result.status === "rejected" && denied(result.reason))
              throw result.reason;
          for (const result of results)
            if (result.status === "rejected") throw result.reason;
        } finally {
          signal.removeEventListener("abort", cancel);
        }
      }, controller.signal);
    } finally {
      retired?.removeEventListener("abort", abort);
      refreshRequests.current.delete(controller);
    }
  };
  async function reviewCurrent() {
    setRefreshError("");
    setCurrent(undefined);
    try {
      const latest = await getResource(resource.id);
      if (!active.current) return;
      if (
        latest.project_id !== resource.project_id ||
        latest.id !== resource.id
      )
        throw new Error("Resource scope mismatch");
      setCurrent(latest);
      await binding.refetch();
      await refreshHistory();
    } catch {
      if (active.current)
        setRefreshError(
          "Unable to refresh current configuration. Keep this request and retry the refresh.",
        );
    }
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (
      resource.source.kind !== "image" ||
      !stateless ||
      !canWrite ||
      newAdmissionHeld ||
      submitting.current ||
      !binding.data ||
      conflict ||
      restoreHeld ||
      !acknowledged ||
      (!attempt && !registrySelection.valid)
    )
      return;
    submitting.current = true;
    let input = attempt;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    setAcceptedRefreshError("");
    try {
      if (!input) {
        const authority = await getStorageDeploymentOptions(
          { actor, project: resource.project_id, resource: resource.id },
          controller.signal,
        );
        if (!active.current || controller.signal.aborted) return;
        client.setQueryData(
          storageDeploymentOptionsQuery({
            actor,
            project: resource.project_id,
            resource: resource.id,
          }).queryKey,
          authority,
        );
        if (
          authority.reason !== "storage_unconfigured" ||
          authority.storage_ref !== null
        ) {
          setAcknowledged(false);
          return;
        }
        const { runtime_configuration: runtime } = await boundedStorageRequest(
          (signal) => getHostingRuntimeConfiguration(resource.id, { signal }),
          controller.signal,
        );
        if (!active.current || controller.signal.aborted) return;
        if (
          runtime.resource_id !== resource.id ||
          runtime.project_id !== resource.project_id ||
          !Number.isSafeInteger(runtime.runtime_generation) ||
          runtime.runtime_generation < 0
        )
          throw new Error("Runtime configuration scope mismatch");
        input = {
          ...(registrySelection.credential
            ? { registry_credential: { ...registrySelection.credential } }
            : {}),
          ...(runtime.runtime_generation > 0
            ? {
                expected_runtime_configuration_generation:
                  runtime.runtime_generation,
              }
            : {}),
          expected_generation: base.generation,
          idempotency_key: crypto.randomUUID(),
          container_port: Number(containerPort),
          ...(portMode === "fixed" ? { host_port: Number(hostPort) } : {}),
          readiness: { path, timeout_seconds: Number(timeout) },
        };
        setAttempt(input);
      }
      const frozenInput = input;
      const operation = await boundedStorageRequest(
        (signal) =>
          requestHostingDeployment(resource.id, frozenInput, { signal }),
        controller.signal,
      );
      if (!active.current || controller.signal.aborted) return;
      if (
        operation.project_id !== resource.project_id ||
        operation.resource_id !== resource.id ||
        operation.binding_id !== binding.data.id ||
        (input.expected_runtime_configuration_generation !== undefined &&
          operation.snapshot.runtime_configuration?.generation !==
            input.expected_runtime_configuration_generation)
      )
        throw new Error("Operation scope mismatch");
      setSelected(operation.id);
      setAcceptedID(operation.id);
      acceptedReceipt.current = operation.id;
      setAttempt(undefined);
      setAcknowledged(false);
      setDifferentIntent(false);
      setOffset(0);
      void refreshHistory().catch((cause) => {
        if (!active.current) return;
        if (
          cause instanceof ApiError &&
          [401, 403, 404].includes(cause.status)
        ) {
          onDenied();
          return;
        }
        if (acceptedReceipt.current !== operation.id) return;
        setAcceptedRefreshError(
          `Deployment request ${operation.id} was accepted, but history could not be refreshed. Refresh deployment history to inspect its recorded state.`,
        );
      });
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      if (cause instanceof ApiError && [401, 403, 404].includes(cause.status)) {
        onDenied();
        return;
      }
      if (!input) {
        setError(
          "Unable to verify saved runtime configuration. No new deployment request was sent. Refresh access and try again.",
        );
        return;
      }
      if (
        cause instanceof HostingDeploymentError &&
        cause.code === "hosting_restore_held"
      ) {
        setRestoreRejected(true);
        setError("");
      } else {
        setError(deploymentError(cause));
        if (cause instanceof ApiError && cause.status === 409) {
          setConflict(true);
          await reviewCurrent();
        }
      }
    } finally {
      submitting.current = false;
      if (request.current === controller) request.current = null;
      if (active.current) setBusy(false);
    }
  }
  function newIntent(next?: Resource) {
    if (next) setReviewed(next);
    setAttempt(undefined);
    setConflict(false);
    setCurrent(undefined);
    setAcknowledged(false);
    setDifferentIntent(false);
    setError("");
  }
  const gitStackChecked =
    resource.source.kind !== "git" ||
    (currentGitStack.isSuccess && !currentGitStack.isFetching);
  const deployedGitCompose =
    resource.source.kind === "git" &&
    gitStackChecked &&
    Boolean(currentGitStack.data?.operation);
  return {
    actor,
    resource,
    writable,
    blocked,
    onDenied,
    id,
    request,
    principal,
    members,
    owner,
    offset,
    setOffset,
    selected,
    setSelected,
    containerPort,
    setContainerPort,
    hostPort,
    setHostPort,
    portMode,
    setPortMode,
    path,
    setPath,
    timeout,
    setTimeout,
    acknowledged,
    setAcknowledged,
    busy,
    attempt,
    registrySelection,
    setRegistrySelection,
    conflict,
    current,
    reviewed,
    error,
    refreshError,
    differentIntent,
    setDifferentIntent,
    acceptedID,
    acceptedRefreshError,
    binding,
    history,
    currentGitStack,
    storageOptions,
    stateless,
    base,
    restoreHeld,
    mutationHeld,
    failed,
    unavailable,
    canWrite,
    newAdmissionHeld,
    refreshHistory,
    reviewCurrent,
    submit,
    newIntent,
    gitStackChecked,
    deployedGitCompose,
  };
}

function useRetainedReleases({
  actor,
  resource,
  writable,
  blocked,
  mutationHeld,
  restoreHeld,
  onSelected,
  onChanged,
}: {
  actor: string;
  resource: Resource;
  writable: boolean;
  blocked: boolean;
  mutationHeld: boolean;
  restoreHeld: boolean;
  onSelected: (id: string) => void;
  onChanged: () => Promise<void>;
}) {
  const releases = useQuery(
    hostingRetainedQuery(resource.project_id, resource.id, actor),
  );
  const [target, setTarget] = useState<HostingRetainedRelease>();
  const [confirmed, setConfirmed] = useState(false);
  const [attempt, setAttempt] = useState<HostingCleanupInput>();
  const expected = useRef(resource.generation);
  const setExpected = (generation: number) => {
    expected.current = generation;
  };
  const [busy, setBusy] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [error, setError] = useState("");
  const active = useRef(true);
  const submitting = useRef(false);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);
  const current = releases.data?.find(
    (item) => item.healthy.operation_id === target?.healthy.operation_id,
  );
  const matches =
    current?.state === "retained" &&
    current.healthy.container_id === target?.healthy.container_id &&
    current.healthy.snapshot.host_port === target?.healthy.snapshot.host_port;
  const paused =
    blocked || releases.isError || releases.isPending || restoreHeld;
  async function cleanup(event: FormEvent) {
    event.preventDefault();
    if (
      !writable ||
      paused ||
      submitting.current ||
      conflict ||
      !target ||
      !confirmed ||
      (!attempt && (mutationHeld || !matches))
    )
      return;
    const body = attempt ?? {
      expected_generation: expected.current,
      idempotency_key: crypto.randomUUID(),
    };
    setAttempt(body);
    setBusy(true);
    submitting.current = true;
    setError("");
    try {
      const operation = await requestHostingReleaseCleanup(
        resource.id,
        target.healthy.operation_id,
        body,
      );
      if (!active.current) return;
      if (
        operation.resource_id !== resource.id ||
        operation.project_id !== resource.project_id ||
        operation.cleanup_plan?.target.operation_id !==
          target.healthy.operation_id ||
        operation.cleanup_plan.target.container_id !==
          target.healthy.container_id
      )
        throw new Error("Cleanup target mismatch");
      onSelected(operation.id);
      setTarget(undefined);
      setAttempt(undefined);
      setConfirmed(false);
      await releases.refetch();
      await onChanged();
    } catch (cause) {
      if (!active.current) return;
      setError(deploymentError(cause));
      if (cause instanceof ApiError && cause.status === 409) {
        setConflict(true);
        await releases.refetch();
        await onChanged();
      }
    } finally {
      submitting.current = false;
      if (active.current) setBusy(false);
    }
  }
  async function review() {
    setBusy(true);
    setError("");
    try {
      const latest = await getResource(resource.id);
      await releases.refetch();
      await onChanged();
      if (!active.current) return;
      if (
        latest.id !== resource.id ||
        latest.project_id !== resource.project_id
      )
        throw new Error("Cleanup resource mismatch");
      setExpected(latest.generation);
      setAttempt(undefined);
      setConflict(false);
      setConfirmed(false);
    } catch {
      if (active.current)
        setError(
          "Unable to refresh cleanup configuration and release state. No request was resubmitted.",
        );
    } finally {
      if (active.current) setBusy(false);
    }
  }
  return {
    resource,
    writable,
    mutationHeld,
    onSelected,
    releases,
    target,
    setTarget,
    confirmed,
    setConfirmed,
    attempt,
    setAttempt,
    setExpected,
    busy,
    conflict,
    setConflict,
    error,
    setError,
    current,
    matches,
    paused,
    cleanup,
    review,
  };
}

type OperationEvidenceModel = Pick<
  ReturnType<typeof useOperationDetail>,
  | "actor"
  | "operation"
  | "stackRef"
  | "gitCompose"
  | "projectID"
  | "resourceID"
  | "operationID"
  | "blocked"
  | "detail"
  | "writable"
  | "busy"
  | "control"
  | "owner"
>;
function OperationEvidence(viewModel: OperationEvidenceModel) {
  const {
    actor,
    operation,
    stackRef,
    gitCompose,
    projectID,
    resourceID,
    operationID,
    blocked,
    detail,
    writable,
  } = viewModel;

  if (!operation) return null;
  return (
    <>
      <h6 className="font-medium">
        {stateText[operation.state] ?? operation.state}
      </h6>
      {stackRef ? (
        <p className="break-all text-sm">
          Saved Compose generation {stackRef.generation} · {stackRef.hash}
        </p>
      ) : null}
      {gitCompose ? (
        <p className="break-all text-sm">
          Prepared Git Compose result {gitCompose.result_hash}
        </p>
      ) : null}
      <StackComponentStatus operation={operation} />
      {operation.snapshot.storage_route_continuity ? (
        <StorageContinuityDetail
          scope={{ actor, project: projectID, resource: resourceID }}
          operation={operationID}
          recovery={{
            record: operation,
            writable,
            blocked: blocked || detail.isError || detail.isFetching,
          }}
        />
      ) : null}
      {operation.stack_stop_plan ? (
        <p>
          Compose Stop · target {operation.stack_stop_plan.target.operation_id}.
          Containers are removed; named volumes are retained.
        </p>
      ) : null}
      {operation.cleanup_plan ? (
        <p>
          Retained release cleanup · target{" "}
          {operation.cleanup_plan.target.operation_id}
        </p>
      ) : null}
      <OperationSnapshot {...viewModel} />
      {operation.mutation_hold ? (
        <p role="status">
          Mutation hold retained. Another workload change must wait for the
          server to settle this operation.
        </p>
      ) : null}
      {operation.restore_ownership_held ? (
        <p role="alert">
          Deployment admission blocked after platform restore. Trusted local
          recovery proof is pending. Reconciliation can observe the target but
          cannot release restored ownership; there is no administrator unlock
          action.
        </p>
      ) : null}
      {operation.state === "cancel_requested" ||
      operation.state === "reconciliation_required" ? (
        <p className="text-sm">
          Apply may have started. Cancellation is not proof of rollback or a
          stopped workload. An owner can request reconciliation.
        </p>
      ) : null}
      <OperationOutcome {...viewModel} />
      <p className="text-xs text-muted-foreground">
        This result describes the last operation, not current health. Reconcile to check the workload again. The displayed port is local to the runner.
      </p>
      <OperationControls {...viewModel} />
      <OperationOwnerControls {...viewModel} />
    </>
  );
}

type OperationSnapshotModel = Pick<
  ReturnType<typeof useOperationDetail>,
  "operation" | "stackRef" | "gitCompose"
>;
function OperationSnapshot(viewModel: OperationSnapshotModel) {
  const { operation } = viewModel;

  if (!operation) return null;
  return (
    <dl className="space-y-1 break-all text-sm">
      <dt>Operation ID</dt>
      <dd>{operation.id}</dd>
      <dt>Recorded update</dt>
      <dd>{operation.updated_at}</dd>
      <OperationSourceEvidence {...viewModel} />
      <dt>Resource configuration generation</dt>
      <dd>{operation.snapshot.resource_generation}</dd>
      {operation.snapshot.runtime_configuration ? (
        <>
          <dt>Captured runtime configuration generation</dt>
          <dd>{operation.snapshot.runtime_configuration.generation}</dd>
          <dt>Runtime configuration reference hash</dt>
          <dd>{operation.snapshot.runtime_configuration.hash}</dd>
        </>
      ) : null}
      <dt>Exact binding</dt>
      <dd>{operation.binding_id}</dd>
      <dt>Destination</dt>
      <dd>{operation.snapshot.destination_id}</dd>
      <dt>Runner</dt>
      <dd>{operation.snapshot.runner_id}</dd>
      <dt>Runtime namespace</dt>
      <dd>{operation.snapshot.runtime_namespace}</dd>
      <dt>Runner-local endpoint</dt>
      <dd>
        127.0.0.1:{operation.snapshot.host_port} → container{" "}
        {operation.snapshot.container_port}
      </dd>
      <dt>Readiness request</dt>
      <dd>
        {operation.snapshot.readiness.path} ·{" "}
        {operation.snapshot.readiness.timeout_seconds}s timeout
      </dd>
      <OperationReleaseEvidence {...viewModel} />{" "}
      {operation.run_id ? (
        <>
          <dt>Deployment run</dt>
          <dd>{operation.run_id}</dd>
        </>
      ) : null}
      {operation.reconciliation_run_id ? (
        <>
          <dt>Observation run</dt>
          <dd>{operation.reconciliation_run_id}</dd>
        </>
      ) : null}
    </dl>
  );
}

type OperationOutcomeModel = Pick<
  ReturnType<typeof useOperationDetail>,
  "operation"
>;
function OperationOutcome(viewModel: OperationOutcomeModel) {
  const { operation } = viewModel;

  if (!operation) return null;
  if (!operation.result) return null;
  return (
    <section
      className="space-y-1 text-sm"
      aria-label={
        operation.cleanup_plan
          ? "Cleanup result"
          : "Deployment result"
      }
    >
      <h6 className="font-medium">
        {operation.cleanup_plan
          ? "Cleanup result"
          : "Deployment result"}
      </h6>
      <p>Outcome: {operation.result.outcome}</p>
      {operation.stack_stop_plan ? (
        <p>
          Recorded stopped:{" "}
          {operation.result.stack_stopped ? "yes" : "not confirmed"}. Named
          volumes are retained.
        </p>
      ) : operation.cleanup_plan ? (
        <p>
          Cleanup outcome is recorded for the exact retained container. Data
          volumes and globally shared images are not deleted by this action.
        </p>
      ) : (
        <p>
          Readiness recorded at completion:{" "}
          {operation.result.ready ? "passed" : "not confirmed"}.
        </p>
      )}
      {operation.result.restored ? (
        <p>The result records restoration of the previous workload.</p>
      ) : null}
      {operation.result.uncertain ? (
        <p>
          The result is uncertain; do not assume the workload is running or
          stopped.
        </p>
      ) : null}
    </section>
  );
}

type ReleaseCleanupReviewModel = Pick<
  ReturnType<typeof useRetainedReleases>,
  | "writable"
  | "target"
  | "cleanup"
  | "confirmed"
  | "busy"
  | "setConfirmed"
  | "matches"
  | "attempt"
  | "paused"
  | "conflict"
  | "mutationHeld"
  | "review"
  | "setTarget"
>;
function ReleaseCleanupReview(viewModel: ReleaseCleanupReviewModel) {
  const {
    writable,
    target,
    cleanup,
    confirmed,
    busy,
    setConfirmed,
    matches,
    attempt,
    paused,
    conflict,
    mutationHeld,
    setTarget,
  } = viewModel;

  if (!(writable && target)) return null;
  return (
    <form
      className="space-y-2 rounded-lg border p-3"
      aria-label="Confirm retained release cleanup"
      onSubmit={(event) => void cleanup(event)}
    >
      <p className="break-all">
        Remove only release {target.healthy.operation_id}, container{" "}
        {target.healthy.container_id}, at 127.0.0.1:
        {target.healthy.snapshot.host_port}?
      </p>
      <p className="text-xs text-muted-foreground">
        This requests exact container cleanup. It does not delete application
        data or volumes, globally prune images, or authorize removal of the
        current workload.
      </p>
      <label className="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          checked={confirmed}
          disabled={busy}
          onChange={(event) => setConfirmed(event.target.checked)}
        />
        <span>
          I confirm cleanup of this exact retained release and container.
        </span>
      </label>
      {!matches && !attempt ? (
        <p role="status">
          The selected release is no longer eligible in the refreshed ledger.
          Keep it and review the current release list.
        </p>
      ) : null}
      <Button
        type="submit"
        className="h-auto max-w-full whitespace-normal"
        disabled={
          paused ||
          busy ||
          conflict ||
          !confirmed ||
          (!attempt && (mutationHeld || !matches))
        }
      >
        {attempt
          ? "Retry identical cleanup request"
          : "Request retained release cleanup"}
      </Button>
      {attempt ? (
        <p className="break-all text-xs">
          Cleanup key: {attempt.idempotency_key} · generation{" "}
          {attempt.expected_generation}. The target and request body remain
          frozen for recovery.
        </p>
      ) : null}
      <ReleaseCleanupConflict {...viewModel} />
      <Button
        type="button"
        variant="outline"
        disabled={busy || Boolean(attempt)}
        onClick={() => setTarget(undefined)}
      >
        Keep retained release
      </Button>
    </form>
  );
}

type ImageDeploymentFormModel = Pick<
  ReturnType<typeof useDeployments>,
  | "writable"
  | "resource"
  | "stateless"
  | "storageOptions"
  | "submit"
  | "base"
  | "binding"
  | "id"
  | "containerPort"
  | "busy"
  | "attempt"
  | "setContainerPort"
  | "setAcknowledged"
  | "portMode"
  | "setPortMode"
  | "hostPort"
  | "setHostPort"
  | "path"
  | "setPath"
  | "timeout"
  | "setTimeout"
  | "actor"
  | "canWrite"
  | "setRegistrySelection"
  | "onDenied"
  | "acknowledged"
  | "newAdmissionHeld"
  | "conflict"
  | "restoreHeld"
  | "registrySelection"
  | "error"
  | "reviewCurrent"
  | "current"
  | "refreshError"
  | "differentIntent"
  | "setDifferentIntent"
  | "unavailable"
  | "mutationHeld"
  | "newIntent"
>;
function ImageDeploymentForm(viewModel: ImageDeploymentFormModel) {
  const {
    writable,
    resource,
    stateless,
    storageOptions,
    submit,
    base,
    binding,
    busy,
    attempt,
    actor,
    canWrite,
    setRegistrySelection,
    onDenied,
    error,
  } = viewModel;

  if (
    !(
      writable &&
      resource.source.kind === "image" &&
      (stateless || storageOptions.isPending)
    )
  )
    return resource.source.kind === "image" ? (
      <p className="text-xs text-muted-foreground">
        Project maintainer access is required to request or cancel deployments.
      </p>
    ) : null;
  return (
    <form
      className="space-y-3 rounded-lg border p-3"
      aria-label={`Deploy image for ${resource.name}`}
      onSubmit={(event) => void submit(event)}
    >
      <p className="text-sm">
        A new deployment captures the current saved runtime generation and
        requires a compatible runner. Saving variables does not update the
        serving release.
      </p>
      <p className="break-all text-sm">
        Deploy saved image{" "}
        {base.source.kind === "image"
          ? base.source.image.reference
          : "unavailable"}{" "}
        at generation {base.generation} to binding{" "}
        {binding.data?.id ?? "unavailable"}.
      </p>
      {!binding.isPending && !binding.isError && !binding.data ? (
        <p role="status">
          Save a desired destination before requesting deployment.
        </p>
      ) : null}
      <ImageDeploymentSettings {...viewModel} />
      <ImageRegistryCredential
        actor={actor}
        project={resource.project_id}
        image={base.source.kind === "image" ? base.source.image.reference : ""}
        disabled={busy || !!attempt || !canWrite}
        onChange={setRegistrySelection}
        onDenied={onDenied}
      />
      <ImageDeploymentAcknowledgment {...viewModel} />{" "}
      <ImageDeploymentSubmit {...viewModel} />
      <ImageDeploymentReceipt {...viewModel} />
      {error ? <p role="alert">{error}</p> : null}
      <SeparateImageDeployment {...viewModel} />
    </form>
  );
}

type ImageDeploymentSettingsModel = Pick<
  ReturnType<typeof useDeployments>,
  | "id"
  | "containerPort"
  | "busy"
  | "attempt"
  | "setContainerPort"
  | "setAcknowledged"
  | "portMode"
  | "setPortMode"
  | "hostPort"
  | "setHostPort"
  | "path"
  | "setPath"
  | "timeout"
  | "setTimeout"
>;
function ImageDeploymentSettings(viewModel: ImageDeploymentSettingsModel) {
  const {
    id,
    containerPort,
    busy,
    attempt,
    setContainerPort,
    setAcknowledged,
    portMode,
    setPortMode,
    hostPort,
    setHostPort,
    path,
    setPath,
    timeout,
    setTimeout,
  } = viewModel;

  return (
    <div className="grid min-w-0 gap-3 sm:grid-cols-2">
      <label className="space-y-1" htmlFor={`${id}-container`}>
        Container port
        <Input
          id={`${id}-container`}
          type="number"
          min={1}
          max={65535}
          required
          value={containerPort}
          disabled={busy || Boolean(attempt)}
          onChange={(event) => {
            setContainerPort(event.target.value);
            setAcknowledged(false);
          }}
        />
      </label>
      <div className="space-y-1">
        <label htmlFor={`${id}-mode`}>Host port allocation</label>
        <select
          id={`${id}-mode`}
          className="h-10 w-full min-w-0 rounded-xl border bg-card px-2 text-sm"
          value={portMode}
          disabled={busy || Boolean(attempt)}
          onChange={(event) => {
            setPortMode(event.target.value);
            setAcknowledged(false);
          }}
        >
          <option value="automatic">Automatic per-release port</option>
          <option value="fixed">Advanced: explicit fixed port</option>
        </select>
      </div>
      {portMode === "fixed" ? (
        <label className="space-y-1" htmlFor={`${id}-host`}>
          Runner-local host port
          <Input
            id={`${id}-host`}
            type="number"
            min={1024}
            max={65535}
            required
            value={hostPort}
            disabled={busy || Boolean(attempt)}
            onChange={(event) => {
              setHostPort(event.target.value);
              setAcknowledged(false);
            }}
          />
        </label>
      ) : null}
      <label className="space-y-1" htmlFor={`${id}-path`}>
        Readiness path
        <Input
          id={`${id}-path`}
          required
          maxLength={1024}
          value={path}
          disabled={busy || Boolean(attempt)}
          onChange={(event) => {
            setPath(event.target.value);
            setAcknowledged(false);
          }}
        />
      </label>
      <label className="space-y-1" htmlFor={`${id}-timeout`}>
        Readiness timeout in seconds
        <Input
          id={`${id}-timeout`}
          type="number"
          min={1}
          max={120}
          required
          value={timeout}
          disabled={busy || Boolean(attempt)}
          onChange={(event) => {
            setTimeout(event.target.value);
            setAcknowledged(false);
          }}
        />
      </label>
    </div>
  );
}

type SeparateImageDeploymentModel = Pick<
  ReturnType<typeof useDeployments>,
  | "attempt"
  | "restoreHeld"
  | "busy"
  | "reviewCurrent"
  | "current"
  | "refreshError"
  | "differentIntent"
  | "setDifferentIntent"
  | "unavailable"
  | "mutationHeld"
  | "newIntent"
>;
function SeparateImageDeployment(viewModel: SeparateImageDeploymentModel) {
  const {
    attempt,
    restoreHeld,
    busy,
    reviewCurrent,
    current,
    refreshError,
    differentIntent,
    setDifferentIntent,
    unavailable,
    mutationHeld,
    newIntent,
  } = viewModel;

  if (!(attempt && !restoreHeld)) return null;
  return (
    <section className="space-y-2" aria-label="Review deployment request">
      <Button
        type="button"
        variant="outline"
        disabled={busy}
        onClick={() => void reviewCurrent()}
      >
        Refresh configuration and history
      </Button>
      {current ? (
        <p className="break-all text-sm">
          Current saved configuration: generation {current.generation} ·{" "}
          {current.source.kind === "image"
            ? current.source.image.reference
            : "Git source"}
          .
        </p>
      ) : null}
      {refreshError ? <p role="alert">{refreshError}</p> : null}
      <label className="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          checked={differentIntent}
          disabled={busy}
          onChange={(event) => setDifferentIntent(event.target.checked)}
        />
        <span>
          I reviewed history and understand the earlier request may already
          exist. I intend a separate request with the current configuration and
          a new request key.
        </span>
      </label>
      <Button
        type="button"
        variant="outline"
        className="h-auto whitespace-normal"
        disabled={
          busy || !current || !differentIntent || unavailable || mutationHeld
        }
        onClick={() => newIntent(current)}
      >
        Use reviewed configuration for a new request
      </Button>
      <p className="text-xs text-muted-foreground">
        This unlocks settings and requires a new deployment acknowledgment. It
        never deploys automatically.
      </p>
    </section>
  );
}

type ComposeDeploymentControlsModel = Pick<
  ReturnType<typeof useDeployments>,
  | "resource"
  | "binding"
  | "actor"
  | "owner"
  | "unavailable"
  | "restoreHeld"
  | "onDenied"
  | "writable"
  | "mutationHeld"
  | "setSelected"
  | "setOffset"
  | "refreshHistory"
  | "storageOptions"
>;
function ComposeDeploymentControls(viewModel: ComposeDeploymentControlsModel) {
  const {
    resource,
    binding,
    actor,
    owner,
    unavailable,
    restoreHeld,
    onDenied,
    writable,
    mutationHeld,
    setSelected,
    setOffset,
    refreshHistory,
    storageOptions,
  } = viewModel;

  return (
    <>
      {resource.source.kind === "compose" ? (
        <>
          {binding.data ? (
            <ComposeStorageBackups
              key={JSON.stringify([
                actor,
                resource.project_id,
                resource.id,
                binding.data.id,
                owner,
              ])}
              scope={{
                actor,
                project: resource.project_id,
                resource: resource.id,
                binding: binding.data.id,
                destination: binding.data.destination_id,
              }}
              generation={resource.generation}
              owner={owner}
              blocked={unavailable || restoreHeld}
              onDenied={onDenied}
            />
          ) : null}
          <StackLifecycle
            key={binding.data?.id ?? "unbound"}
            scope={{
              actor,
              project: resource.project_id,
              resource: resource.id,
            }}
            generation={resource.generation}
            writable={writable}
            blocked={unavailable || restoreHeld}
            mutationHeld={mutationHeld}
            onDenied={onDenied}
            onSelected={(operationID) => {
              setSelected(operationID);
              setOffset(0);
            }}
            onRefresh={refreshHistory}
          />
          {!binding.isPending && !binding.isError && !binding.data ? (
            <p role="status">
              Save a desired destination before requesting deployment.
            </p>
          ) : null}
          {binding.data && storageOptions.isPending ? (
            <p role="status">Loading Compose deployment options...</p>
          ) : null}
          <ComposeDeploymentOptionsStatus {...viewModel} />
          <SavedComposeDeploymentReview {...viewModel} />
          {!writable ? (
            <p className="text-xs text-muted-foreground">
              Project maintainer access is required to deploy saved Compose.
            </p>
          ) : null}
        </>
      ) : null}
    </>
  );
}

type GitDeploymentControlsModel = Pick<
  ReturnType<typeof useDeployments>,
  | "resource"
  | "currentGitStack"
  | "deployedGitCompose"
  | "binding"
  | "actor"
  | "owner"
  | "unavailable"
  | "restoreHeld"
  | "onDenied"
  | "writable"
  | "mutationHeld"
  | "setSelected"
  | "setOffset"
  | "refreshHistory"
>;
function GitDeploymentControls(viewModel: GitDeploymentControlsModel) {
  const {
    resource,
    currentGitStack,
    deployedGitCompose,
    binding,
    actor,
    owner,
    unavailable,
    restoreHeld,
    onDenied,
    writable,
    mutationHeld,
    setSelected,
    setOffset,
    refreshHistory,
  } = viewModel;

  return (
    <>
      {resource.source.kind === "dockerfile" ? (
        <p role="status">
          Dockerfile source generation {resource.source.dockerfile.generation}{" "}
          is saved. Use Dockerfile builds to prepare an image, then select its
          successful artifact for deployment. Configure the HTTPS route after
          the release settles healthy.
        </p>
      ) : null}
      {resource.source.kind === "git" ? (
        <p role="status">
          Git source generation {resource.source.git.generation} is saved. Use
          Git builds to prepare the source and select a successful image or
          Compose result for deployment. Review the resulting operation here,
          then configure the existing HTTPS route after the release settles
          healthy.
        </p>
      ) : null}
      {resource.source.kind === "git" &&
      (currentGitStack.isPending || currentGitStack.isError) ? (
        <p role={currentGitStack.isError ? "alert" : "status"}>
          Current stack evidence is pending or unavailable. Stack controls are
          paused.
        </p>
      ) : null}
      {resource.source.kind === "git" ? (
        <Button
          variant="outline"
          disabled={currentGitStack.isFetching}
          onClick={() => void currentGitStack.refetch()}
        >
          Refresh current stack evidence
        </Button>
      ) : null}
      {deployedGitCompose && binding.data ? (
        <ComposeStorageBackups
          key={JSON.stringify([
            actor,
            resource.id,
            binding.data.id,
            currentGitStack.data?.operation?.id,
            owner,
          ])}
          scope={{
            actor,
            project: resource.project_id,
            resource: resource.id,
            binding: binding.data.id,
            destination: binding.data.destination_id,
          }}
          generation={resource.generation}
          owner={owner}
          blocked={unavailable || restoreHeld}
          onDenied={onDenied}
        />
      ) : null}
      {deployedGitCompose ? (
        <StackLifecycle
          key={currentGitStack.data?.operation?.id ?? "unavailable"}
          scope={{ actor, project: resource.project_id, resource: resource.id }}
          generation={resource.generation}
          writable={writable}
          blocked={unavailable || restoreHeld}
          mutationHeld={mutationHeld}
          onDenied={onDenied}
          onSelected={(operationID) => {
            setSelected(operationID);
            setOffset(0);
          }}
          onRefresh={async (signal) => {
            await refreshHistory(signal);
            await currentGitStack.refetch();
          }}
          gitBuilds
        />
      ) : null}
    </>
  );
}

type DeploymentHistoryModel = Pick<
  ReturnType<typeof useDeployments>,
  | "history"
  | "busy"
  | "resource"
  | "currentGitStack"
  | "setSelected"
  | "setOffset"
  | "offset"
>;
function DeploymentHistory(viewModel: DeploymentHistoryModel) {
  const {
    history,
    busy,
    resource,
    currentGitStack,
    setSelected,
    setOffset,
    offset,
  } = viewModel;

  return (
    <>
      {history.isPending ? (
        <p role="status">Loading deployment history...</p>
      ) : null}
      <Button
        type="button"
        variant="outline"
        disabled={history.isFetching || busy}
        onClick={() => {
          void history.refetch();
          if (resource.source.kind === "git") void currentGitStack.refetch();
        }}
      >
        Refresh deployment history
      </Button>
      {history.data?.items.length === 0 ? (
        <p className="text-sm">No deployment operations recorded.</p>
      ) : null}
      <ol className="space-y-2" aria-label="Deployment history">
        {history.data?.items.map((operation) => (
          <li key={operation.id} className="min-w-0 rounded-lg border p-2">
            <p className="break-all text-sm">
              {stateText[operation.state] ?? operation.state} ·{" "}
              {operation.created_at}
            </p>
            <p className="break-all text-xs">
              {"stack" in operation.snapshot && operation.snapshot.stack
                ? `Saved Compose ${operation.snapshot.stack.hash}`
                : operation.snapshot.local_build
                  ? `Local build ${operation.snapshot.local_build.artifact.build_id} · ${operation.snapshot.local_build.artifact.image_id}`
                  : operation.snapshot.image_reference}{" "}
              · generation {operation.snapshot.resource_generation}
            </p>
            <Button
              type="button"
              variant="outline"
              className="h-auto max-w-full whitespace-normal break-all"
              onClick={() => setSelected(operation.id)}
            >
              View operation {operation.id}
            </Button>
          </li>
        ))}
      </ol>
      <PageNavigation
        label={`deployments for ${resource.name}`}
        page={history.data}
        loading={history.isFetching || busy}
        onFirst={() => setOffset(0)}
        onPrevious={() => setOffset(Math.max(0, offset - 20))}
        onNext={() => setOffset(Math.min(maximumPageOffset, offset + 20))}
      />
    </>
  );
}

type OperationControlsModel = Pick<
  ReturnType<typeof useOperationDetail>,
  "writable" | "operation" | "blocked" | "busy" | "detail" | "control"
>;
function OperationControls(viewModel: OperationControlsModel) {
  const { writable, operation, blocked, busy, detail, control } = viewModel;

  if (!operation) return null;
  if (
    !(
      writable &&
      !terminalStates.has(operation.state) &&
      !["cancel_requested", "reconciliation_required"].includes(operation.state)
    )
  )
    return null;
  return (
    <Button
      type="button"
      variant="outline"
      disabled={blocked || busy || detail.isError}
      onClick={() => void control("cancel")}
    >
      Request cancellation
    </Button>
  );
}

type RetainedDeploymentReleasesModel = Pick<
  ReturnType<typeof useDeployments>,
  | "binding"
  | "resource"
  | "gitStackChecked"
  | "deployedGitCompose"
  | "actor"
  | "principal"
  | "writable"
  | "base"
  | "unavailable"
  | "mutationHeld"
  | "restoreHeld"
  | "setSelected"
  | "refreshHistory"
>;
function RetainedDeploymentReleases(
  viewModel: RetainedDeploymentReleasesModel,
) {
  const {
    binding,
    resource,
    gitStackChecked,
    deployedGitCompose,
    actor,
    principal,
    writable,
    base,
    unavailable,
    mutationHeld,
    restoreHeld,
    setSelected,
    refreshHistory,
  } = viewModel;

  if (
    !(
      binding.data &&
      resource.source.kind !== "compose" &&
      gitStackChecked &&
      !deployedGitCompose
    )
  )
    return null;
  return (
    <RetainedReleases
      actor={actor}
      key={JSON.stringify([
        resource.project_id,
        resource.id,
        binding.data.id,
        principal.data?.provider,
        principal.data?.id,
        writable,
      ])}
      resource={base}
      writable={writable}
      blocked={unavailable}
      mutationHeld={mutationHeld}
      restoreHeld={restoreHeld}
      onSelected={setSelected}
      onChanged={refreshHistory}
    />
  );
}

type ImageStorageDeploymentModel = Pick<
  ReturnType<typeof useDeployments>,
  | "writable"
  | "resource"
  | "storageOptions"
  | "stateless"
  | "binding"
  | "actor"
  | "unavailable"
  | "restoreHeld"
  | "mutationHeld"
  | "onDenied"
  | "setSelected"
  | "setOffset"
  | "refreshHistory"
>;
function ImageStorageDeployment(viewModel: ImageStorageDeploymentModel) {
  const {
    writable,
    resource,
    storageOptions,
    stateless,
    binding,
    actor,
    unavailable,
    restoreHeld,
    mutationHeld,
    onDenied,
    setSelected,
    setOffset,
    refreshHistory,
  } = viewModel;

  if (
    !(
      writable &&
      resource.source.kind === "image" &&
      !storageOptions.isError &&
      storageOptions.data &&
      !stateless
    )
  )
    return null;
  return (
    <StorageDeploymentReview
      key={binding.data?.id ?? "unbound"}
      bindingID={binding.data?.id ?? ""}
      scope={{ actor, project: resource.project_id, resource: resource.id }}
      resource={resource}
      options={storageOptions.data}
      blocked={unavailable || restoreHeld}
      mutationHeld={mutationHeld}
      onDenied={onDenied}
      onSelected={(operationID) => {
        setSelected(operationID);
        setOffset(0);
      }}
      onRefresh={refreshHistory}
    />
  );
}

type ImageDeploymentSubmitModel = Pick<
  ReturnType<typeof useDeployments>,
  | "stateless"
  | "canWrite"
  | "newAdmissionHeld"
  | "busy"
  | "binding"
  | "acknowledged"
  | "conflict"
  | "restoreHeld"
  | "attempt"
  | "registrySelection"
>;
function ImageDeploymentSubmit(viewModel: ImageDeploymentSubmitModel) {
  const {
    stateless,
    canWrite,
    newAdmissionHeld,
    busy,
    binding,
    acknowledged,
    conflict,
    restoreHeld,
    attempt,
    registrySelection,
  } = viewModel;

  return (
    <Button
      type="submit"
      className="h-auto whitespace-normal"
      disabled={
        !stateless ||
        !canWrite ||
        newAdmissionHeld ||
        busy ||
        !binding.data ||
        !acknowledged ||
        conflict ||
        restoreHeld ||
        (!attempt && !registrySelection.valid)
      }
    >
      {restoreHeld
        ? "Deployment blocked after restore"
        : newAdmissionHeld
          ? "Deployment waiting for settlement"
          : busy
            ? "Requesting deployment..."
            : attempt
              ? "Retry identical deployment request"
              : "Request image deployment"}
    </Button>
  );
}

type ImageDeploymentAcknowledgmentModel = Pick<
  ReturnType<typeof useDeployments>,
  "portMode" | "hostPort" | "acknowledged" | "busy" | "setAcknowledged"
>;
function ImageDeploymentAcknowledgment(
  viewModel: ImageDeploymentAcknowledgmentModel,
) {
  const { portMode, hostPort, acknowledged, busy, setAcknowledged } = viewModel;

  return (
    <>
      <p className="text-xs text-muted-foreground">
        Use an HTTP readiness path such as /health.{" "}
        {portMode === "fixed"
          ? `Port 127.0.0.1:${hostPort}`
          : "The allocated loopback port"}{" "}
        is local to the runner. Configure a route for public access. Automatic allocation retains prior containers until explicit cleanup.
      </p>
      {portMode === "fixed" ? (
        <p role="note" className="text-sm">
          The current release stops before the replacement is ready. Protected ports and ports used by another binding cannot be reused.
        </p>
      ) : null}
      <label className="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          checked={acknowledged}
          disabled={busy}
          onChange={(event) => setAcknowledged(event.target.checked)}
        />
        <span>
          I understand that{" "}
          {portMode === "fixed"
            ? "reusing the current release’s unprotected port stops it before replacement readiness is verified and interrupts service; retained-release, other-binding, and proxy-protected port conflicts are rejected"
            : "the server allocates a new release port and previous containers remain until explicit cleanup"}
          . No automatic traffic switch or zero-downtime behavior is promised.
        </span>
      </label>
    </>
  );
}

type OperationOwnerControlsModel = Pick<
  ReturnType<typeof useOperationDetail>,
  "owner" | "blocked" | "busy" | "detail" | "operation" | "control"
>;
function OperationOwnerControls(viewModel: OperationOwnerControlsModel) {
  const { owner, blocked, busy, detail, operation, control } = viewModel;

  if (!operation) return null;
  if (!owner)
    return (
      <p className="text-xs text-muted-foreground">
        Project owner access is required to request reconciliation.
      </p>
    );
  return (
    <Button
      type="button"
      variant="outline"
      disabled={
        blocked || busy || detail.isError || operation.reconcile_requested
      }
      onClick={() => void control("reconcile")}
    >
      {operation.reconcile_requested
        ? "Reconciliation pending"
        : "Request reconciliation"}
    </Button>
  );
}

type SavedComposeDeploymentReviewModel = Pick<
  ReturnType<typeof useDeployments>,
  | "writable"
  | "storageOptions"
  | "binding"
  | "resource"
  | "actor"
  | "unavailable"
  | "restoreHeld"
  | "mutationHeld"
  | "onDenied"
  | "setSelected"
  | "setOffset"
  | "refreshHistory"
>;
function SavedComposeDeploymentReview(
  viewModel: SavedComposeDeploymentReviewModel,
) {
  const {
    writable,
    storageOptions,
    binding,
    resource,
    actor,
    unavailable,
    restoreHeld,
    mutationHeld,
    onDenied,
    setSelected,
    setOffset,
    refreshHistory,
  } = viewModel;

  if (!(writable && storageOptions.data && !storageOptions.isError))
    return null;
  return (
    <StackDeploymentReview
      key={JSON.stringify([
        binding.data?.id,
        storageOptions.data.stopped_operation_id,
      ])}
      resource={resource}
      scope={{ actor, project: resource.project_id, resource: resource.id }}
      bindingID={binding.data?.id ?? ""}
      options={storageOptions.data}
      blocked={unavailable || restoreHeld}
      mutationHeld={mutationHeld}
      onDenied={onDenied}
      onSelected={(operationID) => {
        setSelected(operationID);
        setOffset(0);
      }}
      onRefresh={refreshHistory}
    />
  );
}

type ReleaseCleanupConflictModel = Pick<
  ReturnType<typeof useRetainedReleases>,
  "conflict" | "busy" | "paused" | "mutationHeld" | "review"
>;
function ReleaseCleanupConflict(viewModel: ReleaseCleanupConflictModel) {
  const { conflict, busy, paused, mutationHeld, review } = viewModel;

  if (!conflict) return null;
  return (
    <>
      <p role="status">
        Cleanup conflicts with current generation or release protection. Review
        the refreshed target before a separate request; nothing was
        automatically resubmitted.
      </p>
      <Button
        type="button"
        variant="outline"
        className="h-auto max-w-full whitespace-normal"
        disabled={busy || paused || mutationHeld}
        onClick={() => void review()}
      >
        Review current cleanup generation
      </Button>
      <p className="text-xs">
        Review clears the prior request and confirmation, then requires explicit
        confirmation again.
      </p>
    </>
  );
}

type OperationReleaseEvidenceModel = Pick<
  ReturnType<typeof useOperationDetail>,
  "operation"
>;
function OperationReleaseEvidence(viewModel: OperationReleaseEvidenceModel) {
  const { operation } = viewModel;

  if (!operation) return null;

  return (
    <>
      {operation.release?.stack ? (
        <>
          <dt>Released Compose hash</dt>
          <dd>{operation.release.stack.hash}</dd>
        </>
      ) : operation.release ? (
        <>
          <dt>
            {operation.release.local_build
              ? "Released local build"
              : "Resolved image"}
          </dt>
          <dd>
            {operation.release.local_build?.build_id ??
              operation.release.canonical_image}
          </dd>
          <dt>Image ID</dt>
          <dd>{operation.release.image_id}</dd>
        </>
      ) : null}
    </>
  );
}

function useDeploymentIdentity(projectID: string) {
  const principal = useQuery(principalQuery());
  const members = useQuery({ ...allProjectMembersQuery(), staleTime: 10_000 });
  const owner = canOwnProject(principal.data, members.data ?? [], projectID);
  return { principal, members, owner };
}

type ImageDeploymentReceiptModel = Pick<
  ReturnType<typeof useDeployments>,
  "attempt" | "restoreHeld"
>;
function ImageDeploymentReceipt(viewModel: ImageDeploymentReceiptModel) {
  const { attempt, restoreHeld } = viewModel;

  if (!attempt) return null;
  return (
    <p className="break-all text-xs">
      Request key: {attempt.idempotency_key}. Submitted settings and
      resource/runtime generations {attempt.expected_generation}/
      {attempt.expected_runtime_configuration_generation ?? 0} are retained
      {restoreHeld
        ? ". Deployment admission remains blocked."
        : " for an identical retry."}
    </p>
  );
}

type ComposeDeploymentOptionsStatusModel = Pick<
  ReturnType<typeof useDeployments>,
  "storageOptions"
>;
function ComposeDeploymentOptionsStatus(
  viewModel: ComposeDeploymentOptionsStatusModel,
) {
  const { storageOptions } = viewModel;

  if (!storageOptions.isError) return null;
  return (
    <>
      <p role="alert">
        Compose deployment authority is unavailable. Refresh before reviewing a
        deployment.
      </p>
      <Button
        type="button"
        variant="outline"
        disabled={storageOptions.isFetching}
        onClick={() => void storageOptions.refetch()}
      >
        Retry Compose deployment options
      </Button>
    </>
  );
}

type OperationSourceEvidenceModel = Pick<
  ReturnType<typeof useOperationDetail>,
  "stackRef" | "operation" | "gitCompose"
>;
function OperationSourceEvidence(viewModel: OperationSourceEvidenceModel) {
  const { stackRef, operation, gitCompose } = viewModel;

  if (!operation) return null;

  return (
    <>
      <dt>
        {stackRef
          ? "Source"
          : operation.snapshot.local_build
            ? "Local build / source generation"
            : "Requested image"}
      </dt>
      <dd>
        {stackRef
          ? gitCompose
            ? `Git Compose build ${gitCompose.build_id} / source ${gitCompose.source_generation}`
            : "Saved Compose"
          : operation.snapshot.local_build
            ? `${operation.snapshot.local_build.artifact.build_id} / ${operation.snapshot.local_build.source_generation}`
            : operation.snapshot.image_reference}
      </dd>
      {operation.snapshot.local_build ? (
        <>
          <dt>Recorded artifact image ID</dt>
          <dd>{operation.snapshot.local_build.artifact.image_id}</dd>
          <dt>Source ID / artifact hash</dt>
          <dd>
            {operation.snapshot.local_build.artifact.source_id} /{" "}
            {operation.snapshot.local_build.artifact_hash}
          </dd>
          <dt>Manifest / config digest</dt>
          <dd>
            {operation.snapshot.local_build.artifact.manifest_digest} /{" "}
            {operation.snapshot.local_build.artifact.config_digest}
          </dd>
        </>
      ) : null}
    </>
  );
}
