import type { Backup } from "@/api/hosting-storage-archives";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { StorageArchiveDownload } from "./StorageArchiveDownload";
const scope = {
  actor: "u",
  project: "p",
  resource: "r",
  destination: "d",
  binding: "b",
};
const blob = {
  backup_id: "backup",
  destination_id: "d",
  daemon_id: "daemon",
  sha256: `sha256:${"a".repeat(64)}`,
  size_bytes: 123,
  format: "nerocd-storage-v1",
};
const backup = {
  id: "backup",
  project_id: "p",
  resource_id: "r",
  operation_id: "op",
  state: "succeeded",
  created_by: "u",
  created_at: "2026-10-27",
  blob,
} as Backup;
const options = { available: true, reason_code: "", backup_id: "backup", blob };
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function setup(fetch: ReturnType<typeof vi.fn>, props = {}) {
  vi.stubGlobal("fetch", fetch);
  const onDenied = vi.fn();
  const values = {
    scope,
    backup,
    owner: true,
    blocked: false,
    onDenied,
    ...props,
  };
  return {
    ...render(<StorageArchiveDownload {...values} />),
    values,
    onDenied,
  };
}
test("uses native authenticated download after options, including Compose without retention copies", async () => {
  const fetch = vi.fn(async () => new Response(JSON.stringify(options)));
  setup(fetch);
  const link = await screen.findByRole("link", { name: "Download archive" });
  expect(link.getAttribute("href")).toBe(
    "/api/v1/storage-backups/backup/archive",
  );
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toBe("noopener");
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(fetch.mock.calls[0]).toEqual([
    "/api/v1/storage-backups/backup/archive-options",
    expect.objectContaining({
      credentials: "include",
      cache: "no-store",
      signal: expect.any(AbortSignal),
    }),
  ]);
  fireEvent.click(link);
  expect(fetch).toHaveBeenCalledTimes(1);
});
test.each([
  "disabled",
  "runner_unavailable",
  "local_copy_unavailable",
  "backup_not_succeeded",
])("explains %s and supports explicit refresh", async (reason) => {
  const fetch = vi.fn(
    async () =>
      new Response(
        JSON.stringify({
          available: false,
          reason_code: reason,
          backup_id: "backup",
        }),
      ),
  );
  setup(fetch);
  await screen.findByRole("button", { name: "Refresh download availability" });
  expect(screen.queryByRole("link", { name: "Download archive" })).toBeNull();
  expect(
    (
      screen.getByRole("button", {
        name: "Download archive",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  fetch.mockResolvedValueOnce(new Response(JSON.stringify(options)));
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh download availability" }),
  );
  await screen.findByRole("link", { name: "Download archive" });
});
test.each([401, 403])(
  "denial %i clears eligibility and informs parent",
  async (status) => {
    const { onDenied } = setup(
      vi.fn(async () => new Response("denied", { status })),
    );
    await screen.findByText(/download access is unavailable/);
    expect(onDenied).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("link")).toBeNull();
  },
);
test("missing copy has readable failure and never provides an attachment link", async () => {
  setup(vi.fn(async () => new Response("missing", { status: 404 })));
  await screen.findByText("The local archive copy is unavailable.");
  expect(screen.queryByRole("link")).toBeNull();
});
test.each(["actor", "project", "resource", "destination", "binding"])(
  "retires options on %s identity changes even if fetch ignores abort",
  async (field) => {
    let resolve!: (response: Response) => void;
    const fetch = vi.fn(
      (_url: string, _init: RequestInit) =>
        new Promise<Response>((done) => {
          resolve = done;
        }),
    );
    const { rerender, values } = setup(fetch);
    const signal = fetch.mock.calls[0]![1].signal as AbortSignal;
    rerender(
      <StorageArchiveDownload
        {...values}
        scope={{ ...scope, [field]: "other" }}
        blocked
      />,
    );
    expect(signal.aborted).toBe(true);
    await act(async () => resolve(new Response(JSON.stringify(options))));
    expect(screen.queryByRole("link")).toBeNull();
  },
);
test("selection change hides old eligibility immediately and rejects retired success", async () => {
  let resolve!: (response: Response) => void;
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(new Response(JSON.stringify(options)))
    .mockImplementation(
      () =>
        new Promise<Response>((done) => {
          resolve = done;
        }),
    );
  const { rerender, values } = setup(fetch);
  await screen.findByRole("link", { name: "Download archive" });
  const other = {
    ...backup,
    id: "other",
    blob: { ...blob, backup_id: "other" },
  } as Backup;
  rerender(<StorageArchiveDownload {...values} backup={other} />);
  expect(screen.queryByRole("link")).toBeNull();
  rerender(<StorageArchiveDownload {...values} owner={false} />);
  await act(async () =>
    resolve(
      new Response(
        JSON.stringify({ ...options, backup_id: "other", blob: other.blob }),
      ),
    ),
  );
  expect(screen.queryByRole("link")).toBeNull();
});
test.each([
  { owner: false },
  { blocked: true },
  { backup: { ...backup, state: "failed" } },
])(
  "does not request options without eligible operator context %j",
  async (props) => {
    const fetch = vi.fn();
    setup(fetch, props);
    expect(fetch).not.toHaveBeenCalled();
    expect(screen.queryByRole("link")).toBeNull();
  },
);

test("fresh scope never inherits prior availability or a retired denial", async () => {
  const pending: Array<(response: Response) => void> = [];
  const fetch = vi.fn(
    (_url: string, _init: RequestInit) =>
      new Promise<Response>((resolve) => pending.push(resolve)),
  );
  const { rerender, values, onDenied } = setup(fetch);
  rerender(
    <StorageArchiveDownload {...values} scope={{ ...scope, actor: "next" }} />,
  );
  expect(fetch).toHaveBeenCalledTimes(2);
  expect(fetch.mock.calls[0]![1].signal?.aborted).toBe(true);
  await act(async () => pending[0]!(new Response("denied", { status: 403 })));
  expect(onDenied).not.toHaveBeenCalled();
  expect(screen.queryByRole("link")).toBeNull();
  await act(async () => pending[1]!(new Response(JSON.stringify(options))));
  await screen.findByRole("link", { name: "Download archive" });
});

test("regaining owner access rechecks availability", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(new Response(JSON.stringify(options)))
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          available: false,
          reason_code: "local_copy_unavailable",
          backup_id: "backup",
        }),
      ),
    );
  const { rerender, values } = setup(fetch);
  await screen.findByRole("link", { name: "Download archive" });
  rerender(<StorageArchiveDownload {...values} owner={false} />);
  rerender(<StorageArchiveDownload {...values} />);
  expect(screen.queryByRole("link")).toBeNull();
  await screen.findByText("The local archive copy is unavailable.");
  expect(fetch).toHaveBeenCalledTimes(2);
});

test.each(["project_id", "resource_id"] as const)(
  "changed backup %s retires cached availability even when its ID is unchanged",
  async (field) => {
    const fetch = vi.fn(async () => new Response(JSON.stringify(options)));
    const view = setup(fetch);
    await screen.findByRole("link", { name: "Download archive" });
    view.rerender(
      <StorageArchiveDownload
        {...view.values}
        backup={{ ...backup, [field]: "other" }}
      />,
    );
    expect(screen.queryByRole("link", { name: "Download archive" })).toBeNull();
    await screen.findByText(/availability could not be verified/);
    expect(fetch).toHaveBeenCalledTimes(1);
  },
);
