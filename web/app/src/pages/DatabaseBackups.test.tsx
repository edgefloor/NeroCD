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
