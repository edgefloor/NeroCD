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

test("hung conflict refresh releases its busy state and fences late completion during retry", async () => {
  const pending: ((value: Response) => void)[] = [];
  const { fetch } = setup("owner", (url, options) =>
    options.method === "POST"
      ? json({}, 409)
      : url === "/api/v1/resources/r"
        ? new Promise<Response>((done) => {
            pending.push(done);
          })
        : undefined,
  );
  await consent();
  submit();
  await screen.findByRole("alert");
  const refresh = screen.getByRole<HTMLButtonElement>("button", {
    name: "Refresh storage configuration for review",
  });
  vi.useFakeTimers();
  fireEvent.click(refresh);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(30000);
  });
  expect(refresh.disabled).toBe(false);
  expect(screen.getByText(/Configuration refresh timed out/)).toBeTruthy();
  expect(
    screen.getByLabelText<HTMLInputElement>("Container mount path").value,
  ).toBe("/app/uploads");
  expect(
    fetch.mock.calls.find(([url]) => url === "/api/v1/resources/r")![1].signal
      ?.aborted,
  ).toBe(true);
  expect(screen.queryByText(/Current resource generation/)).toBeNull();
  fireEvent.click(refresh);
  await act(async () => {
    pending[0]!(json({ ...resource, generation: 99 }));
  });
  expect(refresh.disabled).toBe(true);
  expect(screen.queryByText("Current resource generation 99")).toBeNull();
  await act(async () => {
    pending[1]!(json({ ...resource, generation: 4 }));
  });
  expect(screen.getByText("Current resource generation 4")).toBeTruthy();
  expect(refresh.disabled).toBe(false);
  fireEvent.click(
    screen.getByRole("button", {
      name: "Keep mount with reviewed generation for a separate save",
    }),
  );
  fireEvent.click(screen.getByRole("checkbox"));
  submit();
  await act(async () => {
    await Promise.resolve();
  });
  expect(writes(fetch)).toHaveLength(2);
  expect(
    JSON.parse(writes(fetch)[1]![1].body as string)
      .expected_resource_generation,
  ).toBe(4);
});

test.each(["storage", "binding"])(
  "conflict refresh deadline also cancels stalled %s metadata reads",
  async (target) => {
    let refreshing = false;
    let resolve!: (value: Response) => void;
    const { fetch } = setup("owner", (url, options) =>
      options.method === "POST"
        ? json({}, 409)
        : refreshing && url.endsWith(`/${target}`)
          ? new Promise<Response>((done) => {
              resolve = done;
            })
          : undefined,
    );
    await consent();
    submit();
    await screen.findByRole("alert");
    const refresh = screen.getByRole<HTMLButtonElement>("button", {
      name: "Refresh storage configuration for review",
    });
    refreshing = true;
    vi.useFakeTimers();
    fireEvent.click(refresh);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30000);
    });
    expect(refresh.disabled).toBe(false);
    expect(screen.getByText(/Configuration refresh timed out/)).toBeTruthy();
    expect(screen.queryByText(/Current resource generation/)).toBeNull();
    const reads = fetch.mock.calls.filter(([url]) =>
      url.endsWith(`/${target}`),
    );
    expect(reads.at(-1)![1].signal?.aborted).toBe(true);
    await act(async () => {
      resolve(
        target === "storage"
          ? json(saved)
          : json({ id: "other", resource_id: "r", destination_id: "foreign" }),
      );
    });
    expect(screen.queryByLabelText("Configured storage metadata")).toBeNull();
    expect(screen.queryByText(/Current resource generation/)).toBeNull();
    refreshing = false;
    fireEvent.click(refresh);
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.getByText("Current resource generation 4")).toBeTruthy();
  },
);

test("unmount aborts a conflict refresh and rejects its late generation", async () => {
  let resolve!: (value: Response) => void;
  const { fetch } = setup("owner", (url, options) =>
    options.method === "POST"
      ? json({}, 409)
      : url === "/api/v1/resources/r"
        ? new Promise<Response>((done) => {
            resolve = done;
          })
        : undefined,
  );
  await consent();
  submit();
  await screen.findByRole("alert");
  fireEvent.click(
    screen.getByRole("button", {
      name: "Refresh storage configuration for review",
    }),
  );
  await waitFor(() =>
    expect(
      fetch.mock.calls.some(([url]) => url === "/api/v1/resources/r"),
    ).toBe(true),
  );
  cleanup();
  expect(
    fetch.mock.calls.find(([url]) => url === "/api/v1/resources/r")![1].signal
      ?.aborted,
  ).toBe(true);
  await act(async () => {
    resolve(json({ ...resource, generation: 99 }));
  });
  expect(screen.queryByText(/Current resource generation/)).toBeNull();
});

