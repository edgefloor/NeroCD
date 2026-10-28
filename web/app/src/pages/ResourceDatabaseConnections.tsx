import {
  allProjectMembersQuery,
  ApiError,
  principalQuery,
  queryKeys,
  type Principal,
  type ProjectMember,
} from "@/api";
import { getResourceBinding } from "@/api/destinations";
import { getResource, type Resource } from "@/api/hosting";
import {
  connectDatabase,
  connectedProgressQuery,
  connectionError,
  connectionKey,
  connectionOptionsQuery,
  connectionQuery,
  connectionRouteIntent,
  connectionStorageReviewQuery,
  deployConnectedApplication,
  getConnectionStorageReview,
  maintainConnectedDatabase,
  type ConnectedDeploymentInput,
  type ConnectedProgress,
  type DatabaseConnection,
  type DatabaseConnectionInput,
  type DatabaseConnectionScope,
  type DatabaseMaintenanceInput,
} from "@/api/hosting-database-connections";
import {
  boundedDatabaseRequest,
  databaseDenied,
  databaseRequestID,
} from "@/api/hosting-databases";
import {
  getSwitchOptions,
  switchOptionsQuery,
} from "@/api/hosting-route-switch";
import { getHostingRuntimeConfiguration } from "@/api/hosting-runtime-config";
import { ConnectedStorageRecovery } from "@/components/hosting/ConnectedStorageRecovery";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { canOwnProject } from "@/lib/permissions";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { ResourceDeployments } from "./ResourceDeployments";

const states: Record<string, string> = {
  binding_saved: "Connection saved",
  capture_requested: "Network observation requested",
  capture_waiting: "Waiting for network observation",
  upgrade_requested: "Database network update requested",
  upgrade_waiting: "Waiting for database network update",
  application_requested: "Application deployment requested",
  active: "Connection workflow completed",
  held: "Connection requires review",
};
export function ResourceDatabaseConnections({
  resource,
  writable,
  blocked,
}: {
  resource: Resource;
  writable: boolean;
  blocked: boolean;
}) {
  const identity = useQuery(principalQuery());
  const members = useQuery({ ...allProjectMembersQuery(), staleTime: 10_000 });
  const [denied, setDenied] = useState("");
  const actor = identity.data
    ? JSON.stringify([identity.data.provider, identity.data.id])
    : "";
  const scopeID = JSON.stringify([actor, resource.project_id, resource.id]);
  if (!identity.data || !members.data)
    return (
      <p role="status">
        Database connection access is pending or unavailable. Refresh project
        access to continue.
      </p>
    );
  if (
    denied === scopeID ||
    databaseDenied(identity.error) ||
    databaseDenied(members.error)
  )
    return (
      <p role="alert">
        Database connection permission is unavailable. Reload to verify access.
        Retained requests and drafts have been cleared.
      </p>
    );
  const owner =
    writable &&
    canOwnProject(identity.data, members.data ?? [], resource.project_id);
  return (
    <ConnectionSession
      key={JSON.stringify([scopeID, owner])}
      resource={resource}
      owner={owner}
      legacyWritable={writable}
      accessPaused={
        blocked ||
        identity.isFetching ||
        identity.isError ||
        members.isFetching ||
        members.isError
      }
      scope={{ actor, project: resource.project_id, resource: resource.id }}
      onDenied={() => setDenied(scopeID)}
    />
  );
}
function ConnectionSession(props: {
  resource: Resource;
  owner: boolean;
  legacyWritable: boolean;
  accessPaused: boolean;
  scope: DatabaseConnectionScope;
  onDenied: () => void;
}) {
  const viewModel = useConnectionSession(props);
  const {
    resource,
    legacyWritable,
    accessPaused,
    status,
    busy,
    error,
    notice,
    attempt,
    denied,
    review,
  } = viewModel;
  if (resource.source.kind !== "image")
    return (
      <>
        <p>
          Database connections require a public container image. Source builds
          are unsupported.
        </p>
        <ResourceDeployments
          resource={resource}
          writable={legacyWritable}
          blocked={accessPaused}
        />
      </>
    );
  if (denied)
    return <p role="alert">Database connection access is unavailable.</p>;
  return (
    <section
      className="min-w-0 space-y-3 border-t pt-3"
      aria-label={`Database connection for ${resource.name}`}
    >
      <h5 className="font-medium">Database connection</h5>
      {accessPaused ? (
        <p role="status">
          Project access is being verified or is temporarily unavailable.
          Changes are paused; retained requests and draft settings are
          preserved.
        </p>
      ) : null}
      {status.isPending ? (
        <p role="status">Checking saved database connection...</p>
      ) : null}
      {status.isError ? (
        <p role="alert">
          Unable to verify the database connection. Deployment controls are
          paused. {connectionError(status.error)}
        </p>
      ) : null}
      <Button
        type="button"
        variant="outline"
        className="h-auto min-h-9 max-w-full min-w-0 whitespace-normal break-words"
        disabled={accessPaused || busy || status.isFetching}
        onClick={() => void review()}
      >
        Refresh database connection
      </Button>
      <ConnectionRecord {...viewModel} />
      <ConnectionRequestForm {...viewModel} />
      {attempt ? (
        <p className="break-all text-xs">
          Retained request:{" "}
          {attempt.kind === "connect"
            ? attempt.body.binding.request_id
            : attempt.body.idempotency_key}
          . Settings stay frozen for an identical retry.
        </p>
      ) : null}
      <ConnectionConflictReview {...viewModel} />
      {error ? <p role="alert">{error}</p> : null}
      {notice ? (
        <p role="status" className="break-all">
          {notice}
        </p>
      ) : null}
      <UnconnectedApplicationDeployments {...viewModel} />
    </section>
  );
}

