import { queryOptions } from "@tanstack/react-query";
import type { components } from "./generated";
import { request, type RequestOptions } from "./client";
import { ApiError } from "./errors";

type Schema<Name extends keyof components["schemas"]> = components["schemas"][Name];
type Options = Pick<RequestOptions, "signal">;
export type HostingServer = Schema<"HostingServer">;
export type HostingDestination = Schema<"HostingDestination">;
export type ResourceBinding = Schema<"ResourceBinding">;
type Page = { limit: number; offset: number };
export type DestinationScope = Page & ({ project_id: string; server_id?: never } | { server_id: string; project_id?: never });
const destinationPath = (id: string) => `/api/v1/destinations/${encodeURIComponent(id)}`;
const bindingPath = (id: string) => `/api/v1/resources/${encodeURIComponent(id)}/binding`;
export function listHostingServers(query: Page, options?: Options): Promise<Schema<"HostingServerList">> { return request("/api/v1/servers", { ...options, query }); }
export function createHostingServer(body: Schema<"HostingServerInput">, options?: Options): Promise<Schema<"HostingServerDetail">> { return request("/api/v1/servers", { ...options, method: "POST", body }); }
export function getHostingServer(id: string, options?: Options): Promise<Schema<"HostingServerDetail">> { return request(`/api/v1/servers/${encodeURIComponent(id)}`, options); }
export function listDestinations(query: DestinationScope, options?: Options): Promise<Schema<"HostingDestinationList">> { return request("/api/v1/destinations", { ...options, query }); }
export function listDestinationGrants(id: string, query: Page, options?: Options): Promise<Schema<"DestinationProjectGrantList">> { return request(`${destinationPath(id)}/project-grants`, { ...options, query }); }
export function grantDestinationProject(id: string, projectID: string, options?: Options): Promise<Schema<"DestinationProjectGrant">> { return request(`${destinationPath(id)}/project-grants`, { ...options, method: "POST", body: { project_id: projectID } }); }
export function revokeDestinationProject(id: string, projectID: string, options?: Options): Promise<Schema<"DestinationProjectGrant">> { return request(`${destinationPath(id)}/project-grants/revoke`, { ...options, method: "POST", body: { project_id: projectID } }); }
export function getResourceBinding(id: string, options?: Options): Promise<ResourceBinding> { return request(bindingPath(id), options); }
export function bindResourceDestination(id: string, destinationID: string, options?: Options): Promise<ResourceBinding> { return request(bindingPath(id), { ...options, method: "POST", body: { destination_id: destinationID } }); }
export const destinationKeys = {
  servers: ["hostingServers"] as const,
  server: (id: string) => ["hostingServer", id] as const,
  grants: (id: string) => ["destinationGrants", id] as const,
  destinations: ["hostingDestinations"] as const,
  binding: (projectID: string, id: string) => ["resourceBinding", projectID, id] as const,
};
export const hostingServersQuery = (offset: number) => queryOptions({ queryKey: [...destinationKeys.servers, offset], queryFn: ({ signal }) => listHostingServers({ limit: 20, offset }, { signal }), retry: false });
export const hostingServerQuery = (id: string) => queryOptions({ queryKey: destinationKeys.server(id), queryFn: ({ signal }) => getHostingServer(id, { signal }), retry: false });
export const destinationGrantsQuery = (id: string, offset: number) => queryOptions({ queryKey: [...destinationKeys.grants(id), offset], queryFn: ({ signal }) => listDestinationGrants(id, { limit: 20, offset }, { signal }), retry: false });
export const destinationsQuery = (scope: DestinationScope) => queryOptions({ queryKey: [...destinationKeys.destinations, scope], queryFn: ({ signal }) => listDestinations(scope, { signal }), retry: false });
export const resourceBindingQuery = (projectID: string, id: string) => queryOptions({ queryKey: destinationKeys.binding(projectID, id), queryFn: async ({ signal }) => {
  try { return await getResourceBinding(id, { signal }); }
  catch (cause) { if (cause instanceof ApiError && cause.status === 404) return null; throw cause; }
}, retry: false });
