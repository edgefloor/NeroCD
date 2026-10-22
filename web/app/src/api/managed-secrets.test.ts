import { afterEach, expect, test, vi } from "vitest";
import { createManagedSecret, getManagedSecret, listManagedSecrets, listManagedSecretVersions, revealManagedSecret, revokeManagedSecret, rotateManagedSecret } from "./managed-secrets";

afterEach(() => vi.unstubAllGlobals());
test("managed-secret API encodes scoped metadata requests and bypasses browser caching", async () => {
  const fetch = vi.fn().mockImplementation(async () => new Response("{}")); vi.stubGlobal("fetch", fetch);
  const signal = new AbortController().signal;
  await listManagedSecrets({ project_id: "project one", limit: 20, offset: 40 }, { signal });
  await getManagedSecret("secret/one", { signal });
  await listManagedSecretVersions("secret/one", { limit: 20, offset: 20 }, { signal });
  expect(fetch.mock.calls.map(([url]) => url)).toEqual(["/api/v1/managed-secrets?project_id=project+one&limit=20&offset=40", "/api/v1/managed-secrets/secret%2Fone", "/api/v1/managed-secrets/secret%2Fone/versions?limit=20&offset=20"]);
  for (const [, options] of fetch.mock.calls) expect(options).toMatchObject({ credentials: "include", cache: "no-store", signal });
});
test("writes preserve exact values and versions with CSRF, including explicit historical reveal", async () => {
  const fetch = vi.fn().mockImplementation(async () => new Response("{}")); vi.stubGlobal("fetch", fetch);
  const signal = new AbortController().signal;
  await createManagedSecret({ project_id: "project", name: "Database", value: "  test-only secret  " });
  await rotateManagedSecret("secret/one", { expected_version: 3, value: "new test-only value" });
  await revealManagedSecret("secret/one", 1, { signal });
  await revokeManagedSecret("secret/one", { expected_version: 4 });
  expect(fetch.mock.calls.map(([url]) => url)).toEqual(["/api/v1/managed-secrets", "/api/v1/managed-secrets/secret%2Fone/versions", "/api/v1/managed-secrets/secret%2Fone/reveal", "/api/v1/managed-secrets/secret%2Fone/revoke"]);
  expect(fetch.mock.calls.map(([, options]) => JSON.parse(options.body))).toEqual([{ project_id: "project", name: "Database", value: "  test-only secret  " }, { expected_version: 3, value: "new test-only value" }, { version: 1 }, { expected_version: 4 }]);
  for (const [, options] of fetch.mock.calls) expect(options).toMatchObject({ method: "POST", credentials: "include", cache: "no-store", headers: { "X-NeroCD-CSRF": "1" } });
  expect(fetch.mock.calls[2]![1].signal).toBe(signal);
});