function useConnectionSession({
  resource,
  owner,
  legacyWritable,
  accessPaused,
  scope,
  onDenied,
}: {
  resource: Resource;
  owner: boolean;
  legacyWritable: boolean;
  accessPaused: boolean;
  scope: DatabaseConnectionScope;
  onDenied: () => void;
}) {
  const client = useQueryClient();
  const id = useId();
  const active = useRef(true);
  const pending = useRef<AbortController | null>(null);
  const submitting = useRef(false);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      pending.current?.abort();
    };
  }, []);
  const status = useQuery({
    ...connectionQuery(scope),
    enabled: resource.source.kind === "image",
    refetchInterval: (query) => {
      const value = query.state.data;
      return !query.state.error &&
        value &&
        Object.hasOwn(states, value.state) &&
        (value.state !== "active" || Boolean(value.hold_operation_id)) &&
        value.state !== "held" &&
        query.state.dataUpdateCount < 24
        ? 5000
        : false;
    },
  });
  const options = useQuery({
    ...connectionOptionsQuery(scope),
    enabled:
      resource.source.kind === "image" &&
      status.data === null &&
      !status.isError,
  });
  const [database, setDatabase] = useState("");
  const [port, setPort] = useState("8080");
  const [readiness, setReadiness] = useState("/");
  const [timeout, setTimeoutValue] = useState("30");
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [conflict, setConflict] = useState(false);
  type Attempt =
    | { kind: "connect"; body: DatabaseConnectionInput }
    | { kind: "deploy"; body: ConnectedDeploymentInput }
    | { kind: "maintain"; database: string; body: DatabaseMaintenanceInput };
  const [attempt, setAttempt] = useState<Attempt>();
  const [reviewed, setReviewed] = useState(false);
  const reviewResource = useRef<Resource>(undefined);
  const reviewBase = useRef<Resource>(undefined);
  const setReviewBase = (value: Resource | undefined) => {
    reviewBase.current = value;
  };
  const connection = status.data;
  const [continueRoutes, setContinueRoutes] = useState(false);
  const [approvedRouteIntent, setApprovedRouteIntent] = useState<string>();
  const [acknowledgedStorage, setAcknowledgedStorage] = useState<string>();
  const [approvedStorage, setApprovedStorage] = useState<string>();
  const [operationID, setOperationID] = useState("");
  const { storageReview, storageData, routeChoices, routeIntent } =
    useConnectionPrerequisites(scope, resource, owner, connection);
  const {
    storageIdentity,
    hasStorage,
    storageAcknowledged,
    storageReady,
    storageRoutesReady,
    routeIntentIdentity,
    approvalValid,
    routeApprovalChanged,
  } = connectionStorageApproval({
    storageData,
    connection,
    acknowledgedStorage,
    continueRoutes,
    routeIntent,
    confirmed,
    retained: Boolean(attempt),
    approvedRouteIntent,
    approvedStorage,
  });
  const { recoveryPoll, progressID, progress } = useConnectionProgress(
    scope,
    operationID,
    connection,
  );
  const selected = options.data?.items.find(
    (item) => item.resource_id === database,
  );
  const deploymentPending = connectionDeploymentPending(
    progress.data,
    progress.isError,
    operationID,
  );
  const paused =
    deploymentPending ||
    accessPaused ||
    busy ||
    status.isPending ||
    status.isError ||
    status.isFetching ||
    (connection && !Object.hasOwn(states, connection.state));
  const denied = [
    status.error,
    options.error,
    routeChoices.error,
    progress.error,
    storageReview.error,
  ].some(databaseDenied);
  useEffect(() => {
    if (denied) onDenied();
  }, [denied, onDenied]);
  function currentAuthority() {
    const principal = client.getQueryData<Principal>(queryKeys.principal);
    const membership = client.getQueryData<ProjectMember[]>(
      queryKeys.allProjectMembers,
    );
    const identityState = client.getQueryState(queryKeys.principal);
    const membershipState = client.getQueryState(queryKeys.allProjectMembers);
    return (
      active.current &&
      owner &&
      principal &&
      JSON.stringify([principal.provider, principal.id]) === scope.actor &&
      membership &&
      canOwnProject(principal, membership, scope.project) &&
      identityState?.status === "success" &&
      membershipState?.status === "success" &&
      identityState.fetchStatus !== "fetching" &&
      membershipState.fetchStatus !== "fetching" &&
      !databaseDenied(client.getQueryState(connectionKey(scope))?.error) &&
      !databaseDenied(
        client.getQueryState(connectionOptionsQuery(scope).queryKey)?.error,
      ) &&
      !databaseDenied(
        client.getQueryState(switchOptionsQuery(scope).queryKey)?.error,
      ) &&
      !databaseDenied(
        client.getQueryState(connectedProgressQuery(scope, progressID).queryKey)
          ?.error,
      ) &&
      !databaseDenied(
        client.getQueryState(connectionStorageReviewQuery(scope).queryKey)
          ?.error,
      )
    );
  }
  async function settleAuthority() {
    // Join existing revalidation without starting another authority read. Query
    // cache updates are synchronous; React observer notifications may be deferred.
    const reads: Promise<unknown>[] = [];
    if (client.getQueryState(queryKeys.principal)?.fetchStatus === "fetching")
      reads.push(client.fetchQuery({ ...principalQuery(), staleTime: 0 }));
    if (
      client.getQueryState(queryKeys.allProjectMembers)?.fetchStatus ===
      "fetching"
    )
      reads.push(
        client.fetchQuery({ ...allProjectMembersQuery(), staleTime: 0 }),
      );
    try {
      await boundedDatabaseRequest(
        () => Promise.all(reads),
        pending.current?.signal,
      );
    } catch {
      return false;
    }
    return Boolean(currentAuthority());
  }
  async function refresh() {
    recoveryPoll.current = undefined;
    const latest = await status.refetch();
    if (databaseDenied(latest.error)) {
      onDenied();
      throw latest.error;
    }
    if (latest.error) throw latest.error;
    if (owner) await storageReview.refetch();
    if (connection && owner) await routeChoices.refetch();
    if (progressID) await progress.refetch();
    if (latest.data === null) {
      const choices = await options.refetch();
      if (databaseDenied(choices.error)) {
        onDenied();
        throw choices.error;
      }
      if (choices.error) throw choices.error;
    }
  }
  async function requestAction(kind: Attempt["kind"], event?: FormEvent) {
    event?.preventDefault();
    if (
      !attempt &&
      kind !== "maintain" &&
      (!storageReady ||
        (hasStorage && !storageAcknowledged) ||
        (kind === "deploy" && !storageRoutesReady))
    )
      return;
    if (!attempt && kind === "deploy" && continueRoutes && !routeIntent) return;
    if (
      !currentAuthority() ||
      submitting.current ||
      paused ||
      conflict ||
      !approvalValid ||
      (attempt && attempt.kind !== kind)
    )
      return;
    if (
      !attempt &&
      (kind === "connect"
        ? connection !== null ||
          !selected ||
          selected.status !== "available" ||
          selected.reason_code !== "eligible" ||
          options.isError ||
          options.isFetching
        : !connection ||
          connection.state !== "active" ||
          Boolean(connection.hold_operation_id))
    )
      return;
    const controller = new AbortController();
    pending.current = controller;
    submitting.current = true;
    setBusy(true);
    setError("");
    setNotice("");
    let input = attempt;
    try {
      if (!input) {
        const latest = await boundedDatabaseRequest(
          (signal) =>
            getResource(
              kind === "maintain"
                ? connection!.database_resource_id
                : resource.id,
              { signal },
            ),
          controller.signal,
        );
        if (!active.current || controller.signal.aborted) return;
        if (
          latest.project_id !== scope.project ||
          latest.id !==
            (kind === "maintain"
              ? connection!.database_resource_id
              : resource.id) ||
          latest.kind !== (kind === "maintain" ? "database" : "application")
        )
          throw new Error("Resource scope mismatch");
        if (
          kind !== "maintain" &&
          (latest.source.kind !== "image" ||
            latest.generation !== (reviewBase.current ?? resource).generation)
        )
          throw new ApiError(409, "Application configuration changed", null);
        if (kind === "maintain") {
          input = {
            kind,
            database: connection!.database_resource_id,
            body: {
              expected_generation: latest.generation,
              expected_membership_revision: connection!.membership_revision,
              idempotency_key: databaseRequestID(),
            },
          };
        } else {
          const { runtime_configuration: runtime } =
            await boundedDatabaseRequest(
              (signal) =>
                getHostingRuntimeConfiguration(resource.id, { signal }),
              controller.signal,
            );
          if (!active.current || controller.signal.aborted) return;
          if (
            runtime.resource_id !== resource.id ||
            runtime.project_id !== scope.project ||
            runtime.resource_generation !== latest.generation ||
            !Number.isSafeInteger(runtime.runtime_generation) ||
            runtime.runtime_generation < 0
          )
            throw new Error("Runtime configuration scope mismatch");
          const deployment: ConnectedDeploymentInput = {
            expected_generation: latest.generation,
            expected_runtime_configuration_generation:
              runtime.runtime_generation,
            idempotency_key: databaseRequestID(),
            container_port: Number(port),
            readiness: { path: readiness, timeout_seconds: Number(timeout) },
          };
          const currentStorage = await getConnectionStorageReview(
            scope,
            controller.signal,
          );
          const currentStorageIdentity = JSON.stringify({
            options: currentStorage.options,
            mount: currentStorage.storage?.mount_path,
          });
          if (
            currentStorageIdentity !== storageIdentity ||
            currentStorage.options.resource_generation !== latest.generation ||
            (!currentStorage.options.storage_ref &&
              currentStorage.options.reason !== "storage_unconfigured")
          )
            throw new ApiError(409, "Storage configuration changed", null);
          if (currentStorage.options.storage_ref) {
            const descriptor =
              currentStorage.options.application_database_storage;
            if (
              kind !== "deploy" ||
              !currentStorage.options.available ||
              !descriptor ||
              !storageAcknowledged
            )
              throw new ApiError(
                409,
                "Storage deployment prerequisites changed",
                null,
              );
            deployment.application_database_storage = {
              expected_storage_generation:
                descriptor.expected_storage_generation,
              expected_storage_hash: descriptor.expected_storage_hash,
              acknowledge_interruption: true,
            };
          }
          if (kind === "deploy" && continueRoutes) {
            const choices = await getSwitchOptions(
              scope,
              "",
              controller.signal,
            );
            const current = connectionRouteIntent(choices);
            if (
              !current ||
              !routeIntent ||
              JSON.stringify(current) !== JSON.stringify(routeIntent)
            )
              throw new ApiError(409, "Loaded routes changed", null);
            deployment.application_database_route_continuity = current;
          }
          if (kind === "connect") {
            const currentOptions = await options.refetch();
            if (currentOptions.error) throw currentOptions.error;
            const choice = currentOptions.data?.items.find(
              (item) => item.resource_id === database,
            );
            if (
              !choice ||
              choice.status !== "available" ||
              choice.reason_code !== "eligible" ||
              choice.generation !== selected?.generation ||
              choice.destination_id !== selected?.destination_id
            ) {
              setConflict(true);
              throw new ApiError(409, "Selection changed", null);
            }
            const binding = await boundedDatabaseRequest(
              (signal) => getResourceBinding(resource.id, { signal }),
              controller.signal,
            );
            if (!active.current || controller.signal.aborted) return;
            if (
              binding.resource_id !== resource.id ||
              binding.destination_id !== choice.destination_id
            )
              throw new Error("Destination scope mismatch");
            input = {
              kind,
              body: {
                binding: {
                  database_resource_id: choice.resource_id,
                  expected_database_generation: choice.generation,
                  expected_resource_generation: latest.generation,
                  expected_runtime_generation: runtime.runtime_generation,
                  request_id: databaseRequestID(),
                },
                deployment,
              },
            };
          } else input = { kind, body: deployment };
        }
        if (!active.current || controller.signal.aborted) return;
        setAttempt(input);
      }
      if (!(await settleAuthority()) || controller.signal.aborted) return;
      const operation =
        input.kind === "connect"
          ? await connectDatabase(scope, input.body, controller.signal)
          : input.kind === "deploy"
            ? await deployConnectedApplication(
                scope,
                input.body,
                controller.signal,
              )
            : await maintainConnectedDatabase(
                scope,
                input.database,
                input.body,
                controller.signal,
              );
      if (!active.current || controller.signal.aborted) return;
      if (!(await settleAuthority()) || controller.signal.aborted) return;
      setNotice(
        `Request accepted: ${operation.id}. Review recorded progress; acceptance does not confirm runtime health.`,
      );
      if (
        input.kind === "deploy" &&
        (input.body.application_database_route_continuity ||
          input.body.application_database_storage)
      )
        setOperationID(operation.id);
      await client.invalidateQueries({
        queryKey: ["hostingRoutes", scope.actor, scope.project, scope.resource],
      });
      await client.invalidateQueries({
        queryKey: [
          "routeSwitchOptions",
          scope.actor,
          scope.project,
          scope.resource,
        ],
      });
      setAttempt(undefined);
      setConfirmed(false);
      if (input.kind === "connect")
        client.setQueryData(connectionKey(scope), operation);
      await refresh();
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      if (databaseDenied(cause)) {
        onDenied();
        return;
      }
      if (!(await settleAuthority()) || controller.signal.aborted) return;
      setError(
        !input && !(cause instanceof ApiError)
          ? "Unable to verify current configuration. No new request was sent. Refresh before retrying."
          : connectionError(cause),
      );
      if (cause instanceof ApiError && cause.status === 400) {
        setAttempt(undefined);
        setConfirmed(false);
      }
      if (cause instanceof ApiError && cause.status === 409) {
        setConflict(true);
        setReviewed(false);
      }
    } finally {
      submitting.current = false;
      if (pending.current === controller) pending.current = null;
      if (active.current) setBusy(false);
    }
  }
  async function review() {
    if (busy || accessPaused) return;
    setBusy(true);
    setError("");
    try {
      await refresh();
      const latest = await boundedDatabaseRequest((signal) =>
        getResource(resource.id, { signal }),
      );
      if (latest.id !== resource.id || latest.project_id !== scope.project)
        throw new Error("Review scope mismatch");
      if (active.current) {
        reviewResource.current = latest;
        setReviewed(true);
      }
    } catch (cause) {
      if (!active.current) return;
      if (databaseDenied(cause)) {
        onDenied();
        return;
      }
      setError(connectionError(cause));
    } finally {
      if (active.current) setBusy(false);
    }
  }
  const editingLocked = accessPaused || busy || Boolean(attempt);
  const canConnect = connectionCanStart(connection, options, selected);
  return {
    resource,
    owner,
    legacyWritable,
    accessPaused,
    scope,
    onDenied,
    id,
    active,
    status,
    options,
    database,
    setDatabase,
    port,
    setPort,
    readiness,
    setReadiness,
    timeout,
    setTimeoutValue,
    confirmed,
    setConfirmed,
    busy,
    error,
    setError,
    notice,
    conflict,
    setConflict,
    attempt,
    setAttempt,
    reviewed,
    setReviewed,
    reviewResource,
    setReviewBase,
    connection,
    continueRoutes,
    setContinueRoutes,
    setApprovedRouteIntent,
    setAcknowledgedStorage,
    approvedStorage,
    setApprovedStorage,
    storageReview,
    storageData,
    storageIdentity,
    hasStorage,
    storageAcknowledged,
    storageReady,
    setOperationID,
    routeChoices,
    routeIntent,
    storageRoutesReady,
    routeIntentIdentity,
    approvalValid,
    routeApprovalChanged,
    recoveryPoll,
    progress,
    selected,
    paused,
    denied,
    settleAuthority,
    refresh,
    requestAction,
    review,
    editingLocked,
    canConnect,
  };
}

