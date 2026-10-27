import type { components } from "./generated";
import type { CatalogTemplate } from "./hosting-catalog";
import { request } from "./client";
import { boundedStorageRequest } from "./hosting-storage-deployment";
export type CatalogRequirements =
  components["schemas"]["HostingCatalogRequirements"];
export type CatalogDestinationOptions =
  components["schemas"]["HostingCatalogDestinationOptions"];
const reasons = {
  unsupported_runtime: "Requires a standalone Docker destination.",
  runner_unavailable:
    "The server is offline or its runner is unavailable. Ask an administrator to reconnect it.",
  runner_ineligible:
    "This runner cannot host applications. Ask an administrator to configure a hosting runner.",
  missing_capabilities:
    "Required deployment features are disabled. Ask an administrator to enable them on this server.",
};
export function destinationRequirementMessage(reason: string) {
  return (
    reasons[reason as keyof typeof reasons] ??
    "Destination compatibility could not be verified."
  );
}
const id = (value: unknown): value is string =>
  typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const text = (value: unknown): value is string =>
  typeof value === "string" &&
  value.length > 0 &&
  value.length <= 512 &&
  !/[\u0000-\u001f\u007f]/.test(value);
function keys(
  value: unknown,
  expected: string[],
): value is Record<string, unknown> {
  return (
    !!value &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    Object.keys(value).length === expected.length &&
    expected.every((key) => Object.hasOwn(value, key))
  );
}
function strings(value: unknown): value is string[] {
  return (
    Array.isArray(value) &&
    value.length <= 128 &&
    value.every(text) &&
    new Set(value).size === value.length
  );
}
function sameStrings(left: string[], right: string[]) {
  return (
    left.length === right.length &&
    [...left].sort().every((value, index) => value === [...right].sort()[index])
  );
}
export async function getCatalogDestinationOptions(
  project: string,
  template: CatalogTemplate,
  offset: number,
  signal?: AbortSignal,
): Promise<CatalogDestinationOptions> {
  if (
    !id(project) ||
    !id(template.id) ||
    !/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(template.immutable_version) ||
    !Number.isSafeInteger(offset) ||
    offset < 0
  )
    throw new Error("Invalid catalog destination scope");
  const value = await boundedStorageRequest(
    (signal) =>
      request<CatalogDestinationOptions>(
        `/api/v1/hosting-catalog/${encodeURIComponent(template.id)}/destination-options`,
        {
          query: {
            project_id: project,
            immutable_version: template.immutable_version,
            limit: 20,
            offset,
          },
          signal,
          cache: "no-store",
        },
      ),
    signal,
  );
  if (
    !keys(value, [
      "template_id",
      "immutable_version",
      "requirements",
      "items",
      "total",
      "limit",
      "offset",
    ]) ||
    value.template_id !== template.id ||
    value.immutable_version !== template.immutable_version ||
    value.limit !== 20 ||
    value.offset !== offset ||
    !Number.isSafeInteger(value.total) ||
    !Array.isArray(value.items) ||
    value.items.length > 20 ||
    (value.total as number) < 0 ||
    value.items.length > Math.max(0, (value.total as number) - offset)
  )
    throw new Error("Catalog destination page mismatch");
  const r = value.requirements;
  if (
    !keys(r, [
      "source_kind",
      "required_capabilities",
      "architecture_restriction",
      "architecture_verification",
      "cpu",
      "memory",
      "disk",
      "mount_paths",
      "public_url_required",
      "managed_credential_names",
    ]) ||
    r.source_kind !== (template.definition.compose ? "compose" : "image") ||
    !strings(r.required_capabilities) ||
    r.required_capabilities.length === 0 ||
    r.architecture_restriction !==
      (template.amd_only
        ? "amd64"
        : template.arm_only
          ? "arm64"
          : "undeclared") ||
    r.architecture_verification !== "unknown" ||
    r.cpu !== "undeclared" ||
    r.memory !== "undeclared" ||
    r.disk !== "undeclared" ||
    !strings(r.mount_paths) ||
    !r.mount_paths.every((path) => path.startsWith("/")) ||
    typeof r.public_url_required !== "boolean" ||
    r.public_url_required !== Boolean(template.definition.compose) ||
    !strings(r.managed_credential_names) ||
    !sameStrings(
      r.managed_credential_names,
      template.definition.credential_names,
    )
  )
    throw new Error("Catalog requirement evidence mismatch");
  const seen = new Set<string>();
  for (const item of value.items) {
    if (
      !keys(item, [
        "destination",
        "server_name",
        "available",
        "reason_code",
        "missing_capabilities",
      ]) ||
      !keys(item.destination, [
        "id",
        "server_id",
        "name",
        "runtime_kind",
        "created_at",
      ]) ||
      !id(item.destination.id) ||
      seen.has(item.destination.id) ||
      !id(item.destination.server_id) ||
      !text(item.destination.name) ||
      !text(item.destination.runtime_kind) ||
      typeof item.destination.created_at !== "string" ||
      !text(item.server_name) ||
      typeof item.available !== "boolean" ||
      !strings(item.missing_capabilities) ||
      (item.available
        ? item.reason_code !== "" || item.missing_capabilities.length !== 0
        : !Object.hasOwn(reasons, String(item.reason_code))) ||
      (item.reason_code === "missing_capabilities" &&
        item.missing_capabilities.length === 0) ||
      !item.missing_capabilities.every((capability) =>
        r.required_capabilities.includes(capability),
      )
    )
      throw new Error("Catalog destination evidence mismatch");
    seen.add(item.destination.id);
  }
  return value as unknown as CatalogDestinationOptions;
}
