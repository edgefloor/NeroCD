import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { ApiError } from "@/api/errors";
import * as api from "@/api/hosting-storage-archives";
import type { HostingStorage } from "@/api/hosting-storage";
import type { Resource } from "@/api/hosting";
import { StorageArchives } from "./StorageArchives";
vi.mock("@/api/hosting-storage-archives", () => ({
  archiveBackupEligible: vi.fn(),
  listArchiveBackups: vi.fn(),
  getArchiveBackup: vi.fn(),
  getArchiveOperation: vi.fn(),
  getArchiveRestore: vi.fn(),
  requestArchiveBackup: vi.fn(),
  requestArchiveRestore: vi.fn(),
  requestArchiveRecovery: vi.fn(),
  refreshArchiveConfiguration: vi.fn(),
}));
const hash = `sha256:${"a".repeat(64)}`;
const resource = {
  id: "r",
  project_id: "p",
  name: "Uploads",
  generation: 2,
} as Resource;
const storage = {
  id: "storage",
  resource_id: "r",
  destination_id: "d",
  binding_id: "b",
  hash,
} as HostingStorage;
const backup = {
  id: "backup",
  operation_id: "op",
  state: "succeeded",
  created_at: "2026-10-24",
  blob: {},
} as api.Backup;
const operation: api.ArchiveOperation = {
  id: "op",
  resource: "r",
  mode: "backup",
  state: "succeeded",
  backup: "backup",
  planHash: hash,
  mutationHold: false,
  ownershipHold: false,
  phases: ["archive_published"],
  result: {
    archive: "succeeded",
    resume: "resumed",
    uncertain: false,
    verified: false,
    failure: undefined,
  },
};
let client: QueryClient;
beforeEach(() => {
  vi.mocked(api.archiveBackupEligible).mockResolvedValue(true);
  vi.mocked(api.refreshArchiveConfiguration).mockResolvedValue(resource);
  vi.mocked(api.listArchiveBackups).mockResolvedValue({
    items: [backup],
    limit: 20,
    offset: 0,
    count: 1,
    total: 1,
  });
  vi.mocked(api.getArchiveBackup).mockResolvedValue(backup);
  vi.mocked(api.getArchiveOperation).mockResolvedValue(operation);
  vi.mocked(api.requestArchiveBackup).mockResolvedValue(backup);
});
afterEach(() => {
  cleanup();
  client?.clear();
  vi.resetAllMocks();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});
test.each([401, 403])(
  "actual refresh adapter masks a saved intent when delayed %i follows 503, without waiting for a stalled read",
  async (status) => {
    const actual = await vi.importActual<typeof api>(
      "@/api/hosting-storage-archives",
    );
    vi.mocked(api.refreshArchiveConfiguration).mockImplementation(
      actual.refreshArchiveConfiguration,
    );
    vi.mocked(api.requestArchiveBackup).mockRejectedValueOnce(
      new Error("lost delivery"),
    );
    let deny!: (response: Response) => void;
    let finishTail!: (cause: unknown) => void;
    const fetch = vi.fn((path: string) => {
      if (path.endsWith("/storage"))
        return new Promise<Response>((resolve) => {
          deny = resolve;
        });
      if (path.endsWith("/binding"))
        return new Promise<Response>((_, reject) => {
          finishTail = reject;
        });
      return Promise.resolve(new Response("{}", { status: 503 }));
    });
    vi.stubGlobal("fetch", fetch);
    setup();
    await loaded();
    fireEvent.click(screen.getByRole("checkbox"));
    click("Request upload backup");
    await screen.findByText(/Saved backup request/);
    click("Refresh archive status");
    try {
      await act(async () => {
        await Promise.resolve();
      });
      expect(screen.getByText(/Saved backup request/)).toBeTruthy();
      await act(async () => {
        deny(new Response("{}", { status }));
      });
      await screen.findByText(/Archive access is unavailable. Reload the page/);
      expect(screen.queryByText(/Saved backup request/)).toBeNull();
      expect(
        screen.queryByRole("button", {
          name: "Retry identical archive request",
        }),
      ).toBeNull();
      expect(
        screen.queryByRole("button", { name: "Backup backup" }),
      ).toBeNull();
      expect(api.requestArchiveBackup).toHaveBeenCalledTimes(1);
      expect(fetch).toHaveBeenCalledTimes(3);
    } finally {
      await act(async () => {
        finishTail(new Error("late tail"));
      });
    }
  },
);
function setup(owner = true) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <StorageArchives
        actor="actor"
        resource={resource}
        storage={storage}
        owner={owner}
        paused={false}
      />
    </QueryClientProvider>,
  );
}
const click = (name: string) =>
  fireEvent.click(screen.getByRole("button", { name }));
