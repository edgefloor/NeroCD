import { afterEach, expect, test, vi } from "vitest";
import { getCatalogInstallation } from "./hosting-catalog";
import { request } from "./client";
import { ApiError } from "./errors";
vi.mock("./client", () => ({ request: vi.fn() }));
afterEach(() => vi.resetAllMocks());
const compose = { original: "services: {}", public_component: "vikunja", container_port: 3456 };
const installation = {
  id: "res", resource: { id: "res", source: { kind: "compose", compose } }, binding: { resource_id: "res" },
  storage_ref: { storage_id: "", generation: 0, hash: "" },
  template: { definition: { compose, container_port: 3456, readiness: { path: "/health", timeout_seconds: 120 } } },
};
test("Compose catalog accepts its matching source and exact zero storage reference", async () => {
  vi.mocked(request).mockResolvedValueOnce(installation);
  expect(await getCatalogInstallation("res")).toBe(installation);
});
test.each([
  { ...installation, storage_ref: { storage_id: "storage", generation: 1, hash: "hash" } },
  { ...installation, resource: { id: "res", source: { kind: "compose", compose: { ...compose, original: "different" } } } },
  { ...installation, template: { definition: { ...installation.template.definition, compose: undefined } } },
  { ...installation, resource: { id: "res", source: { kind: "image", image: { reference: "image:1" } } }, template: { definition: { ...installation.template.definition, compose: undefined } } },
])("rejects mismatched Compose and storage metadata", async value => {
  vi.mocked(request).mockResolvedValueOnce(value);
  await expect(getCatalogInstallation("res")).rejects.toThrow(/Invalid/);
});
test("legacy image catalog metadata and absent installations keep their existing behavior", async () => {
  const image = { ...installation, resource: { id: "res", source: { kind: "image", image: { reference: "image:1" } } }, storage_ref: { storage_id: "storage", generation: 1, hash: "hash" }, template: { definition: { ...installation.template.definition, compose: undefined } } };
  vi.mocked(request).mockResolvedValueOnce(image).mockRejectedValueOnce(new ApiError(404, "absent", null));
  expect(await getCatalogInstallation("res")).toBe(image);
  expect(await getCatalogInstallation("res")).toBeNull();
});
