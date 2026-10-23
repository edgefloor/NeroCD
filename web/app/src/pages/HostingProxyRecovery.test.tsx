import { Profiler } from "react";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { queryKeys } from "@/api";
import { HostingProxyRecovery } from "./HostingProxyRecovery";
let search: Record<string, unknown> = {};
const navigate = vi.fn(async (options: { search: (value: Record<string, unknown>) => Record<string, unknown> }) => { search = options.search(search); });
vi.mock("@tanstack/react-router", () => ({ useSearch: () => search, useNavigate: () => navigate }));
const props = { serverID: "server", projectID: "project", proxyID: "proxy", operationID: "pop_original", blocked: false };
const hash = "sha256:" + "a".repeat(64), target = { route_id: "route", project_id: "project", resource_id: "resource", binding_id: "binding", release_operation_id: "hop_a", hostname: "web.example.test", path_prefix: "/", host_port: 20101 };
const review = { version: 1, operation_id: "pop_original", proxy_id: "proxy", actor_id: "user", loaded_generation: 3, desired_generation: 5, prior_operation_id: "pop_prior", prior_snapshot_hash: hash, complete_receipt_id: "receipt", complete_receipt_hash: hash, engine_outcome: "rolled_back", facts_hash: hash, routes: [{ route_id: "route", version: 4, selected_release_operation_id: "hop_b", enabled: false, authorized: false, prior_target: target }], convergence_routes: [] };
const plan = { id: "prp_one", review, plan_hash: hash, created_at: "2026-10-23T00:00:00Z", expires_at: "2026-10-23T00:00:30Z" };
const resolution = { id: "prr_one", operation_id: "pop_original", plan_id: plan.id, plan_hash: hash, actor_id: "user", engine_outcome: "rolled_back", complete_receipt_id: "receipt", complete_receipt_hash: hash, convergence_operation_id: "pop_converge", routes: [{ route_id: "route", before_version: 4, after_version: 5, before_release_operation_id: "hop_b", after_release_operation_id: "hop_a", enabled: false }], created_at: "2026-10-23T00:00:01Z" };
const status = { operation_id: "pop_original", eligible: true, reason: "completed_metadata_conflict", review };
const json = (body: unknown, code = 200) => new Response(JSON.stringify(body), { status: code });
let client: QueryClient;
beforeEach(() => { search = {}; navigate.mockClear(); });
afterEach(() => { cleanup(); client?.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
function backend(handler?: (url: string, init: RequestInit) => Response | Promise<Response> | undefined, owner = true) {
  const fetch = vi.fn(async (url: string, init: RequestInit) => await handler?.(url, init) ?? (url.includes("project-members") ? json({ items: owner ? [{ project_id: "project", user_id: "user", role: "owner" }] : [], count: owner ? 1 : 0, total: owner ? 1 : 0, offset: 0 }) : url.endsWith("/recovery") ? json(status) : url.endsWith("/apply") ? json(resolution) : url.endsWith("/recovery-plans") ? json(plan, 201) : json({ id: "pop_original", proxy_id: "proxy", snapshot: { server_id: "server", proxy_id: "proxy" } })));
  vi.stubGlobal("fetch", fetch); return fetch;
}
function setup(roles = ["runner_admin"], initialProps = props, onCommit?: (html: string) => void) { client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } }); client.setQueryData(queryKeys.principal, { id: "user", provider: "local", roles }); const content = <QueryClientProvider client={client}><HostingProxyRecovery {...initialProps} /></QueryClientProvider>; return render(onCommit ? <Profiler id="recovery" onRender={() => onCommit(document.body.innerHTML)}>{content}</Profiler> : content); }
async function requestPlan() { fireEvent.click(await screen.findByRole("button", { name: "Request server recovery plan" })); await screen.findByRole("region", { name: "Review exact metadata recovery plan" }); }
function confirm() { fireEvent.click(screen.getByRole("checkbox", { name: /I confirm this exact metadata repair/ })); fireEvent.click(screen.getByRole("button", { name: "Apply reviewed metadata recovery" })); }
test("held operation auto-reads, server plan displays exact evidence and requires explicit confirmation", async () => {
  const fetch = backend(); setup(); await requestPlan(); expect(screen.getByText("Original database expiry: " + plan.expires_at)).toBeTruthy(); expect(screen.getByText(/hop_b → hop_a. Enabled: no; authorized: no/)).toBeTruthy(); expect(screen.getByText(/Loaded generation remains 3 until ordinary runner work completes/)).toBeTruthy(); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Apply reviewed metadata recovery" }).disabled).toBe(true); expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(1);
  confirm(); await screen.findByRole("region", { name: "Recorded metadata recovery resolution" }); expect(screen.getByText(/one queued convergence. It does not establish fresh TLS/)).toBeTruthy();
  expect(search).toMatchObject({ proxy_recovery_scope: '["local","user","project","proxy"]', proxy_recovery_operation: "pop_original" });
});
test.each(["plan", "apply"])("response loss permits only the identical %s request and preserves its original expiry", async kind => {
  let count = 0; const fetch = backend((url, init) => init.method === "POST" && url.endsWith(kind === "plan" ? "/recovery-plans" : "/apply") && ++count === 1 ? Promise.reject(new TypeError("lost")) : undefined); setup();
  if (kind === "plan") { fireEvent.click(await screen.findByRole("button", { name: "Request server recovery plan" })); } else { await requestPlan(); confirm(); }
  await screen.findByText(new RegExp(`The ${kind} response was not confirmed`)); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Read fresh facts for a new review" }).disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: `Retry identical recovery ${kind} request` }));
  await screen.findByRole("region", { name: kind === "plan" ? "Review exact metadata recovery plan" : "Recorded metadata recovery resolution" });
  const writes = fetch.mock.calls.filter(([url, init]) => init.method === "POST" && url.endsWith(kind === "plan" ? "/recovery-plans" : "/apply")); expect(writes).toHaveLength(2); expect(writes[0]![1].body).toBe(writes[1]![1].body);
});
test("server expiry conflict requires a fresh read and new review; local clock never renews or silently applies", async () => {
  const fetch = backend((url) => url.endsWith("/apply") ? json({}, 409) : undefined); setup(); await requestPlan(); confirm(); await screen.findByText(/Recovery conflict/); expect(fetch.mock.calls.filter(([url]) => url.endsWith("/apply"))).toHaveLength(1); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical recovery apply request" }).disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Read fresh facts for a new review" })); await screen.findByRole("button", { name: "Request server recovery plan" }); fireEvent.click(screen.getByRole("button", { name: "Request server recovery plan" })); await screen.findByRole("region", { name: "Review exact metadata recovery plan" });
  const bodies = fetch.mock.calls.filter(([url, init]) => init.method === "POST" && url.endsWith("/recovery-plans")).map(([, init]) => JSON.parse(String(init.body))); expect(bodies).toHaveLength(2); expect(bodies[0].idempotency_key).not.toBe(bodies[1].idempotency_key); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Apply reviewed metadata recovery" }).disabled).toBe(true);
});
test.each(["actor", "provider", "owner", "project", "proxy", "parent", "principal-fetch", "members-fetch"])("scope/access change %s aborts and fences a late plan response", async change => {
  let resolve!: (response: Response) => void; const fetch = backend((url, init) => init.method === "POST" ? new Promise<Response>(done => { resolve = done; }) : undefined); const view = setup(); fireEvent.click(await screen.findByRole("button", { name: "Request server recovery plan" })); await waitFor(() => expect(resolve).toBeTypeOf("function")); const signal = fetch.mock.calls.find(([, init]) => init.method === "POST")![1].signal;
  if (["actor", "provider"].includes(change)) await act(async () => { client.setQueryData(queryKeys.principal, { id: change === "actor" ? "other" : "user", provider: change === "provider" ? "oidc" : "local", roles: ["runner_admin"] }); });
  else if (["owner", "principal-fetch", "members-fetch"].includes(change)) {
    backend(url => url.includes(change === "principal-fetch" ? "/me" : "/project-members") ? change === "owner" ? json({ items: [], count: 0, total: 0, offset: 0 }) : new Promise<Response>(() => {}) : undefined);
    await act(async () => { void client.invalidateQueries({ queryKey: change === "principal-fetch" ? queryKeys.principal : ["proxyRecoveryMembers"] }); });
  } else view.rerender(<QueryClientProvider client={client}><HostingProxyRecovery {...props} projectID={change === "project" ? "other" : props.projectID} proxyID={change === "proxy" ? "other" : props.proxyID} blocked={change === "parent"} /></QueryClientProvider>);
  await waitFor(() => expect(signal?.aborted).toBe(true)); await act(async () => resolve(json(plan))); expect(screen.queryByRole("region", { name: "Review exact metadata recovery plan" })).toBeNull();
});
test("exact operation lookup cannot discard an unresolved plan response", async () => {
  let resolve!: (response: Response) => void; const fetch = backend((url, init) => init.method === "POST" ? new Promise<Response>(done => { resolve = done; }) : undefined); setup(); fireEvent.click(await screen.findByRole("button", { name: "Request server recovery plan" })); await waitFor(() => expect(resolve).toBeTypeOf("function"));
  expect(screen.getByLabelText<HTMLInputElement>("Original proxy operation ID").disabled).toBe(true);
  fireEvent.change(screen.getByLabelText("Original proxy operation ID"), { target: { value: "pop_other" } }); fireEvent.submit(screen.getByRole("form", { name: "Look up metadata recovery operation" }));
  expect(fetch.mock.calls.some(([url]) => url.includes("pop_other"))).toBe(false); expect(fetch.mock.calls.find(([, init]) => init.method === "POST")![1].signal?.aborted).toBe(false);
  await act(async () => resolve(json(plan))); await screen.findByRole("region", { name: "Review exact metadata recovery plan" }); expect(screen.getByText("Recovery operation: pop_original")).toBeTruthy();
});