async function loaded() {
  await screen.findByRole("button", { name: "Backup backup" });
  await waitFor(() =>
    expect(
      screen
        .getByRole("button", { name: "Refresh archive status" })
        .hasAttribute("disabled"),
    ).toBe(false),
  );
}
test("owner explicitly consents to local backup; no mutation happens during reads", async () => {
  setup();
  await loaded();
  expect(api.requestArchiveBackup).not.toHaveBeenCalled();
  expect(screen.getByText(/Host or disk loss/)).toBeTruthy();
  fireEvent.click(screen.getByRole("checkbox"));
  click("Request upload backup");
  await screen.findByText(/Archive outcome: succeeded/);
  expect(api.requestArchiveBackup).toHaveBeenCalledTimes(1);
  expect(vi.mocked(api.requestArchiveBackup).mock.calls[0]![1]).toEqual({
    request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
    expected_resource_generation: 2,
  });
  expect(screen.getByText(/Writer resumption: resumed/)).toBeTruthy();
});
test("viewer reads history, selected safe progress and known restore details without write controls", async () => {
  setup(false);
  await loaded();
  click("Backup backup");
  await screen.findByText(/Archive outcome: succeeded/);
  expect(
    screen.queryByRole("button", { name: "Request upload backup" }),
  ).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Restore into new resource" }),
  ).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Request archive recovery" }),
  ).toBeNull();
  expect(api.archiveBackupEligible).not.toHaveBeenCalled();
});
test("ambiguous backup retry freezes the body and identity, then separate submission gets a new identity", async () => {
  vi.mocked(api.requestArchiveBackup).mockRejectedValueOnce(new Error("lost"));
  const view = setup();
  await loaded();
  fireEvent.click(screen.getByRole("checkbox"));
  click("Request upload backup");
  await screen.findByText(/Delivery or current status is unknown/);
  view.rerender(
    <QueryClientProvider client={client}>
      <StorageArchives
        actor="actor"
        resource={{ ...resource, generation: 3 }}
        storage={storage}
        owner={true}
        paused={false}
      />
    </QueryClientProvider>,
  );
  await loaded();
  click("Retry identical archive request");
  await screen.findByText(/Archive outcome: succeeded/);
  const calls = vi.mocked(api.requestArchiveBackup).mock.calls;
  expect(calls[0]![1]).toBe(calls[1]![1]);
  expect(calls[1]![1].expected_resource_generation).toBe(2);
  await loaded();
  fireEvent.click(screen.getByRole("checkbox"));
  click("Request upload backup");
  await waitFor(() =>
    expect(api.requestArchiveBackup).toHaveBeenCalledTimes(3),
  );
  expect(calls[2]![1].request_id).not.toBe(calls[0]![1].request_id);
  expect(calls[2]![1].expected_resource_generation).toBe(3);
});
test("restore always names a new resource, retains inputs on retry and links its storage", async () => {
  vi.mocked(api.requestArchiveRestore)
    .mockRejectedValueOnce(new Error("lost"))
    .mockResolvedValue({
      resource: "new",
      name: "Recovered uploads",
      operation: {
        ...operation,
        id: "restore",
        resource: "new",
        mode: "restore",
      },
    });
  vi.mocked(api.getArchiveRestore).mockResolvedValue({
    resource: "new",
    name: "Recovered uploads",
    operation: {
      ...operation,
      id: "restore",
      resource: "new",
      mode: "restore",
      result: { ...operation.result!, verified: true },
    },
  });
  setup();
  await loaded();
  click("Backup backup");
  await screen.findByRole("form", {
    name: "Restore backup into a new resource",
  });
  await waitFor(() =>
    expect(
      screen
        .getByRole("button", { name: "Refresh archive status" })
        .hasAttribute("disabled"),
    ).toBe(false),
  );
  fireEvent.change(screen.getByLabelText("New restored resource name"), {
    target: { value: "Recovered uploads" },
  });
  click("Restore into new resource");
  await screen.findByText(/Delivery or current status is unknown/);
  click("Retry identical archive request");
  expect(
    await screen.findByRole("link", { name: "Open Projects" }),
  ).toHaveProperty("pathname", "/projects");
  expect(
    screen.getByText(/Restore file validation: verified/).textContent,
  ).toContain("Application read/write verification remains pending");
  const calls = vi.mocked(api.requestArchiveRestore).mock.calls;
  expect(calls[0]![2]).toBe(calls[1]![2]);
  expect(calls[0]![2].name).toBe("Recovered uploads");
});
test("409 refresh reviews remote generation without changing the saved retry; explicit new submission uses generation 3", async () => {
  vi.mocked(api.requestArchiveBackup)
    .mockRejectedValueOnce(new ApiError(409, "changed", null))
    .mockRejectedValueOnce(new ApiError(409, "changed", null));
  vi.mocked(api.refreshArchiveConfiguration).mockResolvedValue({
    ...resource,
    generation: 3,
  });
  setup();
  await loaded();
  fireEvent.click(screen.getByRole("checkbox"));
  click("Request upload backup");
  await screen.findByText(/The resource, backup or hold changed/);
  const original = vi.mocked(api.requestArchiveBackup).mock.calls[0]![1];
  click("Refresh archive status");
  await screen.findByText(/Reviewed current resource generation 3/);
  await loaded();
  expect(api.requestArchiveBackup).toHaveBeenCalledTimes(1);
  click("Retry identical archive request");
  await screen.findByText(/The resource, backup or hold changed/);
  expect(vi.mocked(api.requestArchiveBackup).mock.calls[1]![1]).toBe(original);
  expect(original.expected_resource_generation).toBe(2);
  click("Discard saved request and review a separate submission");
  fireEvent.click(screen.getByRole("checkbox"));
  click("Request upload backup");
  await waitFor(() =>
    expect(api.requestArchiveBackup).toHaveBeenCalledTimes(3),
  );
  const separate = vi.mocked(api.requestArchiveBackup).mock.calls[2]![1];
  expect(separate.expected_resource_generation).toBe(3);
  expect(separate.request_id).not.toBe(original.request_id);
});
test("uncertain resume and held mutation require explicit exact-plan recovery", async () => {
  vi.mocked(api.getArchiveOperation).mockResolvedValue({
    ...operation,
    state: "reconciliation_required",
    mutationHold: true,
    result: { ...operation.result!, resume: "uncertain", uncertain: true },
  });
  vi.mocked(api.requestArchiveRecovery).mockRejectedValue(new Error("lost"));
  setup();
  await loaded();
  click("Backup backup");
  await screen.findByText(/Writer resumption: uncertain/);
  expect(api.requestArchiveRecovery).not.toHaveBeenCalled();
  await waitFor(() =>
    expect(
      screen
        .getByRole("button", { name: "Refresh archive status" })
        .hasAttribute("disabled"),
    ).toBe(false),
  );
  fireEvent.click(screen.getByLabelText(/I reviewed this operation/));
  click("Request archive recovery");
  await screen.findByText(/Delivery or current status is unknown/);
  expect(
    vi.mocked(api.requestArchiveRecovery).mock.calls[0]!.slice(1, 4),
  ).toEqual([
    "op",
    "backup",
    {
      request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
      expected_plan_hash: hash,
    },
  ]);
});
test.each([503, 409, 403])(
  "read status %i suppresses stale history and pauses writes",
  async (status) => {
    setup();
    await loaded();
    vi.mocked(api.listArchiveBackups).mockRejectedValue(
      new ApiError(status, "private diagnostic", null),
    );
    click("Refresh archive status");
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: "Backup backup" }),
      ).toBeNull(),
    );
    expect(screen.queryByText("private diagnostic")).toBeNull();
    expect(api.requestArchiveBackup).not.toHaveBeenCalled();
    if (status === 503)
      expect(screen.getByText(/Qualified archive configuration/)).toBeTruthy();
  },
);
test("ineligible deployment cannot request a backup", async () => {
  vi.mocked(api.archiveBackupEligible).mockResolvedValue(false);
  setup();
  await loaded();
  fireEvent.click(screen.getByRole("checkbox"));
  expect(
    screen
      .getByRole("button", { name: "Request upload backup" })
      .hasAttribute("disabled"),
  ).toBe(true);
  expect(api.requestArchiveBackup).not.toHaveBeenCalled();
});
test("status polling stops and exposes staleness without requesting recovery", async () => {
  setup(false);
  await loaded();
  vi.useFakeTimers();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(61000);
  });
  // The initial timer was created with real timers; force a selection to start a fake-timer window.
  click("Backup backup");
  await act(async () => {
    await vi.advanceTimersByTimeAsync(61000);
  });
  expect(screen.getByText(/Status may be stale or unknown/)).toBeTruthy();
  expect(api.requestArchiveRecovery).not.toHaveBeenCalled();
});
