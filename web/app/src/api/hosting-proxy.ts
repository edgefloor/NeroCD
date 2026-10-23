import { queryOptions } from "@tanstack/react-query";
import { request, type RequestOptions } from "./client";
import { ApiError } from "./errors";
import type { components } from "./generated";
type Schema<K extends keyof components["schemas"]> = components["schemas"][K];
type Options = Pick<RequestOptions, "signal">;
export type HostingProxy = Schema<"HostingProxy">;
export type ProxyInput = Schema<"HostingProxyInput">;
export type HostnameGrant = Schema<"HostingHostnameGrant">;
export type GrantInput = Schema<"HostingHostnameGrantInput">;
export type HostingRoute = Schema<"HostingRoute">;
export type RouteInput = Schema<"HostingRouteInput">;
export type RouteDeletion = Schema<"HostingRouteDeletionInput">;
export type ProxyOperation = Schema<"HostingProxyOperation">;
export type ProxyOperationView = Schema<"HostingProxyOperationView">;
const serverPath = (id: string) => `/api/v1/servers/${encodeURIComponent(id)}/proxy`;
const routesPath = (id: string) => `/api/v1/resources/${encodeURIComponent(id)}/routes`;
const routePath = (id: string) => `/api/v1/routes/${encodeURIComponent(id)}`;
const operationPath = (id: string) => `/api/v1/proxy-operations/${encodeURIComponent(id)}`;
export const getProxy = (id: string, options?: Options): Promise<HostingProxy> => request(serverPath(id), options);
export const configureProxy = (id: string, body: ProxyInput, options?: Options): Promise<HostingProxy> => request(serverPath(id), { ...options, method: "POST", body });
export const disableProxy = (id: string, body: { idempotency_key: string }, options?: Options): Promise<HostingProxy> => request(`${serverPath(id)}/disable`, { ...options, method: "POST", body });
export const listHostnameGrants = (id: string, options?: Options): Promise<HostnameGrant[]> => request(`${serverPath(id)}/hostname-grants`, options);
export const grantHostname = (id: string, body: GrantInput, options?: Options): Promise<HostnameGrant> => request(`${serverPath(id)}/hostname-grants`, { ...options, method: "POST", body });
export const revokeHostname = (id: string, grant: string, body: { idempotency_key: string }, options?: Options): Promise<HostingProxy> => request(`${serverPath(id)}/hostname-grants/${encodeURIComponent(grant)}/revoke`, { ...options, method: "POST", body });
export const listRoutes = (id: string, options?: Options): Promise<HostingRoute[]> => request(routesPath(id), options);
export const createRoute = (id: string, body: RouteInput, options?: Options): Promise<HostingRoute> => request(routesPath(id), { ...options, method: "POST", body });
export const updateRoute = (id: string, body: RouteInput, options?: Options): Promise<HostingRoute> => request(routePath(id), { ...options, method: "PATCH", body });
export const deleteRoute = (id: string, body: RouteDeletion, options?: Options): Promise<HostingRoute> => request(routePath(id), { ...options, method: "DELETE", body });
export const getProxyOperation = (id: string, options?: Options): Promise<ProxyOperation> => request(operationPath(id), options);
export const cancelProxyOperation = (id: string, options?: Options): Promise<ProxyOperation> => request(`${operationPath(id)}/cancel`, { ...options, method: "POST", body: {} });
export const proxyKeys = {
  server: (actor: string, server: string) => ["hostingProxy", actor, server] as const,
  grants: (actor: string, server: string) => ["hostnameGrants", actor, server] as const,
  routes: (actor: string, project: string, resource: string) => ["hostingRoutes", actor, project, resource] as const,
  operation: (actor: string, scope: string, id: string) => ["proxyOperation", actor, scope, id] as const,
};
export const proxyQuery = (actor: string, server: string) => queryOptions({ queryKey: proxyKeys.server(actor, server), retry: false, queryFn: async ({ signal }) => {
  try { const p = await getProxy(server, { signal }); if (p.server_id !== server) throw new Error("Proxy scope mismatch"); return p; }
  catch (cause) { if (cause instanceof ApiError && cause.status === 404) return null; throw cause; }
} });
export const grantsQuery = (actor: string, server: string) => queryOptions({ queryKey: proxyKeys.grants(actor, server), retry: false, queryFn: ({ signal }) => listHostnameGrants(server, { signal }) });
export const routesQuery = (actor: string, project: string, resource: string) => queryOptions({ queryKey: proxyKeys.routes(actor, project, resource), retry: false, queryFn: async ({ signal }) => {
  const routes = await listRoutes(resource, { signal });
  if (routes.some((route) => route.resource_id !== resource)) throw new Error("Route scope mismatch");
  return routes;
} });
// Never place an administrator's whole-proxy snapshot into a resource query cache.
// Even administrator responses are projected to own-resource status before return.
export const routeOperationQuery = (actor: string, project: string, resource: string, id: string) => queryOptions({ queryKey: proxyKeys.operation(actor, `${project}:${resource}`, id), retry: false, queryFn: async ({ signal }): Promise<ProxyOperationView> => {
  const raw = await request<ProxyOperation | ProxyOperationView>(operationPath(id), { signal });
  if (raw.id !== id) throw new Error("Proxy operation scope mismatch");
  return { ...(raw.metadata_conflict !== undefined ? { metadata_conflict: raw.metadata_conflict } : {}), ...("engine_outcome" in raw && raw.engine_outcome ? { engine_outcome: raw.engine_outcome } : "result" in raw && raw.result?.outcome ? { engine_outcome: raw.result.outcome } : {}), id: raw.id, state: raw.state, mutation_hold: raw.mutation_hold, restore_ownership_held: raw.restore_ownership_held, routes: "routes" in raw ? raw.routes.filter((route) => route.resource_id === resource) : [] };
} });
export function proxyError(cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.status === 409) return "Conflict with current version, healthy release, hostname authority, or retained ownership. Refresh and review explicitly; nothing was resubmitted.";
    if (cause.status === 403) return "Permission denied. Review current project and server permissions.";
    if (cause.status === 503) return "Proxy storage or dispatch is unavailable. Your draft and exact request are preserved.";
    if (cause.status === 400) return "The server rejected this input. Review hostname, literal path, settings and required identifiers.";
  }
  return "The response was not confirmed. Keep the frozen request for an identical retry, or refresh and review before a separate request.";
}
