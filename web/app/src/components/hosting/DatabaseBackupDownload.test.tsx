import type { DatabaseBackup } from "@/api/hosting-database-backups";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { DatabaseBackupDownload } from "./DatabaseBackupDownload";
const scope = {
  environment: "env",
  actor: "u",
  project: "p",
  resource: "r",
  destination: "d",
  binding: "b",
};
const blob = {
  digest: `sha256:${"a".repeat(64)}`,
  bytes: 123,
  format: "postgresql_custom" as const,
};
const backup = {
  id: "backup",
  backup_id: "backup",
  project_id: "p",
  source_resource_id: "r",
  version: "postgresql.logical-local.v1",
  mode: "backup",
  state: "succeeded",
  blob,
} as DatabaseBackup;
const options = { available: true, reason_code: "", backup_id: "backup", blob };
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function setup(fetch: ReturnType<typeof vi.fn>, props = {}) {
  vi.stubGlobal("fetch", fetch);
  const onDenied = vi.fn();
  const values = {
    context: scope,
    backup,
    owner: true,
    blocked: false,
    onDenied,
    ...props,
  };
  return {
    ...render(<DatabaseBackupDownload {...values} />),
    values,
    onDenied,
  };
}
test("uses native authenticated download after options, including Compose without retention copies", async () => {
  const fetch = vi.fn(async () => new Response(JSON.stringify(options)));
  setup(fetch);
  const link = await screen.findByRole("link", {
    name: "Download database backup",
  });
  expect(link.getAttribute("href")).toBe(
    "/api/v1/database-backups/backup/archive",
  );
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toBe("noopener");
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(fetch.mock.calls[0]).toEqual([
    "/api/v1/database-backups/backup/archive-options",
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
  "copy_busy",
  "placement_changed",
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
  expect(
    screen.queryByRole("link", { name: "Download database backup" }),
  ).toBeNull();
  expect(
    (
      screen.getByRole("button", {
        name: "Download database backup",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  fetch.mockResolvedValueOnce(new Response(JSON.stringify(options)));
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh download availability" }),
  );
  await screen.findByRole("link", { name: "Download database backup" });
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
  await screen.findByText("The local database backup copy is unavailable.");
  expect(screen.queryByRole("link")).toBeNull();
});
test.each([
  "actor",
  "project",
  "environment",
  "resource",
  "destination",
  "binding",
])(
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
      <DatabaseBackupDownload
        {...values}
        context={{ ...scope, [field]: "other" }}
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
  await screen.findByRole("link", { name: "Download database backup" });
  const other = {
    ...backup,
    id: "other",
    backup_id: "other",
    blob: { ...blob, digest: "sha256:" + "b".repeat(64) },
  } as DatabaseBackup;
  rerender(<DatabaseBackupDownload {...values} backup={other} />);
  expect(screen.queryByRole("link")).toBeNull();
  rerender(<DatabaseBackupDownload {...values} owner={false} />);
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
    <DatabaseBackupDownload
      {...values}
      context={{ ...scope, actor: "next" }}
    />,
  );
  expect(fetch).toHaveBeenCalledTimes(2);
  expect(fetch.mock.calls[0]![1].signal?.aborted).toBe(true);
  await act(async () => pending[0]!(new Response("denied", { status: 403 })));
  expect(onDenied).not.toHaveBeenCalled();
  expect(screen.queryByRole("link")).toBeNull();
  await act(async () => pending[1]!(new Response(JSON.stringify(options))));
  await screen.findByRole("link", { name: "Download database backup" });
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
  await screen.findByRole("link", { name: "Download database backup" });
  rerender(<DatabaseBackupDownload {...values} owner={false} />);
  rerender(<DatabaseBackupDownload {...values} />);
  expect(screen.queryByRole("link")).toBeNull();
  await screen.findByText(/The local database backup copy is unavailable/);
  expect(fetch).toHaveBeenCalledTimes(2);
});

test.each(["project_id", "source_resource_id"] as const)(
  "changed backup %s retires cached availability even when its ID is unchanged",
  async (field) => {
    const fetch = vi.fn(async () => new Response(JSON.stringify(options)));
    const view = setup(fetch);
    await screen.findByRole("link", { name: "Download database backup" });
    view.rerender(
      <DatabaseBackupDownload
        {...view.values}
        backup={{ ...backup, [field]: "other" }}
      />,
    );
    expect(
      screen.queryByRole("link", { name: "Download database backup" }),
    ).toBeNull();
    await screen.findByText(/availability could not be verified/);
    expect(fetch).toHaveBeenCalledTimes(1);
  },
);
