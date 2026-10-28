import type { CatalogRequirements as Requirements } from "@/api/hosting-catalog-destinations";
import { catalogFeatureName } from "./catalogFeatures";
export function CatalogRequirements({
  requirements: r,
}: {
  requirements: Requirements;
}) {
  return (
    <section
      aria-label="Catalog application requirements"
      className="min-w-0 space-y-2 break-words"
    >
      <h6 className="font-medium">Application requirements</h6>
      <dl className="grid min-w-0 gap-x-3 gap-y-1 sm:grid-cols-[auto_1fr]">
        <dt>Runtime</dt>
        <dd>
          Standalone Docker
          {r.source_kind === "compose"
            ? " with managed Compose services"
            : " with a managed application container"}
        </dd>
        <dt>Server features</dt>
        <dd>{r.required_capabilities.map(catalogFeatureName).join(", ")}</dd>
        <dt>Architecture</dt>
        <dd>
          {r.architecture_restriction === "undeclared"
            ? "Not declared by the upstream template"
            : r.architecture_restriction === "amd64"
              ? "AMD64 only"
              : "ARM64 only"}
          . Destination architecture is unverified.
        </dd>
        <dt>CPU, memory and disk minimums</dt>
        <dd>
          Not declared by the upstream template. Available server capacity is
          unverified.
        </dd>
        <dt>Persistent mounts</dt>
        <dd>
          {r.mount_paths.length ? r.mount_paths.join(", ") : "None declared"}
        </dd>
        <dt>Public URL</dt>
        <dd>
          {r.public_url_required
            ? "Required during installation. Configure a matching route separately."
            : "Configure a domain and route after deployment."}
        </dd>
        <dt>Generated credentials</dt>
        <dd>
          {r.managed_credential_names.length
            ? r.managed_credential_names.join(", ")
            : "None. Complete account setup in the application after deployment."}
        </dd>
      </dl>
    </section>
  );
}
