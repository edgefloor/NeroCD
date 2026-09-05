import type { components } from "./generated";
import { request, type RequestOptions } from "./client";

export type ServiceAccount = components["schemas"]["ServiceAccount"];
export type ServiceAccountListResponse = components["schemas"]["ServiceAccountListResponse"];
export type ServiceAccountCreateInput = components["schemas"]["ServiceAccountCreateInput"];
export type ServiceAccountUpdateInput = components["schemas"]["ServiceAccountUpdateInput"];
export type ServiceAccountRotateInput = components["schemas"]["ServiceAccountRotateInput"];
export type CreatedServiceAccount = components["schemas"]["CreatedServiceAccount"];
export type RotatedServiceAccountCredential = components["schemas"]["RotatedServiceAccountCredential"];

type SignalOptions = Pick<RequestOptions, "signal">;

export function listServiceAccounts(projectID: string, limit: number, offset: number, options?: SignalOptions): Promise<ServiceAccountListResponse> {
  return request("/api/v1/service-accounts", { ...options, query: { project_id: projectID, limit, offset } });
}

export function createServiceAccount(input: ServiceAccountCreateInput, options?: SignalOptions): Promise<CreatedServiceAccount> {
  return request("/api/v1/service-accounts", { ...options, method: "POST", body: input });
}

export function updateServiceAccount(input: ServiceAccountUpdateInput, options?: SignalOptions): Promise<ServiceAccount> {
  return request("/api/v1/service-accounts", { ...options, method: "PATCH", body: input });
}

export function rotateServiceAccountCredential(input: ServiceAccountRotateInput, options?: SignalOptions): Promise<RotatedServiceAccountCredential> {
  return request("/api/v1/service-accounts/rotate", { ...options, method: "POST", body: input });
}

export function revokeServiceAccount(serviceAccountID: string, options?: SignalOptions): Promise<ServiceAccount> {
  return request("/api/v1/service-accounts/revoke", { ...options, method: "POST", body: { service_account_id: serviceAccountID } });
}
