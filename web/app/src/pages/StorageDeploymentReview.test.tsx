import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import type { Resource } from "@/api/hosting";
import { ApiError } from "@/api/errors";
import type { StorageDeploymentOptions } from "@/api/hosting-storage-deployment";
import {
  StorageContinuityDetail,
  StorageDeploymentReview,
} from "./StorageDeploymentReview";
const scope = { actor: "actor", project: "project", resource: "resource" };
const resource = {
  id: "resource",
  project_id: "project",
  generation: 1,
  source: { kind: "image", image: { reference: "uploads:2" } },
} as Resource;
const options: StorageDeploymentOptions = {
  resource_id: "resource",
  resource_generation: 1,
  storage_ref: { storage_id: "storage", generation: 1, hash: "hash" },
  previous_healthy_operation_id: "old",
  host_port: 8080,
  container_port: 80,
  readiness: { path: "/health", timeout_seconds: 30 },
  available: true,
  reason: "available",
  routes: [
    {
      id: "route",
      hostname: "uploads.example.test",
      path_prefix: "/",
      version: 4,
      selected_release_operation_id: "old",
    },
  ],
  storage_route_continuity: {
    expected_loaded_generation: 7,
    expected_loaded_proxy_operation_id: "proxy_old",
    routes: [
      {
        route_id: "route",
        expected_version: 4,
        expected_selected_release_operation_id: "old",
      },
    ],
  },
};
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status });
let client: QueryClient;
afterEach(() => {
  cleanup();
  client?.clear();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
function setup(
  handler?: (
    url: string,
    init: RequestInit,
  ) => Response | Promise<Response> | undefined,
  authority: StorageDeploymentOptions = options,
  onRefresh: (signal: AbortSignal) => Promise<void> = async () => {},
) {
  const fetch = vi.fn(async (url: string, init: RequestInit) => {
    const response = await handler?.(url, init);
    if (response) return response;
    if (url.endsWith("/storage-deployment-options")) return json(authority);
    if (url.endsWith("/storage"))
      return json({
        id: "storage",
        project_id: "project",
        resource_id: "resource",
        binding_id: "binding",
        generation: 1,
        hash: "hash",
        kind: "platform_managed",
        driver: "local",
        access_mode: "read_write",
        retention: "retain",
        volume_name: "ncd_uploads",
        mount_path: "/app/uploads",
      });
    if (url.endsWith("/runtime-configuration"))
      return json({
        runtime_configuration: {
          project_id: "project",
          resource_id: "resource",
          runtime_generation: 0,
        },
      });
    const input =
      init.method === "POST" ? JSON.parse(init.body as string) : undefined;
    return json(
      {
        id: "hop",
        binding_id: "binding",
        project_id: "project",
        resource_id: "resource",
        snapshot: {
          resource_generation: 1,
          host_port: input?.host_port ?? 8080,
          container_port: input?.container_port ?? 80,
          readiness: input?.readiness ?? options.readiness,
          storage: options.storage_ref,
          ...(authority.reason === "initial_deployment"
            ? {}
            : { storage_route_continuity: { hash: "continuity-hash" } }),
        },
      },
      202,
    );
  });
  vi.stubGlobal("fetch", fetch);
  const selected = vi.fn(),
    denied = vi.fn(),
    refreshed = vi.fn(onRefresh);
  client = new QueryClient();
  const view = render(
    <QueryClientProvider client={client}>
      <StorageDeploymentReview
        bindingID="binding"
        scope={scope}
        resource={resource}
        options={authority}
        blocked={false}
        mutationHeld={false}
        onDenied={denied}
        onSelected={selected}
        onRefresh={refreshed}
      />
    </QueryClientProvider>,
  );
  return { fetch, selected, denied, refreshed, ...view };
}
async function review() {
  fireEvent.click(
    screen.getByRole("button", { name: "Review current storage deployment" }),
  );
  await screen.findByRole("button", {
    name: "Deploy reviewed image with uploads",
  });
  fireEvent.click(screen.getByRole("checkbox"));
}
const initialOptions: StorageDeploymentOptions = {
  ...options,
  reason: "initial_deployment",
  previous_healthy_operation_id: null,
  host_port: null,
  container_port: null,
  readiness: null,
  routes: [],
  storage_route_continuity: null,
};
async function initialReview() {
  fireEvent.change(screen.getByLabelText("Runner-local host port"), {
    target: { value: "9080" },
  });
  fireEvent.change(screen.getByLabelText("Container port"), {
    target: { value: "3000" },
  });
  fireEvent.change(screen.getByLabelText("Readiness path"), {
    target: { value: "/ready" },
  });
  fireEvent.change(screen.getByLabelText("Readiness timeout in seconds"), {
    target: { value: "45" },
  });
  fireEvent.submit(
    screen.getByRole("form", { name: "Configure first storage deployment" }),
  );
  await screen.findByRole("button", {
    name: "Deploy reviewed image with uploads",
  });
  fireEvent.click(screen.getByRole("checkbox"));
}
test("first deployment requires operator settings and reviews exact owned volume and retention", async () => {
  const { fetch, selected } = setup(undefined, initialOptions);
  expect(screen.getByText(/preserves existing or restored data/)).toBeTruthy();
  expect(
    screen.getByLabelText<HTMLInputElement>("Runner-local host port").value,
  ).toBe("");
  expect(screen.getByLabelText<HTMLInputElement>("Readiness path").value).toBe(
    "",
  );
  expect(
    screen.queryByRole("button", {
      name: "Deploy reviewed image with uploads",
    }),
  ).toBeNull();
  await initialReview();
  expect(screen.getByText("ncd_uploads")).toBeTruthy();
  expect(screen.getByText("/app/uploads")).toBeTruthy();
  expect(screen.getByText(/Application data remains/)).toBeTruthy();
  expect(
    screen.getByText(/no previous healthy writer is selected/),
  ).toBeTruthy();
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST"),
  ).toHaveLength(0);
  fireEvent.click(
    screen.getByRole("button", { name: "Deploy reviewed image with uploads" }),
  );
  await vi.waitFor(() => expect(selected).toHaveBeenCalledWith("hop"));
  const body = JSON.parse(
    fetch.mock.calls.find(([, init]) => init.method === "POST")![1]
      .body as string,
  );
  expect(body).toMatchObject({
    expected_generation: 1,
    host_port: 9080,
    container_port: 3000,
    readiness: { path: "/ready", timeout_seconds: 45 },
  });
  expect(body).not.toHaveProperty("storage_route_continuity");
});
test("initial lost response retains exact chosen settings and key on retry", async () => {
  let sent = 0;
  const { fetch, selected } = setup((url) => {
    if (url.endsWith("/deployments") && ++sent === 1)
      throw new TypeError("lost");
    return undefined;
  }, initialOptions);
  await initialReview();
  fireEvent.click(
    screen.getByRole("button", { name: "Deploy reviewed image with uploads" }),
  );
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Retry identical storage deployment request",
    }),
  );
  await vi.waitFor(() => expect(selected).toHaveBeenCalledWith("hop"));
  const writes = fetch.mock.calls.filter(([, init]) => init.method === "POST");
  expect(writes).toHaveLength(2);
  expect(writes[0]![1].body).toBe(writes[1]![1].body);
});
test("initial conflict requires explicit fresh review and acknowledgment while preserving settings", async () => {
  const { fetch } = setup(
    (url) =>
      url.endsWith("/deployments") ? json({ error: "stale" }, 409) : undefined,
    initialOptions,
  );
  await initialReview();
  fireEvent.click(
    screen.getByRole("button", { name: "Deploy reviewed image with uploads" }),
  );
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Review current storage and routes for a new request",
    }),
  );
  await screen.findByRole("button", {
    name: "Deploy reviewed image with uploads",
  });
  expect(screen.getByRole<HTMLInputElement>("checkbox").checked).toBe(false);
  expect(
    screen.getByLabelText<HTMLInputElement>("Runner-local host port").value,
  ).toBe("9080");
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST"),
  ).toHaveLength(1);
});
test("initial authority retirement blocks review and preserves nonsecret input", async () => {
  const { fetch } = setup(
    (url) =>
      url.endsWith("/storage-deployment-options")
        ? json({
            ...initialOptions,
            available: false,
            reason: "no_previous_healthy",
          })
        : undefined,
    initialOptions,
  );
  fireEvent.change(screen.getByLabelText("Runner-local host port"), {
    target: { value: "9080" },
  });
  fireEvent.submit(screen.getByRole("form"));
  await screen.findByRole("alert");
  expect(
    screen.getByLabelText<HTMLInputElement>("Runner-local host port").value,
  ).toBe("9080");
  expect(screen.queryByRole("checkbox")).toBeNull();
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST"),
  ).toHaveLength(0);
});
test("initial stalled metadata read times out without admission and preserves chosen settings", async () => {
  setup(
    (url) =>
      url.endsWith("/storage") ? new Promise<Response>(() => {}) : undefined,
    initialOptions,
  );
  vi.useFakeTimers();
  fireEvent.change(screen.getByLabelText("Runner-local host port"), {
    target: { value: "9080" },
  });
  fireEvent.change(screen.getByLabelText("Container port"), {
    target: { value: "3000" },
  });
  fireEvent.change(screen.getByLabelText("Readiness path"), {
    target: { value: "/ready" },
  });
  fireEvent.change(screen.getByLabelText("Readiness timeout in seconds"), {
    target: { value: "45" },
  });
  fireEvent.submit(screen.getByRole("form"));
  await act(async () => {
    await vi.advanceTimersByTimeAsync(30_000);
  });
  expect(screen.getByRole("alert").textContent).toContain("30 seconds");
  expect(screen.getByLabelText<HTMLInputElement>("Readiness path").value).toBe(
    "/ready",
  );
  expect(screen.queryByRole("checkbox")).toBeNull();
});
test("review freezes exact fixed port, full route selection and key before explicit submission", async () => {
  const { fetch, selected } = setup();
  await review();
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST"),
  ).toHaveLength(0);
  expect(screen.getByText(/uploads.example.test/)).toBeTruthy();
  expect(screen.getByText("ncd_uploads")).toBeTruthy();
  expect(screen.getByText("/app/uploads")).toBeTruthy();
  expect(screen.getByText(/Application data remains/)).toBeTruthy();
  expect(screen.getByText(/Image rollback does not undo writes/)).toBeTruthy();
  fireEvent.click(
    screen.getByRole("button", { name: "Deploy reviewed image with uploads" }),
  );
  await vi.waitFor(() => expect(selected).toHaveBeenCalledWith("hop"));
  const body = JSON.parse(
    fetch.mock.calls.find(([, init]) => init.method === "POST")![1]
      .body as string,
  );
  expect(body).toMatchObject({
    expected_generation: 1,
    host_port: 8080,
    container_port: 80,
    readiness: options.readiness,
    storage_route_continuity: options.storage_route_continuity,
  });
  expect(body.idempotency_key).toHaveLength(36);
});
test.each([
  { id: "foreign" },
  { hash: "foreign" },
  { generation: 2 },
  { resource_id: "foreign" },
  { project_id: "foreign" },
  { binding_id: "foreign" },
  { kind: "external" },
  { retention: "delete" },
])(
  "replacement metadata mismatch blocks review and admission %#",
  async (mismatch) => {
    const { fetch } = setup((url) =>
      url.endsWith("/storage")
        ? json({
            id: "storage",
            project_id: "project",
            resource_id: "resource",
            binding_id: "binding",
            generation: 1,
            hash: "hash",
            kind: "platform_managed",
            driver: "local",
            access_mode: "read_write",
            retention: "retain",
            volume_name: "ncd_uploads",
            mount_path: "/app/uploads",
            ...mismatch,
          })
        : undefined,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Review current storage deployment" }),
    );
    await screen.findByRole("alert");
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(
      screen.queryByRole("button", {
        name: "Deploy reviewed image with uploads",
      }),
    ).toBeNull();
    expect(
      fetch.mock.calls.filter(([, init]) => init.method === "POST"),
    ).toHaveLength(0);
  },
);
test("replacement metadata timeout blocks review without sending a deployment", async () => {
  const { fetch } = setup((url) =>
    url.endsWith("/storage") ? new Promise<Response>(() => {}) : undefined,
  );
  vi.useFakeTimers();
  fireEvent.click(
    screen.getByRole("button", { name: "Review current storage deployment" }),
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(30_000);
  });
  expect(screen.getByRole("alert").textContent).toContain("30 seconds");
  expect(screen.queryByRole("checkbox")).toBeNull();
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST"),
  ).toHaveLength(0);
});
test("lost response preserves frozen body and key for explicit identical retry", async () => {
  let count = 0;
  const { fetch, selected } = setup((url) => {
    if (url.endsWith("/deployments") && ++count === 1)
      throw new TypeError("lost");
    return undefined;
  });
  await review();
  fireEvent.click(
    screen.getByRole("button", { name: "Deploy reviewed image with uploads" }),
  );
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Retry identical storage deployment request",
    }),
  );
  await vi.waitFor(() => expect(selected).toHaveBeenCalledWith("hop"));
  const writes = fetch.mock.calls.filter(([, init]) => init.method === "POST");
  expect(writes).toHaveLength(2);
  expect(writes[0]![1].body).toBe(writes[1]![1].body);
});
test("accepted receipt remains definitive when history refresh rejects", async () => {
  const { selected, fetch } = setup(undefined, options, async () => {
    throw new Error("history unavailable");
  });
  await review();
  fireEvent.click(
    screen.getByRole("button", { name: "Deploy reviewed image with uploads" }),
  );
  await screen.findByText(/Deployment request accepted: hop/);
  await screen.findByText(/was accepted, but history could not be refreshed/);
  expect(selected).toHaveBeenCalledExactlyOnceWith("hop");
  expect(screen.queryByText(/Delivery is unconfirmed/)).toBeNull();
  expect(
    screen.queryByRole("button", {
      name: "Retry identical storage deployment request",
    }),
  ).toBeNull();
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST"),
  ).toHaveLength(1);
});
test("accepted receipt releases submission busy state and bounds stalled refresh", async () => {
  const { selected } = setup(
    undefined,
    options,
    () => new Promise<void>(() => {}),
  );
  await review();
  vi.useFakeTimers();
  await act(async () => {
    fireEvent.click(
      screen.getByRole("button", {
        name: "Deploy reviewed image with uploads",
      }),
    );
  });
  expect(screen.getByText(/Deployment request accepted: hop/)).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Requesting storage deployment..." }),
  ).toBeNull();
  expect(selected).toHaveBeenCalledExactlyOnceWith("hop");
  await act(async () => {
    await vi.advanceTimersByTimeAsync(30_000);
  });
  expect(
    screen.getByText(/was accepted, but history could not be refreshed/),
  ).toBeTruthy();
  expect(
    screen.getByRole<HTMLButtonElement>("button", {
      name: "Review current storage deployment",
    }).disabled,
  ).toBe(false);
  expect(screen.queryByText(/Delivery is unconfirmed/)).toBeNull();
});
test("retired scope cancels accepted-receipt refresh and ignores a late rejection", async () => {
  let signal: AbortSignal | undefined;
  let reject!: (error: Error) => void;
  const { unmount, selected, denied } = setup(undefined, options, (current) => {
    signal = current;
    return new Promise<void>((_, fail) => {
      reject = fail;
    });
  });
  await review();
  fireEvent.click(
    screen.getByRole("button", { name: "Deploy reviewed image with uploads" }),
  );
  await screen.findByText(/Deployment request accepted: hop/);
  unmount();
  expect(signal?.aborted).toBe(true);
  await act(async () => reject(new ApiError(403, "late old actor denial", null)));
  expect(selected).toHaveBeenCalledExactlyOnceWith("hop");
  expect(denied).not.toHaveBeenCalled();
  expect(screen.queryByRole("alert")).toBeNull();
});
test("a stalled submission returns to exact retry after the bounded timeout", async () => {
  const { fetch } = setup((url) =>
    url.endsWith("/deployments") ? new Promise<Response>(() => {}) : undefined,
  );
  await review();
  vi.useFakeTimers();
  fireEvent.click(
    screen.getByRole("button", { name: "Deploy reviewed image with uploads" }),
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(30_000);
  });
  expect(
    screen.getByRole<HTMLButtonElement>("button", {
      name: "Retry identical storage deployment request",
    }).disabled,
  ).toBe(false);
  expect(screen.getByRole<HTMLInputElement>("checkbox").checked).toBe(true);
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST"),
  ).toHaveLength(1);
});
test("foreign storage receipt stays unconfirmed and retains recovery intent", async () => {
  const { selected } = setup((url) =>
    url.endsWith("/deployments")
      ? json(
          {
            id: "hop",
            project_id: "other",
            resource_id: "resource",
            snapshot: {},
          },
          202,
        )
      : undefined,
  );
  await review();
  fireEvent.click(
    screen.getByRole("button", { name: "Deploy reviewed image with uploads" }),
  );
  await screen.findByRole("button", {
    name: "Retry identical storage deployment request",
  });
  expect(selected).not.toHaveBeenCalled();
});
test("409 keeps old plan blocked until explicit fresh review and new acknowledgment", async () => {
  const { fetch } = setup((url) =>
    url.endsWith("/deployments") ? json({ error: "stale" }, 409) : undefined,
  );
  await review();
  fireEvent.click(
    screen.getByRole("button", { name: "Deploy reviewed image with uploads" }),
  );
  const fresh = await screen.findByRole("button", {
    name: "Review current storage and routes for a new request",
  });
  expect(
    screen.getByRole<HTMLButtonElement>("button", {
      name: "Retry identical storage deployment request",
    }).disabled,
  ).toBe(true);
  fireEvent.click(fresh);
  await screen.findByRole("button", {
    name: "Deploy reviewed image with uploads",
  });
  expect(screen.getByRole<HTMLInputElement>("checkbox").checked).toBe(false);
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST"),
  ).toHaveLength(1);
});
test.each([
  "busy",
  "runner_ineligible",
  "stale_storage",
  "continuity_unresolved",
] as const)(
  "unavailable authority blocks %s without submission",
  async (reason) => {
    const { fetch } = setup((url) =>
      url.endsWith("/storage-deployment-options")
        ? json({ ...options, available: false, reason })
        : undefined,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Review current storage deployment" }),
    );
    await screen.findByRole("alert");
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(
      fetch.mock.calls.filter(([, init]) => init.method === "POST"),
    ).toHaveLength(0);
  },
);
test("authority denial retires review rather than retaining a retry under changed access", async () => {
  const { denied } = setup((url) =>
    url.endsWith("/deployments") ? json({}, 403) : undefined,
  );
  await review();
  fireEvent.click(
    screen.getByRole("button", { name: "Deploy reviewed image with uploads" }),
  );
  await vi.waitFor(() => expect(denied).toHaveBeenCalledOnce());
});
test("unmount aborts outstanding advisory reads and prevents a late review", async () => {
  let signal: AbortSignal | undefined;
  let resolve!: (response: Response) => void;
  const { unmount, selected } = setup((url, init) =>
    url.endsWith("/storage-deployment-options")
      ? ((signal = init.signal!),
        new Promise<Response>((done) => {
          resolve = done;
        }))
      : undefined,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Review current storage deployment" }),
  );
  unmount();
  expect(signal?.aborted).toBe(true);
  await act(async () => resolve(json(options)));
  expect(selected).not.toHaveBeenCalled();
});
test.each([
  ["reserved", "Proxy continuity is pending"],
  ["proxy_pending", "Proxy continuity is pending"],
  ["unresolved", "Continuity is unresolved"],
  ["proxy_failed", "Proxy continuity failed"],
  ["loaded", "Historical loaded receipt"],
  ["released", "Continuity reservation released"],
])(
  "%s continuity remains separate from workload and present health",
  async (state, text) => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        json({
          resource_id: "resource",
          operation_id: "hop",
          state,
          reason: state === "loaded" ? "loaded_historical" : "pending",
          ...(state === "loaded" ? { loaded_generation: 8 } : {}),
        }),
      ),
    );
    client = new QueryClient();
    render(
      <QueryClientProvider client={client}>
        <StorageContinuityDetail scope={scope} operation="hop" />
      </QueryClientProvider>,
    );
    await screen.findByText(new RegExp(text));
    expect(
      screen.getByText(
        "Workload completion and proxy continuity are separate results.",
      ),
    ).toBeTruthy();
    if (state === "loaded")
      expect(screen.getByText(/does not confirm current routes/)).toBeTruthy();
  },
);

