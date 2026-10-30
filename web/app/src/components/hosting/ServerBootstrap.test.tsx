import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { ServerBootstrap } from "./ServerBootstrap";
import type { ServerBootstrap as Operation } from "@/api/server-bootstrap";

vi.mock("@/pages/ServerReadiness", () => ({
  ServerReadiness: ({ serverID }: { serverID: string }) => (
    <div>Readiness for {serverID}</div>
  ),
}));
const release = {
  artifact_id: "release",
  version: "v1.2.3",
  architecture: "arm64" as const,
  size: 1234,
  sha256: "a".repeat(64),
  profile: "prepared_debian12_v1" as const,
};
const principal = {
  id: "admin",
  provider: "local",
  name: "Admin",
  roles: ["runner_admin"],
};
const page = <T,>(items: T[]) => ({
  items,
  limit: 20,
  offset: 0,
  total: items.length,
  count: items.length,
  has_more: false,
});
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status });
const fresh = () => new Date(Date.now() + 30000).toISOString();
function operation(): Operation {
  return {
    id: "op",
    project_id: "project",
    name: "Production host",
    target: {
      ip: "192.0.2.10",
      port: 22,
      user: "root",
      host_fingerprint: "SHA256:verified",
    },
    artifact_id: "release",
    artifact: release,
    state: "draft",
    version: 1,
    remote_hold: false,
    restore_held: false,
    created_at: "",
    progress: [],
    available_actions: ["probe", "cancel"],
    readiness_state: "not_registered",
  };
}
function probed(op: Operation): Operation {
  return {
    ...op,
    version: op.version + 1,
    available_actions: ["probe", "review", "cancel"],
    probe: {
      receipt_id: "receipt",
      observed_at: new Date().toISOString(),
      expires_at: fresh(),
      prerequisites: {
        profile: "prepared_debian12_v1",
        os_state: "qualified",
        os_id: "debian",
        os_version: "12",
        architecture: "arm64",
        systemd_state: "qualified",
        systemd_status: "running",
        docker_state: "qualified",
        docker_api_version: "1.54",
        compose_state: "qualified",
        compose_version: "v2.39.1",
        disk_state: "qualified",
        disk_total_bytes: 100000,
        disk_available_bytes: 10000,
      },
    },
  };
}
function reviewed(op = probed(operation())): Operation {
  return {
    ...op,
    version: op.version + 1,
    state: "reviewed",
    available_actions: ["probe", "review", "install", "cancel"],
    review: {
      manifest_sha256: "b".repeat(64),
      probe_receipt_id: "receipt",
      expires_at: fresh(),
    },
  };
}
let client: QueryClient;
afterEach(() => {
  cleanup();
  client?.clear();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
type Handler = (
  url: string,
  options: RequestInit,
) => Promise<Response> | Response | undefined;
function setup(handler?: Handler, roles = principal.roles) {
  const fetch = vi.fn(async (url: string, options: RequestInit) => {
    const custom = await handler?.(url, options);
    if (custom) return custom;
    if (url === "/api/v1/me") return json({ ...principal, roles });
    if (url.includes("/projects?"))
      return json(
        page([
          { id: "project", name: "Shop", description: "", created_at: "" },
          {
            id: "second",
            name: "Second project",
            description: "",
            created_at: "",
          },
        ]),
      );
    if (url.includes("server-bootstrap-options"))
      return json({
        available: true,
        unavailable_reason: "",
        profile: "prepared_debian12_v1",
        preset: "hosting_image_v1",
        artifacts: [release],
      });
    if (url.includes("/managed-secrets/key/versions"))
      return json(
        page([
          { secret_id: "key", version: 2, created_at: "", created_by: "admin" },
        ]),
      );
    if (url.includes("/managed-secrets?"))
      return json(
        page([
          {
            id: "key",
            project_id: new URL(url, "http://local").searchParams.get(
              "project_id",
            ),
            name: "Production SSH key",
            current_version: 2,
            status: "active",
          },
          {
            id: "revoked",
            project_id: new URL(url, "http://local").searchParams.get(
              "project_id",
            ),
            name: "Old key",
            current_version: 1,
            status: "revoked",
          },
        ]),
      );
    if (url.includes("/server-bootstraps?")) return json(page([]));
    throw new Error(`Unexpected test request ${url}`);
  });
  vi.stubGlobal("fetch", fetch);
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  const view = render(
    <QueryClientProvider client={client}>
      <div data-testid="bootstrap-task"><ServerBootstrap blocked={false} /></div>
    </QueryClientProvider>,
  );
  return { fetch, ...view };
}
async function chooseProject() {
  await screen.findByRole("option", { name: "Shop" });
  fireEvent.change(screen.getByLabelText("Project for server onboarding"), {
    target: { value: "project" },
  });
  await screen.findByRole("option", { name: "Production SSH key" });
}
async function prepare() {
  await chooseProject();
  fireEvent.change(screen.getByLabelText("Server name"), {
    target: { value: "Production host" },
  });
  fireEvent.change(screen.getByLabelText("Literal server IP address"), {
    target: { value: "192.0.2.10" },
  });
  fireEvent.change(screen.getByLabelText("Verified SSH host fingerprint"), {
    target: { value: "SHA256:verified" },
  });
  fireEvent.change(screen.getByLabelText("Managed SSH key"), {
    target: { value: "key" },
  });
  await screen.findByRole("option", { name: "Version 2 · current" });
  fireEvent.change(screen.getByLabelText("SSH key version"), {
    target: { value: "2" },
  });
  fireEvent.change(screen.getByLabelText("Qualified runner release"), {
    target: { value: "release" },
  });
  fireEvent.submit(
    screen.getByRole("form", { name: "Prepare SSH server onboarding" }),
  );
  await screen.findByRole("region", { name: "Current server onboarding" });
  await waitFor(() =>
    expect(
      screen.getByRole<HTMLButtonElement>("button", {
        name: "Probe host prerequisites",
      }).disabled,
    ).toBe(false),
  );
}
function writes(fetch: ReturnType<typeof vi.fn>) {
  return fetch.mock.calls.filter(([, options]) => options.method === "POST");
}
test.each([false, true])(
  "successful onboarding requires explicit grant choice %s and reaches readiness",
  async (grant) => {
    let op = operation();
    const { fetch } = setup((url, options) => {
      if (url === "/api/v1/server-bootstraps" && options.method === "POST")
        return json(op);
      if (url === "/api/v1/server-bootstraps/op") return json(op);
      if (options.method === "POST" && url.includes("/op/")) {
        if (url.endsWith("/probe")) op = probed(op);
        if (url.endsWith("/review")) op = reviewed(op);
        if (url.endsWith("/install"))
          op = {
            ...op,
            version: op.version + 1,
            state: "installed_waiting_enrollment",
            available_actions: ["inspect"],
          };
        if (url.endsWith("/inspect"))
          op = {
            ...op,
            version: op.version + 1,
            available_actions: ["register"],
          };
        if (url.endsWith("/register"))
          op = {
            ...op,
            version: op.version + 1,
            state: grant ? "registered" : "completed",
            available_actions: grant ? ["grant"] : [],
            server_id: "server",
            readiness_state: "check_server_readiness",
          };
        if (url.endsWith("/grant"))
          op = {
            ...op,
            version: op.version + 1,
            state: "completed",
            available_actions: [],
          };
        return json(op);
      }
    });
    await prepare();
    fireEvent.click(
      screen.getByRole("button", { name: "Probe host prerequisites" }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Review planned install" }),
    );
    await screen.findByRole("region", { name: "Reviewed install plan" });
    const install = screen.getByRole<HTMLButtonElement>("button", {
      name: "Install and start runner",
    });
    expect(install.disabled).toBe(true);
    fireEvent.click(
      screen.getByLabelText(
        "I approve installing and starting this runner on the reviewed host.",
      ),
    );
    expect(install.disabled).toBe(true);
    fireEvent.change(
      screen.getByLabelText("Project access after registration"),
      { target: { value: grant ? "yes" : "no" } },
    );
    await waitFor(() => expect(install.disabled).toBe(false));
    fireEvent.click(install);
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Inspect installation and enrollment",
      }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Register enrolled server" }),
    );
    if (grant) {
      fireEvent.click(
        await screen.findByRole("button", {
          name: "Apply chosen project grant",
        }),
      );
      await screen.findByText("Onboarding state: completed");
    }
    await screen.findByText("Readiness for server");
    const installWrite = writes(fetch).find(([url]) =>
      url.endsWith("/install"),
    );
    expect(JSON.parse(installWrite![1].body)).toMatchObject({
      expected_version: 3,
      manifest_sha256: "b".repeat(64),
      acknowledge_install_and_start: true,
      grant_project: grant,
    });
    expect(fetch.mock.calls.some(([url]) => url.includes("reveal"))).toBe(
      false,
    );
    expect(document.body.textContent).not.toContain("PRIVATE KEY");
  },
);
test("response loss preserves identical install body, UUID, reviewed version and false grant despite polling", async () => {
  let op = reviewed(),
    lost = true;
  const { fetch } = setup((url, options) => {
    if (url.includes("/server-bootstraps?")) return json(page([op]));
    if (url === "/api/v1/server-bootstraps/op") return json(op);
    if (url.endsWith("/install")) {
      op = { ...op, version: 10, state: "running", available_actions: [] };
      if (lost) {
        lost = false;
        throw new TypeError("response lost");
      }
      return json(op);
    }
  });
  await chooseProject();
  fireEvent.click(
    await screen.findByRole("button", { name: /Production host ·/ }),
  );
  await screen.findByRole("region", { name: "Reviewed install plan" });
  fireEvent.change(screen.getByLabelText("Project access after registration"), {
    target: { value: "no" },
  });
  fireEvent.click(
    screen.getByLabelText(
      "I approve installing and starting this runner on the reviewed host.",
    ),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Install and start runner" }),
  );
  await screen.findByText(/response was not confirmed/);
  screen.getByTestId("bootstrap-task").hidden = true;
  expect(screen.queryByRole("button", { name: "Retry exact onboarding request" })).toBeNull();
  screen.getByTestId("bootstrap-task").hidden = false;
  await act(async () => {
    await client.invalidateQueries({ queryKey: ["serverBootstrap"] });
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Retry exact onboarding request" }),
  );
  await waitFor(() => expect(writes(fetch)).toHaveLength(2));
  expect(writes(fetch)[0]![1].body).toBe(writes(fetch)[1]![1].body);
  expect(JSON.parse(writes(fetch)[1]![1].body)).toMatchObject({
    expected_version: 3,
    grant_project: false,
  });
});
test("response loss preserves identical create request and selections", async () => {
  let lost = true;
  const { fetch } = setup((url, options) => {
    if (url === "/api/v1/server-bootstraps" && options.method === "POST") {
      if (lost) {
        lost = false;
        throw new TypeError("lost");
      }
      return json(operation());
    }
    if (url === "/api/v1/server-bootstraps/op") return json(operation());
  });
  await chooseProject();
  fireEvent.change(screen.getByLabelText("Managed SSH key"), {
    target: { value: "key" },
  });
  await screen.findByRole("option", { name: "Version 2 · current" });
  fireEvent.change(screen.getByLabelText("SSH key version"), {
    target: { value: "2" },
  });
  fireEvent.change(screen.getByLabelText("Qualified runner release"), {
    target: { value: "release" },
  });
  fireEvent.submit(
    screen.getByRole("form", { name: "Prepare SSH server onboarding" }),
  );
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Retry exact onboarding request",
    }),
  );
  await waitFor(() => expect(writes(fetch)).toHaveLength(2));
  expect(writes(fetch)[0]![1].body).toBe(writes(fetch)[1]![1].body);
});
test("409 fetches fresh state, clears consent and requires a new probe/review", async () => {
  let op = reviewed();
  const { fetch } = setup((url, options) => {
    if (url.includes("/server-bootstraps?")) return json(page([op]));
    if (url === "/api/v1/server-bootstraps/op") return json(op);
    if (url.endsWith("/install") && options.method === "POST") {
      op = { ...op, version: 7 };
      return json({}, 409);
    }
  });
  await chooseProject();
  fireEvent.click(
    await screen.findByRole("button", { name: /Production host ·/ }),
  );
  await screen.findByRole("region", { name: "Reviewed install plan" });
  fireEvent.change(screen.getByLabelText("Project access after registration"), {
    target: { value: "yes" },
  });
  fireEvent.click(
    screen.getByLabelText(
      "I approve installing and starting this runner on the reviewed host.",
    ),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Install and start runner" }),
  );
  await screen.findByText(/operation changed or the probe/);
  expect(
    screen.queryByRole("button", { name: "Retry exact onboarding request" }),
  ).toBeNull();
  expect(
    screen.getByRole<HTMLButtonElement>("button", {
      name: "Install and start runner",
    }).disabled,
  ).toBe(true);
  expect(
    screen.getByLabelText<HTMLSelectElement>(
      "Project access after registration",
    ).value,
  ).toBe("");
  expect(
    fetch.mock.calls.filter(([url]) => url === "/api/v1/server-bootstraps/op")
      .length,
  ).toBeGreaterThanOrEqual(2);
});
test("reopened review uses saved release metadata, expires and requires fresh probe/review", async () => {
  let op = reviewed();
  op.artifact = { ...release, version: "v0.saved", sha256: "c".repeat(64) };
  op.review!.expires_at = new Date(Date.now() + 1000).toISOString();
  setup((url) => {
    if (url.endsWith("/probe")) {
      op = probed(op);
      return json(op);
    }
    if (url.endsWith("/review")) {
      op = reviewed(op);
      return json(op);
    }
    return url.includes("/server-bootstraps?")
      ? json(page([op]))
      : url === "/api/v1/server-bootstraps/op"
        ? json(op)
        : undefined;
  });
  await chooseProject();
  fireEvent.click(
    await screen.findByRole("button", { name: /Production host ·/ }),
  );
  const panel = await screen.findByRole("region", {
    name: "Current server onboarding",
  });
  expect(within(panel).getByText("v0.saved")).toBeTruthy();
  expect(within(panel).getByText("c".repeat(64))).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Project access after registration"), {
    target: { value: "no" },
  });
  fireEvent.click(
    screen.getByLabelText(
      "I approve installing and starting this runner on the reviewed host.",
    ),
  );
  await waitFor(
    () =>
      expect(
        screen.getByRole<HTMLButtonElement>("button", {
          name: "Install and start runner",
        }).disabled,
      ).toBe(true),
    { timeout: 2500 },
  );
  await screen.findByText(/Review expired or changed/);
  fireEvent.click(
    screen.getByRole("button", { name: "Probe host prerequisites" }),
  );
  await waitFor(() =>
    expect(
      screen.getByRole<HTMLButtonElement>("button", {
        name: "Review planned install",
      }).disabled,
    ).toBe(false),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Review planned install" }),
  );
  await waitFor(() =>
    expect(screen.queryByText(/Review expired or changed/)).toBeNull(),
  );
  expect(
    screen.getByRole<HTMLButtonElement>("button", {
      name: "Install and start runner",
    }).disabled,
  ).toBe(true);
  expect(
    screen.getByLabelText<HTMLSelectElement>(
      "Project access after registration",
    ).value,
  ).toBe("");
});
test("a refreshed review version requires new consent and grant choice", async () => {
  let op = reviewed();
  setup((url) =>
    url.includes("/server-bootstraps?")
      ? json(page([op]))
      : url === "/api/v1/server-bootstraps/op"
        ? json(op)
        : undefined,
  );
  await chooseProject();
  fireEvent.click(
    await screen.findByRole("button", { name: /Production host ·/ }),
  );
  await screen.findByRole("region", { name: "Reviewed install plan" });
  fireEvent.change(screen.getByLabelText("Project access after registration"), {
    target: { value: "yes" },
  });
  fireEvent.click(
    screen.getByLabelText(
      "I approve installing and starting this runner on the reviewed host.",
    ),
  );
  await waitFor(() =>
    expect(
      screen.getByRole<HTMLButtonElement>("button", {
        name: "Install and start runner",
      }).disabled,
    ).toBe(false),
  );
  op = {
    ...op,
    version: op.version + 1,
    review: { ...op.review!, manifest_sha256: "new digest" },
  };
  await act(async () => {
    await client.invalidateQueries({ queryKey: ["serverBootstrap"] });
  });
  await screen.findByText("Manifest SHA-256: new digest");
  expect(
    screen.getByLabelText<HTMLInputElement>(
      "I approve installing and starting this runner on the reviewed host.",
    ).checked,
  ).toBe(false);
  expect(
    screen.getByLabelText<HTMLSelectElement>(
      "Project access after registration",
    ).value,
  ).toBe("");
  expect(
    screen.getByRole<HTMLButtonElement>("button", {
      name: "Install and start runner",
    }).disabled,
  ).toBe(true);
});
test("viewer performs no project, secret or bootstrap requests", async () => {
  const { fetch } = setup(undefined, ["viewer"]);
  await screen.findByText(/requires runner administrator access/);
  expect(fetch.mock.calls.map(([url]) => url)).toEqual(["/api/v1/me"]);
});
test("unauthorized project clears operation evidence and never exposes server error text", async () => {
  setup((url) =>
    url.includes("server-bootstrap-options")
      ? json({ error: "PRIVATE KEY must never display" }, 403)
      : undefined,
  );
  await screen.findByRole("option", { name: "Shop" });
  fireEvent.change(screen.getByLabelText("Project for server onboarding"), {
    target: { value: "project" },
  });
  await screen.findByText(/Onboarding access is unavailable/);
  expect(
    screen.queryByRole("form", { name: "Prepare SSH server onboarding" }),
  ).toBeNull();
  expect(document.body.textContent).not.toContain("PRIVATE KEY");
});
test("disabled onboarding, revoked keys and missing qualified release show prerequisites", async () => {
  setup((url) =>
    url.includes("server-bootstrap-options")
      ? json({
          available: false,
          unavailable_reason: "durable_store_required",
          artifacts: [],
          profile: "prepared_debian12_v1",
          preset: "hosting_image_v1",
        })
      : undefined,
  );
  await chooseProject();
  await screen.findByText(/requires durable PostgreSQL/);
  expect(
    screen.getByRole<HTMLOptionElement>("option", { name: "Old key · revoked" })
      .disabled,
  ).toBe(true);
  expect(
    screen
      .getByRole<HTMLButtonElement>("button", { name: "Prepare onboarding" })
      .closest("fieldset")!.disabled,
  ).toBe(true);
  expect(screen.getByText(/Debian 12 arm64 host/)).toBeTruthy();
});
test("late response from a previous project cannot populate the new project", async () => {
  let resolve: (response: Response) => void = () => {};
  setup((url) =>
    url.includes("server-bootstraps?project_id=project")
      ? new Promise<Response>((done) => {
          resolve = done;
        })
      : undefined,
  );
  await chooseProject();
  fireEvent.change(screen.getByLabelText("Project for server onboarding"), {
    target: { value: "second" },
  });
  await screen.findByText("New server for Second project");
  await act(async () => {
    resolve(json(page([reviewed()])));
  });
  expect(
    screen.queryByRole("button", { name: /Production host ·/ }),
  ).toBeNull();
});
test("same-actor authority revalidation retains a saved request; actor change clears its scope", async () => {
  let lost = true,
    actor = "admin";
  const { fetch } = setup((url, options) => {
    if (url === "/api/v1/me") return json({ ...principal, id: actor });
    if (url === "/api/v1/server-bootstraps" && options.method === "POST") {
      if (lost) {
        lost = false;
        throw new TypeError("lost response");
      }
      return json(operation());
    }
    if (url === "/api/v1/server-bootstraps/op") return json(operation());
  });
  await chooseProject();
  fireEvent.change(screen.getByLabelText("Managed SSH key"), {
    target: { value: "key" },
  });
  await screen.findByRole("option", { name: "Version 2 · current" });
  fireEvent.change(screen.getByLabelText("SSH key version"), {
    target: { value: "2" },
  });
  fireEvent.change(screen.getByLabelText("Qualified runner release"), {
    target: { value: "release" },
  });
  fireEvent.submit(
    screen.getByRole("form", { name: "Prepare SSH server onboarding" }),
  );
  await screen.findByText(/response was not confirmed/);
  await act(async () => {
    await client.invalidateQueries({ queryKey: ["principal"] });
  });
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Retry exact onboarding request",
    }),
  );
  await screen.findByRole("region", { name: "Current server onboarding" });
  expect(writes(fetch)[0]![1].body).toBe(writes(fetch)[1]![1].body);
  actor = "new-admin";
  await act(async () => {
    await client.invalidateQueries({ queryKey: ["principal"] });
  });
  await waitFor(() =>
    expect(
      screen.queryByRole("region", { name: "Current server onboarding" }),
    ).toBeNull(),
  );
  expect(
    screen.getByLabelText<HTMLSelectElement>("Project for server onboarding")
      .value,
  ).toBe("");
});
test("actor change during a request rejects its late response", async () => {
  let actor = "admin";
  let resolve: (response: Response) => void = () => {};
  const { fetch } = setup((url, options) => {
    if (url === "/api/v1/me") return json({ ...principal, id: actor });
    if (url === "/api/v1/server-bootstraps" && options.method === "POST")
      return new Promise<Response>((done) => {
        resolve = done;
      });
  });
  await chooseProject();
  fireEvent.change(screen.getByLabelText("Managed SSH key"), {
    target: { value: "key" },
  });
  await screen.findByRole("option", { name: "Version 2 · current" });
  fireEvent.change(screen.getByLabelText("SSH key version"), {
    target: { value: "2" },
  });
  fireEvent.change(screen.getByLabelText("Qualified runner release"), {
    target: { value: "release" },
  });
  fireEvent.submit(
    screen.getByRole("form", { name: "Prepare SSH server onboarding" }),
  );
  await waitFor(() => expect(writes(fetch)).toHaveLength(1));
  expect(
    screen.getByLabelText<HTMLSelectElement>("Project for server onboarding")
      .disabled,
  ).toBe(true);
  actor = "new-admin";
  await act(async () => {
    await client.invalidateQueries({ queryKey: ["principal"] });
  });
  await waitFor(() =>
    expect(
      screen.getByLabelText<HTMLSelectElement>("Project for server onboarding")
        .value,
    ).toBe(""),
  );
  expect((writes(fetch)[0]![1].signal as AbortSignal).aborted).toBe(true);
  await act(async () => {
    resolve(json(operation()));
  });
  expect(
    screen.queryByRole("region", { name: "Current server onboarding" }),
  ).toBeNull();
  expect(
    client.getQueryData(["serverBootstrap", "local:admin", "project", "op"]),
  ).toBeUndefined();
});

