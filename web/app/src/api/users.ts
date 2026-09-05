import type { components } from "./generated";
import { request, type RequestOptions } from "./client";

export type User = components["schemas"]["User"];
export type UserListResponse = components["schemas"]["UserListResponse"];
export type UserOffboardingResult = components["schemas"]["UserOffboardingResult"];
export type UserPage = { limit: number; offset: number };

export function listUsers(page: UserPage, options?: Pick<RequestOptions, "signal">): Promise<UserListResponse> {
  return request("/api/v1/users", { ...options, query: page });
}

export function offboardUser(userID: string, options?: Pick<RequestOptions, "signal">): Promise<UserOffboardingResult> {
  return request("/api/v1/users/offboard", { ...options, method: "POST", body: { user_id: userID } });
}
