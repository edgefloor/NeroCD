import { queryOptions } from "@tanstack/react-query";
import { request, type RequestOptions } from "./client";
import { ApiError } from "./errors";
import type { components } from "./generated";
export type HostingStorage = components["schemas"]["HostingStorage"];
export type HostingStorageInput = components["schemas"]["HostingStorageInput"];
const path = (id: string) =>
  `/api/v1/resources/${encodeURIComponent(id)}/storage`;
type Options = Pick<RequestOptions, "signal">;
export const storageKey = (actor: string, project: string, resource: string) =>
  ["hostingStorage", actor, project, resource] as const;
export function scopedStorage(
  value: HostingStorage,
  project: string,
  resource: string,
) {
  if (value.resource_id !== resource || value.project_id !== project)
    throw new Error("Storage scope mismatch");
  return value;
}
export const storageQuery = (
  actor: string,
  project: string,
  resource: string,
) =>
  queryOptions({
    queryKey: storageKey(actor, project, resource),
    retry: false,
    gcTime: 0,
    queryFn: async ({ signal }) => {
      try {
        return scopedStorage(
          await request<HostingStorage>(path(resource), {
            signal,
            cache: "no-store",
          }),
          project,
          resource,
        );
      } catch (cause) {
        if (cause instanceof ApiError && cause.status === 404) return null;
        throw cause;
      }
    },
  });
export const createHostingStorage = (
  id: string,
  body: HostingStorageInput,
  options?: Options,
): Promise<HostingStorage> =>
  request(path(id), { ...options, method: "POST", body, cache: "no-store" });
export function storageRequestID() {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
    byte.toString(16).padStart(2, "0"),
  ).join("");
}
