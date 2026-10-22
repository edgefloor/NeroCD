import type { components, paths } from "./generated";
import { request, type RequestOptions } from "./client";

export type ProjectEnvironment = components["schemas"]["ProjectEnvironment"];
export type Resource = components["schemas"]["Resource"];
export type ResourceSourceInput = components["schemas"]["ResourceSourceInput"];
export type ResourceSource = components["schemas"]["ResourceSource"];
type Options = Pick<RequestOptions, "signal">;
type EnvironmentFilters = paths["/api/v1/project-environments"]["get"]["parameters"]["query"];
type ResourceFilters = paths["/api/v1/resources"]["get"]["parameters"]["query"];
// The generated ResourcePatch union includes unknown from the schema's
// validation clauses. Keep the client boundary narrower than unknown.
export type ResourcePatch = { expected_generation: number } & (
  | { name: string; source?: ResourceSourceInput }
  | { name?: string; source: ResourceSourceInput }
);

export function listProjectEnvironments(query: EnvironmentFilters, options?: Options): Promise<components["schemas"]["ProjectEnvironmentListResponse"]> {
  return request("/api/v1/project-environments", { ...options, query });
}
export function createProjectEnvironment(body: components["schemas"]["ProjectEnvironmentInput"], options?: Options): Promise<ProjectEnvironment> {
  return request("/api/v1/project-environments", { ...options, method: "POST", body });
}
export function listResources(query: ResourceFilters, options?: Options): Promise<components["schemas"]["ResourceListResponse"]> {
  return request("/api/v1/resources", { ...options, query });
}
export function createResource(body: components["schemas"]["ResourceInput"], options?: Options): Promise<Resource> {
  return request("/api/v1/resources", { ...options, method: "POST", body });
}
export function getResource(id: string, options?: Options): Promise<Resource> {
  return request(`/api/v1/resources/${encodeURIComponent(id)}`, options);
}
export function updateResource(id: string, body: ResourcePatch, options?: Options): Promise<Resource> {
  return request(`/api/v1/resources/${encodeURIComponent(id)}`, { ...options, method: "PATCH", body });
}
