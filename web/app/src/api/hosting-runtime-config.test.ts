import { afterEach, expect, test, vi } from "vitest";
import { getHostingRuntimeConfiguration, putHostingRuntimeConfiguration, type HostingRuntimeConfigurationInput } from "./hosting-runtime-config";

afterEach(() => vi.unstubAllGlobals());
test("runtime configuration preserves exact CAS, retry identity and reference-only input", async () => {
  const fetch = vi.fn().mockImplementation(async () => new Response(JSON.stringify({runtime_configuration:{resource_id:"resource/one",project_id:"project",resource_generation:3,runtime_generation:0,entries:[]}}))); vi.stubGlobal("fetch", fetch);
  const signal = new AbortController().signal;
  const body: HostingRuntimeConfigurationInput = { expected_resource_generation: 3, expected_runtime_generation: 0, request_id: "0123456789abcdef0123456789abcdef", entries: [{ name: "VISIBLE", kind: "literal", value: "  visible  " }, { name: "PASSWORD", kind: "managed_secret", secret: { secret_id: "secret/one", version: 2 } }] };
  const response = await getHostingRuntimeConfiguration("resource/one", { signal });
  expect(response.runtime_configuration.runtime_generation).toBe(0);
  await putHostingRuntimeConfiguration("resource/one", body, { signal });
  await putHostingRuntimeConfiguration("resource/one", body);
  expect(fetch.mock.calls.map(([url]) => url)).toEqual(Array(3).fill("/api/v1/resources/resource%2Fone/runtime-configuration"));
  for (const [, options] of fetch.mock.calls) expect(options).toMatchObject({ credentials: "include", cache: "no-store" });
  expect(fetch.mock.calls[0]![1].signal).toBe(signal);
  expect(fetch.mock.calls[1]![1]).toMatchObject({ method: "PUT", signal, headers: { "X-NeroCD-CSRF": "1" } });
  expect(JSON.parse(fetch.mock.calls[1]![1].body)).toEqual(body);
  expect(fetch.mock.calls[2]![1].body).toEqual(fetch.mock.calls[1]![1].body);
});
