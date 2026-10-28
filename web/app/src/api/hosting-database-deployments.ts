import { queryOptions } from "@tanstack/react-query";
import { request } from "./client";
import { ApiError } from "./errors";
import type { components } from "./generated";
import {
  boundedDatabaseRequest,
  type DatabaseScope,
  type HostingDatabase,
} from "./hosting-databases";

export type DatabaseDeploymentInput =
  components["schemas"]["HostingDatabaseDeploymentInput"];
type Operation = components["schemas"]["HostingOperation"];
type Snapshot = Operation["snapshot"];
type Release = NonNullable<Operation["release"]>;
// Cache only the nonsecret evidence needed by the private database view.
export type DatabaseOperation = Pick<
  Operation,
  | "id"
  | "state"
  | "created_at"
  | "updated_at"
  | "mutation_hold"
  | "restore_ownership_held"
  | "snapshot"
  | "release"
  | "result"
> & { plan_hash?: string; evidence_incomplete?: boolean };
export type DatabaseDeploymentPage = {
  items: DatabaseOperation[];
  count: number;
  limit: number;
  offset: number;
  total: number;
};
const invalid = () => {
  throw new Error(
    "Database operation evidence is invalid or outside this resource",
  );
};
function fields(
  value: unknown,
  required: string[],
  optional: string[] = [],
): asserts value is Record<string, unknown> {
  const allowed = new Set([...required, ...optional]);
  if (
    !value ||
    typeof value !== "object" ||
    Array.isArray(value) ||
    required.some((key) => !Object.hasOwn(value, key)) ||
    Object.keys(value).some((key) => !allowed.has(key))
  )
    invalid();
}
const id = (value: unknown) =>
  typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const hash = (value: unknown) =>
  typeof value === "string" && /^sha256:[a-f0-9]{64}$/.test(value);
const safeText = (value: unknown, max = 1024) =>
  typeof value === "string" &&
  value.length > 0 &&
  value.length <= max &&
  !/[\x00-\x1f\x7f]/.test(value);
const positive = (value: unknown): value is number =>
  Number.isSafeInteger(value) && Number(value) > 0;