type ConnectionRecordModel = Pick<
  ReturnType<typeof useConnectionSession>,
  | "connection"
  | "progress"
  | "recoveryPoll"
  | "owner"
  | "scope"
  | "accessPaused"
  | "busy"
  | "settleAuthority"
  | "onDenied"
  | "setOperationID"
  | "refresh"
  | "status"
  | "options"
  | "id"
  | "database"
  | "editingLocked"
  | "setDatabase"
  | "setConfirmed"
>;
function ConnectionRecord(viewModel: ConnectionRecordModel) {
  const { connection, progress, recoveryPoll, status } = viewModel;

  if (!connection)
    return status.data === null ? (
      <AvailableDatabaseConnections {...viewModel} />
    ) : null;
  return (
    <>
      <ConnectedDeploymentProgress {...viewModel} />
      {progress.data?.storageState &&
      ["queued", "running"].includes(
        progress.data.storageState.recovery_state,
      ) &&
      recoveryPoll.current &&
      Date.now() >= recoveryPoll.current.deadline ? (
        <p role="status">
          Automatic recovery checks paused after two minutes. Recovery is still
          unconfirmed. Refresh the database connection for another bounded
          status check.
        </p>
      ) : null}
      <ConnectionStorageRecovery {...viewModel} />
      <p role="status">
        {states[connection.state] ??
          "Unrecognized connection status. Actions are paused."}
      </p>
      <ConnectionMetadata {...viewModel} />
      {connection.state === "active" ? (
        <p className="text-sm">
          The connection workflow completed. This recorded result is not
          continuous health monitoring.
        </p>
      ) : null}
      {connection.hold_operation_id || connection.state === "held" ? (
        <div role="status" className="space-y-2 break-all">
          <p>
            Changes are paused for operation{" "}
            {connection.hold_operation_id || "awaiting recorded identity"}. The
            hold remains in place.
          </p>
          <p>
            Recovery is available only when the server provides the original
            operation and explicit eligible actions. A refresh does not unlock
            the connection or replay a deployment.
          </p>
        </div>
      ) : null}
    </>
  );
}