test("catalog first deployment prefills image settings and pins saved runtime generation", async () => {
  const { fetch, selected } = setup((url, init) => {
    if (url.endsWith("/catalog-installation")) return json({
      id: "resource", resource: { id: "resource", project_id: "project" },
      binding: { resource_id: "resource" },
      template: { definition: { container_port: 9090, readiness: { path: "/health", timeout_seconds: 120 } } },
    });
    if (url.endsWith("/runtime-configuration")) return json({ runtime_configuration: {
      project_id: "project", resource_id: "resource", runtime_generation: 1,
    } });
    if (url.endsWith("/deployments") && init.method === "POST") {
      const input=JSON.parse(init.body as string);
      return json({id:"hop",binding_id:"binding",project_id:"project",resource_id:"resource",snapshot:{resource_generation:1,host_port:input.host_port,container_port:input.container_port,readiness:input.readiness,storage:options.storage_ref,runtime_configuration:{generation:1,hash:"runtime"}}},202);
    }
    return undefined;
  }, initialOptions);
  await vi.waitFor(() => expect(screen.getByLabelText<HTMLInputElement>("Container port").value).toBe("9090"));
  expect(screen.getByLabelText<HTMLInputElement>("Readiness path").value).toBe("/health");
  expect(screen.getByLabelText<HTMLInputElement>("Readiness timeout in seconds").value).toBe("120");
  fireEvent.change(screen.getByLabelText("Runner-local host port"),{target:{value:"20101"}});
  fireEvent.submit(screen.getByRole("form",{name:"Configure first storage deployment"}));
  await screen.findByRole("button",{name:"Deploy reviewed image with uploads"});
  fireEvent.click(screen.getByLabelText(/I reviewed this exact image/));
  fireEvent.click(screen.getByRole("button",{name:"Deploy reviewed image with uploads"}));
  await vi.waitFor(()=>expect(selected).toHaveBeenCalledWith("hop"));
  const body=JSON.parse(fetch.mock.calls.find(([,init])=>init.method==="POST")![1].body as string);
  expect(body).toMatchObject({expected_runtime_configuration_generation:1,container_port:9090,host_port:20101,readiness:{path:"/health",timeout_seconds:120}});
});

