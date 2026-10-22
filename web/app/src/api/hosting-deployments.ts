import { queryOptions } from "@tanstack/react-query";
import { request, type RequestOptions } from "./client";

import type { components } from "./generated";
import { ApiError } from "./errors";

export type HostingDeploymentInput = components["schemas"]["HostingDeploymentInput"];
export type HostingOperation = components["schemas"]["HostingOperation"];
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
  return hostingMutation(deploymentsPath(resourceID), body, options);
}
async function hostingMutation(path: string, body: HostingDeploymentInput | HostingCleanupInput, options?: Options): Promise<HostingOperation> {
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
  history: (projectID: string, resourceID: string) => ["hostingDeployments", projectID, resourceID] as const,
  operation: (projectID: string, resourceID: string, id: string) => ["hostingOperation", projectID, resourceID, id] as const,
  retained: (projectID: string, resourceID: string) => ["hostingRetained", projectID, resourceID] as const,
};
export const hostingRetainedQuery = (projectID: string, resourceID: string) => queryOptions({
  queryKey: hostingDeploymentKeys.retained(projectID, resourceID), retry: false,
  queryFn: async ({ signal }) => {
    const releases = await listHostingRetainedReleases(resourceID, { signal });
    if (releases.some((release) => release.healthy.snapshot.resource_id !== resourceID)) throw new Error("Retained release scope mismatch");
    return releases;
  },
});
export const hostingDeploymentsQuery = (projectID: string, resourceID: string, offset: number) => queryOptions({
  queryKey: [...hostingDeploymentKeys.history(projectID, resourceID), offset],
  queryFn: async ({ signal }) => {
    const page = await listHostingDeployments(resourceID, { limit: 20, offset }, { signal });
    if (page.items.some((operation) => operation.project_id !== projectID || operation.resource_id !== resourceID)) throw new Error("Operation history scope mismatch");
    return page;
  }, retry: false,
});
export const hostingOperationQuery = (projectID: string, resourceID: string, id: string) => queryOptions({
  queryKey: hostingDeploymentKeys.operation(projectID, resourceID, id),
  queryFn: async ({ signal }) => {
    const operation = await getHostingOperation(id, { signal });
    if (operation.project_id !== projectID || operation.resource_id !== resourceID || operation.id !== id) throw new Error("Operation scope mismatch");
    return operation;
  }, retry: false,
});
