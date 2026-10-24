import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { queryKeys } from "@/api";
import { ApiError } from "@/api/errors";
import { DatabaseCreateForm } from "./DatabaseCreateForm";
let client: QueryClient;
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "X-Request-ID": "request-server" },
  });
const page = (items: unknown[], offset = 0, total = items.length) => ({
  items,
  offset,
  total,
  count: items.length,
  limit: 20,
});
function response(body: Record<string, string>) {
  return {
    resource: {
      id: "db",
      project_id: body.project_id,
      project_environment_id: body.project_environment_id,
      name: body.name,
      kind: "database",
      source: { kind: "image", image: { reference: "postgres:17" } },
      generation: 1,
      created_by: "owner",
      created_at: "",
      updated_at: "",
    },
    binding: {
      id: "binding",
      resource_id: "db",
      destination_id: body.destination_id,
      runtime_namespace: "ncd_db",
      created_by: "owner",
      created_at: "",
    },
    storage_ref: { storage_id: "storage", generation: 1, hash: "hash" },
    engine: body.engine,
    recipe_version: body.recipe_version,
    credentials:
      body.engine === "postgresql"
        ? [
            {
              role: "admin",
              username: "postgres",
              secret_id: "admin_secret",
              version: 1,
            },
            {
              role: "app",
              username: "ncd_app",
              secret_id: "app_secret",
              version: 1,
            },
          ]
        : [
            {
              role: "app",
              username: "ncd_app",
              secret_id: "app_secret",
              version: 1,
            },
            {
              role: "probe",
              username: "ncd_probe",
              secret_id: "probe_secret",
              version: 1,
            },
          ],
    runtime_state: "adapter_unqualified",
  };
}
function setup(
  role = "owner",
  handler?: (
    url: string,
    init: RequestInit,
  ) => Response | Promise<Response> | undefined,
) {
  client = new QueryClient({
    defaultOptions: { queries: { staleTime: Infinity, retry: false } },
  });
  client.setQueryData(queryKeys.principal, {
    id: "owner",
    provider: "oidc",
    roles: [],
  });
  client.setQueryData(queryKeys.allProjectMembers, [
    { user_id: "owner", project_id: "project", role },
  ]);
  const fetch = vi.fn(async (url: string, init: RequestInit) => {
    const custom = await handler?.(url, init);
    if (custom) return custom;
    if (url.startsWith("/api/v1/destinations")) {
      const offset = Number(
        new URL(url, "https://example.test").searchParams.get("offset"),
      );
      return json(
        page(
          [
            {
              id: offset ? "dest-two" : "dest",
              name: offset ? "Second destination" : "First destination",
              server_id: "server",
            },
          ],
          offset,
          21,
        ),
      );
    }
    if (url === "/api/v1/databases")
      return json(response(JSON.parse(init.body as string)), 201);
    throw new Error(`unexpected ${url}`);
  });
  vi.stubGlobal("fetch", fetch);
  const view = render(
    <QueryClientProvider client={client}>
      <DatabaseCreateForm projectID="project" environmentID="env" />
    </QueryClientProvider>,
  );
  return { fetch, ...view };
}
async function draft() {
  await screen.findByText(/First destination/);
  fireEvent.change(screen.getByLabelText("Database name"), {
    target: { value: "Data" },
  });
  fireEvent.change(screen.getByLabelText("Database destination"), {
    target: { value: "dest" },
  });
}
afterEach(() => {
  cleanup();
  client?.clear();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
test.each(["maintainer", "viewer", "admin"])(
  "%s cannot create database metadata",
  (role) => {
    const { fetch } = setup(role);
    expect(screen.queryByRole("form")).toBeNull();
    expect(screen.getByText(/Project owner access/)).toBeTruthy();
    expect(fetch).not.toHaveBeenCalled();
  },
);
test("owner chooses paged project-granted destination and receives metadata immediately without a first-page refetch", async () => {
  const { fetch } = setup();
  await draft();
  fireEvent.click(
    screen.getByRole("button", { name: "Next database destinations" }),
  );
  await screen.findByText(/Second destination/);
  fireEvent.change(screen.getByLabelText("Database destination"), {
    target: { value: "dest-two" },
  });
  fireEvent.change(screen.getByLabelText("Database engine"), {
    target: { value: "redis" },
  });
  vi.spyOn(client, "invalidateQueries").mockImplementation(
    () => new Promise(() => {}),
  );
  fireEvent.submit(screen.getByRole("form"));
  await screen.findByRole("region", {
    name: "Saved database metadata for Data",
  });
  const writes = fetch.mock.calls.filter(([, init]) => init.method === "POST");
  expect(writes).toHaveLength(1);
  expect(JSON.parse(writes[0]![1].body as string)).toMatchObject({
    destination_id: "dest-two",
    engine: "redis",
    recipe_version: "redis-8.2.10-bookworm.v1",
    project_id: "project",
    project_environment_id: "env",
  });
  expect(fetch.mock.calls[1]![0]).toContain("project_id=project");
  expect(fetch.mock.calls[1]![0]).toContain("offset=20");
  expect(client.invalidateQueries).toHaveBeenCalledWith(
    { queryKey: ["hostingResources", "project"] },
    { throwOnError: true },
  );
  expect(
    screen.queryByRole("button", { name: "Saving database metadata..." }),
  ).toBeNull();
});
test.each(["lost", 400, 404, 409, 503])(
  "%s freezes exact body and request identity until explicit retry or draft change",
  async (failure) => {
    let first = true;
    const { fetch } = setup("owner", (url) => {
      if (url === "/api/v1/databases" && first) {
        first = false;
        if (failure === "lost") throw new TypeError("lost");
        return json({ error: "failed" }, Number(failure));
      }
      return undefined;
    });
    await draft();
    fireEvent.submit(screen.getByRole("form"));
    await screen.findByRole("button", {
      name: "Retry identical database request",
    });
    expect(
      screen.getByLabelText<HTMLInputElement>("Database name").disabled,
    ).toBe(true);
    fireEvent.submit(screen.getByRole("form"));
    await screen.findByRole("region", {
      name: "Saved database metadata for Data",
    });
    const writes = fetch.mock.calls.filter(
      ([, init]) => init.method === "POST",
    );
    expect(writes).toHaveLength(2);
    expect(writes[0]![1].body).toBe(writes[1]![1].body);
    expect(JSON.parse(writes[0]![1].body as string).request_id).toMatch(
      /^[a-f0-9]{32}$/,
    );
  },
);
test.each([401, 403])(
  "HTTP%s retires caches and actions while retaining request identity and error",
  async (status) => {
    const { fetch } = setup("owner", (url) =>
      url === "/api/v1/databases"
        ? json({ error: "denied" }, status)
        : undefined,
    );
    await draft();
    fireEvent.submit(screen.getByRole("form"));
    await screen.findByText(/Database creation access is unavailable/);
    const body = JSON.parse(
      fetch.mock.calls.find(([, init]) => init.method === "POST")![1]
        .body as string,
    );
    expect(screen.getByRole("alert").textContent).toContain(body.request_id);
    expect(screen.getByRole("alert").textContent).toContain("request-server");
    expect(screen.queryByRole("form")).toBeNull();
    expect(
      client.getQueryCache().findAll({ queryKey: ["databaseDestinations"] }),
    ).toHaveLength(0);
  },
);
test("explicit draft change abandons the old request and rapid submits cannot create two intents", async () => {
  let resolve!: (response: Response) => void;
  const { fetch } = setup("owner", (url) =>
    url === "/api/v1/databases"
      ? new Promise<Response>((done) => {
          resolve = done;
        })
      : undefined,
  );
  await draft();
  const form = screen.getByRole("form");
  fireEvent.submit(form);
  fireEvent.submit(form);
  await waitFor(() =>
    expect(
      fetch.mock.calls.filter(([, init]) => init.method === "POST"),
    ).toHaveLength(1),
  );
  await act(async () => resolve(json({}, 503)));
  const first = fetch.mock.calls.find(([, init]) => init.method === "POST")![1]
    .body;
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Change draft and abandon retained request",
    }),
  );
  fireEvent.change(screen.getByLabelText("Database name"), {
    target: { value: "Changed" },
  });
  fireEvent.submit(form);
  await waitFor(() =>
    expect(
      fetch.mock.calls.filter(([, init]) => init.method === "POST"),
    ).toHaveLength(2),
  );
  expect(
    fetch.mock.calls.filter(([, init]) => init.method === "POST")[1]![1].body,
  ).not.toBe(first);
});
test.each(["unmount", "environment", "actor", "permission"])(
  "%s fences late create responses",
  async (change) => {
    let resolve!: (response: Response) => void;
    let signal: AbortSignal | null | undefined;
    const { fetch, unmount, rerender } = setup("owner", (url, init) =>
      url === "/api/v1/databases"
        ? ((signal = init.signal),
          new Promise<Response>((done) => {
            resolve = done;
          }))
        : undefined,
    );
    await draft();
    fireEvent.submit(screen.getByRole("form"));
    await waitFor(() => expect(resolve).toBeTypeOf("function"));
    const body = JSON.parse(
      fetch.mock.calls.find(([, init]) => init.method === "POST")![1]
        .body as string,
    );
    if (change === "unmount") unmount();
    if (change === "environment")
      rerender(
        <QueryClientProvider client={client}>
          <DatabaseCreateForm projectID="project" environmentID="other" />
        </QueryClientProvider>,
      );
    if (change === "permission") {
      await act(async () => {
        client.setQueryData(queryKeys.allProjectMembers, [
          { user_id: "owner", project_id: "project", role: "maintainer" },
        ]);
      });
      await waitFor(() => expect(screen.queryByRole("form")).toBeNull());
    }
    if (change === "actor") {
      await act(async () => {
        client.setQueryData(queryKeys.principal, {
          id: "different-user", provider: "oidc", roles: [],
        });
      });
      await waitFor(() => expect(screen.queryByRole("form")).toBeNull());
    }
    expect(signal?.aborted).toBe(true);
    await act(async () => resolve(json(response(body), 201)));
    expect(
      screen.queryByRole("region", {
        name: "Saved database metadata for Data",
      }),
    ).toBeNull();
  },
);