function storageRef(value: unknown, database: HostingDatabase) {
  fields(value, ["storage_id", "generation", "hash"]);
  const expected = database.storage_ref;
  if (
    value.storage_id !== expected.storage_id ||
    value.generation !== expected.generation ||
    value.hash !== expected.hash
  )
    invalid();
}
function databaseRef(
  value: unknown,
  database: HostingDatabase,
): asserts value is Record<string, unknown> {
  fields(value, ["engine", "recipe_version", "configuration_hash"]);
  if (
    value.engine !== database.engine ||
    value.recipe_version !== database.recipe_version ||
    !hash(value.configuration_hash)
  )
    invalid();
}
function validateSnapshot(
  value: unknown,
  database: HostingDatabase,
  generation?: number,
): Snapshot {
  fields(value, [
    "resource_id",
    "resource_generation",
    "image_reference",
    "binding_id",
    "destination_id",
    "server_id",
    "runner_id",
    "runtime_namespace",
    "container_port",
    "host_port",
    "readiness",
    "policy_version",
    "database",
    "storage",
  ]);
  const { resource, binding } = database;
  if (
    value.resource_id !== resource.id ||
    value.binding_id !== binding.id ||
    value.destination_id !== binding.destination_id ||
    value.runtime_namespace !== binding.runtime_namespace ||
    !id(value.server_id) ||
    !id(value.runner_id) ||
    !positive(value.resource_generation) ||
    value.resource_generation > resource.generation ||
    (generation !== undefined && value.resource_generation !== generation) ||
    value.image_reference !==
      (resource.source.kind === "image"
        ? resource.source.image.reference
        : "") ||
    value.policy_version !== "database.private.owned-storage.v1" ||
    value.host_port !== 0 ||
    value.container_port !== (database.engine === "postgresql" ? 5432 : database.engine === "mysql" ? 3306 : 6379)
  )
    invalid();
  fields(value.readiness, ["path", "timeout_seconds"]);
  if (value.readiness.path !== "" || value.readiness.timeout_seconds !== 0)
    invalid();
  databaseRef(value.database, database);
  storageRef(value.storage, database);
  return value as Snapshot;
}
function validateRelease(
  value: unknown,
  engine: HostingDatabase["engine"],
): Release {
  fields(
    value,
    ["canonical_image", "image_id", "os", "architecture", "daemon_id", "hash"],
    ["variant"],
  );
  if (
    !safeText(value.canonical_image) ||
    !/^docker\.io\/library\/(postgres|mysql|redis)@sha256:[a-f0-9]{64}$/.test(
      String(value.canonical_image),
    ) ||
    !String(value.canonical_image).startsWith(
      `docker.io/library/${engine === "postgresql" ? "postgres" : engine === "mysql" ? "mysql" : "redis"}@`,
    ) ||
    !hash(value.image_id) ||
    !hash(value.hash) ||
    value.os !== "linux" ||
    !["amd64", "arm64"].includes(String(value.architecture)) ||
    !safeText(value.daemon_id, 255) ||
    (value.variant !== undefined && !safeText(value.variant, 32))
  )
    invalid();
  return value as Release;
}
function validateStoragePlan(value: unknown, database: HostingDatabase) {
  fields(value, [
    "reference",
    "volume_name",
    "mount_path",
    "creation_operation_id",
    "creation_nonce",
    "daemon_id",
  ]);
  storageRef(value.reference, database);
  if (
    !id(value.volume_name) ||
    !id(value.creation_operation_id) ||
    !safeText(value.creation_nonce, 128) ||
    !safeText(value.daemon_id, 255) ||
    value.mount_path !==
      (database.engine === "postgresql" ? "/var/lib/postgresql/data" : database.engine === "mysql" ? "/var/lib/mysql" : "/data")
  )
    invalid();
}
function sameSnapshot(left: Snapshot, right: Snapshot) {
  return (
    [
      "resource_id",
      "resource_generation",
      "image_reference",
      "binding_id",
      "destination_id",
      "server_id",
      "runner_id",
      "runtime_namespace",
      "container_port",
      "host_port",
      "policy_version",
    ].every(
      (key) => left[key as keyof Snapshot] === right[key as keyof Snapshot],
    ) &&
    left.database?.configuration_hash === right.database?.configuration_hash
  );
}
function validateHealthy(
  value: unknown,
  database: HostingDatabase,
  parent: Snapshot,
) {
  fields(value, [
    "snapshot",
    "operation_id",
    "container_id",
    "release",
    "plan_hash",
    "storage",
  ]);
  const snapshot = validateSnapshot(value.snapshot, database);
  if (
    !id(value.operation_id) ||
    !id(value.container_id) ||
    !hash(value.plan_hash) ||
    snapshot.resource_generation > parent.resource_generation ||
    snapshot.server_id !== parent.server_id ||
    snapshot.runner_id !== parent.runner_id ||
    snapshot.database?.configuration_hash !==
      parent.database?.configuration_hash
  )
    invalid();
  validateRelease(value.release, database.engine);
  fields(value.storage, ["plan", "creation_plan_hash"]);
  validateStoragePlan(value.storage.plan, database);
  if (!hash(value.storage.creation_plan_hash)) invalid();
}
const states = [
  "queued",
  "dispatched",
  "preparing",
  "prepared",
  "applying",
  "verifying",
  "succeeded",
  "failed",
  "canceled",
  "cancel_requested",
  "reconciliation_required",
];
export const databaseOperationHeld = (operation: DatabaseOperation) =>
  operation.evidence_incomplete === true ||
  operation.mutation_hold ||
  operation.restore_ownership_held ||
  !["succeeded", "failed", "canceled"].includes(operation.state) ||
  operation.result?.uncertain === true;