test("stored image review and identical retry pin the selected registry credential version", async () => {
  let count = 0;
  const { fetch, selected } = setup((url) => {
    if (url.includes("managed-secrets")) return json({ items: url.includes("versions") ? [{ secret_id: "credential", version: 1 }] : [{ id: "credential", name: "Private registry", project_id: "project", status: "active" }], limit: 20, offset: 0, count: 1, total: 1 });
    if (url.endsWith("deployments") && ++count === 1) throw new TypeError("lost");
  });
  fireEvent.change(screen.getByLabelText("Image access"), { target: { value: "private" } });
  await screen.findByRole("option", { name: "Private registry · credential" });
  fireEvent.change(screen.getByLabelText("Registry credential secret"), { target: { value: "credential" } });
  await screen.findByRole("option", { name: "Version 1" });
  fireEvent.change(screen.getByLabelText("Registry credential version"), { target: { value: "1" } });
  await review();
  expect(screen.getByLabelText<HTMLSelectElement>("Image access").disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Deploy reviewed image with uploads" }));
  fireEvent.click(await screen.findByRole("button", { name: "Retry identical storage deployment request" }));
  await vi.waitFor(() => expect(selected).toHaveBeenCalledWith("hop"));
  const calls = fetch.mock.calls.filter(([, init]) => init.method === "POST");
  expect(calls).toHaveLength(2);
  expect(calls[0]![1].body).toBe(calls[1]![1].body);
  expect(JSON.parse(calls[0]![1].body as string).registry_credential).toEqual({ secret_id: "credential", version: 1 });
  expect(fetch.mock.calls.some(([url]) => url.includes("reveal"))).toBe(false);
});