test("successful save with hung confirmation releases exact retry and fences the old GET", async () => {
  const pending: ((value: Response) => void)[] = [];
  let posted = false;
  const { fetch } = setup("owner", (url, options) => {
    if (options.method === "POST") {
      posted = true;
      return json(saved);
    }
    if (posted && url.endsWith("/storage"))
      return new Promise<Response>((done) => {
        pending.push(done);
      });
  });
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
  const reads = fetch.mock.calls.filter(([url]) => url.endsWith("/storage"));
  expect(reads.at(-1)![1].signal?.aborted).toBe(true);
  fireEvent.click(retry);
  await act(async () => {
    await Promise.resolve();
  });
  expect(writes(fetch)).toHaveLength(2);
  expect(writes(fetch)[1]![1].body).toBe(writes(fetch)[0]![1].body);
  await act(async () => {
    pending[0]!(json({ ...saved, volume_name: "retired-volume" }));
  });
  expect(screen.queryByText("retired-volume")).toBeNull();
  expect(retry.disabled).toBe(true);
  const currentRead = fetch.mock.calls
    .filter(
      ([url, options]) => url.endsWith("/storage") && options.method !== "POST",
    )
    .at(-1)!;
  expect(currentRead[1].signal?.aborted).toBe(false);
  await act(async () => {
    pending[1]!(json(saved));
  });
  expect(screen.getByText("stable-volume")).toBeTruthy();
});

test.each(["storage", "binding"])(
  "ordinary refresh releases stalled %s read and rejects old completion during retry",
  async (target) => {
    const pending: ((value: Response) => void)[] = [];
    let refreshReads = false;
    const { fetch } = setup("owner", (url) =>
      refreshReads && url.endsWith(`/${target}`)
        ? new Promise<Response>((done) => {
            pending.push(done);
          })
        : undefined,
    );
    await consent();
    const refresh = screen.getByRole<HTMLButtonElement>("button", {
      name: "Refresh configured storage",
    });
    refreshReads = true;
    vi.useFakeTimers();
    fireEvent.click(refresh);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30000);
    });
    expect(refresh.disabled).toBe(false);
    expect(
      screen.getByLabelText<HTMLInputElement>("Container mount path").value,
    ).toBe("/app/uploads");
    const reads = fetch.mock.calls.filter(([url]) =>
      url.endsWith(`/${target}`),
    );
    expect(reads.at(-1)![1].signal?.aborted).toBe(true);
    fireEvent.click(refresh);
    await act(async () => {
      await Promise.resolve();
    });
    await act(async () => {
      pending[0]!(
        target === "storage"
          ? json(saved)
          : json({
              id: "retired",
              resource_id: "r",
              destination_id: "foreign",
            }),
      );
    });
    expect(screen.queryByLabelText("Configured storage metadata")).toBeNull();
    expect(refresh.disabled).toBe(true);
    const current = fetch.mock.calls
      .filter(([url]) => url.endsWith(`/${target}`))
      .at(-1)!;
    expect(current[1].signal?.aborted).toBe(false);
    await act(async () => {
      pending[1]!(
        target === "storage"
          ? json({}, 404)
          : json({ id: "b", resource_id: "r", destination_id: "d" }),
      );
    });
    expect(refresh.disabled).toBe(false);
    expect(
      screen.getByRole<HTMLButtonElement>("button", {
        name: "Save desired storage",
      }).disabled,
    ).toBe(false);
  },
);

test.each(["storage", "binding"])(
  "initial %s read has a deadline and supports explicit read retry",
  async (target) => {
    let hung = true;
    let resolve!: (value: Response) => void;
    vi.useFakeTimers();
    const { fetch } = setup("owner", (url) =>
      hung && url.endsWith(`/${target}`)
        ? new Promise<Response>((done) => {
            resolve = done;
          })
        : undefined,
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(fetch.mock.calls.some(([url]) => url.endsWith(`/${target}`))).toBe(
      true,
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30001);
    });
    const refresh = screen.getByRole<HTMLButtonElement>("button", {
      name: "Refresh configured storage",
    });
    expect(refresh.disabled).toBe(false);
    expect(
      fetch.mock.calls.find(([url]) => url.endsWith(`/${target}`))![1].signal
        ?.aborted,
    ).toBe(true);
    hung = false;
    fireEvent.click(refresh);
    await act(async () => {
      await Promise.resolve();
    });
    await act(async () => {
      resolve(
        target === "storage"
          ? json(saved)
          : json({
              id: "retired",
              resource_id: "r",
              destination_id: "foreign",
            }),
      );
    });
    expect(screen.queryByLabelText("Configured storage metadata")).toBeNull();
    expect(refresh.disabled).toBe(false);
  },
);

