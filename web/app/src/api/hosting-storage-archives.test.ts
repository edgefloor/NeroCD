import { afterEach, expect, test, vi } from "vitest";
import {
  archiveBackup,
  archiveOperation,
  archiveRestore,
  archiveBackupEligible,
  getArchiveBackup,
  listArchiveBackups,
  requestArchiveBackup,
  requestArchiveRecovery,
  requestArchiveRestore,
  refreshArchiveConfiguration,
  type ArchiveScope,
} from "./hosting-storage-archives";
import type { components } from "./generated";
import { ApiError } from "./errors";
const hash = `sha256:${"a".repeat(64)}`;
const scope: ArchiveScope = {
  actor: "u",
  project: "p",
  resource: "r",
  destination: "d",
  binding: "b",
};
const storage = {
  id: "storage",
  project_id: "p",
  resource_id: "r",
  binding_id: "b",
  destination_id: "d",
  kind: "platform_managed",
  driver: "local",
  access_mode: "read_write",
  retention: "retain",
  generation: 1,
  hash,
};
const blob = {
  backup_id: "backup",
  destination_id: "d",
  daemon_id: "daemon",
  sha256: hash,
  size_bytes: 10,
  format: "nerocd-storage-v1",
};
const backup = {
  id: "backup",
  project_id: "p",
  resource_id: "r",
  operation_id: "op",
  state: "succeeded",
  created_by: "u",
  created_at: "2026-10-24T00:00:00Z",
  blob,
};
const fixture = () => ({
  id: "op",
  project_id: "p",
  resource_id: "r",
  binding_id: "b",
  state: "reconciliation_required",
  task_mode: "backup",
  mutation_hold: true,
  restore_ownership_held: false,
  storage_archive: {
    mode: "backup",
    backup_id: "backup",
    plan: {
      operation_id: "op",
      project_id: "p",
      destination_id: "d",
      mode: "backup",
      backup_id: "backup",
      hash,
      source_storage: storage,
    },
    progress: [
      {
        receipt: {
          id: "receipt",
          body_hash: hash,
          created_at: "2026-10-24T00:00:00Z",
        },
        plan_hash: hash,
        phase: "archive_published",
        blob,
        restore_verified: false,
      },
    ],
    result: {
      archive_outcome: "succeeded",
      resume_outcome: "uncertain",
      uncertain: true,
      restore_verified: false,
    },
  },
});
type Operation = components["schemas"]["HostingOperation"];
const op = (value: unknown) => value as Operation;
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
function deferredResponse() {
  let resolve!: (value: Response) => void;
  let reject!: (cause: unknown) => void;
  const promise = new Promise<Response>((accept, fail) => {
    resolve = accept;
    reject = fail;
  });
  return { promise, resolve, reject };
}
test.each(
  [0, 1, 2].flatMap((endpoint) =>
    [401, 403].map((status) => ({ endpoint, status })),
  ),
)(
  "refresh prioritizes delayed $status from endpoint $endpoint over early 503 and a stalled sibling",
  async ({ endpoint, status }) => {
    vi.useFakeTimers();
    const replies = [
      deferredResponse(),
      deferredResponse(),
      deferredResponse(),
    ];
    const fetch = vi
      .fn()
      .mockImplementationOnce(() => replies[0]!.promise)
      .mockImplementationOnce(() => replies[1]!.promise)
      .mockImplementationOnce(() => replies[2]!.promise);
    vi.stubGlobal("fetch", fetch);
    const observed = vi.fn();
    const pending = refreshArchiveConfiguration(
      scope,
      storage as components["schemas"]["HostingStorage"],
    );
    void pending.then(
      () => observed("resolved"),
      (cause) => observed(cause),
    );
    try {
      replies[(endpoint + 1) % 3]!.resolve(new Response("{}", { status: 503 }));
      await vi.advanceTimersByTimeAsync(1);
      expect(observed).not.toHaveBeenCalled();
      replies[endpoint]!.resolve(new Response("{}", { status }));
      await vi.advanceTimersByTimeAsync(1);
      expect(observed).toHaveBeenCalledExactlyOnceWith(
        expect.objectContaining({ status }),
      );
      expect(fetch).toHaveBeenCalledTimes(3);
    } finally {
      replies.forEach((reply) =>
        reply.reject(new Error("late sibling rejection")),
      );
      await vi.advanceTimersByTimeAsync(1);
    }
    expect(vi.getTimerCount()).toBe(0);
  },
);
test("refresh waits for ordinary siblings and then preserves the first endpoint error", async () => {
  vi.useFakeTimers();
  const replies = [deferredResponse(), deferredResponse(), deferredResponse()];
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockImplementationOnce(() => replies[0]!.promise)
      .mockImplementationOnce(() => replies[1]!.promise)
      .mockImplementationOnce(() => replies[2]!.promise),
  );
  const observed = vi.fn();
  const pending = refreshArchiveConfiguration(
    scope,
    storage as components["schemas"]["HostingStorage"],
  );
  void pending.then(
    () => observed("resolved"),
    (cause) => observed(cause),
  );
  replies[0]!.resolve(new Response("{}", { status: 503 }));
  await vi.advanceTimersByTimeAsync(1);
  expect(observed).not.toHaveBeenCalled();
  replies[1]!.reject(new Error("ordinary network failure"));
  replies[2]!.resolve(new Response("{}", { status: 500 }));
  await expect(pending).rejects.toMatchObject({ status: 503 });
  expect(vi.getTimerCount()).toBe(0);
});
test.each([401, 403])(
  "refresh preserves observed %i headers when its error body never finishes and other reads fail",
  async (status) => {
    vi.useFakeTimers();
    const fetch = vi.fn((path: string) =>
      Promise.resolve(
        path.endsWith("/storage")
          ? new Response(
              new ReadableStream<Uint8Array>({
                start(controller) {
                  controller.enqueue(new TextEncoder().encode("{"));
                },
              }),
              { status, headers: { "X-Request-ID": "unfinished-denial" } },
            )
          : new Response("{}", {
              status: path.endsWith("/binding") ? 500 : 503,
            }),
      ),
    );
    vi.stubGlobal("fetch", fetch);
    const observed = vi.fn();
    void refreshArchiveConfiguration(
      scope,
      storage as components["schemas"]["HostingStorage"],
    ).then(
      () => observed("resolved"),
      (cause) => observed(cause),
    );
    await vi.advanceTimersByTimeAsync(1);
    expect(observed).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({ status, requestID: "unfinished-denial" }),
    );
    expect(fetch).toHaveBeenCalledTimes(3);
    expect(vi.getTimerCount()).toBe(0);
  },
);
test("refresh cancellation retires every bounded read and consumes late transport rejections", async () => {
  vi.useFakeTimers();
  const replies = [deferredResponse(), deferredResponse(), deferredResponse()];
  const fetch = vi
    .fn()
    .mockImplementationOnce(() => replies[0]!.promise)
    .mockImplementationOnce(() => replies[1]!.promise)
    .mockImplementationOnce(() => replies[2]!.promise);
  vi.stubGlobal("fetch", fetch);
  const cancel = new AbortController();
  const pending = refreshArchiveConfiguration(
    scope,
    storage as components["schemas"]["HostingStorage"],
    cancel.signal,
  );
  const observed = expect(pending).rejects.toMatchObject({
    name: "AbortError",
  });
  cancel.abort();
  await observed;
  replies.forEach((reply) =>
    reply.reject(new ApiError(503, "late failure", null)),
  );
  await vi.advanceTimersByTimeAsync(1);
  expect(vi.getTimerCount()).toBe(0);
  expect(fetch).toHaveBeenCalledTimes(3);
});
test("safe projection separates archive success, resume uncertainty and holds", () => {
  const result = archiveOperation(op(fixture()), scope, "op", "backup");
  expect(result).toMatchObject({
    mutationHold: true,
    result: {
      archive: "succeeded",
      resume: "uncertain",
      uncertain: true,
      verified: false,
    },
    phases: ["archive_published"],
  });
  expect(result).not.toHaveProperty("storage_archive");
  expect(result).not.toHaveProperty("plan");
});
test.each(["project_id", "resource_id", "binding_id"])(
  "reject foreign operation %s",
  (field) => {
    expect(() =>
      archiveOperation(op({ ...fixture(), [field]: "foreign" }), scope, "op"),
    ).toThrow();
  },
);
test("reject wrong destination, source, private progress authority and unknown phases", () => {
  for (const mutate of [
    (v: ReturnType<typeof fixture>) => {
      v.storage_archive.plan.destination_id = "foreign";
    },
    (v: ReturnType<typeof fixture>) => {
      v.storage_archive.plan.source_storage = {
        ...storage,
        resource_id: "foreign",
      };
    },
    (v: ReturnType<typeof fixture>) => {
      Object.assign(v.storage_archive.progress[0]!, { lease_id: "private" });
    },
    (v: ReturnType<typeof fixture>) => {
      v.storage_archive.progress[0]!.phase = "ready";
    },
    (v: ReturnType<typeof fixture>) => {
      v.storage_archive.progress[0]!.blob = {
        ...blob,
        destination_id: "foreign",
      };
    },
  ]) {
    const value = fixture();
    mutate(value);
    expect(() => archiveOperation(op(value), scope, "op")).toThrow();
  }
});
test("backup metadata is closed, scoped and never grants a download path", () => {
  expect(archiveBackup(backup, scope)).toEqual(backup);
  for (const value of [
    { ...backup, project_id: "foreign" },
    { ...backup, resource_id: "foreign" },
    { ...backup, blob: { ...blob, path: "/private" } },
    { ...backup, state: "ready" },
  ])
    expect(() => archiveBackup(value, scope)).toThrow();
});
test("restore requires a new same-project resource and matching target storage", () => {
  const operation = {
    ...fixture(),
    id: "restore",
    resource_id: "new",
    binding_id: "newbinding",
    task_mode: "restore",
    storage_archive: {
      ...fixture().storage_archive,
      mode: "restore",
      restore_id: "restore",
      plan: {
        ...fixture().storage_archive.plan,
        operation_id: "restore",
        mode: "restore",
        target: {
          resource: {
            id: "new",
            project_id: "p",
            kind: "application",
            source: { kind: "image" },
          },
          storage: {
            ...storage,
            id: "newstorage",
            resource_id: "new",
            binding_id: "newbinding",
          },
          plan: { creation_operation_id: "restore" },
        },
      },
    },
  };
  const value = {
    resource: {
      id: "new",
      name: "Restored",
      project_id: "p",
      kind: "application",
      source: { kind: "image" },
    },
    storage: operation.storage_archive.plan.target.storage,
    operation,
  } as unknown as components["schemas"]["HostingStorageRestore"];
  expect(archiveRestore(value, scope, "restore").resource).toBe("new");
  expect(() =>
    archiveRestore(
      { ...value, resource: { ...value.resource, id: "r" } },
      scope,
    ),
  ).toThrow();
  expect(() =>
    archiveRestore(
      { ...value, storage: { ...value.storage, destination_id: "foreign" } },
      scope,
    ),
  ).toThrow();
});
test("routes, pagination, CSRF and exact saved request identity are retained", async () => {
  const fetch = vi.fn(
    async (_url: string, options: RequestInit) =>
      new Response(
        JSON.stringify(
          options.method === "POST"
            ? backup
            : { items: [backup], count: 1, limit: 20, offset: 0, total: 1 },
        ),
      ),
  );
  vi.stubGlobal("fetch", fetch);
  await listArchiveBackups(scope, 0);
  const body = { request_id: "a".repeat(32), expected_resource_generation: 2 };
  await requestArchiveBackup(scope, body);
  await requestArchiveBackup(scope, body);
  expect(fetch.mock.calls[0]![0]).toBe(
    "/api/v1/resources/r/storage-backups?limit=20&offset=0",
  );
  expect(fetch.mock.calls[1]![1]).toMatchObject({
    method: "POST",
    credentials: "include",
    cache: "no-store",
    body: JSON.stringify(body),
    headers: { "X-NeroCD-CSRF": "1" },
  });
  expect(fetch.mock.calls[2]![1].body).toBe(fetch.mock.calls[1]![1].body);
});
test("list rejects foreign rows and invalid count instead of rendering partial history", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          items: [{ ...backup, resource_id: "foreign" }],
          count: 1,
          limit: 20,
          offset: 0,
          total: 1,
        }),
      ),
    ),
  );
  await expect(listArchiveBackups(scope, 0)).rejects.toThrow();
});
test("invalid write authority never reaches transport", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  await expect(
    requestArchiveBackup(scope, {
      request_id: "invalid",
      expected_resource_generation: 2,
    }),
  ).rejects.toThrow();
  await expect(
    requestArchiveRestore(scope, "backup", {
      request_id: "a".repeat(32),
      name: "",
    }),
  ).rejects.toThrow();
  await expect(
    requestArchiveRecovery(scope, "op", "backup", {
      request_id: "a".repeat(32),
      expected_plan_hash: "wrong",
    }),
  ).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
});
test("stalled read times out even when transport ignores abort", async () => {
  vi.useFakeTimers();
  vi.stubGlobal(
    "fetch",
    vi.fn(() => new Promise(() => {})),
  );
  const pending = expect(getArchiveBackup(scope, "backup")).rejects.toThrow(
    /timed out/,
  );
  await vi.advanceTimersByTimeAsync(30001);
  await pending;
});
test("configuration refresh reads authoritative resource, storage and binding with scope validation", async () => {
  const resource = {
    id: "r",
    project_id: "p",
    generation: 3,
    kind: "application",
    source: { kind: "image" },
  };
  const binding = { id: "b", resource_id: "r", destination_id: "d" };
  const check = async (r: unknown, s: unknown, b: unknown) => {
    const fetch = vi.fn(
      async (path: string) =>
        new Response(
          JSON.stringify(
            path.endsWith("/storage") ? s : path.endsWith("/binding") ? b : r,
          ),
        ),
    );
    vi.stubGlobal("fetch", fetch);
    return refreshArchiveConfiguration(
      scope,
      storage as components["schemas"]["HostingStorage"],
    );
  };
  expect((await check(resource, storage, binding)).generation).toBe(3);
  await expect(
    check({ ...resource, project_id: "foreign" }, storage, binding),
  ).rejects.toThrow();
  await expect(
    check(resource, { ...storage, hash: `sha256:${"b".repeat(64)}` }, binding),
  ).rejects.toThrow();
  await expect(
    check(resource, storage, { ...binding, destination_id: "foreign" }),
  ).rejects.toThrow();
});
test("eligibility rejects foreign operation identity and excludes local builds and database policy", async () => {
  const options = {
    resource_id: "r",
    resource_generation: 2,
    storage_ref: { storage_id: "storage", generation: 1, hash },
    previous_healthy_operation_id: "healthy",
    host_port: 8080,
    container_port: 80,
    readiness: { path: "/ready", timeout_seconds: 30 },
    available: true,
    reason: "available",
    routes: [],
    storage_route_continuity: null,
  };
  const healthy = {
    id: "healthy",
    project_id: "p",
    resource_id: "r",
    binding_id: "b",
    state: "succeeded",
    mutation_hold: false,
    restore_ownership_held: false,
    snapshot: {
      resource_id: "r",
      destination_id: "d",
      policy_version: "public-image.owned-storage.release-ports.v1",
      storage: { storage_id: "storage", hash },
    },
  };
  const check = async (value: unknown) => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(new Response(JSON.stringify(options)))
        .mockResolvedValueOnce(new Response(JSON.stringify(value))),
    );
    return archiveBackupEligible(
      scope,
      storage as components["schemas"]["HostingStorage"],
      2,
    );
  };
  expect(await check(healthy)).toBe(true);
  const combined = {
    ...healthy,
    snapshot: {
      ...healthy.snapshot,
      policy_version: "public-image.owned-storage.runtime-env.release-ports.v1",
      runtime_configuration: { generation: 7, hash },
    },
  };
  expect(await check(combined)).toBe(true);
  for (const runtime of [undefined, {}, { generation: 0, hash }, { generation: 1, hash: "wrong" }]) {
    expect(await check({ ...combined, snapshot: { ...combined.snapshot, runtime_configuration: runtime } })).toBe(false);
  }
  expect(await check({ ...healthy, snapshot: { ...healthy.snapshot, runtime_configuration: { generation: 1, hash } } })).toBe(false);
  expect(await check({ ...combined, snapshot: { ...combined.snapshot, application_database: { binding_id: "foreign" } } })).toBe(false);

  await expect(check({ ...healthy, project_id: "foreign" })).rejects.toThrow();
  expect(
    await check({
      ...healthy,
      snapshot: { ...healthy.snapshot, policy_version: "database" },
    }),
  ).toBe(false);
  expect(
    await check({
      ...healthy,
      snapshot: {
        ...healthy.snapshot,
        local_build: { artifact: { build_id: "build" } },
      },
    }),
  ).toBe(false);
});
test("recovery validates original plan selection and projects only the fresh recovery result", async () => {
  const original = fixture();
  const value = {
    ...original,
    id: "recovery",
    task_mode: "recover",
    storage_archive: {
      ...original.storage_archive,
      mode: "recover",
      recovery_of_operation_id: "op",
      plan: {
        ...original.storage_archive.plan,
        operation_id: "recovery",
        mode: "recover",
        recovery_of_operation_id: "op",
        recovery_evidence: { original_plan_hash: hash },
      },
      progress: [],
      result: {
        archive_outcome: "not_attempted",
        resume_outcome: "resumed",
        uncertain: false,
        restore_verified: false,
      },
    },
  };
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(new Response(JSON.stringify(value)))
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          ...value,
          storage_archive: {
            ...value.storage_archive,
            recovery_of_operation_id: "foreign",
          },
        }),
      ),
    );
  vi.stubGlobal("fetch", fetch);
  const body = { request_id: "b".repeat(32), expected_plan_hash: hash };
  expect(
    await requestArchiveRecovery(scope, "op", "backup", body),
  ).toMatchObject({
    id: "recovery",
    mode: "recover",
    result: { archive: "not_attempted", resume: "resumed" },
  });
  await expect(
    requestArchiveRecovery(scope, "op", "backup", body),
  ).rejects.toThrow();
  expect(fetch.mock.calls[0]![0]).toBe(
    "/api/v1/hosting-operations/op/storage-archive-recovery",
  );
});