type ConnectedDeploymentProgressModel = Pick<
  ReturnType<typeof useConnectionSession>,
  "progress"
>;
function ConnectedDeploymentProgress(
  viewModel: ConnectedDeploymentProgressModel,
) {
  const { progress } = viewModel;

  if (!progress.data)
    return progress.isError ? (
      <p role="status">
        Application deployment progress is unavailable. Refresh before relying
        on recorded state.
      </p>
    ) : null;
  return (
    <section
      className="min-w-0 space-y-1 break-all text-sm"
      aria-label="Connected application deployment progress"
    >
      <p>
        Application deployment {progress.data.id}: {progress.data.state}
      </p>
      <p>
        Current progress read:{" "}
        {progress.isError
          ? "unavailable, recorded evidence only"
          : progress.isFetching
            ? "refreshing"
            : "recorded"}
      </p>
      {progress.data.storage ? (
        <p>
          Owned upload storage {progress.data.storage.storage_id}, generation{" "}
          {progress.data.storage.generation}. Replacement interrupts service and
          retains the volume. Recorded storage phase:{" "}
          {progress.data.storagePhase ?? "not recorded"}. Storage phase alone
          does not authorize recovery.
        </p>
      ) : null}
      {progress.data.yielded && progress.data.state !== "succeeded" ? (
        <p>
          Candidate readiness was recorded. Route switching and finalization are
          incomplete. The candidate is not a completed deployment.
        </p>
      ) : null}
      {progress.data.proxy_operation_id ? (
        <p>
          Route continuation requested through proxy operation{" "}
          {progress.data.proxy_operation_id}. Review the existing route controls
          for loaded HTTP and HTTPS observations.
        </p>
      ) : null}
      {progress.data.mutation_hold || progress.data.restore_ownership_held ? (
        <p>
          Ownership remains held. Refreshing does not release a hold or
          authorize recovery.
        </p>
      ) : null}
      <p>
        This operation records deployment progress. It does not verify current
        public HTTPS health or database contents.
      </p>
    </section>
  );
}

type ConnectionMetadataModel = Pick<
  ReturnType<typeof useConnectionSession>,
  "connection"
>;
function ConnectionMetadata(viewModel: ConnectionMetadataModel) {
  const { connection } = viewModel;

  if (!connection) return null;
  return (
    <dl className="space-y-1 break-all text-sm">
      <dt>Application</dt>
      <dd>{connection.application_resource_id}</dd>
      <dt>Database</dt>
      <dd>{connection.database_resource_id}</dd>
      <dt>Membership revision</dt>
      <dd>{connection.membership_revision}</dd>
      <dt>Recorded reason</dt>
      <dd>{connection.reason_code || "None recorded"}</dd>
      <dt>Network observation</dt>
      <dd>{connection.capture_id || "Not recorded"}</dd>
      <dt>Database update operation</dt>
      <dd>{connection.upgrade_operation_id || "Not recorded"}</dd>
      <dt>Application operation</dt>
      <dd>{connection.application_operation_id || "Not recorded"}</dd>
      <dt>Recorded update</dt>
      <dd>{connection.updated_at}</dd>
    </dl>
  );
}

type DatabaseConnectionChoicesModel = Pick<
  ReturnType<typeof useConnectionSession>,
  | "owner"
  | "id"
  | "database"
  | "editingLocked"
  | "options"
  | "setDatabase"
  | "setConfirmed"