test.each([401, 403, 404])("HTTP%s clears sensitive review and prevents any new apply", async code => { backend(url => url.endsWith("/apply") ? json({}, code) : undefined); setup(); await requestPlan(); confirm(); await screen.findByText(/Metadata recovery permission is unavailable/); expect(screen.queryByRole("region", { name: "Review exact metadata recovery plan" })).toBeNull(); await waitFor(() => expect(client.getQueriesData({ queryKey: ["proxyRecovery"] }).every(([, data]) => data === undefined)).toBe(true)); });
test("system administrator override and runner-owner contract match the server", async () => { backend(undefined, false); const view = setup(["system_admin"]); await screen.findByRole("button", { name: "Request server recovery plan" }); view.unmount(); client.clear(); setup(["runner_admin"]); await screen.findByText(/requires server administrator and control-project owner access/); expect(screen.queryByRole("button", { name: "Request server recovery plan" })).toBeNull(); });
test("project owners without server-administrator roles do not fetch recovery evidence", async () => { const fetch = backend(); setup([]); await screen.findByText(/requires server administrator/); expect(fetch.mock.calls.every(([url]) => url.includes("project-members"))).toBe(true); });
test("scoped locator reload reads original resolution after current operation becomes convergence", async () => { search = { q: "retained", proxy_recovery_scope: '["local","user","project","proxy"]', proxy_recovery_operation: "pop_original" }; const fetch = backend(url => url.endsWith("/recovery") ? json({ operation_id: "pop_original", eligible: false, reason: "resolved", resolution }) : undefined); const view = setup(); view.rerender(<QueryClientProvider client={client}><HostingProxyRecovery {...props} operationID="pop_converge" /></QueryClientProvider>); await screen.findByRole("region", { name: "Recorded metadata recovery resolution" }); expect(fetch.mock.calls.some(([url]) => url.endsWith("pop_original/recovery"))).toBe(true); expect(search.q).toBe("retained"); });
test("foreign actor/project/proxy locator is replaced with current held operation", async () => { search = { q: "host", proxy_recovery_scope: '["local","foreign","other","foreign-proxy"]', proxy_recovery_operation: "pop_foreign" }; const fetch = backend(); setup(); await screen.findByRole("button", { name: "Request server recovery plan" }); expect(fetch.mock.calls.every(([url]) => !url.includes("pop_foreign"))).toBe(true); expect(search).toMatchObject({ q: "host", proxy_recovery_operation: "pop_original" }); });
test("synthetic unsupported uncertain status offers no generic unlock or plan", async () => { backend(url => url.endsWith("/recovery") ? json({ operation_id: "pop_original", eligible: false, reason: "unsupported_or_unfinished" }) : undefined); setup(); await screen.findByText(/Recovery is unsupported or unfinished/); expect(screen.queryByRole("button", { name: "Request server recovery plan" })).toBeNull(); expect(screen.queryByRole("button", { name: /unlock/i })).toBeNull(); });
test.each(["actor", "role"])("late accepted apply response cannot restore a resolution after %s loss", async change => {
  let resolve!: (response: Response) => void; const fetch = backend(url => url.endsWith("/apply") ? new Promise<Response>(done => { resolve = done; }) : undefined); setup(); await requestPlan(); confirm(); await waitFor(() => expect(resolve).toBeTypeOf("function")); const signal = fetch.mock.calls.find(([url]) => url.endsWith("/apply"))![1].signal;
  await act(async () => { client.setQueryData(queryKeys.principal, { id: change === "actor" ? "other" : "user", provider: "local", roles: change === "role" ? [] : ["runner_admin"] }); });
  await waitFor(() => expect(signal?.aborted).toBe(true)); await act(async () => resolve(json(resolution))); expect(screen.queryByRole("region", { name: "Recorded metadata recovery resolution" })).toBeNull(); expect(screen.queryByRole("region", { name: "Review exact metadata recovery plan" })).toBeNull();
});