test("a timed-out create preserves the frozen identity for an exact retry", async () => {
  const { fetch } = setup("owner", (url) => url === "/api/v1/databases"
    ? new Promise<Response>(() => {}) : undefined);
  await draft();
  vi.useFakeTimers();
  fireEvent.submit(screen.getByRole("form"));
  await act(async () => { await vi.advanceTimersByTimeAsync(30_001); });
  expect(screen.getByRole("button", { name: "Retry identical database request" }).hasAttribute("disabled")).toBe(false);
  const first = fetch.mock.calls.find(([, init]) => init.method === "POST")![1].body;
  fireEvent.submit(screen.getByRole("form"));
  await act(async () => { await Promise.resolve(); });
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")[1]![1].body).toBe(first);
});

test.each([401, 403])("a later resource-list %s masks saved metadata and retains its request identity", async (status) => {
  const { fetch, rerender } = setup();
  await draft();
  fireEvent.submit(screen.getByRole("form"));
  await screen.findByRole("region", { name: "Saved database metadata for Data" });
  const identity = JSON.parse(fetch.mock.calls.find(([, init]) => init.method === "POST")![1].body as string).request_id;
  rerender(<QueryClientProvider client={client}><DatabaseCreateForm projectID="project" environmentID="env" accessError={new ApiError(status, "unsafe remote detail", "server-request")} /></QueryClientProvider>);
  await waitFor(() => expect(screen.queryByRole("form")).toBeNull());
  expect(screen.queryByRole("region", { name: "Saved database metadata for Data" })).toBeNull();
  expect(screen.getByRole("alert").textContent).toContain(identity);
  expect(screen.getByRole("alert").textContent).not.toContain("unsafe remote detail");
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(1);
});

