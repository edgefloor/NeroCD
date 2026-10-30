import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { HostingServers } from "./HostingServers";
import { eligibleHostingRunner } from "./hostingRunnerEligibility";
import type { Runner } from "@/api";
vi.mock("./ServerReadiness", () => ({ ServerReadiness: ({ serverID, blocked }: { serverID: string; blocked: boolean }) => <div data-testid="readiness-panel" data-server={serverID} data-blocked={String(blocked)}>Readiness for {serverID}</div> }));
vi.mock("./HostingProxy", () => ({ HostingProxy: ({ serverID }: { serverID: string }) => <div>Managed proxy for {serverID}</div> }));
const runner: Runner = { id: "runner_one", name: "Compose runner", status: "active", capabilities: ["compose-deploy"], tags: [], registered_at: "", last_heartbeat_at: "" };
const server = { id: "server_one", name: "Production host", runner_id: runner.id, created_by: "admin", created_at: "" };
const destination = { id: "dest_one", server_id: server.id, name: "default", runtime_kind: "docker_compose", created_at: "" };
const grant = { destination_id: destination.id, project_id: "project_external", granted_by: "admin", created_at: "" };
const page = <T,>(items: T[], offset = 0, total = items.length) => ({ items, offset, total, count: items.length, limit: 20 });
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
let client: QueryClient;
afterEach(() => { cleanup(); client?.clear(); vi.unstubAllGlobals(); });
function setup(canAdmin = true, blocked = false) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}><HostingServers canAdmin={canAdmin} blocked={blocked} /></QueryClientProvider>);
}
function standard(url: string) {
  if (url.includes("/runners?")) return json(page([runner, { ...runner, id: "shell", name: "Shell runner", capabilities: ["compose-deploy", "shell"] }]));
  if (url.includes("project-grants")) return json(page([grant]));
  if (url.includes("/servers/")) return json({ server, destination });
  return json(page([server]));
}
test("global hosting administration makes no requests for a project-only user", () => {
  const fetch = vi.fn(); vi.stubGlobal("fetch", fetch); setup(false); expect(fetch).not.toHaveBeenCalled(); expect(screen.queryByRole("form")).toBeNull();
});
test("eligibility offers canonical or historical Compose capabilities, active only and never shell", () => {
  expect(eligibleHostingRunner(runner)).toBe(true);
  expect(eligibleHostingRunner({ ...runner, capabilities: ["compose_deploy"] })).toBe(true);
  expect(eligibleHostingRunner({ ...runner, capabilities: ["compose-deploy", "shell"] })).toBe(false);
  expect(eligibleHostingRunner({ ...runner, status: "revoked" })).toBe(false);
  expect(eligibleHostingRunner({ ...runner, capabilities: [] })).toBe(false);
});
test("server registration uses only selected eligible runner and retains invalid drafts", async () => {
  const fetch = vi.fn(async (url: string, options: RequestInit) => options.method === "POST" ? json({}, 503) : standard(url));
  vi.stubGlobal("fetch", fetch); setup(); await screen.findByRole("option", { name: /Compose runner/ });
  expect(screen.queryByRole("option", { name: /Shell runner/ })).toBeNull();
  fireEvent.change(screen.getByLabelText("Hosting server name"), { target: { value: "Draft host" } });
  fireEvent.change(screen.getByLabelText("Enrolled Compose runner"), { target: { value: runner.id } });
  fireEvent.submit(screen.getByRole("form", { name: "Register hosting server" }));
  await screen.findByText(/Server registration failed/);
  expect(screen.getByLabelText<HTMLInputElement>("Hosting server name").value).toBe("Draft host");
  expect(screen.getByLabelText<HTMLSelectElement>("Enrolled Compose runner").value).toBe(runner.id);
  const writes = fetch.mock.calls.filter(([, options]) => options.method === "POST");
  expect(writes).toHaveLength(1); expect(writes[0]![1].body).toBe(JSON.stringify({ name: "Draft host", runner_id: runner.id }));
});
test("an admin grants a project outside membership and revokes without deleting the destination", async () => {
  let revoked = false;
  const fetch = vi.fn(async (url: string, options: RequestInit) => {
    if (url.includes("project-grants") && options.method === "POST") { revoked = url.endsWith("/revoke"); return json({ ...grant, ...(revoked ? { revoked_at: "2026-01-01T00:00:00Z" } : {}) }); }
    if (url.includes("project-grants")) return json(page([{ ...grant, ...(revoked ? { revoked_at: "2026-01-01T00:00:00Z" } : {}) }]));
    return standard(url);
  });
  vi.stubGlobal("fetch", fetch); setup(); fireEvent.click(await screen.findByRole("button", { name: "View server destination" }));
  await screen.findByLabelText("Project ID to grant");
  expect(screen.getByTestId("readiness-panel").getAttribute("data-server")).toBe(server.id);
  fireEvent.change(screen.getByLabelText("Project ID to grant"), { target: { value: "project_external" } });
  fireEvent.submit(screen.getByRole("form", { name: "Grant destination to project" }));
  await waitFor(() => expect(screen.getByLabelText<HTMLInputElement>("Project ID to grant").value).toBe(""));
  fireEvent.click(screen.getByRole("button", { name: "Revoke grant for project_external" }));
  await screen.findByText("Grant revoked"); expect(screen.getByText("dest_one")).toBeTruthy();
  expect(fetch.mock.calls.filter(([, options]) => options.method === "POST").map(([, options]) => JSON.parse(String(options.body)))).toEqual([{ project_id: "project_external" }, { project_id: "project_external" }]);
});
test("server list refresh failure preserves the registration draft until retry", async () => {
  let failing = false;
  vi.stubGlobal("fetch", vi.fn(async (url: string) => url.includes("/servers?") && failing ? json({}, 503) : standard(url)));
  setup(); await screen.findByRole("option", { name: /Compose runner/ });
  fireEvent.change(screen.getByLabelText("Hosting server name"), { target: { value: "Unsent host" } });
  failing = true; await act(async () => { await client.invalidateQueries({ queryKey: ["hostingServers"] }); });
  await screen.findByRole("button", { name: "Retry hosting servers" });
  expect(screen.getByLabelText<HTMLInputElement>("Hosting server name").value).toBe("Unsent host");
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Register hosting server" }).disabled).toBe(true);
  failing = false; fireEvent.click(screen.getByRole("button", { name: "Retry hosting servers" }));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Retry hosting servers" })).toBeNull());
});
test("bounded server and runner choices request separate page offsets", async () => {
  const fetch = vi.fn(async (url: string) => url.includes("/runners?") ? json(page([runner], Number(new URL(url, "http://local").searchParams.get("offset")), 41)) : json(page([server], Number(new URL(url, "http://local").searchParams.get("offset")), 41)));
  vi.stubGlobal("fetch", fetch); setup(); await screen.findByRole("option", { name: /Compose runner/ });
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Next hosting servers" }).disabled).toBe(false));
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Next enrolled runners" }).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "Next hosting servers" }));
  fireEvent.click(screen.getByRole("button", { name: "Next enrolled runners" }));
  await waitFor(() => expect(fetch.mock.calls.map(([url]) => url)).toContain("/api/v1/servers?limit=20&offset=20"));
  expect(fetch.mock.calls.map(([url]) => url)).toContain("/api/v1/runners?limit=20&offset=20");
});


test("changing setup task preserves the existing-runner registration draft", async () => {
  vi.stubGlobal("fetch", vi.fn(async (url: string) => standard(url)));
  const view = setup();
  await screen.findByRole("option", { name: /Compose runner/ });
  fireEvent.change(screen.getByLabelText("Hosting server name"), { target: { value: "Unsent host" } });
  fireEvent.change(screen.getByLabelText("Enrolled Compose runner"), { target: { value: runner.id } });
  const ui = (setupMode: "manual" | "ssh" | "existing") => <QueryClientProvider client={client}><HostingServers canAdmin blocked={false} setupMode={setupMode} /></QueryClientProvider>;
  view.rerender(ui("ssh"));
  expect(screen.queryByRole("form", { name: "Register hosting server" })).toBeNull();
  view.rerender(ui("manual"));
  view.rerender(ui("existing"));
  expect(screen.getByRole<HTMLInputElement>("textbox", { name: "Hosting server name" }).value).toBe("Unsent host");
  expect(screen.getByLabelText<HTMLSelectElement>("Enrolled Compose runner").value).toBe(runner.id);
});