test("same-actor read denial fences an older pending command success", async () => {
  let deny = false;
  let resolve: (response: Response) => void = () => {};
  const { fetch } = setup((url) => {
    if (url.includes("/server-bootstraps?")) return json(page([operation()]));
    if (url === "/api/v1/server-bootstraps/op")
      return deny ? json({}, 403) : json(operation());
    if (url.endsWith("/probe"))
      return new Promise<Response>((done) => {
        resolve = done;
      });
  });
  await chooseProject();
  fireEvent.click(
    await screen.findByRole("button", { name: /Production host ·/ }),
  );
  fireEvent.click(
    await screen.findByRole("button", { name: "Probe host prerequisites" }),
  );
  await waitFor(() => expect(writes(fetch)).toHaveLength(1));
  deny = true;
  await act(async () => {
    await client.invalidateQueries({ queryKey: ["serverBootstrap"] });
  });
  await screen.findByText(/Onboarding access is unavailable/);
  await act(async () => {
    resolve(json(probed(operation())));
  });
  expect(
    screen.queryByRole("region", { name: "Current server onboarding" }),
  ).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Retry exact onboarding request" }),
  ).toBeNull();
  expect(
    client.getQueryState(["serverBootstrap", "local:admin", "project", "op"])
      ?.status,
  ).toBe("error");
  expect((writes(fetch)[0]![1].signal as AbortSignal).aborted).toBe(true);
});

