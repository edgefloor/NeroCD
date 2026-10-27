import { afterEach, expect, test, vi } from "vitest";
import type { CatalogTemplate } from "./hosting-catalog";
import {
  getCatalogDestinationOptions,
  type CatalogDestinationOptions,
} from "./hosting-catalog-destinations";
const template = {
  id: "linkding",
  immutable_version: "coolify-b12d1386.linkding-1.47.0.v1",
  definition: {
    mount_path: "/etc/linkding/data",
    credential_names: ["LD_SUPERUSER_NAME", "LD_SUPERUSER_PASSWORD"],
  },
} as CatalogTemplate;
const options: CatalogDestinationOptions = {
  template_id: template.id,
  immutable_version: template.immutable_version,
  requirements: {
    source_kind: "image",
    required_capabilities: ["image_deploy", "hosting_owned_storage_v1"],
    architecture_restriction: "undeclared",
    architecture_verification: "unknown",
    cpu: "undeclared",
    memory: "undeclared",
    disk: "undeclared",
    mount_paths: ["/etc/linkding/data"],
    public_url_required: false,
    managed_credential_names: template.definition.credential_names,
  },
  items: [
    {
      destination: {
        id: "dest",
        server_id: "server",
        name: "Docker",
        runtime_kind: "docker_compose",
        created_at: "2026-10-27T00:00:00Z",
      },
      server_name: "Server",
      available: true,
      reason_code: "",
      missing_capabilities: [],
    },
  ],
  total: 1,
  limit: 20,
  offset: 0,
};
afterEach(() => vi.unstubAllGlobals());
function mock(value: unknown) {
  const fetch = vi.fn(
    async (_url: string, _options: RequestInit) =>
      new Response(JSON.stringify(value)),
  );
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
test("reads exact version and page using current session and no-store, with truthful unknown hardware", async () => {
  const fetch = mock(options);
  expect(await getCatalogDestinationOptions("project", template, 0)).toEqual(
    options,
  );
  const [url, request] = fetch.mock.calls[0]!;
  expect(url).toBe(
    `/api/v1/hosting-catalog/linkding/destination-options?project_id=project&immutable_version=${template.immutable_version}&limit=20&offset=0`,
  );
  expect(request).toMatchObject({
    credentials: "include",
    cache: "no-store",
    signal: expect.any(AbortSignal),
  });
});
test.each([
  { ...options, template_id: "other" },
  { ...options, immutable_version: "old" },
  { ...options, offset: 20 },
  { ...options, limit: 100 },
  { ...options, total: -1 },
  { ...options, items: [options.items[0], options.items[0]], total: 2 },
  { ...options, extra: "private" },
  { ...options, requirements: { ...options.requirements, cpu: "2" } },
  {
    ...options,
    requirements: { ...options.requirements, source_kind: "compose" },
  },
  {
    ...options,
    requirements: {
      ...options.requirements,
      architecture_verification: "verified",
    },
  },
  {
    ...options,
    requirements: {
      ...options.requirements,
      architecture_restriction: "amd64",
    },
  },
  {
    ...options,
    requirements: { ...options.requirements, public_url_required: true },
  },
  {
    ...options,
    requirements: {
      ...options.requirements,
      managed_credential_names: ["foreign"],
    },
  },
  {
    ...options,
    requirements: { ...options.requirements, mount_paths: ["../private"] },
  },
  {
    ...options,
    requirements: { ...options.requirements, required_capabilities: [] },
  },
  {
    ...options,
    items: [
      {
        ...options.items[0],
        available: true,
        reason_code: "runner_unavailable",
      },
    ],
  },
  {
    ...options,
    items: [
      { ...options.items[0], available: false, reason_code: "unrecognized" },
    ],
  },
  {
    ...options,
    items: [
      {
        ...options.items[0],
        available: false,
        reason_code: "missing_capabilities",
        missing_capabilities: [],
      },
    ],
  },
  {
    ...options,
    items: [
      {
        ...options.items[0],
        available: false,
        reason_code: "missing_capabilities",
        missing_capabilities: ["not_required"],
      },
    ],
  },
  { ...options, items: [{ ...options.items[0], runner_id: "private" }] },
  {
    ...options,
    items: [
      {
        ...options.items[0],
        destination: { ...options.items[0]!.destination, id: "../x" },
      },
    ],
  },
])("rejects mismatched or malformed eligibility %j", async (value) => {
  mock(value);
  await expect(
    getCatalogDestinationOptions("project", template, 0),
  ).rejects.toThrow();
});
test("empty page remains navigable when grants shrink below its offset", async () => {
  const page = { ...options, items: [], total: 0, offset: 20 };
  mock(page);
  expect(await getCatalogDestinationOptions("project", template, 20)).toEqual(
    page,
  );
});
test.each([
  "runner_unavailable",
  "runner_ineligible",
  "unsupported_runtime",
  "missing_capabilities",
] as const)("accepts closed unavailable reason %s", async (reason) => {
  const value = {
    ...options,
    items: [
      {
        ...options.items[0]!,
        available: false,
        reason_code: reason,
        missing_capabilities:
          reason === "missing_capabilities" ? ["image_deploy"] : [],
      },
    ],
  };
  mock(value);
  expect(await getCatalogDestinationOptions("project", template, 0)).toEqual(
    value,
  );
});
test("retired scope rejects even if fetch ignores abort and finishes later", async () => {
  let finish!: (response: Response) => void;
  const fetch = vi.fn(
    (_url: string, _options: RequestInit) =>
      new Promise<Response>((resolve) => {
        finish = resolve;
      }),
  );
  vi.stubGlobal("fetch", fetch);
  const controller = new AbortController();
  const pending = getCatalogDestinationOptions(
    "project",
    template,
    0,
    controller.signal,
  );
  const rejected = expect(pending).rejects.toMatchObject({
    name: "AbortError",
  });
  controller.abort();
  expect(fetch.mock.calls[0]![1].signal?.aborted).toBe(true);
  finish(new Response(JSON.stringify(options)));
  await rejected;
});
test.each(["", "../project", "project?x=y"])(
  "invalid project %s never sends request",
  async (project) => {
    const fetch = mock(options);
    await expect(
      getCatalogDestinationOptions(project, template, 0),
    ).rejects.toThrow();
    expect(fetch).not.toHaveBeenCalled();
  },
);
