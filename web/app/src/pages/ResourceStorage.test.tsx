import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { queryKeys } from "@/api";
import type { Resource } from "@/api/hosting";
import { ResourceStorage } from "./ResourceStorage";
const resource: Resource = {
  id: "r",
  project_id: "p",
  project_environment_id: "e",
  name: "Uploads",
  kind: "application",
  generation: 2,
  source: { kind: "image", image: { reference: "app:1" } },
  created_by: "u",
  created_at: "",
  updated_at: "",
};
const principal = { id: "u", provider: "local", roles: [] };
const saved = {
  id: "storage",
  project_id: "p",
  resource_id: "r",
  binding_id: "b",
  destination_id: "d",
  volume_name: "stable-volume",
  mount_path: "/app/uploads",
  retention: "retain",
};
const json = (data: unknown, status = 200) =>
  new Response(JSON.stringify(data), { status });
let client: QueryClient;
afterEach(() => {
  cleanup();
  client?.clear();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
function setup(
  role = "owner",
  handler?: (
    url: string,
    options: RequestInit,
  ) => Response | Promise<Response> | undefined,
) {
  let storage: unknown = null;
  const fetch = vi.fn(async (url: string, options: RequestInit) => {
    const custom = await handler?.(url, options);
    if (custom) return custom;
    if (url.includes("/project-members"))
      return json({
        items: [{ project_id: "p", user_id: "u", role }],
        count: 1,
        total: 1,
        offset: 0,
      });
    if (url.endsWith("/binding"))
      return json({ id: "b", resource_id: "r", destination_id: "d" });
    if (url.endsWith("/storage")) {
      if (options.method === "POST")
        storage = {
          ...saved,
          mount_path: JSON.parse(options.body as string).mount_path,
        };
      return storage ? json(storage) : json({}, 404);
    }
    if (url === "/api/v1/resources/r")
      return json({ ...resource, generation: 4 });
    throw new Error(`Unexpected endpoint ${url}`);
  });
  vi.stubGlobal("fetch", fetch);
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  client.setQueryData(queryKeys.principal, principal);
  const view = render(
    <QueryClientProvider client={client}>
      <ResourceStorage resource={resource} blocked={false} />
    </QueryClientProvider>,
  );
  return { fetch, view };
}
const writes = (fetch: ReturnType<typeof vi.fn>) =>
  fetch.mock.calls.filter(([, options]) => options.method === "POST");
async function consent() {
  await screen.findByText("No configured persistent storage.");
  fireEvent.click(screen.getByRole("checkbox"));
}
const submit = () =>
  fireEvent.submit(
    screen.getByRole("form", { name: "Configure persistent storage" }),
  );
test("owner explicitly saves metadata only and sees immutable retained identity, not activation", async () => {
  const { fetch } = setup();
  await screen.findByText("No configured persistent storage.");
  submit();
  expect(writes(fetch)).toHaveLength(0);
  await consent();
  submit();
  await screen.findByLabelText("Configured storage metadata");
  expect(JSON.parse(writes(fetch)[0]![1].body as string)).toEqual({
    request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
    expected_resource_generation: 2,
    mount_path: "/app/uploads",
  });
  expect(screen.getByText("stable-volume")).toBeTruthy();
  expect(screen.getByText("Unknown; configured metadata only")).toBeTruthy();
  expect(screen.queryByRole("form")).toBeNull();
  expect(
    screen.getByText(/Image rollback does not undo file writes/),
  ).toBeTruthy();
  expect(
    screen.getByText(/Image files and existing uploads are not imported/),
  ).toBeTruthy();
  expect(screen.getByText(/write access for non-root images/)).toBeTruthy();
});
test.each(["viewer", "maintainer"])(
  "%s can read configured metadata without a create control",
  async (role) => {
    const { fetch } = setup(role, (url) =>
      url.endsWith("/storage") ? json(saved) : undefined,
    );
    await screen.findByLabelText("Configured storage metadata");
    expect(screen.queryByRole("form")).toBeNull();
    expect(writes(fetch)).toHaveLength(0);
  },
);
test.each([400, 503, "lost"])(
  "%s preserves draft and byte-identical retry; changed submission gets a new key",
  async (failure) => {
    const { fetch } = setup("owner", (_url, options) =>
      options.method === "POST"
        ? failure === "lost"
          ? Promise.reject(new TypeError("lost"))
          : json({ error: "unsafe server detail" }, failure as number)
        : undefined,
    );
    await consent();
    submit();
    await screen.findByRole("alert");
    submit();
    await waitFor(() => expect(writes(fetch)).toHaveLength(2));
    expect(writes(fetch)[0]![1].body).toBe(writes(fetch)[1]![1].body);
    expect(
      screen.getByLabelText<HTMLInputElement>("Container mount path").value,
    ).toBe("/app/uploads");
    expect(screen.queryByText(/unsafe server detail/)).toBeNull();
    await waitFor(() =>
      expect(
        screen.getByRole<HTMLButtonElement>("button", {
          name: "Retry identical storage request",
        }).disabled,
      ).toBe(false),
    );
    fireEvent.change(screen.getByLabelText("Container mount path"), {
      target: { value: "/files" },
    });
    fireEvent.click(screen.getByRole("checkbox"));
    submit();
    await waitFor(() => expect(writes(fetch)).toHaveLength(3));
    expect(JSON.parse(writes(fetch)[2]![1].body as string).request_id).not.toBe(
      JSON.parse(writes(fetch)[0]![1].body as string).request_id,
    );
  },
);
test("conflict requires explicit current-generation review and a separate submission", async () => {
  const { fetch } = setup("owner", (_url, options) =>
    options.method === "POST" ? json({}, 409) : undefined,
  );
  await consent();
  submit();
  await screen.findByRole("alert");
  submit();
  expect(writes(fetch)).toHaveLength(1);
  fireEvent.click(
    screen.getByRole("button", {
      name: "Refresh storage configuration for review",
    }),
  );
  await screen.findByText("Current resource generation 4");
  await waitFor(() =>
    expect(
      screen.getByRole<HTMLButtonElement>("button", {
        name: "Keep mount with reviewed generation for a separate save",
      }).disabled,
    ).toBe(false),
  );
  fireEvent.click(
    screen.getByRole("button", {
      name: "Keep mount with reviewed generation for a separate save",
    }),
  );
  fireEvent.click(screen.getByRole("checkbox"));
  submit();
  await waitFor(() => expect(writes(fetch)).toHaveLength(2));
  const bodies = writes(fetch).map(([, o]) => JSON.parse(o.body as string));
  expect(bodies[1].expected_resource_generation).toBe(4);
  expect(bodies[1].request_id).not.toBe(bodies[0].request_id);
});
test("unbound placement blocks configuration without manual identifiers", async () => {
  const { fetch } = setup("owner", (url) =>
    url.endsWith("/binding") ? json({}, 404) : undefined,
  );
  await consent();
  submit();
  expect(screen.getByText(/Save a desired destination above/)).toBeTruthy();
  expect(writes(fetch)).toHaveLength(0);
});
test("read denial fences pending save and actor change aborts late responses", async () => {
  let resolve!: (value: Response) => void,
    denyRead = false;
  const { fetch } = setup("owner", (url, options) =>
    options.method === "POST"
      ? new Promise<Response>((done) => {
          resolve = done;
        })
      : url.endsWith("/storage") && denyRead
        ? json({}, 403)
        : undefined,
  );
  await consent();
  submit();
  await waitFor(() => expect(writes(fetch)).toHaveLength(1));
  denyRead = true;
  await act(async () => {
    await client.invalidateQueries({ queryKey: ["hostingStorage"] });
  });
  await screen.findByText(/Storage access is unavailable/);
  expect(writes(fetch)[0]![1].signal?.aborted).toBe(true);
  await act(async () => {
    resolve(json(saved));
  });
  expect(screen.queryByLabelText("Configured storage metadata")).toBeNull();
});
test("actor change clears pending submission and ignores its old success", async () => {
  let resolve!: (value: Response) => void;
  const { fetch } = setup("owner", (_url, options) =>
    options.method === "POST"
      ? new Promise<Response>((done) => {
          resolve = done;
        })
      : undefined,
  );
  await consent();
  submit();
  await waitFor(() => expect(writes(fetch)).toHaveLength(1));
  await act(async () => {
    client.setQueryData(queryKeys.principal, { ...principal, id: "other" });
  });
  await waitFor(() => expect(writes(fetch)[0]![1].signal?.aborted).toBe(true));
  await act(async () => {
    resolve(json(saved));
  });
  expect(screen.queryByLabelText("Configured storage metadata")).toBeNull();
});

test("hung save releases exact retry after 30 seconds and ignores an older late success", async () => {
  const pending: ((value: Response) => void)[] = [];
  let confirmed = false;
  const { fetch } = setup("owner", (url, options) =>
    options.method === "POST"
      ? new Promise<Response>((done) => {
          pending.push(done);
        })
      : url.endsWith("/storage") && confirmed
        ? json(saved)
        : undefined,
  );
  await consent();
  vi.useFakeTimers();
  submit();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(30000);
  });
  const retry = screen.getByRole<HTMLButtonElement>("button", {
    name: "Retry identical storage request",
  });
  expect(retry.disabled).toBe(false);
  expect(writes(fetch)[0]![1].signal?.aborted).toBe(true);
  fireEvent.click(retry);
  await act(async () => {
    pending[0]!(json(saved));
  });
  expect(screen.queryByLabelText("Configured storage metadata")).toBeNull();
  expect(retry.disabled).toBe(true);
  expect(writes(fetch)[1]![1].body).toBe(writes(fetch)[0]![1].body);
  confirmed = true;
  await act(async () => {
    pending[1]!(json(saved));
  });
  expect(screen.getByLabelText("Configured storage metadata")).toBeTruthy();
});
