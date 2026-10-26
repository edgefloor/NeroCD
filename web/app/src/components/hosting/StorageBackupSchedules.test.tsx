import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { StorageBackupSchedules } from "./StorageBackupSchedules";
import * as api from "@/api/hosting-storage-backup-schedules";
import type { Resource } from "@/api/hosting";
vi.mock("@/api/hosting-storage-backup-schedules", async (original) => ({
  ...(await original<
    typeof import("@/api/hosting-storage-backup-schedules")
  >()),
  listStorageBackupSchedules: vi.fn(),
  listStorageBackupOccurrences: vi.fn(),
  saveStorageBackupSchedule: vi.fn(),
}));
const resource = {
  id: "resource",
  project_id: "project",
  generation: 4,
} as Resource;
const scope = {
  actor: "owner",
  project: "project",
  resource: "resource",
  destination: "destination",
  binding: "binding",
};
const schedule: api.StorageBackupSchedule = {
  id: "schedule",
  version: "hosting.storage-backup.schedule.v1",
  project_id: "project",
  resource_id: "resource",
  revision: 1,
  frequency: "daily",
  timezone: "UTC",
  enabled: true,
  created_at: "2026-10-26T00:00:00Z",
  updated_at: "2026-10-26T00:00:00Z",
};
let client: QueryClient;
beforeEach(() => {
  vi.mocked(api.listStorageBackupSchedules).mockResolvedValue([]);
  vi.mocked(api.listStorageBackupOccurrences).mockResolvedValue([]);
  vi.mocked(api.saveStorageBackupSchedule).mockReset();
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(() => {
  cleanup();
  client.clear();
  vi.clearAllMocks();
});
function mount(owner = true) {
  render(
    <QueryClientProvider client={client}>
      <StorageBackupSchedules
        scope={scope}
        resource={resource}
        owner={owner}
        blocked={false}
        onRemoteTarget={() => {}}
        onSelectBackup={() => {}}
        onDenied={() => {}}
      />
    </QueryClientProvider>,
  );
}
test("ambiguous create preserves all inputs and retries the same identity", async () => {
  vi.mocked(api.saveStorageBackupSchedule)
    .mockRejectedValueOnce(new Error("lost ACK"))
    .mockResolvedValueOnce(schedule);
  mount();
  fireEvent.change(screen.getByLabelText("Backup frequency"), {
    target: { value: "hourly" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Create backup schedule" }),
  );
  await screen.findByRole("button", {
    name: "Retry identical schedule request",
  });
  const saved = vi.mocked(api.saveStorageBackupSchedule).mock.calls[0]![3];
  expect(saved.request_id).toMatch(/^[a-f0-9]{32}$/);
  expect(saved.frequency).toBe("hourly");
  expect(saved.expected_resource_generation).toBe(4);
  expect(
    (screen.getByLabelText("Backup frequency") as HTMLInputElement).disabled,
  ).toBe(true);
  fireEvent.click(
    screen.getByRole("button", { name: "Retry identical schedule request" }),
  );
  await waitFor(() =>
    expect(api.saveStorageBackupSchedule).toHaveBeenCalledTimes(2),
  );
  expect(vi.mocked(api.saveStorageBackupSchedule).mock.calls[1]![3]).toBe(saved);
});
test("disable uses exact revision and preserves remote selection", async () => {
  const remote = { target_id: "target", revision: 3 };
  vi.mocked(api.listStorageBackupSchedules).mockResolvedValue([
    { ...schedule, remote_target: remote },
  ]);
  vi.mocked(api.saveStorageBackupSchedule).mockResolvedValue({
    ...schedule,
    revision: 2,
    enabled: false,
    remote_target: remote,
  });
  mount();
  fireEvent.click(
    await screen.findByRole("button", { name: "View schedule schedule" }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Disable schedule" }));
  await waitFor(() =>
    expect(api.saveStorageBackupSchedule).toHaveBeenCalledOnce(),
  );
  expect(
    vi.mocked(api.saveStorageBackupSchedule).mock.calls[0]![3],
  ).toMatchObject({
    enabled: false,
    expected_version: 1,
    remote_target: remote,
  });
});
test("viewer can read history and cannot create or disable", async () => {
  vi.mocked(api.listStorageBackupSchedules).mockResolvedValue([schedule]);
  mount(false);
  fireEvent.click(
    await screen.findByRole("button", { name: "View schedule schedule" }),
  );
  await waitFor(() =>
    expect(api.listStorageBackupOccurrences).toHaveBeenCalled(),
  );
  expect(
    screen.queryByRole("form", { name: "Save recurring upload backup" }),
  ).toBeNull();
  expect(screen.queryByRole("button", { name: "Disable schedule" })).toBeNull();
});