>;
function DatabaseConnectionChoices(viewModel: DatabaseConnectionChoicesModel) {
  const {
    owner,
    id,
    database,
    editingLocked,
    options,
    setDatabase,
    setConfirmed,
  } = viewModel;

  if (!owner) return null;
  return (
    <div className="space-y-1">
      <label htmlFor={`${id}-database`}>Database to connect</label>
      <select
        id={`${id}-database`}
        className="h-10 w-full min-w-0 rounded-xl border bg-card px-2 text-sm"
        value={database}
        disabled={editingLocked || options.isError || options.isPending}
        onChange={(event) => {
          setDatabase(event.target.value);
          setConfirmed(false);
        }}
      >
        <option value="">Select an eligible database</option>
        {options.data?.items.map((item) => (
          <option
            key={item.resource_id}
            value={item.resource_id}
            disabled={
              item.status !== "available" || item.reason_code !== "eligible"
            }
          >
            {item.name} · {item.engine} ·{" "}
            {item.status === "available" && item.reason_code === "eligible"
              ? "Available"
              : "Unavailable"}
          </option>
        ))}
      </select>
    </div>
  );
}

type ConnectionRequestFormModel = Pick<
  ReturnType<typeof useConnectionSession>,
  | "owner"
  | "status"
  | "requestAction"
  | "attempt"
  | "connection"
  | "id"
  | "port"
  | "editingLocked"
  | "setPort"
  | "setConfirmed"
  | "readiness"
  | "setReadiness"
  | "timeout"
  | "setTimeoutValue"
  | "routeChoices"
  | "routeIntent"
  | "continueRoutes"
  | "setContinueRoutes"
  | "storageReview"
  | "hasStorage"
  | "storageData"
  | "storageReady"
  | "storageAcknowledged"
  | "setAcknowledgedStorage"
  | "storageIdentity"
  | "storageRoutesReady"
  | "confirmed"
  | "approvedStorage"
  | "routeApprovalChanged"
  | "approvalValid"
  | "accessPaused"
  | "busy"
  | "setApprovedStorage"
  | "setApprovedRouteIntent"
  | "routeIntentIdentity"
  | "paused"
  | "conflict"
  | "canConnect"
>;
function ConnectionRequestForm(viewModel: ConnectionRequestFormModel) {
  const {
    owner,
    status,
    requestAction,
    attempt,
    connection,
    setConfirmed,
    continueRoutes,
    storageIdentity,
    routeApprovalChanged,
    approvalValid,
    accessPaused,
    busy,
    setApprovedStorage,
    setApprovedRouteIntent,
    routeIntentIdentity,
  } = viewModel;

  if (!(owner && !status.isPending && !status.isError))
    return (
      <p className="text-xs text-muted-foreground">
        Project owner access is required for database connection changes.
      </p>
    );
  return (
    <form
      className="min-w-0 space-y-3 rounded-lg border p-3"
      aria-label="Review database connection request"
      onSubmit={(event) =>
        void requestAction(
          attempt?.kind ?? (connection ? "deploy" : "connect"),
          event,
        )
      }
    >
      <ConnectionPorts {...viewModel} />
      <p className="text-xs text-muted-foreground">
        The readiness path is local, such as /health. The server allocates a
        separate runner-local application port. These actions do not promise
        public traffic or uninterrupted service. Database redeployment can
        interrupt database access.
      </p>
      <ConnectionRouteReview {...viewModel} />
      <ConnectionStorageReview {...viewModel} />
      {routeApprovalChanged ? (
        <p role="status">
          Loaded route choices changed. Review the current routes and confirm
          this request again.
        </p>
      ) : null}
      <label className="flex items-start gap-2 text-sm">
        <input
          type="checkbox"
          checked={approvalValid}
          disabled={accessPaused || busy}
          onChange={(event) => {
            setConfirmed(event.target.checked);
            setApprovedStorage(
              event.target.checked ? storageIdentity : undefined,
            );
            setApprovedRouteIntent(
              event.target.checked && continueRoutes
                ? routeIntentIdentity
                : undefined,
            );
          }}
        />
        <span>
          I reviewed the selected database and application settings and approve
          this request.
        </span>
      </label>
      <ConnectionRequestActions {...viewModel} />
    </form>
  );
}

type ConnectionPortsModel = Pick<
  ReturnType<typeof useConnectionSession>,
  | "id"
  | "port"
  | "editingLocked"
  | "setPort"
  | "setConfirmed"
  | "readiness"
  | "setReadiness"
  | "timeout"
  | "setTimeoutValue"
