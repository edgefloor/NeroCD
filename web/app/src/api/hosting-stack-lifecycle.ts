import { queryOptions } from "@tanstack/react-query";
import { request } from "./client";
import { getHostingOperation, type HostingOperation } from "./hosting-deployments";
import { boundedStorageRequest, type StorageDeploymentScope } from "./hosting-storage-deployment";

export type StackLifecycleState = { resource_id: string; state: "running" | "stopped" | "unavailable" | "held"; deployed_operation_id?: string; stop_operation_id?: string; resource_generation: number };
export type StackStopInput = { idempotency_key: string; expected_deployed_operation_id: string; expected_generation: number };
const identifier = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
export function validateStackLifecycle(value: unknown, scope: StorageDeploymentScope): asserts value is StackLifecycleState {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Invalid Compose lifecycle evidence");
  const item = value as Record<string, unknown>;
  if (Object.keys(item).some((key) => !["resource_id", "state", "deployed_operation_id", "stop_operation_id", "resource_generation"].includes(key)) || item.resource_id !== scope.resource || !["running", "stopped", "unavailable", "held"].includes(String(item.state)) || !Number.isSafeInteger(item.resource_generation) || Number(item.resource_generation) < 1 || item.deployed_operation_id !== undefined && !identifier(item.deployed_operation_id) || item.stop_operation_id !== undefined && !identifier(item.stop_operation_id) || item.state === "running" && (!identifier(item.deployed_operation_id) || item.stop_operation_id !== undefined) || item.state === "stopped" && (!identifier(item.deployed_operation_id) || !identifier(item.stop_operation_id))) throw new Error("Compose lifecycle scope or state mismatch");
}
const path = (resource: string) => `/api/v1/resources/${encodeURIComponent(resource)}`;
export async function getStackLifecycle(scope: StorageDeploymentScope, signal?: AbortSignal): Promise<StackLifecycleState> {
  const value: unknown = await boundedStorageRequest((signal) => request(`${path(scope.resource)}/stack-lifecycle`, { signal, cache: "no-store" }), signal);
  validateStackLifecycle(value, scope); return value;
}
export const stackLifecycleKey = (scope: StorageDeploymentScope) => ["stackLifecycle", scope.actor, scope.project, scope.resource] as const;
export const stackLifecycleQuery = (scope: StorageDeploymentScope) => queryOptions({ queryKey: stackLifecycleKey(scope), queryFn: ({ signal }) => getStackLifecycle(scope, signal), retry: false, gcTime: 0, refetchOnWindowFocus: false, refetchOnReconnect: false });
export const currentGitStackKey = (scope: StorageDeploymentScope) => ["currentGitStack", scope.actor, scope.project, scope.resource] as const;
export async function getCurrentGitStack(scope: StorageDeploymentScope, signal?: AbortSignal) {
  const lifecycle = await getStackLifecycle(scope, signal);
  if (lifecycle.state === "unavailable" || !lifecycle.deployed_operation_id) return { lifecycle, operation: null };
  const operation = await boundedStorageRequest(signal => getHostingOperation(lifecycle.deployed_operation_id!, { signal }), signal);
  const stack = "stack" in operation.snapshot ? operation.snapshot.stack : undefined;
  const git = "git_compose" in operation.snapshot ? operation.snapshot.git_compose : undefined;
  if (operation.id !== lifecycle.deployed_operation_id || operation.project_id !== scope.project || operation.resource_id !== scope.resource || operation.state !== "succeeded" ||
      operation.snapshot.resource_id !== scope.resource || operation.snapshot.binding_id !== operation.binding_id ||
      !stack || !Number.isSafeInteger(stack.generation) || stack.generation < 1 || !/^sha256:[a-f0-9]{64}$/.test(stack.hash) ||
      !git || !identifier(git.build_id) || !Number.isSafeInteger(git.source_generation) || git.source_generation < 1 || !/^sha256:[a-f0-9]{64}$/.test(git.result_hash))
    throw new Error("Current Git Compose deployment evidence mismatch");
  return { lifecycle, operation };
}
export const currentGitStackQuery = (scope: StorageDeploymentScope) => queryOptions({ queryKey: currentGitStackKey(scope), queryFn: ({ signal }) => getCurrentGitStack(scope, signal), retry: false, gcTime: 0, refetchOnWindowFocus: false, refetchOnReconnect: false });
export async function requestStackStop(scope: StorageDeploymentScope, body: StackStopInput, signal?: AbortSignal): Promise<HostingOperation> {
  const operation = await boundedStorageRequest((signal) => request<HostingOperation>(`${path(scope.resource)}/stack-stop`, { method: "POST", body, signal, cache: "no-store" }), signal);
  const plan = operation?.stack_stop_plan, target = plan?.target;
  if (!identifier(operation?.id) || operation.resource_id !== scope.resource || operation.project_id !== scope.project || !target || target.operation_id !== body.expected_deployed_operation_id || target.snapshot?.resource_id !== scope.resource || target.snapshot.binding_id !== operation.binding_id || operation.snapshot?.resource_id !== scope.resource || operation.snapshot.binding_id !== operation.binding_id || !target.stack || !plan?.target_plan?.stack || plan.target_plan.snapshot.resource_id !== scope.resource || plan.target_plan.snapshot.binding_id !== operation.binding_id || plan.target_plan.hash !== target.plan_hash || !/^sha256:[a-f0-9]{64}$/.test(plan.hash) || !plan.compiler_version) throw new Error("Compose stop receipt mismatch");
  return operation;
}