test("hung command releases exact retry and navigation, rejecting late success during retry", async () => {
  const pending: ((response: Response) => void)[] = [];
  const { fetch } = setup((url) => {
    if (url.includes("/server-bootstraps?")) return json(page([operation()]));
    if (url === "/api/v1/server-bootstraps/op") return json(operation());
    if (url.endsWith("/probe"))
      return new Promise<Response>((done) => {
        pending.push(done);
      });
  });
  await chooseProject();
  fireEvent.click(
    await screen.findByRole("button", { name: /Production host ·/ }),
  );
  await screen.findByRole("button", { name: "Probe host prerequisites" });
  vi.useFakeTimers();
  fireEvent.click(
    screen.getByRole("button", { name: "Probe host prerequisites" }),
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(180000);
  });
  const retry = screen.getByRole<HTMLButtonElement>("button", {
    name: "Retry exact onboarding request",
  });
  expect(retry.disabled).toBe(false);
  expect(
    screen.getByLabelText<HTMLSelectElement>("Project for server onboarding")
      .disabled,
  ).toBe(false);
  expect((writes(fetch)[0]![1].signal as AbortSignal).aborted).toBe(true);
  expect(screen.getByText(/does not cancel/)).toBeTruthy();
  fireEvent.click(retry);
  await act(async () => {
    await Promise.resolve();
  });
  expect(writes(fetch)).toHaveLength(2);
  expect(writes(fetch)[0]![1].body).toBe(writes(fetch)[1]![1].body);
  await act(async () => {
    pending[0]!(json(probed(operation())));
  });
  expect(
    screen.queryByRole("button", { name: "Review planned install" }),
  ).toBeNull();
  expect(retry.disabled).toBe(true);
  await act(async () => {
    pending[1]!(json(probed(operation())));
  });
  expect(
    screen.getByRole("button", { name: "Review planned install" }),
  ).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Retry exact onboarding request" }),
  ).toBeNull();
});