test("shared authority refresh pauses creation without discarding its accepted receipt", async () => {
  const { fetch } = setup();
  await draft();
  fireEvent.submit(screen.getByRole("form"));
  await screen.findByRole("region", { name: "Saved database metadata for Data" });
  const identity = JSON.parse(fetch.mock.calls.find(([, init]) => init.method === "POST")![1].body as string).request_id;
  let resolve!: (value: unknown) => void;
  let reading!: Promise<unknown>;
  await act(async () => {
    reading = client.fetchQuery({ queryKey: queryKeys.principal, staleTime: 0,
      queryFn: () => new Promise(done => { resolve = done; }) });
  });
  await screen.findByText(/Refreshing database authority/);
  expect(screen.queryByRole("region", { name: "Saved database metadata for Data" })).toBeNull();
  expect(screen.getByText(new RegExp(`Saved request ID: ${identity}`))).toBeTruthy();
  await act(async () => {
    resolve({ id: "owner", provider: "oidc", roles: [] });
    await reading;
  });
  await screen.findByRole("region", { name: "Saved database metadata for Data" });
  expect(screen.getByText(new RegExp(`Saved request ID: ${identity}`))).toBeTruthy();
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(1);
});

test.each([401, 403, 503])("shared authority %s preserves safe identity and fences protected metadata", async (status) => {
  const { fetch } = setup();
  await draft();
  fireEvent.submit(screen.getByRole("form"));
  await screen.findByRole("region", { name: "Saved database metadata for Data" });
  const identity = JSON.parse(fetch.mock.calls.find(([, init]) => init.method === "POST")![1].body as string).request_id;
  fireEvent.change(screen.getByLabelText("Database name"), { target: { value: "Next database" } });
  await act(async () => {
    await client.fetchQuery({ queryKey: queryKeys.principal, staleTime: 0, retry: false,
      queryFn: async () => { throw new ApiError(status, "unsafe upstream detail", "authority-request"); },
    }).catch(() => {});
  });
  await waitFor(() => expect(screen.queryByRole("region", { name: "Saved database metadata for Data" })).toBeNull());
  expect(document.body.textContent).toContain(identity);
  expect(document.body.textContent).not.toContain("unsafe upstream detail");
  if (status === 503) {
    expect((screen.getByLabelText("Database name") as HTMLInputElement).value).toBe("Next database");
    expect(screen.getByRole("button", { name: "Save database configuration" }).hasAttribute("disabled")).toBe(true);
    await act(async () => { client.setQueryData(queryKeys.principal, { id: "owner", provider: "oidc", roles: [] }); });
    await screen.findByRole("region", { name: "Saved database metadata for Data" });
  } else await waitFor(() => expect(screen.queryByRole("form")).toBeNull());
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(1);
});

