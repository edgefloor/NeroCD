import { afterEach, expect, test, vi } from "vitest";
import { createProjectEnvironment, createResource, getResource, listProjectEnvironments, listResources, updateResource } from "./hosting";
import { ApiError } from "./errors";

afterEach(() => vi.unstubAllGlobals());
test("hosting clients scope bounded reads and encode resource IDs", async () => {
  const fetch = vi.fn().mockImplementation(async () => new Response(JSON.stringify({ items: [] })));
  vi.stubGlobal("fetch", fetch);
  const signal = new AbortController().signal;
  await listProjectEnvironments({ project_id: "project one", limit: 20, offset: 40 }, { signal });
  await listResources({ project_id: "project one", project_environment_id: "env/two", limit: 20, offset: 0 }, { signal });
  await getResource("app/one", { signal });
  expect(fetch.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/project-environments?project_id=project+one&limit=20&offset=40",
    "/api/v1/resources?project_id=project+one&project_environment_id=env%2Ftwo&limit=20&offset=0",
    "/api/v1/resources/app%2Fone",
  ]);
  for (const [, options] of fetch.mock.calls) expect(options).toMatchObject({ credentials: "include", signal });
});

test("hosting writes send exact accepted bodies with cookie CSRF protection", async () => {
  const fetch = vi.fn().mockImplementation(async () => new Response(JSON.stringify({ id: "saved" })));
  vi.stubGlobal("fetch", fetch);
  const source = { kind: "image" as const, image: { reference: "localhost:5000/team/app@sha256:" + "a".repeat(64) } };
  await createProjectEnvironment({ project_id: "p", name: "Production" });
  await createResource({ project_id: "p", project_environment_id: "e", name: "Web", kind: "application", source });
  await updateResource("app/one", { expected_generation: 3, source });
  expect(fetch.mock.calls.map(([path]) => path)).toEqual(["/api/v1/project-environments", "/api/v1/resources", "/api/v1/resources/app%2Fone"]);
  expect(fetch.mock.calls.map(([, options]) => JSON.parse(options.body))).toEqual([
    { project_id: "p", name: "Production" },
    { project_id: "p", project_environment_id: "e", name: "Web", kind: "application", source },
    { expected_generation: 3, source },
  ]);
  for (const [, options] of fetch.mock.calls) expect(options).toMatchObject({ credentials: "include", headers: { "X-NeroCD-CSRF": "1" } });
});

test.each([400, 401, 403, 404, 409, 500])("hosting failures retain status %s and request identity", async (status) => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: "unable to save" }), { status, headers: { "X-Request-ID": "hosting-request" } })));
  await expect(updateResource("app", { name: "Draft", expected_generation: 1 })).rejects.toEqual(new ApiError(status, "unable to save", "hosting-request"));
});