test("unmount aborts a pending mutation and ignores its late response", async () => {
  let resolve: (response: Response) => void = () => {};
  const { fetch } = setup((url) => {
    if (url.includes("/server-bootstraps?")) return json(page([operation()]));
    if (url === "/api/v1/server-bootstraps/op") return json(operation());
    if (url.endsWith("/probe"))
      return new Promise<Response>((done) => {
        resolve = done;
      });
  });
  await chooseProject();
  fireEvent.click(
    await screen.findByRole("button", { name: /Production host ·/ }),
  );
  fireEvent.click(
    await screen.findByRole("button", { name: "Probe host prerequisites" }),
  );
  await waitFor(() => expect(writes(fetch)).toHaveLength(1));
  cleanup();
  expect((writes(fetch)[0]![1].signal as AbortSignal).aborted).toBe(true);
  await act(async () => {
    resolve(json(probed(operation())));
  });
  expect(
    client.getQueryData<Operation>([
      "serverBootstrap",
      "local:admin",
      "project",
      "op",
    ])?.version,
  ).toBe(1);
});

test("hung create retries the frozen UUID and body after its 30 second deadline", async () => {
  const pending: ((response: Response) => void)[] = [];
  const { fetch } = setup((url, options) => {
    if (url === "/api/v1/server-bootstraps" && options.method === "POST")
      return new Promise<Response>((done) => {
        pending.push(done);
      });
    if (url === "/api/v1/server-bootstraps/op") return json(operation());
  });
  await chooseProject();
  fireEvent.change(screen.getByLabelText("Managed SSH key"), {
    target: { value: "key" },
  });
  await screen.findByRole("option", { name: "Version 2 · current" });
  fireEvent.change(screen.getByLabelText("SSH key version"), {
    target: { value: "2" },
  });
  fireEvent.change(screen.getByLabelText("Qualified runner release"), {
    target: { value: "release" },
  });
  vi.useFakeTimers();
  fireEvent.submit(
    screen.getByRole("form", { name: "Prepare SSH server onboarding" }),
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(29999);
  });
  expect(
    screen.getByRole<HTMLButtonElement>("button", {
      name: "Retry exact onboarding request",
    }).disabled,
  ).toBe(true);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
  });
  const retry = screen.getByRole<HTMLButtonElement>("button", {
    name: "Retry exact onboarding request",
  });
  expect(retry.disabled).toBe(false);
  fireEvent.click(retry);
  await act(async () => {
    pending[0]!(json({}, 403));
  });
  expect(screen.queryByText(/Onboarding access is unavailable/)).toBeNull();
  expect(retry.disabled).toBe(true);
  expect(writes(fetch)[0]![1].body).toBe(writes(fetch)[1]![1].body);
  expect(JSON.parse(writes(fetch)[1]![1].body).request_id).toBeTruthy();
  await act(async () => {
    pending[1]!(json(operation()));
  });
  expect(
    screen.getByRole("region", { name: "Current server onboarding" }),
  ).toBeTruthy();
});

