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
) {
  const fetch = vi.fn(async (url: string, init: RequestInit) => {
    const response = await handler?.(url, init);
    if (response) return response;
    if (url.endsWith("/storage-deployment-options")) return json(options);
    if (url.endsWith("/runtime-configuration"))
      return json({
        runtime_configuration: {
          project_id: "project",
          resource_id: "resource",
          runtime_generation: 0,
        },
      });
    return json(
      {
        id: "hop",
        binding_id: "binding",
        project_id: "project",
        resource_id: "resource",
        snapshot: {
          resource_generation: 1,
          host_port: 8080,
          container_port: 80,
          readiness: options.readiness,
          storage: options.storage_ref,
          storage_route_continuity: { hash: "continuity-hash" },
        },
      },
      202,
    );
  });
  vi.stubGlobal("fetch", fetch);
  const selected = vi.fn(),
    denied = vi.fn();
  client = new QueryClient();
  const view = render(
    <QueryClientProvider client={client}>
      <StorageDeploymentReview
        bindingID="binding"
        scope={scope}
        resource={resource}
        options={options}
        blocked={false}
        onDenied={denied}
        onSelected={selected}
      />
    </QueryClientProvider>,
  );
  return { fetch, selected, denied, ...view };
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
test("review freezes exact fixed port, full route selection and key before explicit submission", async () => {
  const { fetch, selected } = setup();
  await review();
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST"),
  ).toHaveLength(0);
  expect(screen.getByText(/uploads.example.test/)).toBeTruthy();
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
      vi
        .fn()
        .mockResolvedValue(
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
