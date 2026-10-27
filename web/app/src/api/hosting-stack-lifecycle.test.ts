import { afterEach, expect, test, vi } from "vitest";
import { getStackLifecycle, requestStackStop, stackLifecycleKey, validateStackLifecycle } from "./hosting-stack-lifecycle";
const scope = { actor: "u", project: "p", resource: "r" }, digest = "sha256:" + "a".repeat(64);
const running = { resource_id: "r", resource_generation: 2, state: "running", deployed_operation_id: "deploy" };
const body = Object.freeze({ idempotency_key: "key", expected_deployed_operation_id: "deploy", expected_generation: 2 });
const snapshot = { resource_id: "r", project_id: "p", binding_id: "b", resource_generation: 1 };
export const stopReceipt = { id: "stop", project_id: "p", resource_id: "r", binding_id: "b", snapshot, stack_stop_plan: { target: { operation_id: "deploy", snapshot, stack: {}, plan_hash: digest }, target_plan: { snapshot, stack: {}, hash: digest }, hash: digest, compiler_version: "managed-compose.stop.v1" } };
afterEach(() => { vi.unstubAllGlobals(); });
test("lifecycle reads and exact Stop retries are scoped, abortable and no-store", async () => {
  const fetch = vi.fn(async (url: string, _options: RequestInit) => new Response(JSON.stringify(url.endsWith("/stack-lifecycle") ? running : stopReceipt))); vi.stubGlobal("fetch", fetch);
  const signal = new AbortController().signal;
  expect(await getStackLifecycle(scope, signal)).toEqual(running);
  await requestStackStop(scope, body, signal); await requestStackStop(scope, body, signal);
  expect(fetch.mock.calls[0]).toMatchObject(["/api/v1/resources/r/stack-lifecycle", { signal, cache: "no-store", method: "GET" }]);
  expect(fetch.mock.calls[1]).toMatchObject(["/api/v1/resources/r/stack-stop", { signal, cache: "no-store", method: "POST", body: JSON.stringify(body) }]);
  expect(fetch.mock.calls[1]![0]).toBe(fetch.mock.calls[2]![0]); expect(fetch.mock.calls[1]![1].body).toBe(fetch.mock.calls[2]![1].body);
  expect(stackLifecycleKey(scope)).not.toEqual(stackLifecycleKey({ ...scope, actor: "other" }));
});
test.each([{ ...running, resource_id: "other" }, { ...running, state: "healthy" }, { ...running, resource_generation: 0 }, { ...running, deployed_operation_id: undefined }, { ...running, stop_operation_id: "stop" }, { ...running, state: "stopped" }, { ...running, extra: true }])("rejects malformed lifecycle evidence %j", (value) => expect(() => validateStackLifecycle(value, scope)).toThrow());
test.each([401, 403])("permission HTTP%s is preserved for clearing UI drafts", async (status) => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response("", { status })));
  await expect(getStackLifecycle(scope)).rejects.toMatchObject({ status }); await expect(requestStackStop(scope, body)).rejects.toMatchObject({ status });
});
test.each([{ ...stopReceipt, resource_id: "other" }, { ...stopReceipt, project_id: "other" }, { ...stopReceipt, stack_stop_plan: undefined }, { ...stopReceipt, stack_stop_plan: { ...stopReceipt.stack_stop_plan, target: { ...stopReceipt.stack_stop_plan.target, operation_id: "other" } } }, { ...stopReceipt, stack_stop_plan: { ...stopReceipt.stack_stop_plan, target_plan: { ...stopReceipt.stack_stop_plan.target_plan, hash: "foreign" } } }])("rejects a foreign or mismatched Stop receipt %j", async (value) => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(value)))); await expect(requestStackStop(scope, body)).rejects.toThrow();
});