test("fresh project authorization cannot admit a denied lifetime's late response", async () => {
  let deny = false;
  let resolve: (response: Response) => void = () => {};
  setup((url) => {
    if (url.includes("server-bootstraps?project_id=project"))
      return json(page([operation()]));
    if (url === "/api/v1/server-bootstraps/op")
      return deny ? json({}, 403) : json(operation());
    if (url.endsWith("/probe"))
      return new Promise<Response>((done) => {
        resolve = done;
      });
  });
  await chooseProject();
  fireEvent.click(
    await screen.findByRole("button", { name: /Production host ·/ }),
  );
  fireEvent.click(
    await screen.findByRole("button", { name: "Probe host prerequisites" }),
  );
  deny = true;
  await act(async () => {
    await client.invalidateQueries({ queryKey: ["serverBootstrap"] });
  });
  await screen.findByText(/Onboarding access is unavailable/);
  fireEvent.change(screen.getByLabelText("Project for server onboarding"), {
    target: { value: "second" },
  });
  await screen.findByText("New server for Second project");
  deny = false;
  fireEvent.change(screen.getByLabelText("Project for server onboarding"), {
    target: { value: "project" },
  });
  fireEvent.click(
    await screen.findByRole("button", { name: /Production host ·/ }),
  );
  await screen.findByRole("region", { name: "Current server onboarding" });
  await act(async () => {
    resolve(json(probed(operation())));
  });
  expect(
    screen.queryByRole("button", { name: "Review planned install" }),
  ).toBeNull();
  expect(
    client.getQueryData<Operation>([
      "serverBootstrap",
      "local:admin",
      "project",
      "op",
    ])?.version,
  ).toBe(1);
});
