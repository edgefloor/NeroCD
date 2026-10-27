import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { StackLifecycle } from "./StackLifecycle";
const scope = { actor: "u", project: "p", resource: "r" }, digest = "sha256:" + "a".repeat(64);
const running = { resource_id: "r", resource_generation: 2, state: "running", deployed_operation_id: "deploy" };
const snapshot = { resource_id: "r", project_id: "p", binding_id: "b", resource_generation: 1 };
const receipt = { id: "stop", project_id: "p", resource_id: "r", binding_id: "b", snapshot, stack_stop_plan: { target: { operation_id: "deploy", snapshot, stack: {}, plan_hash: digest }, target_plan: { snapshot, stack: {}, hash: digest }, hash: digest, compiler_version: "managed-compose.stop.v1" } };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
let client: QueryClient;
afterEach(() => { cleanup(); client?.clear(); vi.unstubAllGlobals(); });
function setup(handler?: (url: string, options: RequestInit) => Response | Promise<Response> | undefined, writable = true) {
  const fetch = vi.fn(async (url: string, options: RequestInit) => await handler?.(url, options) ?? json(url.endsWith("/stack-lifecycle") ? running : receipt)); vi.stubGlobal("fetch", fetch);
  client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  const denied = vi.fn(), selected = vi.fn(), refresh = vi.fn(async () => {});
  const props = { scope, generation: 2, writable, blocked: false, mutationHeld: false, onDenied: denied, onSelected: selected, onRefresh: refresh };
  const view = render(<QueryClientProvider client={client}><StackLifecycle key="r" {...props} /></QueryClientProvider>);
  return { fetch, denied, selected, props, view };
}
async function review() { fireEvent.click(await screen.findByRole("button", { name: "Review Stop Compose" })); await screen.findByRole("region", { name: "Reviewed Compose stop" }); }
test("Stop requires current review and confirmation, then reports acceptance without claiming stopped", async () => {
  const { fetch, selected } = setup(); await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Review Stop Compose" }).disabled).toBe(false));
  expect(fetch.mock.calls.some(([, options]) => options.method === "POST")).toBe(false);
  await review(); expect(screen.getByText(/Named volumes and application data are retained/)).toBeTruthy();
  expect(fetch.mock.calls.some(([, options]) => options.method === "POST")).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: "Confirm Stop Compose" }));
  await screen.findByText(/Stop request accepted: stop/); expect(selected).toHaveBeenCalledWith("stop");
  const write = fetch.mock.calls.find(([, options]) => options.method === "POST")!;
  expect(JSON.parse(String(write[1].body))).toMatchObject({ expected_generation: 2, expected_deployed_operation_id: "deploy" });
});
test("lost Stop receipt retries the identical reviewed request", async () => {
  let count = 0; const { fetch } = setup((_url, options) => options.method === "POST" && ++count === 1 ? Promise.reject(new Error("lost")) : undefined);
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Review Stop Compose" }).disabled).toBe(false)); await review();
  fireEvent.click(screen.getByRole("button", { name: "Confirm Stop Compose" })); await screen.findByRole("alert");
  fireEvent.click(screen.getByRole("button", { name: "Retry identical Compose Stop request" })); await screen.findByText(/Stop request accepted/);
  const writes = fetch.mock.calls.filter(([, options]) => options.method === "POST"); expect(writes).toHaveLength(2); expect(writes[0]![1].body).toBe(writes[1]![1].body);
});
test.each(["held", "unavailable", "stopped"])("recorded %s blocks Stop", async (state) => {
  setup(() => json({ ...running, state, ...(state === "stopped" ? { stop_operation_id: "prior_stop" } : {}) }));
  await screen.findByText(new RegExp("Recorded lifecycle: " + state)); expect(screen.getByRole<HTMLButtonElement>("button", { name: "Review Stop Compose" }).disabled).toBe(true);
});
test.each([401, 403])("Stop HTTP%s clears its review and reports permission loss", async (status) => {
  const { denied } = setup((_url, options) => options.method === "POST" ? json({}, status) : undefined);
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Review Stop Compose" }).disabled).toBe(false)); await review();
  fireEvent.click(screen.getByRole("button", { name: "Confirm Stop Compose" })); await waitFor(() => expect(denied).toHaveBeenCalled()); expect(screen.queryByRole("region", { name: "Reviewed Compose stop" })).toBeNull();
});
test("retired Stop response cannot select an operation in a new resource scope", async () => {
  let resolve!: (value: Response) => void;
  const { selected, props, view, fetch } = setup((_url, options) => options.method === "POST" ? new Promise<Response>((done) => { resolve = done; }) : undefined);
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Review Stop Compose" }).disabled).toBe(false)); await review(); fireEvent.click(screen.getByRole("button", { name: "Confirm Stop Compose" }));
  const signal = fetch.mock.calls.find(([, options]) => options.method === "POST")![1].signal;
  view.rerender(<QueryClientProvider client={client}><StackLifecycle key="other" {...props} scope={{ ...scope, resource: "other" }} /></QueryClientProvider>);
  expect(signal?.aborted).toBe(true); await act(async () => resolve(json(receipt))); expect(selected).not.toHaveBeenCalled(); expect(screen.queryByRole("region", { name: "Reviewed Compose stop" })).toBeNull();
});
test("viewers receive recorded lifecycle without Stop controls", async () => { setup(undefined, false); await screen.findByText(/Recorded lifecycle: running/); expect(screen.queryByRole("button", { name: "Review Stop Compose" })).toBeNull(); });

test("stale saved generation cannot create a Stop review or request", async () => {
  const { fetch } = setup(() => json({ ...running, resource_generation: 3 }));
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Review Stop Compose" }).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "Review Stop Compose" })); await screen.findByRole("alert");
  expect(screen.queryByRole("region", { name: "Reviewed Compose stop" })).toBeNull(); expect(fetch.mock.calls.some(([, options]) => options.method === "POST")).toBe(false);
});
test("Stop conflicts require a fresh review and explicit confirmation without automatic resubmission", async () => {
  const { fetch } = setup((_url, options) => options.method === "POST" ? json({}, 409) : undefined);
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Review Stop Compose" }).disabled).toBe(false)); await review();
  fireEvent.click(screen.getByRole("button", { name: "Confirm Stop Compose" })); await screen.findByRole("alert");
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Retry identical Compose Stop request" }).disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Review current Compose Stop" })); await screen.findByRole("button", { name: "Confirm Stop Compose" });
  expect(fetch.mock.calls.filter(([, options]) => options.method === "POST")).toHaveLength(1);
});