test.each((["membership", "resource"] as const).flatMap(source =>
  [401, 403].flatMap(status => ["ordinary-first", "denial-first"].map(order => ({ source, status, order }))),
))(
  "$source $status outranks principal 503 ($order) and permanently fences a pending intent",
  async ({ source, status, order }) => {
    let resolve!: (value: Response) => void;
    let signal: AbortSignal | null | undefined;
    const { fetch, rerender } = setup("owner", (url, init) =>
      url === "/api/v1/databases"
        ? ((signal = init.signal), new Promise<Response>((done) => { resolve = done; }))
        : undefined,
    );
    await draft();
    fireEvent.submit(screen.getByRole("form"));
    await waitFor(() => expect(resolve).toBeTypeOf("function"));
    const body = JSON.parse(fetch.mock.calls.find(([, init]) => init.method === "POST")![1].body as string);
    const protectedKey = ["hostingDatabase", JSON.stringify(["oidc", "owner"]), "project", "env", "existing"];
    client.setQueryData(protectedKey, response(body));
    const ordinaryFailure = () => act(async () => {
      await client.fetchQuery({ queryKey: queryKeys.principal, staleTime: 0, retry: false,
        queryFn: async () => { throw new ApiError(503, "ordinary failure", "principal-error"); },
      }).catch(() => {});
    });
    const denial = async () => {
      if (source === "membership") await act(async () => {
        await client.fetchQuery({ queryKey: queryKeys.allProjectMembers, staleTime: 0, retry: false,
          queryFn: async () => { throw new ApiError(status, "denied", "member-error"); },
        }).catch(() => {});
      });
      else rerender(<QueryClientProvider client={client}><DatabaseCreateForm projectID="project" environmentID="env" accessError={new ApiError(status, "denied", "resource-error")} /></QueryClientProvider>);
    };
    if (order === "ordinary-first") { await ordinaryFailure(); await denial(); }
    else { await denial(); await ordinaryFailure(); }
    await waitFor(() => expect(screen.queryByRole("form")).toBeNull());
    expect(signal?.aborted).toBe(true);
    expect(client.getQueryData(protectedKey)).toBeUndefined();
    expect(screen.getByRole("alert").textContent).toContain(body.request_id);
    // The denied source recovering first must not revive the retained intent.
    await act(async () => {
      if (source === "membership") client.setQueryData(queryKeys.allProjectMembers, [{ user_id: "owner", project_id: "project", role: "owner" }]);
    });
    if (source === "resource") rerender(<QueryClientProvider client={client}><DatabaseCreateForm projectID="project" environmentID="env" /></QueryClientProvider>);
    await act(async () => {
      resolve(json(response(body), 201));
      client.setQueryData(queryKeys.principal, { id: "owner", provider: "oidc", roles: [] });
    });
    expect(screen.queryByRole("form")).toBeNull();
    expect(screen.queryByRole("region", { name: "Saved database metadata for Data" })).toBeNull();
    expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(1);
    expect(fetch.mock.calls.filter(([url]) => url !== "/api/v1/databases")).toHaveLength(1);
  },
);