>;
function ConnectionPorts(viewModel: ConnectionPortsModel) {
  const {
    id,
    port,
    editingLocked,
    setPort,
    setConfirmed,
    readiness,
    setReadiness,
    timeout,
    setTimeoutValue,
  } = viewModel;

  return (
    <div className="grid min-w-0 gap-3 sm:grid-cols-2">
      <label htmlFor={`${id}-port`}>
        Application container port
        <Input
          id={`${id}-port`}
          type="number"
          min={1}
          max={65535}
          value={port}
          disabled={editingLocked}
          required
          onChange={(event) => {
            setPort(event.target.value);
            setConfirmed(false);
          }}
        />
      </label>
      <label htmlFor={`${id}-path`}>
        Application readiness path
        <Input
          id={`${id}-path`}
          maxLength={1024}
          value={readiness}
          required
          disabled={editingLocked}
          onChange={(event) => {
            setReadiness(event.target.value);
            setConfirmed(false);
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
          disabled={editingLocked}
          required
          onChange={(event) => {
            setTimeoutValue(event.target.value);
            setConfirmed(false);
          }}
        />
      </label>
    </div>
  );
}

type ConnectionRouteReviewModel = Pick<
  ReturnType<typeof useConnectionSession>,
  | "connection"
  | "routeChoices"
  | "routeIntent"
  | "continueRoutes"
  | "editingLocked"
  | "setContinueRoutes"
  | "setConfirmed"
>;
function ConnectionRouteReview(viewModel: ConnectionRouteReviewModel) {
  const {
    connection,
    routeChoices,
    routeIntent,
    continueRoutes,
    editingLocked,
    setContinueRoutes,
    setConfirmed,
  } = viewModel;

  if (!connection) return null;
  return (
    <section
      aria-label="Connected application route continuation"
      className="min-w-0 space-y-2 break-all text-sm"
    >
      <p>Existing public routes</p>
      {routeChoices.isError ? (
        <p role="status">
          Loaded route choices are unavailable. Route continuation is paused.
        </p>
      ) : routeChoices.isPending ? (
        <p role="status">Checking loaded route choices...</p>
      ) : routeIntent ? (
        <>
          <p>
            Loaded generation {routeIntent.expected_loaded_generation}. Continue
            all {routeIntent.routes.length} enabled application routes from the
            reviewed release.
          </p>
          <ul>
            {routeChoices.data?.routes.map((route) => (
              <li key={route.id}>
                {route.hostname}
                {route.path_prefix}
              </li>
            ))}
          </ul>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={continueRoutes}
              disabled={editingLocked}
              onChange={(event) => {
                setContinueRoutes(event.target.checked);
                setConfirmed(false);
              }}
            />
            <span>
              Continue all reviewed public routes when redeploying the connected
              application.
            </span>
          </label>
          <p>
            The server prepares the candidate, switches the reviewed routes,
            then finalizes the deployment. A ready candidate alone does not
            confirm loaded routes or HTTPS health.
          </p>
        </>
      ) : (
        <p>
          No verified route continuation is available. Existing route controls
          show prerequisites and recorded HTTP/HTTPS observations.
        </p>
      )}
      {continueRoutes && !routeIntent ? (
        <Button
          type="button"
          variant="outline"
          className="h-auto min-h-9 max-w-full min-w-0 whitespace-normal break-words"
          disabled={editingLocked}
          onClick={() => {
            setContinueRoutes(false);
            setConfirmed(false);
          }}
        >
          Remove route continuation from draft
        </Button>
      ) : null}
    </section>
  );
}

type ConnectionStorageReviewModel = Pick<
  ReturnType<typeof useConnectionSession>,
  | "storageReview"
  | "hasStorage"
  | "storageData"
  | "storageReady"
  | "storageAcknowledged"
  | "editingLocked"
  | "setAcknowledgedStorage"
  | "storageIdentity"
  | "setConfirmed"
  | "storageRoutesReady"
  | "confirmed"
  | "attempt"
  | "approvedStorage"
>;
function ConnectionStorageReview(viewModel: ConnectionStorageReviewModel) {
  const {
    storageIdentity,
    storageRoutesReady,
    confirmed,
    attempt,
    approvedStorage,
  } = viewModel;

  return (
    <section
      aria-label="Connected application storage review"
      className="min-w-0 space-y-2 break-words text-sm"
    >
      <SavedConnectionStorage {...viewModel} />
      {!storageRoutesReady ? (
        <p role="status">
          Review and select all current public routes before replacing this
          storage-backed application. Refresh if route evidence differs.
        </p>
      ) : null}
      {confirmed &&
      !attempt &&
      storageIdentity &&
      approvedStorage !== storageIdentity ? (
        <p role="status">
          Saved storage changed. Review the mount and acknowledge interruption
          again before confirming a new request.
        </p>
      ) : null}
    </section>
  );
}

type ConnectionRequestActionsModel = Pick<
  ReturnType<typeof useConnectionSession>,
  | "paused"
  | "conflict"
  | "approvalValid"
  | "attempt"
  | "storageReady"
  | "hasStorage"
  | "storageAcknowledged"
  | "storageRoutesReady"
  | "continueRoutes"
  | "routeIntent"
  | "connection"
  | "canConnect"
  | "requestAction"
>;
function ConnectionRequestActions(viewModel: ConnectionRequestActionsModel) {
  return (
    <div className="flex flex-wrap gap-2">
      <ConnectionApplicationSubmission {...viewModel} />
      <ConnectionDatabaseAction {...viewModel} />
    </div>
  );
}

type ConnectionConflictReviewModel = Pick<
  ReturnType<typeof useConnectionSession>,
  | "conflict"
  | "busy"
  | "accessPaused"
  | "review"
  | "reviewed"
  | "status"
  | "setContinueRoutes"
  | "setReviewBase"
  | "reviewResource"
  | "setConflict"
  | "setAttempt"
  | "setConfirmed"
  | "setReviewed"
  | "setError"
>;
function ConnectionConflictReview(viewModel: ConnectionConflictReviewModel) {
  const {
    conflict,
    busy,
    accessPaused,
    review,
    reviewed,
    status,
    setContinueRoutes,
    setReviewBase,
    reviewResource,
    setConflict,
    setAttempt,
    setConfirmed,
    setReviewed,
    setError,
  } = viewModel;

  if (!conflict) return null;
  return (
    <section className="space-y-2" aria-label="Review changed connection">
      <p role="status">
        Nothing was resubmitted. Refresh the recorded connection, then
        explicitly approve a separate request.
      </p>
      <Button
        type="button"
        variant="outline"
        className="h-auto min-h-9 max-w-full min-w-0 whitespace-normal break-words"
        disabled={busy || accessPaused}
        onClick={() => void review()}
      >
        Refresh for conflict review
      </Button>
      <Button
        type="button"
        variant="outline"
        className="h-auto min-h-9 max-w-full min-w-0 whitespace-normal break-words"
        disabled={!reviewed || busy || accessPaused || status.isError}
        onClick={() => {
          setContinueRoutes(false);
          setReviewBase(reviewResource.current);
          setConflict(false);
          setAttempt(undefined);
          setConfirmed(false);
          setReviewed(false);
          setError("");
        }}
      >
        Use refreshed configuration for a new request
      </Button>
    </section>
  );
}

type ConnectionDatabaseActionModel = Pick<
  ReturnType<typeof useConnectionSession>,
  | "connection"
  | "paused"
  | "conflict"
  | "approvalValid"
  | "attempt"
  | "requestAction"
>;
function ConnectionDatabaseAction(viewModel: ConnectionDatabaseActionModel) {
  const {
    connection,
    paused,
    conflict,
    approvalValid,
    attempt,
    requestAction,
  } = viewModel;

  if (!connection) return null;
  return (
    <Button
      type="button"
      variant="outline"
      className="h-auto min-h-9 max-w-full min-w-0 whitespace-normal break-words"
      disabled={
        Boolean(paused) ||
        conflict ||
        !approvalValid ||
        (attempt
          ? attempt.kind !== "maintain"
          : connection.state !== "active" ||
            connection.membership_revision < 1 ||
            Boolean(connection.hold_operation_id))
      }
      onClick={() => void requestAction("maintain")}
    >
      {attempt?.kind === "maintain"
        ? "Retry identical database request"
        : "Redeploy connected database"}
    </Button>
  );
}

type SavedConnectionStorageModel = Pick<
  ReturnType<typeof useConnectionSession>,
  | "storageReview"
  | "hasStorage"
  | "storageData"
  | "storageReady"
  | "storageAcknowledged"
  | "editingLocked"
  | "setAcknowledgedStorage"
  | "storageIdentity"
  | "setConfirmed"
>;
function SavedConnectionStorage(viewModel: SavedConnectionStorageModel) {
  const {
    storageReview,
    hasStorage,
    storageData,
    storageReady,
    storageAcknowledged,
    editingLocked,
    setAcknowledgedStorage,
    storageIdentity,
    setConfirmed,
  } = viewModel;

  if (storageReview.isError)
    return (
      <p role="alert">
        Unable to verify saved application storage. New application requests are
        paused. Refresh the database connection to review storage again.
      </p>
    );
  if (storageReview.isPending || storageReview.isFetching)
    return <p role="status">Checking saved application storage...</p>;
  if (!hasStorage)
    return (
      <p>
        No saved application volume. To retain uploads, connect the database,
        save one mount through Persistent storage, then refresh this review.
      </p>
    );
  return (
    <>
      <p>
        Saved upload mount: {storageData?.storage?.mount_path}. Volume:{" "}
        {storageData?.storage?.volume_name}. Retention: retain.
      </p>
      <p>
        Replacement stops the current application before starting its
        replacement so one release writes to this volume. Service is
        interrupted. Existing volume data is retained; image rollback does not
        undo file writes. The database remains separately managed.
      </p>
      {!storageReady ? (
        <p role="status">
          Storage deployment is unavailable. Recorded reason:{" "}
          {storageData?.options.application_database_storage
            ? storageData.options.reason
            : "connected storage evidence is missing"}
          . Connect the database first, then attach storage through Persistent
          storage and refresh. Missing or uncertain prerequisites require server
          review.
        </p>
      ) : (
        <label className="flex items-start gap-2">
          <input
            type="checkbox"
            checked={storageAcknowledged}
            disabled={editingLocked}
            onChange={(event) => {
              setAcknowledgedStorage(
                event.target.checked ? storageIdentity : undefined,
              );
              setConfirmed(false);
            }}
          />
          <span>
            I acknowledge application interruption while replacing the release
            using this saved upload volume.
          </span>
        </label>
      )}
    </>
  );
}

function connectionStorageApproval({
  storageData,
  connection,
  acknowledgedStorage,
  continueRoutes,
  routeIntent,
  confirmed,
  retained,
  approvedRouteIntent,
  approvedStorage,
}: {
  storageData:
    | Awaited<ReturnType<typeof getConnectionStorageReview>>
    | undefined;
  connection: DatabaseConnection | null | undefined;
  acknowledgedStorage: string | undefined;
  continueRoutes: boolean;
  routeIntent: ReturnType<typeof connectionRouteIntent> | undefined;
  confirmed: boolean;
  retained: boolean;
  approvedRouteIntent: string | undefined;
  approvedStorage: string | undefined;
}) {
  const storageIdentity = storageData
    ? JSON.stringify({
        options: storageData.options,
        mount: storageData.storage?.mount_path,
      })
    : undefined;
  const hasStorage = Boolean(storageData?.options.storage_ref);
  const storageAcknowledged = Boolean(
    storageIdentity && acknowledgedStorage === storageIdentity,
  );
  const storageReady = Boolean(
    storageData &&
      (hasStorage
        ? connection &&
          storageData.options.available &&
          storageData.options.application_database_storage &&
          storageData.storage
        : storageData.options.reason === "storage_unconfigured"),
  );
  const storageRoutesReady =
    !hasStorage ||
    !storageData?.options.routes.length ||
    Boolean(
      continueRoutes &&
        routeIntent &&
        storageData.options.routes.length === routeIntent.routes.length &&
        storageData.options.routes.every((route) =>
          routeIntent.routes.some(
            (selection) =>
              selection.route_id === route.id &&
              selection.expected_version === route.version &&
              selection.expected_selected_release_operation_id ===
                route.selected_release_operation_id,
          ),
        ),
    );
  const routeIntentIdentity = routeIntent
    ? JSON.stringify(routeIntent)
    : undefined;
  const approvalValid = Boolean(
    confirmed &&
      (retained ||
        ((!continueRoutes ||
          (routeIntentIdentity &&
            approvedRouteIntent === routeIntentIdentity)) &&
          storageIdentity &&
          approvedStorage === storageIdentity)),
  );
  const routeApprovalChanged = Boolean(
    !retained &&
      confirmed &&
      continueRoutes &&
      routeIntentIdentity &&
      approvedRouteIntent !== routeIntentIdentity,
  );
  return {
    storageIdentity,
    hasStorage,
    storageAcknowledged,
    storageReady,
    storageRoutesReady,
    routeIntentIdentity,
    approvalValid,
    routeApprovalChanged,
  };
}

function connectionDeploymentPending(
  progress: ConnectedProgress | undefined,
  progressUnavailable: boolean,
  operationID: string,
) {
  const recoveryUnresolved = Boolean(
    progress?.storageState &&
      ["queued", "running", "uncertain"].includes(
        progress.storageState.recovery_state,
      ),
  );
  const deploymentPending =
    recoveryUnresolved ||
    Boolean(
      operationID &&
        (!progress ||
          progressUnavailable ||
          !["succeeded", "failed", "canceled"].includes(progress.state) ||
          progress.mutation_hold ||
          progress.restore_ownership_held),
    );
  return deploymentPending;
}

type UnconnectedApplicationDeploymentsModel = Pick<
  ReturnType<typeof useConnectionSession>,
  | "status"
  | "attempt"
  | "busy"
  | "conflict"
  | "resource"
  | "legacyWritable"
  | "accessPaused"
>;
function UnconnectedApplicationDeployments(
  viewModel: UnconnectedApplicationDeploymentsModel,
) {
  const {
    status,
    attempt,
    busy,
    conflict,
    resource,
    legacyWritable,
    accessPaused,
  } = viewModel;

  if (
    !(status.data === null && !status.isError && !attempt && !busy && !conflict)
  )
    return null;
  return (
    <ResourceDeployments
      resource={resource}
      writable={legacyWritable}
      blocked={accessPaused}
    />
  );
}

type ConnectionStorageRecoveryModel = Pick<
  ReturnType<typeof useConnectionSession>,
  | "owner"
  | "progress"
  | "scope"
  | "accessPaused"
  | "busy"
  | "settleAuthority"
  | "onDenied"
  | "setOperationID"
  | "refresh"
>;
function ConnectionStorageRecovery(viewModel: ConnectionStorageRecoveryModel) {
  const {
    owner,
    progress,
    scope,
    accessPaused,
    busy,
    settleAuthority,
    onDenied,
    setOperationID,
    refresh,
  } = viewModel;

  if (!(owner && progress.data)) return null;
  return (
    <ConnectedStorageRecovery
      key={progress.data.id}
      scope={scope}
      progress={progress.data}
      paused={accessPaused || busy || progress.isFetching}
      readUnavailable={progress.isError}
      authorize={settleAuthority}
      onDenied={onDenied}
      onSubmitting={setOperationID}
      onUpdated={refresh}
    />
  );
}

function connectionCanStart(
  connection: DatabaseConnection | null | undefined,
  options: { isError: boolean; isPending: boolean },
  selected: { status: string; reason_code: string } | undefined,
) {
  return (
    !connection &&
    !options.isError &&
    !options.isPending &&
    selected?.status === "available" &&
    selected.reason_code === "eligible"
  );
}

function useConnectionProgress(
  scope: DatabaseConnectionScope,
  operationID: string,
  connection: DatabaseConnection | null | undefined,
) {
  const client = useQueryClient();
  const recoveryPoll = useRef<{ key: string; deadline: number } | undefined>(
    undefined,
  );
  const progressID =
    operationID ||
    connection?.hold_operation_id ||
    connection?.application_operation_id ||
    "";
  const progress = useQuery({
    ...connectedProgressQuery(scope, progressID),
    enabled: Boolean(progressID),
    refetchInterval: (query) => {
      if (query.state.error) return false;
      const value = query.state.data,
        recovery = value?.storageState;
      if (recovery && ["queued", "running"].includes(recovery.recovery_state)) {
        const key = `${value!.id}:${recovery.recovery_action}`;
        if (recoveryPoll.current?.key !== key)
          recoveryPoll.current = { key, deadline: Date.now() + 120_000 };
        return Date.now() < recoveryPoll.current.deadline ? 5000 : false;
      }
      recoveryPoll.current = undefined;
      return value &&
        ["succeeded", "failed", "canceled", "reconciliation_required"].includes(
          value.state,
        )
        ? false
        : 5000;
    },
  });
  useEffect(() => {
    const currentScope = {
      actor: scope.actor,
      project: scope.project,
      resource: scope.resource,
    };
    const recovered =
      progress.data?.storageState?.recovery_state === "succeeded";
    if (progress.data?.state !== "succeeded" && !recovered) return;
    if (recovered) {
      void client.invalidateQueries({
        queryKey: connectionKey(currentScope),
        exact: true,
      });
      void client.invalidateQueries({
        queryKey: connectionStorageReviewQuery(currentScope).queryKey,
        exact: true,
      });
    }
    void client.invalidateQueries({
      queryKey: ["hostingRoutes", scope.actor, scope.project, scope.resource],
    });
    void client.invalidateQueries({
      queryKey: [
        "routeSwitchOptions",
        scope.actor,
        scope.project,
        scope.resource,
      ],
    });
  }, [
    progress.data?.id,
    progress.data?.state,
    progress.data?.storageState?.recovery_state,
    client,
    scope.actor,
    scope.project,
    scope.resource,
  ]);
  return { recoveryPoll, progressID, progress };
}

function useConnectionPrerequisites(
  scope: DatabaseConnectionScope,
  resource: Resource,
  owner: boolean,
  connection: DatabaseConnection | null | undefined,
) {
  const storageReview = useQuery({
    ...connectionStorageReviewQuery(scope),
    enabled: owner && resource.source.kind === "image",
  });
  const storageData =
    !storageReview.isError && !storageReview.isFetching
      ? storageReview.data
      : undefined;
  const routeChoices = useQuery({
    ...switchOptionsQuery(scope),
    enabled: Boolean(connection && owner),
  });
  const routeIntent =
    routeChoices.data && !routeChoices.isError && !routeChoices.isFetching
      ? connectionRouteIntent(routeChoices.data)
      : undefined;
  return { storageReview, storageData, routeChoices, routeIntent };
}

type AvailableDatabaseConnectionsModel = Pick<
  ReturnType<typeof useConnectionSession>,
  | "options"
  | "owner"
  | "id"
  | "database"
  | "editingLocked"
  | "setDatabase"
  | "setConfirmed"
>;
function AvailableDatabaseConnections(
  viewModel: AvailableDatabaseConnectionsModel,
) {
  const { options } = viewModel;

  return (
    <>
      <p>
        Connect one undeployed public-image application to a healthy PostgreSQL
        or Redis database on the same destination. The server checks eligibility
        and adds the named PG* or REDIS* credential references. Credential
        values are never displayed.
      </p>
      <p className="text-xs text-muted-foreground">
        One application per database. Source builds and an already running
        unbound application are unsupported. Connect first, then save one owned
        upload volume through Persistent storage before a storage redeployment.
        Add the first public route through the existing route controls after
        connecting. The controller observes the database network, updates it and
        deploys the application.
      </p>
      {options.isPending ? (
        <p role="status">Loading eligible databases...</p>
      ) : null}
      {options.isError ? (
        <p role="alert">
          Eligible databases are unavailable. {connectionError(options.error)}
        </p>
      ) : null}
      {options.data?.items.length === 0 ? (
        <p>
          No eligible databases were returned. Check database health, placement
          and application prerequisites.
        </p>
      ) : null}
      <DatabaseConnectionChoices {...viewModel} />
    </>
  );
}

type ConnectionApplicationSubmissionModel = Pick<
  ReturnType<typeof useConnectionSession>,
  | "paused"
  | "conflict"
  | "approvalValid"
  | "attempt"
  | "storageReady"
  | "hasStorage"
  | "storageAcknowledged"
  | "storageRoutesReady"
  | "continueRoutes"
  | "routeIntent"
  | "connection"
  | "canConnect"
>;
function ConnectionApplicationSubmission(
  viewModel: ConnectionApplicationSubmissionModel,
) {
  const {
    paused,
    conflict,
    approvalValid,
    attempt,
    storageReady,
    hasStorage,
    storageAcknowledged,
    storageRoutesReady,
    continueRoutes,
    routeIntent,
    connection,
    canConnect,
  } = viewModel;

  if (attempt)
    return <RetainedConnectionSubmission {...viewModel} attempt={attempt} />;
  return (
    <Button
      type="submit"
      className="h-auto min-h-9 max-w-full min-w-0 whitespace-normal break-words"
      disabled={
        Boolean(paused) ||
        conflict ||
        !approvalValid ||
        !storageReady ||
        (hasStorage && !storageAcknowledged) ||
        !storageRoutesReady ||
        (continueRoutes && !routeIntent) ||
        (connection
          ? connection.state !== "active" ||
            Boolean(connection.hold_operation_id)
          : !canConnect)
      }
    >
      {connection
        ? "Redeploy connected application"
        : "Connect database and deploy application"}
    </Button>
  );
}

type RetainedConnectionSubmissionModel = Pick<
  ReturnType<typeof useConnectionSession>,
  "paused" | "conflict" | "approvalValid" | "connection"
> & {
  attempt: NonNullable<ReturnType<typeof useConnectionSession>["attempt"]>;
};
function RetainedConnectionSubmission(
  viewModel: RetainedConnectionSubmissionModel,
) {
  const { paused, conflict, approvalValid, attempt, connection } = viewModel;

  return (
    <Button
      type="submit"
      className="h-auto min-h-9 max-w-full min-w-0 whitespace-normal break-words"
      disabled={
        Boolean(paused) ||
        conflict ||
        !approvalValid ||
        attempt.kind === "maintain"
      }
    >
      {attempt.kind === "connect"
        ? "Retry identical connection request"
        : attempt.kind === "deploy"
          ? "Retry identical application request"
          : connection
            ? "Redeploy connected application"
            : "Connect database and deploy application"}
    </Button>
  );
}
