import { queryOptions } from "@tanstack/react-query";
import { request, type RequestOptions } from "./client";

import type { components } from "./generated";
import { ApiError } from "./errors";
import { getResourceBinding } from "./destinations";

export type HostingBuildDeploymentInput = components["schemas"]["HostingBuildDeploymentInput"];
export type HostingDeploymentInput = components["schemas"]["HostingDeploymentInput"] & { managed_stack?: { acknowledge_interruption: boolean; expected_stopped_operation_id?: string } };
export type HostingOperation = components["schemas"]["HostingOperation"] & {
  stack_stop_plan?: { target: components["schemas"]["HostingHealthy"]; target_plan: components["schemas"]["HostingApplyPlan"]; compiler_version: string; hash: string };
  snapshot: { stack_stopped_operation_id?: string };
  plan?: { previous_stopped?: { stop_operation_id: string; target: components["schemas"]["HostingHealthy"] } };
  result?: { stack_stopped?: boolean };
};
export type HostingOperationList = components["schemas"]["HostingOperationList"];
export type HostingCleanupInput = components["schemas"]["HostingCleanupInput"];
export type HostingRetainedRelease = components["schemas"]["HostingRetainedRelease"];
export class HostingDeploymentError extends ApiError {
  constructor(status: number, message: string, requestID: string | null, readonly code?: string) {
    super(status, message, requestID);
  }
}
type Options = Pick<RequestOptions, "signal">;
const deploymentsPath = (id: string) => `/api/v1/resources/${encodeURIComponent(id)}/deployments`;
const operationPath = (id: string) => `/api/v1/hosting-operations/${encodeURIComponent(id)}`;
// Preserve the structured admission code, which the shared generic client does
// not expose. A restored binding must never become a generation-retry flow.
export async function requestHostingDeployment(resourceID: string, body: HostingDeploymentInput, options?: Options): Promise<HostingOperation> {
  if (body.registry_credential !== undefined) {
    const ref = body.registry_credential;
    if (!ref || Object.keys(ref).some((key) => !["secret_id", "version"].includes(key)) || typeof ref.secret_id !== "string" || !/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(ref.secret_id) || !Number.isSafeInteger(ref.version) || ref.version < 1)
      throw new Error("Invalid registry credential reference");
    return hostingMutation(deploymentsPath(resourceID), { ...body, registry_credential: { secret_id: ref.secret_id, version: ref.version } }, options);
  }
  return hostingMutation(deploymentsPath(resourceID), body, options);
}
export async function requestHostingBuildDeployment(resourceID: string, body: HostingBuildDeploymentInput, options?: Options): Promise<HostingOperation> {
  return hostingMutation(`${deploymentsPath(resourceID)}/from-build`, body, options);
}
async function hostingMutation(path: string, body: HostingDeploymentInput | HostingBuildDeploymentInput | HostingCleanupInput, options?: Options): Promise<HostingOperation> {
  const response = await fetch(path, {
    method: "POST", credentials: "include", signal: options?.signal,
    headers: { Accept: "application/json", "Content-Type": "application/json", "X-NeroCD-CSRF": "1" }, body: JSON.stringify(body),
  });
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`.trim();
    let code: string | undefined;
    try {
      const error = await response.json() as { code?: unknown; error?: unknown };
      if (typeof error.error === "string") message = error.error;
      if (typeof error.code === "string") code = error.code;
    } catch { /* non-JSON errors retain the status */ }
    throw new HostingDeploymentError(response.status, message, response.headers.get("X-Request-ID"), code);
  }
  return await response.json() as HostingOperation;
}
const retainedPath = (id: string) => `/api/v1/resources/${encodeURIComponent(id)}/retained-releases`;
export const listHostingRetainedReleases = (resourceID: string, options?: Options): Promise<HostingRetainedRelease[]> => request(retainedPath(resourceID), options);
export const requestHostingReleaseCleanup = (resourceID: string, operationID: string, body: HostingCleanupInput, options?: Options): Promise<HostingOperation> => hostingMutation(`${retainedPath(resourceID)}/${encodeURIComponent(operationID)}/cleanup`, body, options);
export const listHostingDeployments = (resourceID: string, query: { limit: number; offset: number }, options?: Options): Promise<HostingOperationList> => request(deploymentsPath(resourceID), { ...options, query });
export const getHostingOperation = (id: string, options?: Options): Promise<HostingOperation> => request(operationPath(id), options);
export const cancelHostingOperation = (id: string, options?: Options): Promise<HostingOperation> => request(`${operationPath(id)}/cancel`, { ...options, method: "POST", body: {} });
export const reconcileHostingOperation = (id: string, options?: Options): Promise<HostingOperation> => request(`${operationPath(id)}/reconcile`, { ...options, method: "POST", body: {} });
export const hostingDeploymentKeys = {
  history: (projectID: string, resourceID: string, actor?: string) => ["hostingDeployments", projectID, resourceID, ...(actor ? [actor] : [])] as const,
  operation: (projectID: string, resourceID: string, id: string, actor?: string) => ["hostingOperation", projectID, resourceID, id, ...(actor ? [actor] : [])] as const,
  retained: (projectID: string, resourceID: string, actor?: string) => ["hostingRetained", projectID, resourceID, ...(actor ? [actor] : [])] as const,
};
export const hostingRetainedQuery = (projectID: string, resourceID: string, actor?: string) => queryOptions({
  queryKey: hostingDeploymentKeys.retained(projectID, resourceID, actor), retry: false,
  queryFn: async ({ signal }) => {
    const releases = await listHostingRetainedReleases(resourceID, { signal });
    if (releases.some((release) => release.healthy.snapshot.resource_id !== resourceID)) throw new Error("Retained release scope mismatch");
    return releases;
  },
});
export const hostingDeploymentsQuery = (projectID: string, resourceID: string, offset: number, actor?: string) => queryOptions({
  queryKey: [...hostingDeploymentKeys.history(projectID, resourceID, actor), offset],
  queryFn: async ({ signal }) => {
    const page = await listHostingDeployments(resourceID, { limit: 20, offset }, { signal });
    if (page.items.some((operation) => operation.project_id !== projectID || operation.resource_id !== resourceID)) throw new Error("Operation history scope mismatch");
    return page;
  }, retry: false,
});
export const hostingOperationQuery = (projectID: string, resourceID: string, id: string, actor?: string) => queryOptions({
  queryKey: hostingDeploymentKeys.operation(projectID, resourceID, id, actor),
  queryFn: async ({ signal }) => {
    const operation = await getHostingOperation(id, { signal });
    if (operation.project_id !== projectID || operation.resource_id !== resourceID || operation.id !== id) throw new Error("Operation scope mismatch");
    return operation;
  }, retry: false,
});

export const hostingBindingQuery = (projectID: string, resourceID: string, actor: string) => queryOptions({
  queryKey: ["resourceBinding", projectID, resourceID, actor], retry: false,
  queryFn: async ({ signal }) => {
    try {
      const binding = await getResourceBinding(resourceID, { signal });
      if (binding.resource_id !== resourceID) throw new Error("Deployment binding scope mismatch");
      return binding;
    } catch (cause) { if (cause instanceof ApiError && cause.status === 404) return null; throw cause; }
  },
});
