import type { components } from "./generated";
import { request } from "./client";
import { ApiError } from "./errors";
export type CatalogTemplate = components["schemas"]["HostingCatalogTemplate"];
export type CatalogInstallInput = components["schemas"]["HostingCatalogInstallInput"];
export type CatalogInstallation = components["schemas"]["HostingCatalogInstallation"];
export const getCatalog = (project: string, signal?: AbortSignal): Promise<CatalogTemplate[]> => request("/api/v1/hosting-catalog", { query: { project_id: project }, signal, cache: "no-store" });
export const installCatalog = (body: CatalogInstallInput, signal?: AbortSignal): Promise<CatalogInstallation> => request("/api/v1/hosting-catalog/installations", { method: "POST", body, signal, cache: "no-store" });
export async function getCatalogInstallation(resource: string, signal?: AbortSignal): Promise<CatalogInstallation | null> {
    try {
        const value = await request<CatalogInstallation>(`/api/v1/resources/${encodeURIComponent(resource)}/catalog-installation`, { signal, cache: "no-store" });
        if(value.id!==resource || value.resource?.id!==resource || value.binding?.resource_id!==resource || !value.template?.definition || !Number.isInteger(value.template.definition.container_port) || value.template.definition.container_port<1 || value.template.definition.container_port>65535 || typeof value.template.definition.readiness?.path!=="string" || !Number.isInteger(value.template.definition.readiness.timeout_seconds)) throw new Error("Invalid catalog installation metadata");
        const compose = value.template.definition.compose;
        const zeroStorage = value.storage_ref?.storage_id === "" && value.storage_ref.generation === 0 && value.storage_ref.hash === "";
        if (compose) {
            if (value.resource.source.kind !== "compose" || !compose.original || !compose.public_component || compose.container_port !== value.template.definition.container_port ||
                value.resource.source.compose.original !== compose.original || value.resource.source.compose.public_component !== compose.public_component || value.resource.source.compose.container_port !== compose.container_port || !zeroStorage)
                throw new Error("Invalid Compose catalog installation metadata");
        } else if (value.resource.source?.kind === "compose" || zeroStorage) {
            throw new Error("Invalid catalog storage metadata");
        }
        return value;
    }
    catch (error) {
        if (error instanceof ApiError && error.status === 404)
            return null;
        throw error;
    }
}