test("Compose archive uses real stack proof without fabricated image storage", () => {
  const value = fixture();
  const snapshot = { resource_id: "r", binding_id: "b", destination_id: "d" };
  const volume = { name: "uploads", volume_name: "owned-uploads" };
  const plan = { snapshot, hash, stack: { volumes: [volume] } };
  const healthy = { snapshot, operation_id: "deploy", plan_hash: hash, stack: {} };
  const compose = { ...value, storage_archive: { ...value.storage_archive, plan: { ...value.storage_archive.plan, source_storage: {}, source_healthy: {}, source_materialization: {}, writers: [], compiler_version: "managed-compose.local-volume-archive.v1", policy_version: "managed-compose.local-volume-archive.v1", compose_source: { healthy, plan, volume, retained: [] } } } };
  expect(archiveOperation(op(compose), scope, "op", "backup").backup).toBe("backup");
  const mixed = structuredClone(compose); mixed.storage_archive.plan.source_storage = storage;
  expect(() => archiveOperation(op(mixed), scope, "op", "backup")).toThrow();
  const wrong = { ...value, storage_archive: { ...value.storage_archive, plan: { ...value.storage_archive.plan, compiler_version: "managed-compose.local-volume-archive.v1" } } };
  expect(() => archiveOperation(op(wrong), scope, "op", "backup")).toThrow();
  for (const change of [ { ...plan, hash: `sha256:${"b".repeat(64)}` }, { ...plan, snapshot: { ...snapshot, resource_id: "other" } }, { ...plan, stack: { volumes: [] } } ]) {
    const invalid = structuredClone(compose); invalid.storage_archive.plan.compose_source.plan = change;
    expect(() => archiveOperation(op(invalid), scope, "op", "backup")).toThrow();
  }
  const invalid = structuredClone(compose); invalid.storage_archive.plan.compose_source.healthy.snapshot.binding_id = "other";
  expect(() => archiveOperation(op(invalid), scope, "op", "backup")).toThrow();
});
test("Compose history preserves logical volume and source deployment, rejecting partial metadata", () => {
  expect(archiveBackup({ ...backup, volume: "uploads", source_deployed_operation_id: "deploy" } as never, scope)).toMatchObject({ volume: "uploads" });
  expect(() => archiveBackup({ ...backup, volume: "uploads" } as never, scope)).toThrow();
});

test("legacy archives reject zero Compose storage proof without a Compose source", () => {
  const value = fixture();
  value.storage_archive.plan.source_storage = { ...storage, id: "", kind: "" };
  expect(() => archiveOperation(op(value), scope, "op", "backup")).toThrow();
});
