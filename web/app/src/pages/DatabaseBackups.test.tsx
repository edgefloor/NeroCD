vi.mock("../api/hosting-database-backup-retention", async (original) => ({
  ...(await original<typeof import("../api/hosting-database-backup-retention")>()),
  getRetentionPolicy: vi.fn(async () => null),
  listRetentionExecutions: vi.fn(async () => []),
}));
import * as remote from "../api/hosting-database-backup-targets";
vi.mock("../api/hosting-database-backup-targets", async (original) => ({
  ...(await original<
    typeof import("../api/hosting-database-backup-targets")
  >()),
  listDatabaseBackupTargets: vi.fn(async () => []),
  listDatabaseBackupValidations: vi.fn(async () => []),
  listDatabaseBackupTransfers: vi.fn(async () => []),
  retryDatabaseBackupUpload: vi.fn(),
}));
import { StrictMode } from "react";
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
import { DatabaseBackups } from "./DatabaseBackups";
import type { HostingDatabase } from "../api/hosting-databases";
import type { DatabaseBackup } from "../api/hosting-database-backups";
import { ApiError } from "../api/errors";
import * as api from "../api/hosting-database-backups";
vi.mock("../api/hosting-database-backup-schedules", async (original) => ({
  ...(await original<
    typeof import("../api/hosting-database-backup-schedules")
  >()),
  listDatabaseBackupSchedules: vi.fn(async () => []),
}));
vi.mock("../api/hosting-database-backups", async (original) => {
  const actual =
    await original<typeof import("../api/hosting-database-backups")>();
  return {
    ...actual,
    listDatabaseBackups: vi.fn(),
    requestDatabaseBackup: vi.fn(),
    restoreDatabaseBackup: vi.fn(),
  };
});
const database = {
  resource: {
    id: "db",
    project_id: "project",
    project_environment_id: "env",
    generation: 2,
    name: "Data",
  },
  binding: { destination_id: "runner" },
  engine: "postgresql",
} as HostingDatabase;
const scope = { actor: "owner", project: "project", environment: "env" };
const completed: DatabaseBackup = {
  max_bytes: 1073741824,
  timeout_seconds: 900,
  id: "backup",
  backup_id: "backup",
  version: "postgresql.logical-local.v1",
  project_id: "project",
  source_resource_id: "db",
  source_generation: 2,
  mode: "backup",
  state: "succeeded",
  consistency: "logical_snapshot_source_writable",
  ownership_held: false,
  created_at: "2026-10-25T12:00:00Z",
  updated_at: "2026-10-25T12:00:00Z",
  blob: {
    digest: "sha256:" + "a".repeat(64),
    bytes: 65537,
    format: "postgresql_custom",
  },
};
beforeEach(() => {
  vi.mocked(remote.listDatabaseBackupTargets).mockResolvedValue([]);
  vi.mocked(remote.listDatabaseBackupValidations).mockResolvedValue([]);
  vi.mocked(remote.listDatabaseBackupTransfers).mockResolvedValue([]);
  vi.mocked(api.listDatabaseBackups).mockResolvedValue([completed]);
  vi.mocked(api.requestDatabaseBackup).mockResolvedValue({
    ...completed,
    id: "new_backup",
    backup_id: "new_backup",
    state: "queued",
    ownership_held: true,
  });
  vi.mocked(api.restoreDatabaseBackup).mockResolvedValue({
    ...completed,
    id: "restore",
    mode: "restore",
    state: "waiting_target",
    ownership_held: true,
    target_resource_id: "new_database",
  });
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.useRealTimers();
});
function mount(owner = true, strict = false) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const denied = vi.fn();
  const element = (
    <QueryClientProvider client={client}>
      <DatabaseBackups
        database={database}
        scope={scope}
        owner={owner}
        blocked={false}
        onDenied={denied}
      />
    </QueryClientProvider>
  );
  return {
    ...render(strict ? <StrictMode>{element}</StrictMode> : element),
    client,
    denied,
  };
}
async function selectRestore() {
  await waitFor(() =>
    expect(
      (screen.getByLabelText("Completed backup") as HTMLSelectElement).disabled,
    ).toBe(false),
  );
  fireEvent.change(screen.getByLabelText("Completed backup"), {
    target: { value: "backup" },
  });
  fireEvent.change(screen.getByLabelText("New database name"), {
    target: { value: "Restored data" },
  });
}
test("owner can submit under StrictMode with exact reviewed generation and stable request", async () => {
  mount(true, true);
  const button = screen.getByRole("button", { name: "Back up now" });
  await waitFor(() =>
    expect((button as HTMLButtonElement).disabled).toBe(false),
  );
  fireEvent.click(button);
  await waitFor(() =>
    expect(api.requestDatabaseBackup).toHaveBeenCalledTimes(1),
  );
  const args = vi.mocked(api.requestDatabaseBackup).mock.calls[0]!;
  expect(args[2]).toEqual({
    request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
    expected_resource_generation: 2,
  });
  expect(args[3].aborted).toBe(false);
  expect(await screen.findByText(/Accepted operation new_backup/)).toBeTruthy();
});
test("viewer can inspect history but cannot execute or restore", async () => {
  mount(false);
  expect(await screen.findByText(/Backup · succeeded/)).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Back up now" })).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Restore into new database" }),
  ).toBeNull();
  expect(api.requestDatabaseBackup).not.toHaveBeenCalled();
  expect(api.restoreDatabaseBackup).not.toHaveBeenCalled();
});
test("ambiguous restore response preserves one request and target intent for explicit retry", async () => {
  vi.mocked(api.restoreDatabaseBackup).mockRejectedValueOnce(
    new TypeError("network"),
  );
  mount();
  await selectRestore();
  fireEvent.click(
    screen.getByRole("button", { name: "Restore into new database" }),
  );
  expect(
    await screen.findByRole("button", { name: "Retry the same request" }),
  ).toBeTruthy();
  const first = vi.mocked(api.restoreDatabaseBackup).mock.calls[0]!;
  fireEvent.click(
    screen.getByRole("button", { name: "Retry the same request" }),
  );
  await waitFor(() =>
    expect(api.restoreDatabaseBackup).toHaveBeenCalledTimes(2),
  );
  expect(vi.mocked(api.restoreDatabaseBackup).mock.calls[1]![3]).toEqual(
    first[3],
  );
  expect(first[2]).toBe("backup");
  expect(first[3]).toEqual({
    request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
    name: "Restored data",
  });
  expect(await screen.findByText(/Accepted operation restore/)).toBeTruthy();
});
test("definitive validation rejection allows name correction and preserves nonsecret input", async () => {
  vi.mocked(api.restoreDatabaseBackup).mockRejectedValueOnce(
    new ApiError(400, "bad_request", null),
  );
  mount();
  await selectRestore();
  fireEvent.click(
    screen.getByRole("button", { name: "Restore into new database" }),
  );
  await screen.findByRole("alert");
  const input = screen.getByLabelText("New database name") as HTMLInputElement;
  expect(input.disabled).toBe(false);
  expect(input.value).toBe("Restored data");
  expect(
    (screen.getByLabelText("Completed backup") as HTMLSelectElement).value,
  ).toBe("backup");
  fireEvent.change(input, { target: { value: "Corrected name" } });
  fireEvent.click(
    screen.getByRole("button", { name: "Restore into new database" }),
  );
  await waitFor(() =>
    expect(api.restoreDatabaseBackup).toHaveBeenCalledTimes(2),
  );
  expect(vi.mocked(api.restoreDatabaseBackup).mock.calls[1]![3].name).toBe(
    "Corrected name",
  );
});
test("unmount aborts the owned request and ignores a late accepted response", async () => {
  let resolve!: (v: DatabaseBackup) => void;
  vi.mocked(api.requestDatabaseBackup).mockImplementation(
    () =>
      new Promise((r) => {
        resolve = r;
      }),
  );
  const view = mount();
  await waitFor(() =>
    expect(
      (screen.getByRole("button", { name: "Back up now" }) as HTMLButtonElement)
        .disabled,
    ).toBe(false),
  );
  fireEvent.click(screen.getByRole("button", { name: "Back up now" }));
  await waitFor(() =>
    expect(api.requestDatabaseBackup).toHaveBeenCalledTimes(1),
  );
  const signal = vi.mocked(api.requestDatabaseBackup).mock.calls[0]![3];
  const count = vi.mocked(api.listDatabaseBackups).mock.calls.length;
  view.unmount();
  expect(signal.aborted).toBe(true);
  await act(async () => resolve(completed));
  expect(api.listDatabaseBackups).toHaveBeenCalledTimes(count);
});
test("denial clears scoped backup cache and disables controls", async () => {
  vi.mocked(api.listDatabaseBackups).mockRejectedValue(
    new ApiError(403, "forbidden", null),
  );
  const view = mount();
  await waitFor(() => expect(view.denied).toHaveBeenCalled());
  expect(screen.queryByRole("button", { name: "Back up now" })).toBeNull();
  expect(api.requestDatabaseBackup).not.toHaveBeenCalled();
});

