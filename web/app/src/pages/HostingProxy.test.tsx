import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { queryKeys } from "@/api";
import { HostingProxy } from "./HostingProxy";
const proxy = { id: "proxy_one", server_id: "server", control_project_id: "project", destination_id: "dest", runner_id: "runner", configuration: { email: "ops@example.test", ca: "letsencrypt-staging", management_port: 18080 }, enabled: true, desired_generation: 1, loaded_generation: 0, restore_ownership_held: false };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
let client: QueryClient;
afterEach(() => { cleanup(); client?.clear(); vi.unstubAllGlobals(); });
function setup(roles = ["runner_admin"], owner = true) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  client.setQueryData(queryKeys.principal, { id: "user", provider: "oidc", roles });
  client.setQueryData(queryKeys.allProjectMembers, owner ? [{ project_id: "project", user_id: "user", role: "owner" }] : []);
  return render(<QueryClientProvider client={client}><HostingProxy serverID="server" blocked={false} /></QueryClientProvider>);
}
function backend(handler?: (url: string, options: RequestInit) => Response | Promise<Response> | undefined) {
  const fetch = vi.fn(async (url: string, options: RequestInit) => await handler?.(url, options) ?? (url.endsWith("hostname-grants") ? json([]) : json(proxy)));
  vi.stubGlobal("fetch", fetch); return fetch;
}
test("project-only users do not fetch admin proxy detail", () => { const fetch = backend(); setup([]); expect(fetch).not.toHaveBeenCalled(); expect(screen.queryByRole("region")).toBeNull(); });
test.each([["runner_admin", false], ["system_admin", true]])("%s owner override follows actual permission contract", async (role, allowed) => {
  backend(); setup([role as string], false); await screen.findByText("Loaded generation");
  expect(Boolean(screen.queryByRole("button", { name: "Review proxy withdrawal" }))).toBe(allowed);
  expect(Boolean(screen.queryByRole("form", { name: "Grant proxy hostname" }))).toBe(allowed);
});
test("configuration preserves exact body after uncertain delivery and clears it on actor change", async () => {
  const fetch = backend((url, options) => options.method === "POST" ? Promise.reject(new TypeError("response lost")) : url.endsWith("/proxy") ? json({}, 404) : undefined);
  setup(); await screen.findByRole("form", { name: "Configure managed proxy" });
  fireEvent.change(screen.getByLabelText("Immutable control project ID"), { target: { value: "project" } });
  fireEvent.change(screen.getByLabelText("ACME email"), { target: { value: "ops@example.test" } });
  fireEvent.submit(screen.getByRole("form", { name: "Configure managed proxy" }));
  fireEvent.click(await screen.findByRole("button", { name: "Retry identical proxy request" }));
  await waitFor(() => expect(fetch.mock.calls.filter(([, o]) => o.method === "POST")).toHaveLength(2));
  const writes = fetch.mock.calls.filter(([, o]) => o.method === "POST"); expect(writes[0]![1].body).toBe(writes[1]![1].body);
  await act(async () => { client.setQueryData(queryKeys.principal, { id: "other", provider: "oidc", roles: ["runner_admin"] }); });
  await waitFor(() => expect(screen.queryByRole("button", { name: "Retry identical proxy request" })).toBeNull());
});
test("503 preserves configuration input and 403 does not turn into an owner grant", async () => {
  backend((url, options) => options.method === "POST" ? json({}, 503) : url.endsWith("/proxy") ? json({}, 404) : undefined);
  setup(); await screen.findByRole("form", { name: "Configure managed proxy" });
  fireEvent.change(screen.getByLabelText("Immutable control project ID"), { target: { value: "project" } }); fireEvent.change(screen.getByLabelText("ACME email"), { target: { value: "draft@example.test" } });
  fireEvent.submit(screen.getByRole("form", { name: "Configure managed proxy" })); await screen.findByText(/storage or dispatch is unavailable/);
  expect(screen.getByLabelText<HTMLInputElement>("ACME email").value).toBe("draft@example.test");
  await act(async () => { client.setQueryData(queryKeys.allProjectMembers, []); });
  await waitFor(() => expect(screen.queryByRole("button", { name: "Retry identical proxy request" })).toBeNull());
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Configure managed proxy" }).disabled).toBe(true);
});
test("withdrawal is named, confirmed, versionless keyed and honest about loaded routing", async () => {
  const fetch = backend((url, options) => options.method === "POST" ? json({ ...proxy, enabled: false, desired_generation: 2 }) : undefined);
  setup(); await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Review proxy withdrawal" }).disabled).toBe(false)); fireEvent.click(screen.getByRole("button", { name: "Review proxy withdrawal" }));
  expect(screen.getByText(/Withdraw all routes from proxy proxy_one on server server/)).toBeTruthy();
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request proxy withdrawal" }).disabled).toBe(true);
  fireEvent.click(screen.getByLabelText("I confirm this exact withdrawal.")); fireEvent.click(screen.getByRole("button", { name: "Request proxy withdrawal" }));
  await waitFor(() => expect(fetch.mock.calls.some(([url]) => url.endsWith("/disable"))).toBe(true));
  expect(screen.getByText(/Loaded generation can lag/)).toBeTruthy();
});
test("administrator operation state and cancellation require control-project ownership", async () => {
  const fetch = backend((url, options) => url.includes("/proxy-operations/") ? json({ id: "pop_one", state: "queued", generation: 1, snapshot: { server_id: "server" }, mutation_hold: false, restore_ownership_held: false }) : url.endsWith("/proxy") ? json({ ...proxy, active_operation_id: "pop_one" }) : undefined);
  setup(); await screen.findByText("Operation state: queued · generation 1");
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Request proxy operation cancellation" }).disabled).toBe(true);
  fireEvent.click(screen.getByLabelText("I confirm cancellation of proxy operation pop_one.")); fireEvent.click(screen.getByRole("button", { name: "Request proxy operation cancellation" }));
  await waitFor(() => expect(fetch.mock.calls.some(([url, options]) => url.endsWith("/cancel") && options.method === "POST")).toBe(true));
  await act(async () => { client.setQueryData(queryKeys.allProjectMembers, []); });
  await waitFor(() => expect(screen.queryByRole("button", { name: "Request proxy operation cancellation" })).toBeNull());
});
test("late configuration response cannot restore intent after control-project ownership is removed", async () => {
  let resolve!: (response: Response) => void;
  backend((url, options) => options.method === "POST" ? new Promise<Response>((done) => { resolve = done; }) : url.endsWith("/proxy") ? json({}, 404) : undefined);
  setup(); await screen.findByRole("form", { name: "Configure managed proxy" });
  fireEvent.change(screen.getByLabelText("Immutable control project ID"), { target: { value: "project" } }); fireEvent.change(screen.getByLabelText("ACME email"), { target: { value: "ops@example.test" } }); fireEvent.submit(screen.getByRole("form", { name: "Configure managed proxy" }));
  await waitFor(() => expect(resolve).toBeTypeOf("function"));
  await act(async () => { client.setQueryData(queryKeys.allProjectMembers, []); });
  await waitFor(() => expect(screen.getByLabelText<HTMLInputElement>("ACME email").value).toBe(""));
  await act(async () => { resolve(json(proxy, 202)); });
  expect(screen.queryByText("Loaded generation")).toBeNull();
  expect(screen.queryByRole("button", { name: "Retry identical proxy request" })).toBeNull();
});
test("grant failure retains input and requires refreshed grants before another submission", async () => {
  const fetch = backend((url, options) => options.method === "POST" ? json({}, 503) : undefined); setup(); await screen.findByRole("form", { name: "Grant proxy hostname" });
  fireEvent.change(screen.getByLabelText("Hostname project ID"), { target: { value: "project" } }); fireEvent.change(screen.getByLabelText("Hostname or namespace root"), { target: { value: "example.test" } }); fireEvent.click(screen.getByLabelText("Authorize descendants of this namespace"));
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Grant hostname authority" }).disabled).toBe(false)); fireEvent.submit(screen.getByRole("form", { name: "Grant proxy hostname" }));
  await screen.findByText(/hostname grants do not accept an idempotency key/); expect(screen.getByLabelText<HTMLInputElement>("Hostname or namespace root").value).toBe("example.test"); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Grant hostname authority" }).disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Refresh proxy information" })); await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Grant hostname authority" }).disabled).toBe(false)); expect(fetch.mock.calls.filter(([, options]) => options.method === "POST")).toHaveLength(1);
});

test("metadata conflict preserves the original engine outcome and ownership warning", async () => {
  backend((url) => url.includes("/proxy-operations/") ? json({ id: "pop_one", state: "uncertain", generation: 2, snapshot: { server_id: "server" }, mutation_hold: true, restore_ownership_held: false, metadata_conflict: true, result: { outcome: "rolled_back", loaded_generation: 1, routes: [] } }) : url.endsWith("/proxy") ? json({ ...proxy, active_operation_id: "pop_one" }) : undefined);
  setup();
  await screen.findByText("Historical engine outcome: rolled_back");
  expect(screen.getByText(/Route metadata conflict. Keep ownership held/)).toBeTruthy();
  expect(screen.getByText(/Proxy mutation ownership remains held/)).toBeTruthy();
  expect(screen.queryByRole("button", { name: /reconcile|unlock/i })).toBeNull();
});
