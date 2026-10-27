import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { queryKeys } from "@/api";
import type { HostingOperation } from "@/api/hosting-deployments";
import { StorageContinuityDetail } from "./StorageDeploymentReview";

const scope = { actor: JSON.stringify(["oidc", "user"]), project: "project", resource: "resource" };
const descriptor = `sha256:${"a".repeat(64)}`, receiptHash = `sha256:${"b".repeat(64)}`;
const record = { id: "hop", project_id: "project", resource_id: "resource", binding_id: "binding", state: "succeeded", mutation_hold: false, restore_ownership_held: false, reconcile_requested: false,
  snapshot: { resource_id: "resource", storage_route_continuity: { hash: descriptor }, stack: { generation: 2, hash: `sha256:${"c".repeat(64)}` } },
  complete_receipt: { id: "complete_hop", body_hash: receiptHash, created_at: "" }, result: { outcome: "succeeded", ready: true, uncertain: false, restored: false },
} as HostingOperation;
const input = { expected_descriptor_hash: descriptor, expected_complete_receipt_id: "complete_hop", expected_complete_receipt_hash: receiptHash };
const unresolved = { resource_id: "resource", operation_id: "hop", state: "unresolved", reason: "reconciliation_required" };
const queued = { ...unresolved, state: "proxy_pending", reason: "pending", successor_proxy_operation_id: "pop_storage_hop" };
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
let client: QueryClient;
afterEach(() => { cleanup(); client?.clear(); vi.unstubAllGlobals(); });
function setup(current = record, status = unresolved, handler?: (url: string, init: RequestInit) => Response | Promise<Response> | undefined, writable = true) {
  const fetch = vi.fn(async (url: string, init: RequestInit) => await handler?.(url, init) ?? json(url.endsWith("/recover") ? queued : url.endsWith("/hosting-operations/hop") ? current : status));
  vi.stubGlobal("fetch", fetch);
  client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  client.setQueryData(queryKeys.principal, { id: "user", provider: "oidc", roles: [] });
  client.setQueryData(queryKeys.allProjectMembers, [{ project_id: "project", user_id: "user", role: "maintainer" }]);
  const view = render(<QueryClientProvider client={client}><StorageContinuityDetail scope={scope} operation="hop" recovery={{ record: current, writable, blocked: false }} /></QueryClientProvider>);
  return { fetch, ...view };
}
async function review() {
  fireEvent.click(await screen.findByRole("button", { name: "Review proxy continuity recovery" }));
  fireEvent.click(await screen.findByLabelText("I reviewed this completed deployment and want to retry its proxy continuity queue."));
  fireEvent.click(screen.getByRole("button", { name: "Retry proxy continuity queue" }));
}
test.each(["compose", "git"])("healthy %s history reviews exact receipt and requests only proxy recovery", async (family) => {
  const current = family === "git" ? { ...record, snapshot: { ...record.snapshot, git_compose: { build_id: "build", source_generation: 2, result_hash: descriptor } } } : record;
  const { fetch } = setup(current as HostingOperation); await review();
  await waitFor(() => expect(fetch.mock.calls.filter(([url]) => url.endsWith("/recover"))).toHaveLength(1));
  expect(screen.getByText(/does not deploy containers or recreate data volumes/)).toBeTruthy();
  const writes = fetch.mock.calls.filter(([, init]) => init.method === "POST");
  expect(writes).toHaveLength(1); expect(writes[0]![0]).toBe("/api/v1/resources/resource/storage-route-continuity/hop/recover");
  expect(JSON.parse(String(writes[0]![1].body))).toEqual(input);
});
test("lost recovery response retains identical evidence after status refresh and sends no deployment", async () => {
  let writes = 0;
  const { fetch } = setup(record, unresolved, url => url.endsWith("/recover") && ++writes === 1 ? Promise.reject(new TypeError("lost")) : undefined);
  await review(); await screen.findByText(/Recovery delivery is unconfirmed/);
  fireEvent.click(screen.getByRole("button", { name: "Refresh continuity evidence" }));
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical proxy continuity recovery" }).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "Retry identical proxy continuity recovery" }));
  await waitFor(() => expect(fetch.mock.calls.filter(([url]) => url.endsWith("/recover"))).toHaveLength(2));
  const requests = fetch.mock.calls.filter(([url]) => url.endsWith("/recover"));
  expect(requests[1]![1].body).toBe(requests[0]![1].body);
  expect(fetch.mock.calls.filter(([url]) => url.endsWith("/hosting-operations/hop"))).toHaveLength(1);
});
test.each([
  { ...record, state: "failed" }, { ...record, mutation_hold: true }, { ...record, restore_ownership_held: true }, { ...record, reconcile_requested: true },
  { ...record, complete_receipt: undefined }, { ...record, result: { ...record.result!, ready: false } }, { ...record, result: { ...record.result!, uncertain: true } },
  { ...record, result: { ...record.result!, restored: true } }, { ...record, complete_receipt: { ...record.complete_receipt!, body_hash: "invalid" } },
])("unqualified workload evidence never offers proxy queue recovery %#", async (current) => {
  setup(current as HostingOperation); await screen.findByText(/Continuity is unresolved/);
  expect(screen.queryByRole("button", { name: "Review proxy continuity recovery" })).toBeNull();
});
test.each(["loaded", "proxy_pending", "proxy_failed", "released"])("%s history never offers a fresh recovery", async (state) => {
  setup(record, { ...unresolved, state, ...(state === "loaded" ? { reason: "loaded_historical", loaded_generation: 3 } : {}) });
  await screen.findByText(`Recorded continuity state: ${state}`);
  expect(screen.queryByRole("button", { name: "Review proxy continuity recovery" })).toBeNull();
});
test("changed receipt during review fails closed before POST", async () => {
  const { fetch } = setup(record, unresolved, url => url.endsWith("/hosting-operations/hop") ? json({ ...record, complete_receipt: { ...record.complete_receipt!, id: "other_receipt" } }) : undefined);
  fireEvent.click(await screen.findByRole("button", { name: "Review proxy continuity recovery" }));
  await screen.findByText(/Unable to review current recovery evidence/);
  expect(fetch.mock.calls.some(([, init]) => init.method === "POST")).toBe(false);
});
test.each([403, 409])("HTTP %s stops recovery without automatic evidence changes", async (status) => {
  const { fetch } = setup(record, unresolved, url => url.endsWith("/recover") ? json({}, status) : undefined);
  await review(); await screen.findByText(status === 403 ? /Recovery permission is unavailable/ : /Recovery conflicts with current evidence/);
  expect(fetch.mock.calls.filter(([url]) => url.endsWith("/recover"))).toHaveLength(1);
  const retry = screen.queryByRole<HTMLButtonElement>("button", { name: "Retry identical proxy continuity recovery" });
  expect(!retry || retry.disabled).toBe(true);
});
test("membership downgrade aborts pending recovery and removes its retained intent", async () => {
  let finish!: (response: Response) => void;
  const { fetch } = setup(record, unresolved, url => url.endsWith("/recover") ? new Promise<Response>(resolve => { finish = resolve; }) : undefined);
  await review(); await waitFor(() => expect(finish).toBeTypeOf("function"));
  const request = fetch.mock.calls.find(([url]) => url.endsWith("/recover"))![1];
  await act(async () => client.setQueryData(queryKeys.allProjectMembers, [{ project_id: "project", user_id: "user", role: "viewer" }]));
  await waitFor(() => expect(request.signal?.aborted).toBe(true));
  expect(screen.queryByRole("region", { name: "Proxy continuity recovery" })).toBeNull();
  await act(async () => finish(json(queued)));
});

test("foreign linked proxy receipt remains unconfirmed for an identical retry", async () => {
  setup(record, unresolved, url => url.endsWith("/recover") ? json({ ...queued, successor_proxy_operation_id: "foreign_proxy" }) : undefined);
  await review(); await screen.findByText(/Recovery delivery is unconfirmed/);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical proxy continuity recovery" }).disabled).toBe(false);
});