const target: remote.DatabaseBackupTarget = {
  id: "storage",
  project_id: "project",
  version: "postgresql.backup-target.v1",
  revision: 2,
  name: "Daily archive",
  endpoint: "https://s3.example.invalid",
  bucket: "backups",
  region: "us-east-1",
  enabled: true,
  created_at: completed.created_at,
  updated_at: completed.updated_at,
};
const validation: remote.DatabaseBackupTransfer = {
  id: "validation",
  version: "postgresql.logical-s3-transfer.v1",
  project_id: "project",
  target_id: "storage",
  target_revision: 2,
  destination_id: "runner",
  mode: "validate",
  state: "succeeded",
  failure_code: "",
  created_at: completed.created_at,
  updated_at: completed.updated_at,
};
const upload: remote.DatabaseBackupTransfer = {
  ...validation,
  id: "upload",
  mode: "upload",
  backup_id: "backup",
  blob: completed.blob,
};
function remoteFixture() {
  vi.mocked(remote.listDatabaseBackupTargets).mockResolvedValue([target]);
  vi.mocked(remote.listDatabaseBackupValidations).mockResolvedValue([
    validation,
  ]);
}
test("only current runner validation enables immediate remote choice and freezes the selected revision", async () => {
  remoteFixture();
  mount();
  await screen.findAllByRole("option", { name: "Daily archive · revision 2" });
  fireEvent.change(screen.getByLabelText("Immediate backup destination"), {
    target: { value: "storage:2" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Back up now" }));
  await waitFor(() =>
    expect(api.requestDatabaseBackup).toHaveBeenCalledTimes(1),
  );
  expect(
    vi.mocked(api.requestDatabaseBackup).mock.calls[0]![2].remote_target,
  ).toEqual({ target_id: "storage", revision: 2 });
});
test("wrong runner, stale revision and disabled targets cannot be selected", async () => {
  remoteFixture();
  vi.mocked(remote.listDatabaseBackupValidations).mockResolvedValue([
    { ...validation, destination_id: "foreign" },
    { ...validation, id: "old", target_revision: 1 },
  ]);
  mount();
  await screen.findByText(/Daily archive · revision 2 · Enabled/);
  expect(
    screen.queryAllByRole("option", { name: "Daily archive · revision 2" }),
  ).toHaveLength(0);
});
test("successful dump and failed upload persist as separate outcomes and retry replays the same archive fence", async () => {
  remoteFixture();
  vi.mocked(remote.listDatabaseBackupTransfers).mockResolvedValue([
    { ...upload, state: "failed", failure_code: "access_denied" },
  ]);
  vi.mocked(remote.retryDatabaseBackupUpload)
    .mockRejectedValueOnce(new TypeError("network"))
    .mockResolvedValueOnce({ ...upload, id: "upload_retry", state: "queued" });
  mount();
  expect(
    await screen.findByText(
      /Local dump: succeeded. Latest remote upload: failed/,
    ),
  ).toBeTruthy();
  expect(screen.getByText(/Remote upload failed: access_denied/)).toBeTruthy();
  fireEvent.click(
    screen.getByRole("button", { name: "Retry upload for backup" }),
  );
  await screen.findByRole("button", { name: "Retry the same upload request" });
  const first = vi.mocked(remote.retryDatabaseBackupUpload).mock.calls[0]![2];
  fireEvent.click(
    screen.getByRole("button", { name: "Retry the same upload request" }),
  );
  await waitFor(() =>
    expect(remote.retryDatabaseBackupUpload).toHaveBeenCalledTimes(2),
  );
  expect(vi.mocked(remote.retryDatabaseBackupUpload).mock.calls[1]![2]).toEqual(
    first,
  );
  expect(first.expected_transfer_id).toBe("upload");
  expect(api.requestDatabaseBackup).not.toHaveBeenCalled();
});
test("restore requires an explicit saved remote object choice and sends no arbitrary URL or key", async () => {
  remoteFixture();
  vi.mocked(remote.listDatabaseBackupTargets).mockResolvedValue([
    { ...target, revision: 3 },
  ]);
  vi.mocked(remote.listDatabaseBackupTransfers).mockResolvedValue([
    upload,
    { ...upload, id: "failed", state: "failed" },
  ]);
  mount();
  await selectRestore();
  await screen.findByRole("option", { name: /Saved remote object/ });
  expect(
    (screen.getByLabelText("Restore archive source") as HTMLSelectElement)
      .value,
  ).toBe("");
  fireEvent.change(screen.getByLabelText("Restore archive source"), {
    target: { value: "upload" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Restore into new database" }),
  );
  await waitFor(() =>
    expect(api.restoreDatabaseBackup).toHaveBeenCalledTimes(1),
  );
  expect(vi.mocked(api.restoreDatabaseBackup).mock.calls[0]![3]).toEqual({
    request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
    name: "Restored data",
    remote_source: { transfer_id: "upload", expected_target_revision: 2 },
  });
});
test("viewer cannot retry a failed upload", async () => {
  vi.mocked(remote.listDatabaseBackupTransfers).mockResolvedValue([
    { ...upload, state: "failed" },
  ]);
  mount(false);
  await screen.findByText(
    /Local dump: succeeded. Latest remote upload: failed/,
  );
  expect(
    screen.queryByRole("button", { name: "Retry upload for backup" }),
  ).toBeNull();
});

for (const state of ["failed", "uncertain"] as const)
  test(`terminal ${state} dump without an archive stops waiting for an impossible upload`, async () => {
    vi.useFakeTimers();
    vi.mocked(api.listDatabaseBackups).mockResolvedValue([
      {
        ...completed,
        remote_target: { target_id: "storage", revision: 2 },
        state,
        blob: undefined,
        ownership_held: state === "uncertain",
      },
    ]);
    mount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(50);
    });
    const initial = vi.mocked(remote.listDatabaseBackupTransfers).mock.calls
      .length;
    expect(initial).toBeGreaterThan(0);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(9000);
    });
    expect(remote.listDatabaseBackupTransfers).toHaveBeenCalledTimes(initial);
  });
test("a successful archived remote dump polls until upload admission appears", async () => {
  vi.useFakeTimers();
  vi.mocked(api.listDatabaseBackups).mockResolvedValue([
    { ...completed, remote_target: { target_id: "storage", revision: 2 } },
  ]);
  mount();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(50);
  });
  const initial = vi.mocked(remote.listDatabaseBackupTransfers).mock.calls
    .length;
  await act(async () => {
    await vi.advanceTimersByTimeAsync(6000);
  });
  expect(
    vi.mocked(remote.listDatabaseBackupTransfers).mock.calls.length,
  ).toBeGreaterThan(initial);
});
test("an active transfer keeps polling even if the dump is terminal", async () => {
  vi.useFakeTimers();
  vi.mocked(api.listDatabaseBackups).mockResolvedValue([
    { ...completed, state: "failed", blob: undefined },
  ]);
  vi.mocked(remote.listDatabaseBackupTransfers).mockResolvedValue([
    { ...upload, state: "applying" },
  ]);
  mount();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(50);
  });
  const initial = vi.mocked(remote.listDatabaseBackupTransfers).mock.calls
    .length;
  await act(async () => {
    await vi.advanceTimersByTimeAsync(6000);
  });
  expect(
    vi.mocked(remote.listDatabaseBackupTransfers).mock.calls.length,
  ).toBeGreaterThan(initial);
});

test("backup workflow includes the retention policy and history panel", async () => {
  mount();
  await screen.findByRole("region", { name: "Database backup retention" });
  await screen.findByText("No retention policy saved.");
  expect(screen.getByRole("region", { name: "Retention execution history" })).toBeTruthy();
});
