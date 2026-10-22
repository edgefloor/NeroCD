import { queryOptions } from "@tanstack/react-query";
import { request, type RequestOptions } from "./client";

// Temporary public contract until the accepted backend supplies generated types.
// Runner-only authority, lease and fence documents are intentionally excluded.
export type HostingDeploymentInput = {
  expected_generation: number;
  idempotency_key: string;
  container_port: number;
  host_port: number;
  readiness: { path: string; timeout_seconds: number };
};
export type HostingOperation = {
  id: string;
  project_id: string;
  resource_id: string;
  binding_id: string;
  state: string;
  snapshot: {
    resource_id: string; resource_generation: number; image_reference: string;
    binding_id: string; destination_id: string; server_id: string; runner_id: string;
    runtime_namespace: string; container_port: number; host_port: number;
    readiness: { path: string; timeout_seconds: number }; policy_version: string;
  };
  mutation_hold: boolean;
  reconcile_requested: boolean;
  created_by: string;
  created_at: string;
  updated_at: string;
  run_id?: string;
  reconciliation_run_id?: string;
  release?: { canonical_image: string; image_id: string; daemon_id: string; hash: string; os: string; architecture: string; variant?: string };
  result?: { outcome: string; ready: boolean; restored: boolean; uncertain: boolean; container_id?: string; previous_container_id?: string; image_id?: string; daemon_id?: string; plan_hash?: string };
};
export type HostingOperationList = { items: HostingOperation[]; count: number; total: number; limit: number; offset: number };
type Options = Pick<RequestOptions, "signal">;
const deploymentsPath = (id: string) => `/api/v1/resources/${encodeURIComponent(id)}/deployments`;
const operationPath = (id: string) => `/api/v1/hosting-operations/${encodeURIComponent(id)}`;
export const requestHostingDeployment = (resourceID: string, body: HostingDeploymentInput, options?: Options): Promise<HostingOperation> => request(deploymentsPath(resourceID), { ...options, method: "POST", body });
export const listHostingDeployments = (resourceID: string, query: { limit: number; offset: number }, options?: Options): Promise<HostingOperationList> => request(deploymentsPath(resourceID), { ...options, query });
export const getHostingOperation = (id: string, options?: Options): Promise<HostingOperation> => request(operationPath(id), options);
export const cancelHostingOperation = (id: string, options?: Options): Promise<HostingOperation> => request(`${operationPath(id)}/cancel`, { ...options, method: "POST", body: {} });
export const reconcileHostingOperation = (id: string, options?: Options): Promise<HostingOperation> => request(`${operationPath(id)}/reconcile`, { ...options, method: "POST", body: {} });
export const hostingDeploymentKeys = {
  history: (projectID: string, resourceID: string) => ["hostingDeployments", projectID, resourceID] as const,
  operation: (projectID: string, resourceID: string, id: string) => ["hostingOperation", projectID, resourceID, id] as const,
};
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