test("unmount cancels both reads owned by an ordinary refresh", async () => {
  const pending: ((value: Response) => void)[] = [];
  let refreshReads = false;
  const { fetch } = setup("owner", (url) =>
    refreshReads && (url.endsWith("/storage") || url.endsWith("/binding"))
      ? new Promise<Response>((done) => {
          pending.push(done);
        })
      : undefined,
  );
  await consent();
  refreshReads = true;
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh configured storage" }),
  );
  await waitFor(() => expect(pending).toHaveLength(2));
  const reads = ["storage", "binding"].map(
    (target) =>
      fetch.mock.calls.filter(([url]) => url.endsWith(`/${target}`)).at(-1)!,
  );
  cleanup();
  for (const [, options] of reads) expect(options.signal?.aborted).toBe(true);
  await act(async () => {
    pending[0]!(json(saved));
    pending[1]!(
      json({ id: "retired", resource_id: "r", destination_id: "foreign" }),
    );
  });
  expect(
    client.getQueryData([
      "hostingStorage",
      JSON.stringify([principal.provider, principal.id]),
      "p",
      "r",
    ]),
  ).not.toEqual(saved);
});

test("initial membership timeout enables explicit access retry and fences its late owner result", async () => {
  const pending: ((value: Response) => void)[] = [];
  vi.useFakeTimers();
  const { fetch } = setup("owner", (url) =>
    url.includes("/project-members")
      ? new Promise<Response>((done) => {
          pending.push(done);
        })
      : undefined,
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(30001);
  });
  const refresh = screen.getByRole<HTMLButtonElement>("button", {
    name: "Refresh storage access",
  });
  expect(refresh.disabled).toBe(false);
  const reads = () =>
    fetch.mock.calls.filter(([url]) => url.includes("/project-members"));
  expect(reads()[0]![1].signal?.aborted).toBe(true);
  expect(
    screen.queryByRole("form", { name: "Configure persistent storage" }),
  ).toBeNull();
  fireEvent.click(refresh);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
  });
  await act(async () => {
    pending[0]!(
      json({
        items: [{ project_id: "p", user_id: "u", role: "owner" }],
        count: 1,
        total: 1,
        offset: 0,
      }),
    );
  });
  expect(refresh.disabled).toBe(true);
  expect(reads()[1]![1].signal?.aborted).toBe(false);
  expect(
    screen.queryByRole("form", { name: "Configure persistent storage" }),
  ).toBeNull();
  await act(async () => {
    pending[1]!(
      json({
        items: [{ project_id: "p", user_id: "u", role: "viewer" }],
        count: 1,
        total: 1,
        offset: 0,
      }),
    );
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
  });
  expect(refresh.disabled).toBe(false);
  expect(
    screen.getByText(
      "Project owner access is required to configure persistent storage.",
    ),
  ).toBeTruthy();
  expect(
    screen.queryByRole("form", { name: "Configure persistent storage" }),
  ).toBeNull();
});

test("membership access refresh timeout masks authority until a fresh explicit retry succeeds", async () => {
  const pending: ((value: Response) => void)[] = [];
  let revalidating = false;
  const { fetch } = setup("owner", (url) =>
    revalidating && url.includes("/project-members")
      ? new Promise<Response>((done) => {
          pending.push(done);
        })
      : undefined,
  );
  await consent();
  const refresh = screen.getByRole<HTMLButtonElement>("button", {
    name: "Refresh storage access",
  });
  revalidating = true;
  vi.useFakeTimers();
  fireEvent.click(refresh);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(30001);
  });
  expect(refresh.disabled).toBe(false);
  expect(
    screen.queryByRole("form", { name: "Configure persistent storage" }),
  ).toBeNull();
  const reads = () =>
    fetch.mock.calls.filter(([url]) => url.includes("/project-members"));
  expect(reads()[1]![1].signal?.aborted).toBe(true);
  fireEvent.click(refresh);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
  });
  await act(async () => {
    pending[0]!(
      json({
        items: [{ project_id: "p", user_id: "u", role: "owner" }],
        count: 1,
        total: 1,
        offset: 0,
      }),
    );
  });
  expect(refresh.disabled).toBe(true);
  expect(
    screen.queryByRole("form", { name: "Configure persistent storage" }),
  ).toBeNull();
  await act(async () => {
    pending[1]!(
      json({
        items: [{ project_id: "p", user_id: "u", role: "owner" }],
        count: 1,
        total: 1,
        offset: 0,
      }),
    );
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
  });
  expect(refresh.disabled).toBe(false);
  expect(
    screen.getByRole("form", { name: "Configure persistent storage" }),
  ).toBeTruthy();
});
