import { queryOptions } from "@tanstack/react-query";
import type { components } from "./generated";
import { request } from "./client";

type Schema<K extends keyof components["schemas"]> = components["schemas"][K];
export type ServerBootstrap = Schema<"ServerBootstrap"> & {
  artifact: Schema<"ServerBootstrapArtifactOption">;
};
export type BootstrapCreate = Schema<"ServerBootstrapCreateInput">;
export type BootstrapAction = ServerBootstrap["available_actions"][number];
export type BootstrapCommand =
  | Schema<"ServerBootstrapCommandInput">
  | Schema<"ServerBootstrapReviewInput">
  | Schema<"ServerBootstrapInstallInput">;
const path = (id: string) =>
  `/api/v1/server-bootstraps/${encodeURIComponent(id)}`;
export const bootstrapKeys = {
  options: (actor: string, project: string) =>
    ["serverBootstrapOptions", actor, project] as const,
  list: (actor: string, project: string, offset: number) =>
    ["serverBootstraps", actor, project, offset] as const,
  operation: (actor: string, project: string, id: string) =>
    ["serverBootstrap", actor, project, id] as const,
};
export const bootstrapOptionsQuery = (actor: string, project: string) =>
  queryOptions({
    queryKey: bootstrapKeys.options(actor, project),
    retry: false,
    queryFn: ({ signal }) =>
      request<Schema<"ServerBootstrapOptions">>(
        "/api/v1/server-bootstrap-options",
        { signal, query: { project_id: project }, cache: "no-store" },
      ),
  });
export const bootstrapListQuery = (
  actor: string,
  project: string,
  offset: number,
) =>
  queryOptions({
    queryKey: bootstrapKeys.list(actor, project, offset),
    retry: false,
    queryFn: async ({ signal }) => {
      const page = await request<Schema<"ServerBootstrapList">>(
        "/api/v1/server-bootstraps",
        {
          signal,
          query: { project_id: project, limit: 20, offset },
          cache: "no-store",
        },
      );
      if (page.items.some((item) => item.project_id !== project))
        throw new Error("Bootstrap project mismatch");
      return page;
    },
  });
export const bootstrapOperationQuery = (
  actor: string,
  project: string,
  id: string,
) =>
  queryOptions({
    queryKey: bootstrapKeys.operation(actor, project, id),
    retry: false,
    queryFn: async ({ signal }) => {
      const result = await request<ServerBootstrap>(path(id), {
        signal,
        cache: "no-store",
      });
      if (result.id !== id || result.project_id !== project)
        throw new Error("Bootstrap scope mismatch");
      return result;
    },
  });
export const createServerBootstrap = (
  body: BootstrapCreate,
  signal?: AbortSignal,
) =>
  request<ServerBootstrap>("/api/v1/server-bootstraps", {
    method: "POST",
    signal,
    body,
    cache: "no-store",
  });
export const commandServerBootstrap = (
  id: string,
  action: BootstrapAction,
  body: BootstrapCommand,
  signal?: AbortSignal,
) =>
  request<ServerBootstrap>(`${path(id)}/${action}`, {
    method: "POST",
    signal,
    body,
    cache: "no-store",
  });