export function validateDatabaseOperation(
  raw: unknown,
  database: HostingDatabase,
  scope: DatabaseScope,
  expected?: { id?: string; generation?: number },
): DatabaseOperation {
  fields(
    raw,
    [
      "id",
      "project_id",
      "resource_id",
      "binding_id",
      "created_by",
      "state",
      "snapshot",
      "restore_ownership_held",
      "mutation_hold",
      "reconcile_requested",
      "created_at",
      "updated_at",
    ],
    [
      "run_id",
      "reconciliation_run_id",
      "release",
      "plan",
      "prepared_receipt",
      "complete_receipt",
      "result",
      "task_mode",
      "observation_lease",
      "reconciliation_healthy",
    ],
  );
  if (
    database.resource.project_id !== scope.project ||
    database.resource.project_environment_id !== scope.environment ||
    raw.project_id !== scope.project ||
    raw.resource_id !== database.resource.id ||
    raw.binding_id !== database.binding.id ||
    !id(raw.id) ||
    (expected?.id && raw.id !== expected.id) ||
    !states.includes(String(raw.state)) ||
    typeof raw.mutation_hold !== "boolean" ||
    typeof raw.restore_ownership_held !== "boolean" ||
    typeof raw.reconcile_requested !== "boolean" ||
    !safeText(raw.created_at, 64) ||
    !Number.isFinite(Date.parse(String(raw.created_at))) ||
    !safeText(raw.updated_at, 64) ||
    !Number.isFinite(Date.parse(String(raw.updated_at))) ||
    (raw.task_mode !== undefined &&
      !["deploy", "observe"].includes(String(raw.task_mode)))
  )
    invalid();
  const snapshot = validateSnapshot(
    raw.snapshot,
    database,
    expected?.generation,
  );
  const release =
    raw.release === undefined
      ? undefined
      : validateRelease(raw.release, database.engine);
  if (
    release &&
    !release.canonical_image.startsWith(
      `docker.io/library/${database.engine === "postgresql" ? "postgres" : database.engine === "mysql" ? "mysql" : "redis"}@`,
    )
  )
    invalid();
  let planHash: string | undefined;
  if (raw.plan !== undefined) {
    fields(
      raw.plan,
      [
        "snapshot",
        "release",
        "compiler_version",
        "hash",
        "storage",
        "database",
      ],
      ["previous_healthy", "retain_previous", "retained_releases"],
    );
    const planSnapshot = validateSnapshot(raw.plan.snapshot, database);
    if (
      !sameSnapshot(snapshot, planSnapshot) ||
      raw.plan.compiler_version !==
        "database-hosting.private.owned-storage.v1" ||
      !hash(raw.plan.hash) ||
      (raw.plan.retain_previous !== undefined &&
        raw.plan.retain_previous !== false) ||
      (raw.plan.retained_releases !== undefined &&
        (!Array.isArray(raw.plan.retained_releases) ||
          raw.plan.retained_releases.length !== 0))
    )
      invalid();
    const planRelease = validateRelease(raw.plan.release, database.engine);
    if (
      !release ||
      !Object.keys(planRelease).every(
        (key) =>
          planRelease[key as keyof Release] === release[key as keyof Release],
      )
    )
      invalid();
    validateStoragePlan(raw.plan.storage, database);
    fields(raw.plan.database, ["reference", "network_name", "credentials"]);
    databaseRef(raw.plan.database.reference, database);
    if (
      raw.plan.database.reference.configuration_hash !==
        snapshot.database?.configuration_hash ||
      !/^ncdn_[a-f0-9]{32}$/.test(String(raw.plan.database.network_name)) ||
      !Array.isArray(raw.plan.database.credentials) ||
      raw.plan.database.credentials.length !== database.credentials.length
    )
      invalid();
    const credentials = raw.plan.database.credentials as unknown[];
    for (const [index, credential] of credentials.entries()) {
      fields(credential, ["role", "username", "secret_id", "version"]);
      const wanted = [...database.credentials].sort((a, b) =>
        a.role.localeCompare(b.role),
      )[index];
      if (
        !wanted ||
        credential.role !== wanted.role ||
        credential.username !== wanted.username ||
        credential.secret_id !== wanted.secret_id ||
        credential.version !== wanted.version
      )
        invalid();
    }
    if (raw.plan.previous_healthy !== undefined)
      validateHealthy(raw.plan.previous_healthy, database, snapshot);
    planHash = raw.plan.hash as string;
  }
  if (raw.reconciliation_healthy !== undefined)
    validateHealthy(raw.reconciliation_healthy, database, snapshot);
  let result: Operation["result"];
  if (raw.result !== undefined) {
    fields(
      raw.result,
      ["outcome", "ready", "restored", "uncertain"],
      [
        "container_id",
        "previous_container_id",
        "image_id",
        "daemon_id",
        "plan_hash",
      ],
    );
    if (
      !["succeeded", "failed"].includes(String(raw.result.outcome)) ||
      [raw.result.ready, raw.result.restored, raw.result.uncertain].some(
        (value) => typeof value !== "boolean",
      ) ||
      (raw.result.plan_hash !== undefined &&
        (!hash(raw.result.plan_hash) ||
          (planHash && raw.result.plan_hash !== planHash))) ||
      (raw.result.container_id !== undefined && !id(raw.result.container_id))
    )
      invalid();
    result = {
      outcome: raw.result.outcome,
      ready: raw.result.ready,
      restored: raw.result.restored,
      uncertain: raw.result.uncertain,
      ...(raw.result.container_id
        ? { container_id: raw.result.container_id }
        : {}),
      ...(raw.result.plan_hash ? { plan_hash: raw.result.plan_hash } : {}),
    } as Operation["result"];
  }
  return {
    id: raw.id as string,
    state: raw.state as Operation["state"],
    created_at: raw.created_at as string,
    updated_at: raw.updated_at as string,
    mutation_hold: raw.mutation_hold as boolean,
    restore_ownership_held: raw.restore_ownership_held as boolean,
    snapshot,
    release,
    result,
    plan_hash: planHash,
  };
}
// Execution proofs remain controller authority. V2 readers retain only the
// fields displayed or used to gate another explicit, server-authorized request.
// Never cast a V2 plan/proof into the V1 mutation-authority type.
export function validateDatabaseHistoryOperation(
  raw: unknown,
  database: HostingDatabase,
  scope: DatabaseScope,
  expected?: { id?: string; generation?: number },
): DatabaseOperation {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) invalid();
  const value = raw as Record<string, unknown>;
  if (
    value.database_upgrade === undefined &&
    value.database_maintenance === undefined
  )
    return validateDatabaseOperation(raw, database, scope, expected);
  fields(
    value,
    [
      "id",
      "project_id",
      "resource_id",
      "binding_id",
      "created_by",
      "state",
      "snapshot",
      "restore_ownership_held",
      "mutation_hold",
      "reconcile_requested",
      "created_at",
      "updated_at",
    ],
    [
      "run_id",
      "reconciliation_run_id",
      "release",
      "plan",
      "prepared_receipt",
      "complete_receipt",
      "result",
      "task_mode",
      "observation_lease",
      "reconciliation_healthy",
      "database_upgrade",
      "database_maintenance",
    ],
  );
  if (
    database.resource.project_id !== scope.project ||
    database.resource.project_environment_id !== scope.environment ||
    value.project_id !== scope.project ||
    value.resource_id !== database.resource.id ||
    value.binding_id !== database.binding.id ||
    !id(value.id) ||
    (expected?.id !== undefined && value.id !== expected.id) ||
    !states.includes(String(value.state)) ||
    typeof value.mutation_hold !== "boolean" ||
    typeof value.restore_ownership_held !== "boolean" ||
    typeof value.reconcile_requested !== "boolean" ||
    !safeText(value.created_at, 64) ||
    !Number.isFinite(Date.parse(String(value.created_at))) ||
    !safeText(value.updated_at, 64) ||
    !Number.isFinite(Date.parse(String(value.updated_at))) ||
    (value.task_mode !== undefined &&
      !["deploy", "observe"].includes(String(value.task_mode)))
  )
    invalid();
  if (
    (value.database_upgrade !== undefined) ===
    (value.database_maintenance !== undefined)
  )
    invalid();
  const maintenance = value.database_maintenance !== undefined;
  const proof = maintenance
    ? value.database_maintenance
    : value.database_upgrade;
  fields(proof, [
    "version",
    "capture_plan",
    "origin_receipt",
    "previous_plan",
    "previous_healthy",
    "hash",
    ...(maintenance ? ["membership_revision", "members", "retired"] : []),
  ]);
  if (
    proof.version !==
      (maintenance
        ? "database-network.maintenance.v1"
        : "database-network.empty-upgrade.v1") ||
    !hash(proof.hash)
  )
    invalid();
  const snapshot = validateSnapshot(
    value.snapshot,
    database,
    expected?.generation,
  );
  const release =
    value.release === undefined
      ? undefined
      : validateRelease(value.release, database.engine);
  let planHash: string | undefined;
  if (value.plan !== undefined) {
    fields(
      value.plan,
      [
        "snapshot",
        "release",
        "compiler_version",
        "hash",
        "storage",
        "database",
        "previous_healthy",
      ],
      ["retain_previous", "retained_releases"],
    );
    const planSnapshot = validateSnapshot(value.plan.snapshot, database);
    const planRelease = validateRelease(value.plan.release, database.engine);
    if (
      value.plan.compiler_version !==
        "database-hosting.private.membership.v2" ||
      !hash(value.plan.hash) ||
      !sameSnapshot(snapshot, planSnapshot) ||
      !release ||
      !Object.keys(planRelease).every(
        (key) =>
          planRelease[key as keyof Release] === release[key as keyof Release],
      )
    )
      invalid();
    planHash = value.plan.hash as string;
  } else if (
    release !== undefined ||
    value.prepared_receipt !== undefined ||
    value.result !== undefined
  )
    invalid();
  let result: Operation["result"];
  if (value.result !== undefined) {
    fields(
      value.result,
      ["outcome", "ready", "restored", "uncertain"],
      [
        "container_id",
        "previous_container_id",
        "image_id",
        "daemon_id",
        "plan_hash",
      ],
    );
    if (
      !["succeeded", "failed"].includes(String(value.result.outcome)) ||
      [value.result.ready, value.result.restored, value.result.uncertain].some(
        (v) => typeof v !== "boolean",
      ) ||
      !planHash ||
      value.result.plan_hash !== planHash ||
      (value.result.container_id !== undefined &&
        !id(value.result.container_id)) ||
      (value.result.previous_container_id !== undefined &&
        !id(value.result.previous_container_id)) ||
      (value.result.image_id !== undefined &&
        value.result.image_id !== release?.image_id) ||
      (value.result.daemon_id !== undefined &&
        value.result.daemon_id !== release?.daemon_id)
    )
      invalid();
    result = {
      outcome: value.result.outcome,
      ready: value.result.ready,
      restored: value.result.restored,
      uncertain: value.result.uncertain,
      ...(value.result.container_id
        ? { container_id: value.result.container_id }
        : {}),
      plan_hash: planHash,
    } as Operation["result"];
  }
  for (const receipt of [value.prepared_receipt, value.complete_receipt]) {
    if (receipt === undefined) continue;
    fields(receipt, ["id", "body_hash", "created_at"]);
    if (
      !id(receipt.id) ||
      !hash(receipt.body_hash) ||
      !safeText(receipt.created_at, 64) ||
      !Number.isFinite(Date.parse(String(receipt.created_at)))
    )
      invalid();
  }
  if (
    value.state === "succeeded" &&
    (result?.outcome !== "succeeded" ||
      !result.ready ||
      result.restored ||
      result.uncertain ||
      value.mutation_hold ||
      value.restore_ownership_held ||
      value.complete_receipt === undefined)
  )
    invalid();
  return {
    id: value.id as string,
    state: value.state as Operation["state"],
    created_at: value.created_at as string,
    updated_at: value.updated_at as string,
    mutation_hold: value.mutation_hold as boolean,
    restore_ownership_held: value.restore_ownership_held as boolean,
    snapshot,
    release,
    result,
    plan_hash: planHash,
    evidence_incomplete:
      result === undefined || value.complete_receipt === undefined,
  };
}
export const databaseDeploymentKey = (scope: DatabaseScope, resource: string) =>
  [
    "databaseDeployments",
    scope.actor,
    scope.project,
    scope.environment,
    resource,
  ] as const;