test("a 503-only authority refresh preserves an uncertain intent for exact retry", async () => {
  let first = true;
  const { fetch } = setup("owner", (url) => {
    if (url === "/api/v1/databases" && first) { first = false; throw new TypeError("lost response"); }
    return undefined;
  });
  await draft();
  fireEvent.submit(screen.getByRole("form"));
  await screen.findByRole("button", { name: "Retry identical database request" });
  const body = fetch.mock.calls.find(([, init]) => init.method === "POST")![1].body;
  await act(async () => {
    await client.fetchQuery({ queryKey: queryKeys.principal, staleTime: 0, retry: false,
      queryFn: async () => { throw new ApiError(503, "ordinary failure", "principal-error"); },
    }).catch(() => {});
  });
  expect((screen.getByLabelText("Database name") as HTMLInputElement).value).toBe("Data");
  await waitFor(() => expect(screen.getByRole("button", { name: "Retry identical database request" }).hasAttribute("disabled")).toBe(true));
  await act(async () => { client.setQueryData(queryKeys.principal, { id: "owner", provider: "oidc", roles: [] }); });
  await waitFor(() => expect(screen.getByRole("button", { name: "Retry identical database request" }).hasAttribute("disabled")).toBe(false));
  fireEvent.submit(screen.getByRole("form"));
  await screen.findByRole("region", { name: "Saved database metadata for Data" });
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")[1]![1].body).toBe(body);
});