const nextOperation = "pop_next";
function nextBackend(url: string) {
  if (url.endsWith(nextOperation + "/recovery")) return json({ ...status, operation_id: nextOperation, review: { ...review, operation_id: nextOperation } });
  if (url.endsWith(nextOperation)) return json({ id: nextOperation, proxy_id: "proxy", snapshot: { server_id: "server", proxy_id: "proxy" } });
}
test("empty selection exposes a new current operation only through an explicit authorized read", async () => {
  const fetch = backend(); const view = setup(["runner_admin"], { ...props, operationID: "" }); await screen.findByText("Select a held proxy operation to read its recovery status.");
  view.rerender(<QueryClientProvider client={client}><HostingProxyRecovery {...props} /></QueryClientProvider>);
  const current = await screen.findByRole("button", { name: "Read current proxy operation" }); expect(screen.getByText(/Current proxy operation: pop_original. Recovery eligibility requires a fresh authorized read/)).toBeTruthy();
  expect(fetch.mock.calls.some(([url]) => url.includes("proxy-operations"))).toBe(false);
  fireEvent.click(current); await screen.findByRole("button", { name: "Request server recovery plan" }); expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(0);
});
test("resolved original stays selected until current operation is explicitly read, without an automatic new plan", async () => {
  const fetch = backend(url => nextBackend(url) ?? (url.endsWith("/recovery") ? json({ operation_id: "pop_original", eligible: false, reason: "resolved", resolution }) : undefined)); const view = setup(); await screen.findByRole("region", { name: "Recorded metadata recovery resolution" });
  view.rerender(<QueryClientProvider client={client}><HostingProxyRecovery {...props} operationID={nextOperation} /></QueryClientProvider>);
  expect(screen.getByRole("region", { name: "Recorded metadata recovery resolution" })).toBeTruthy(); expect(fetch.mock.calls.some(([url]) => url.includes(nextOperation))).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: "Read current proxy operation" })); await screen.findByRole("button", { name: "Request server recovery plan" }); expect(screen.getByText("Recovery operation: " + nextOperation)).toBeTruthy(); expect(screen.queryByRole("region", { name: "Recorded metadata recovery resolution" })).toBeNull(); expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(0);
});
test("switching current operation aborts a read and fences its late old response", async () => {
  let resolve!: (response: Response) => void;
  const fetch = backend(url => nextBackend(url) ?? (url.endsWith("pop_original/recovery") ? new Promise<Response>(done => { resolve = done; }) : undefined)); const view = setup(); await waitFor(() => expect(resolve).toBeTypeOf("function")); const signal = fetch.mock.calls.find(([url]) => url.endsWith("pop_original/recovery"))![1].signal;
  view.rerender(<QueryClientProvider client={client}><HostingProxyRecovery {...props} operationID={nextOperation} /></QueryClientProvider>); fireEvent.click(await screen.findByRole("button", { name: "Read current proxy operation" })); await screen.findByRole("button", { name: "Request server recovery plan" }); await waitFor(() => expect(signal?.aborted).toBe(true));
  await act(async () => resolve(json(status))); expect(screen.getByText("Recovery operation: " + nextOperation)).toBeTruthy(); expect(screen.queryByText("Recovery operation: pop_original")).toBeNull();
});
test.each(["plan", "apply"])("a new current operation cannot discard the frozen %s request or bypass navigation controls", async kind => {
  let writes = 0; const fetch = backend((url, init) => nextBackend(url) ?? (init.method === "POST" && url.endsWith(kind === "plan" ? "/recovery-plans" : "/apply") && ++writes === 1 ? Promise.reject(new TypeError("lost")) : undefined)); const view = setup();
  if (kind === "plan") fireEvent.click(await screen.findByRole("button", { name: "Request server recovery plan" })); else { await requestPlan(); confirm(); }
  await screen.findByText(new RegExp(`The ${kind} response was not confirmed`));
  view.rerender(<QueryClientProvider client={client}><HostingProxyRecovery {...props} operationID={nextOperation} /></QueryClientProvider>);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Read current proxy operation" }).disabled).toBe(true); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Read exact metadata recovery" }).disabled).toBe(true);
  fireEvent.change(screen.getByLabelText("Original proxy operation ID"), { target: { value: nextOperation } }); fireEvent.submit(screen.getByRole("form", { name: "Look up metadata recovery operation" })); expect(fetch.mock.calls.some(([url]) => url.includes(nextOperation))).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: `Retry identical recovery ${kind} request` })); await screen.findByRole("region", { name: kind === "plan" ? "Review exact metadata recovery plan" : "Recorded metadata recovery resolution" });
  const bodies = fetch.mock.calls.filter(([url, init]) => init.method === "POST" && url.endsWith(kind === "plan" ? "/recovery-plans" : "/apply")).map(([, init]) => init.body); expect(bodies).toHaveLength(2); expect(bodies[0]).toBe(bodies[1]);
  if (kind === "plan") { expect(screen.getByRole<HTMLButtonElement>("button", { name: "Read current proxy operation" }).disabled).toBe(true); fireEvent.click(screen.getByRole("button", { name: "Read fresh facts for a new review" })); await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Read current proxy operation" }).disabled).toBe(false)); }
  fireEvent.click(screen.getByRole("button", { name: "Read current proxy operation" })); await screen.findByText("Recovery operation: " + nextOperation);
});
test.each([401, 403, 404].flatMap(code => ["review", "resolution"].map(evidence => ({ code, evidence }))))("HTTP$code masks obsolete $evidence in every denied DOM commit before passive cleanup", async ({ code, evidence }) => {
  let rejectRead = false; const commits: string[] = [];
  backend(url => url.endsWith("/recovery") ? rejectRead ? json({}, code) : evidence === "resolution" ? json({ operation_id: "pop_original", eligible: false, reason: "resolved", resolution }) : undefined : undefined);
  setup(["runner_admin"], props, html => commits.push(html)); if (evidence === "review") await requestPlan(); else await screen.findByRole("region", { name: "Recorded metadata recovery resolution" });
  rejectRead = true; fireEvent.click(screen.getByRole("button", { name: "Read metadata recovery status" })); await screen.findByText(/Metadata recovery permission is unavailable/);
  const deniedCommits = commits.filter(html => html.includes("Metadata recovery permission is unavailable") || html.includes("Recovery status is unknown because the read failed")); expect(deniedCommits.length).toBeGreaterThan(0);
  for (const html of deniedCommits) { expect(html).not.toContain('aria-label="Review exact metadata recovery plan"'); expect(html).not.toContain('aria-label="Recorded metadata recovery resolution"'); expect(html).not.toContain("Completion receipt hash"); }
  await waitFor(() => expect(client.getQueriesData({ queryKey: ["proxyRecovery"] }).every(([, data]) => data === undefined)).toBe(true));
});
