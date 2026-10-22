import type { components } from "./generated";
import { request, type RequestOptions } from "./client";

type Schema<Name extends keyof components["schemas"]> = components["schemas"][Name];
type Options = Pick<RequestOptions, "signal">;
export type HostingRuntimeConfiguration = Schema<"HostingRuntimeConfiguration">;
export type HostingRuntimeConfigurationResponse = Schema<"HostingRuntimeConfigurationResponse">;
export type HostingRuntimeConfigurationInput = Schema<"HostingRuntimeConfigurationInput">;
const path = (id: string) => `/api/v1/resources/${encodeURIComponent(id)}/runtime-configuration`;

export function getHostingRuntimeConfiguration(id: string, options?: Options): Promise<HostingRuntimeConfigurationResponse> {
  return request(path(id), { ...options, cache: "no-store" });
}

// Retain the caller's cryptographically random request ID and exact CAS input for retries.
// Saving desired input does not start deployment or imply runtime injection support.
export function putHostingRuntimeConfiguration(id: string, body: HostingRuntimeConfigurationInput, options?: Options): Promise<HostingRuntimeConfigurationResponse> {
  return request(path(id), { ...options, method: "PUT", body, cache: "no-store" });
}
