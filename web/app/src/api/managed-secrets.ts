import type { components } from "./generated";
import { request, type RequestOptions } from "./client";

type Schema<Name extends keyof components["schemas"]> = components["schemas"][Name];
type Options = Pick<RequestOptions, "signal">;
export type ManagedSecret = Schema<"ManagedSecret">;
export type ManagedSecretVersion = Schema<"ManagedSecretVersion">;
const path = (id: string) => `/api/v1/managed-secrets/${encodeURIComponent(id)}`;
export function listManagedSecrets(query: { project_id: string; limit: number; offset: number }, options?: Options): Promise<Schema<"ManagedSecretListResponse">> {
  return request("/api/v1/managed-secrets", { ...options, query, cache: "no-store" });
}
export function getManagedSecret(id: string, options?: Options): Promise<ManagedSecret> {
  return request(path(id), { ...options, cache: "no-store" });
}
export function createManagedSecret(body: Schema<"ManagedSecretInput">, options?: Options): Promise<ManagedSecret> {
  return request("/api/v1/managed-secrets", { ...options, method: "POST", body, cache: "no-store" });
}
export function listManagedSecretVersions(id: string, query: { limit: number; offset: number }, options?: Options): Promise<Schema<"ManagedSecretVersionListResponse">> {
  return request(`${path(id)}/versions`, { ...options, query, cache: "no-store" });
}
export function rotateManagedSecret(id: string, body: Schema<"ManagedSecretRotateInput">, options?: Options): Promise<ManagedSecret> {
  return request(`${path(id)}/versions`, { ...options, method: "POST", body, cache: "no-store" });
}
export function revokeManagedSecret(id: string, body: Schema<"ManagedSecretRevokeInput">, options?: Options): Promise<ManagedSecret> {
  return request(`${path(id)}/revoke`, { ...options, method: "POST", body, cache: "no-store" });
}
// Call only from an explicit reveal action. Never use a query or mutation cache
// for the plaintext response.
export function revealManagedSecret(id: string, version: number, options?: Options): Promise<Schema<"ManagedSecretReveal">> {
  return request(`${path(id)}/reveal`, { ...options, method: "POST", body: { version }, cache: "no-store" });
}