export const databaseOperationKey = (
  scope: DatabaseScope,
  resource: string,
  operation: string,
) =>
  [
    "databaseOperation",
    scope.actor,
    scope.project,
    scope.environment,
    resource,
    operation,
  ] as const;
export async function deployDatabase(
  database: HostingDatabase,
  scope: DatabaseScope,
  body: DatabaseDeploymentInput,
  signal?: AbortSignal,
) {
  fields(body, ["expected_generation", "idempotency_key"]);
  if (
    body.expected_generation !== database.resource.generation ||
    !/^[a-f0-9]{32}$/.test(String(body.idempotency_key))
  )
    invalid();
  const raw = await boundedDatabaseRequest(async (signal) => {
    const response = await fetch(
      `/api/v1/resources/${encodeURIComponent(database.resource.id)}/database/deployments`,
      {
        method: "POST",
        body: JSON.stringify(body),
        signal,
        cache: "no-store",
        credentials: "include",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
          "X-NeroCD-CSRF": "1",
        },
      },
    );
    if (!response.ok) {
      const requestID = response.headers.get("X-Request-ID");
      throw new ApiError(
        response.status,
        "Database deployment admission failed",
        requestID && /^[A-Za-z0-9_-]{1,128}$/.test(requestID)
          ? requestID
          : null,
      );
    }
    if (response.status !== 202) invalid();
    return response.json() as Promise<unknown>;
  }, signal);
  return validateDatabaseHistoryOperation(raw, database, scope, {
    generation: body.expected_generation as number,
  });
}
export const databaseDeploymentsQuery = (
  database: HostingDatabase,
  scope: DatabaseScope,
  offset = 0,
) =>
  queryOptions({
    queryKey: [...databaseDeploymentKey(scope, database.resource.id), offset],
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    queryFn: async ({ signal }): Promise<DatabaseDeploymentPage> => {
      const raw = await boundedDatabaseRequest(
        (signal) =>
          request(
            `/api/v1/resources/${encodeURIComponent(database.resource.id)}/deployments`,
            { signal, cache: "no-store", query: { limit: 20, offset } },
          ),
        signal,
      );
      fields(raw, ["items", "count", "limit", "offset", "total"]);
      if (
        !Array.isArray(raw.items) ||
        raw.items.length > 20 ||
        raw.limit !== 20 ||
        raw.offset !== offset ||
        raw.count !== raw.items.length ||
        !Number.isSafeInteger(raw.total) ||
        Number(raw.total) < 0 ||
        (Number(raw.total) < offset + raw.items.length && raw.items.length > 0)
      )
        invalid();
      const items = (raw.items as unknown[]).map((value) =>
        validateDatabaseHistoryOperation(value, database, scope),
      );
      if (new Set(items.map((value) => value.id)).size !== items.length)
        invalid();
      return {
        items,
        count: raw.count as number,
        limit: 20,
        offset,
        total: raw.total as number,
      };
    },
  });
export const databaseOperationQuery = (
  database: HostingDatabase,
  scope: DatabaseScope,
  operation: string,
) =>
  queryOptions({
    queryKey: databaseOperationKey(scope, database.resource.id, operation),
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    queryFn: async ({ signal }) =>
      validateDatabaseHistoryOperation(
        await boundedDatabaseRequest(
          (signal) =>
            request(
              `/api/v1/hosting-operations/${encodeURIComponent(operation)}`,
              { signal, cache: "no-store" },
            ),
          signal,
        ),
        database,
        scope,
        { id: operation },
      ),
  });
