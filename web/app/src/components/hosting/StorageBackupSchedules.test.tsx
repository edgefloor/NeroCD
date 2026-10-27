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
  vi.unstubAllGlobals();
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
  expect(vi.mocked(api.saveStorageBackupSchedule).mock.calls[1]![3]).toBe(
    saved,
  );
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

const composeSchedule: api.StorageBackupSchedule = {
  ...schedule,
  version: "managed-compose.local-volume-archive.schedule.v1",
  volume: "uploads",
  acknowledge_interruption: true,
};
const volumeEvidence = {
  resource_id: "resource",
  resource_generation: 4,
  operation_id: "deploy",
  available: true,
  volumes: [
    {
      volume: "uploads",
      eligible: true,
      mounts: [{ component: "web", mount_path: "/files", read_only: false }],
    },
  ],
};
function mountCompose(owner = true, blocked = false) {
  const onDenied = vi.fn();
  const view = render(
    <QueryClientProvider client={client}>
      <StorageBackupSchedules
        scope={scope}
        resource={resource}
        owner={owner}
        blocked={blocked}
        composeVolume="uploads"
        onRemoteTarget={() => {}}
        onSelectBackup={() => {}}
        onDenied={onDenied}
      />
    </QueryClientProvider>,
  );
  return { ...view, onDenied };
}
async function reviewCompose() {
  fireEvent.click(
    screen.getByRole("button", { name: "Review Compose volume schedule" }),
  );
  await screen.findByRole("region", {
    name: "Reviewed Compose volume schedule",
  });
}
function consentCompose() {
  fireEvent.click(
    screen.getByRole("checkbox", { name: /I acknowledge interruption/ }),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Confirm Compose volume schedule" }),
  );
}
test("initially disabled Compose schedule reviews fresh volume and requires interruption consent", async () => {
  const fetch = vi.fn(async () => new Response(JSON.stringify(volumeEvidence)));
  vi.stubGlobal("fetch", fetch);
  vi.mocked(api.saveStorageBackupSchedule).mockResolvedValue({
    ...composeSchedule,
    enabled: false,
  });
  mountCompose();
  fireEvent.click(screen.getByRole("checkbox", { name: "Enabled" }));
  await reviewCompose();
  expect(
    screen.getByRole<HTMLButtonElement>("button", {
      name: "Confirm Compose volume schedule",
    }).disabled,
  ).toBe(true);
  expect(api.saveStorageBackupSchedule).not.toHaveBeenCalled();
  consentCompose();
  await waitFor(() =>
    expect(api.saveStorageBackupSchedule).toHaveBeenCalledOnce(),
  );
  expect(
    vi.mocked(api.saveStorageBackupSchedule).mock.calls[0]![3],
  ).toMatchObject({
    volume: "uploads",
    enabled: false,
    expected_version: 0,
    expected_deployed_operation_id: "deploy",
    acknowledge_interruption: true,
  });
  expect(fetch).toHaveBeenCalledOnce();
});
test("enable after redeploy pins fresh deployment and lost response retries identical body without another review", async () => {
  vi.mocked(api.listStorageBackupSchedules).mockResolvedValue([
    { ...composeSchedule, enabled: false },
  ]);
  const fetch = vi.fn(
    async () =>
      new Response(
        JSON.stringify({ ...volumeEvidence, operation_id: "new-deploy" }),
      ),
  );
  vi.stubGlobal("fetch", fetch);
  vi.mocked(api.saveStorageBackupSchedule)
    .mockRejectedValueOnce(Error("lost response"))
    .mockResolvedValueOnce({ ...composeSchedule, revision: 2 });
  mountCompose();
  fireEvent.click(
    await screen.findByRole("button", { name: "View schedule schedule" }),
  );
  fireEvent.click(screen.getByRole("checkbox", { name: "Enabled" }));
  await reviewCompose();
  consentCompose();
  await screen.findByRole("button", {
    name: "Retry identical schedule request",
  });
  await waitFor(() =>
    expect(
      screen.getByRole<HTMLButtonElement>("button", {
        name: "Retry identical schedule request",
      }).disabled,
    ).toBe(false),
  );
  const input = vi.mocked(api.saveStorageBackupSchedule).mock.calls[0]![3];
  expect(input).toMatchObject({
    volume: "uploads",
    enabled: true,
    expected_version: 1,
    expected_deployed_operation_id: "new-deploy",
    acknowledge_interruption: true,
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Retry identical schedule request" }),
  );
  await waitFor(() =>
    expect(api.saveStorageBackupSchedule).toHaveBeenCalledTimes(2),
  );
  expect(vi.mocked(api.saveStorageBackupSchedule).mock.calls[1]![3]).toBe(
    input,
  );
  expect(fetch).toHaveBeenCalledOnce();
});
test("Compose history and disable require no live runtime evidence", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  vi.mocked(api.listStorageBackupSchedules).mockResolvedValue([
    composeSchedule,
  ]);
  vi.mocked(api.saveStorageBackupSchedule).mockResolvedValue({
    ...composeSchedule,
    revision: 2,
    enabled: false,
  });
  mountCompose();
  fireEvent.click(
    await screen.findByRole("button", { name: "View schedule schedule" }),
  );
  await waitFor(() =>
    expect(api.listStorageBackupOccurrences).toHaveBeenCalledOnce(),
  );
  fireEvent.click(screen.getByRole("button", { name: "Disable schedule" }));
  await waitFor(() =>
    expect(api.saveStorageBackupSchedule).toHaveBeenCalledOnce(),
  );
  const input = vi.mocked(api.saveStorageBackupSchedule).mock.calls[0]![3];
  expect(input).toMatchObject({
    volume: "uploads",
    enabled: false,
    expected_version: 1,
  });
  expect(input.expected_deployed_operation_id).toBeUndefined();
  expect(input.acknowledge_interruption).toBeUndefined();
  expect(fetch).not.toHaveBeenCalled();
});
test("unavailable or stale Compose evidence cannot produce a schedule review", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify({ ...volumeEvidence, available: false })),
    ),
  );
  mountCompose();
  await reviewUnavailable();
  expect(api.saveStorageBackupSchedule).not.toHaveBeenCalled();
  expect(
    screen.queryByRole("region", { name: "Reviewed Compose volume schedule" }),
  ).toBeNull();
});
async function reviewUnavailable() {
  fireEvent.click(
    screen.getByRole("button", { name: "Review Compose volume schedule" }),
  );
  await screen.findByText(/Current Compose volume evidence is unavailable/);
}
test("retirement aborts pending schedule mutation and ignores its late receipt", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(JSON.stringify(volumeEvidence))),
  );
  let resolve!: (v: api.StorageBackupSchedule) => void;
  vi.mocked(api.saveStorageBackupSchedule).mockImplementation(
    () =>
      new Promise((r) => {
        resolve = r;
      }),
  );
  const view = mountCompose();
  await reviewCompose();
  consentCompose();
  await waitFor(() =>
    expect(api.saveStorageBackupSchedule).toHaveBeenCalledOnce(),
  );
  const signal = vi.mocked(api.saveStorageBackupSchedule).mock.calls[0]![4];
  view.unmount();
  expect(signal.aborted).toBe(true);
  resolve(composeSchedule);
  await Promise.resolve();
  expect(api.listStorageBackupSchedules).toHaveBeenCalledOnce();
});
test("Compose viewer can inspect history without mutation controls", async () => {
  vi.mocked(api.listStorageBackupSchedules).mockResolvedValue([
    composeSchedule,
  ]);
  mountCompose(false);
  fireEvent.click(
    await screen.findByRole("button", { name: "View schedule schedule" }),
  );
  await waitFor(() =>
    expect(api.listStorageBackupOccurrences).toHaveBeenCalled(),
  );
  expect(
    screen.queryByRole("button", { name: "Review Compose volume schedule" }),
  ).toBeNull();
  expect(screen.queryByRole("button", { name: "Disable schedule" })).toBeNull();
});

test("blocked Compose panel cannot start a review", () => {
  mountCompose(true, true);
  expect(
    screen.getByRole<HTMLButtonElement>("button", {
      name: "Review Compose volume schedule",
    }).disabled,
  ).toBe(true);
  expect(api.saveStorageBackupSchedule).not.toHaveBeenCalled();
});
test("denied fresh review retires scheduling and saved mutation controls", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify({ error: "denied" }), { status: 403 }),
    ),
  );
  const { onDenied } = mountCompose();
  fireEvent.click(
    screen.getByRole("button", { name: "Review Compose volume schedule" }),
  );
  await waitFor(() => expect(onDenied).toHaveBeenCalledOnce());
  await waitFor(() =>
    expect(
      screen.queryByRole("form", {
        name: "Save recurring Compose volume backup",
      }),
    ).toBeNull(),
  );
  expect(api.saveStorageBackupSchedule).not.toHaveBeenCalled();
});
