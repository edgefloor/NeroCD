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
import { ApiError } from "../api/errors";
import type { HostingDatabase } from "../api/hosting-databases";
import { DatabaseBackupSchedules } from "./DatabaseBackupSchedules";
import * as api from "../api/hosting-database-backup-schedules";
vi.mock("../api/hosting-database-backup-schedules", async (original) => ({
  ...(await original<
    typeof import("../api/hosting-database-backup-schedules")
  >()),
  listDatabaseBackupSchedules: vi.fn(),
  listDatabaseBackupOccurrences: vi.fn(),
  saveDatabaseBackupSchedule: vi.fn(),
}));
const database = {
  resource: { id: "database", generation: 2 },
  engine: "postgresql",
} as HostingDatabase;
const scope = {
  actor: "owner",
  project: "project",
  environment: "environment",
};
const saved: api.DatabaseBackupSchedule = {
  id: "schedule",
  version: "postgresql.logical-local.schedule.v1",
  project_id: "project",
  resource_id: "database",
  revision: 1,
  frequency: "daily",
  timezone: "UTC",
  enabled: true,
  created_at: "2026-10-25T12:00:00Z",
  updated_at: "2026-10-25T12:00:00Z",
};
let values: api.DatabaseBackupSchedule[];
beforeEach(() => {
  values = [];
  vi.mocked(api.listDatabaseBackupSchedules).mockImplementation(
    async () => values,
  );
  vi.mocked(api.listDatabaseBackupOccurrences).mockResolvedValue([]);
  vi.mocked(api.saveDatabaseBackupSchedule).mockImplementation(
    async (_scope, _database, id, input) => {
      const out = {
        ...saved,
        id: id || saved.id,
        revision: input.expected_version + 1,
        frequency: input.frequency,
        timezone: input.timezone,
        enabled: input.enabled,
      };
      values = [out];
      return out;
    },
  );
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
  const element = (db = database) => (
    <QueryClientProvider client={client}>
      <DatabaseBackupSchedules
        database={db}
        scope={scope}
        owner={owner}
        blocked={false}
        onDenied={denied}
      />
    </QueryClientProvider>
  );
  return {
    ...render(strict ? <StrictMode>{element()}</StrictMode> : element()),
    client,
    denied,
    element,
  };
}
function savedCall(index: number) {
  const call = vi.mocked(api.saveDatabaseBackupSchedule).mock.calls[index];
  if (!call) throw new Error("Expected saved schedule request");
  return call;
}
async function enabledButton(name = "Create backup schedule") {
  await waitFor(() =>
    expect(
      (screen.getByRole("button", { name }) as HTMLButtonElement).disabled,
    ).toBe(false),
  );
  return screen.getByRole("button", { name });
}
test("StrictMode owner creates, edits and disables with exact reviewed revisions", async () => {
  mount(true, true);
  await enabledButton();
  fireEvent.change(screen.getByLabelText("Backup frequency"), {
    target: { value: "every_minute" },
  });
  fireEvent.change(screen.getByLabelText("Backup timezone"), {
    target: { value: "Europe/Budapest" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Create backup schedule" }),
  );
  await screen.findByText(/Saved schedule schedule revision 1/);
  const first = savedCall(0);
  expect(first[2]).toBe("");
  expect(first[3]).toMatchObject({
    expected_resource_generation: 2,
    expected_version: 0,
    frequency: "every_minute",
    timezone: "Europe/Budapest",
    enabled: true,
  });
  expect(first[4].aborted).toBe(false);
  fireEvent.click(await enabledButton("Disable schedule"));
  await screen.findByText(/Saved schedule schedule revision 2/);
  expect(savedCall(1)[3]).toMatchObject({
    expected_version: 1,
    enabled: false,
  });
  expect(
    (screen.getByLabelText("Schedule enabled") as HTMLInputElement).checked,
  ).toBe(false);
  fireEvent.change(screen.getByLabelText("Backup frequency"), {
    target: { value: "hourly" },
  });
  fireEvent.click(await enabledButton("Save schedule changes"));
  await screen.findByText(/Saved schedule schedule revision 3/);
  expect(savedCall(2)[3]).toMatchObject({
    expected_version: 2,
    frequency: "hourly",
    enabled: false,
  });
});
test("viewer reads occurrences without mutation controls or inferred restore authority", async () => {
  values = [saved];
  vi.mocked(api.listDatabaseBackupOccurrences).mockResolvedValue([
    {
      id: "occurrence",
      schedule_id: "schedule",
      project_id: "project",
      resource_id: "database",
      schedule_revision: 1,
      scheduled_at: "2026-10-25T12:00:00Z",
      frequency: "daily",
      timezone: "UTC",
      operation_id: "backup_exact",
      state: "uncertain",
      reason: "interrupted",
      ownership_held: true,
      created_at: "2026-10-25T12:00:03Z",
    },
  ]);
  mount(false);
  fireEvent.click(
    await screen.findByRole("button", { name: "View occurrences" }),
  );
  await screen.findByText("Backup operation: backup_exact");
  await screen.findByText(/Source ownership remains held/);
  expect(
    screen.queryByRole("button", { name: "Create backup schedule" }),
  ).toBeNull();
  expect(screen.queryByRole("button", { name: "Disable schedule" })).toBeNull();
  expect(api.saveDatabaseBackupSchedule).not.toHaveBeenCalled();
});
test("ambiguous response retains exact request and nonsecret fields for explicit replay", async () => {
  vi.mocked(api.saveDatabaseBackupSchedule).mockRejectedValueOnce(
    new TypeError("private_network_canary"),
  );
  mount();
  fireEvent.click(await enabledButton());
  await screen.findByRole("button", {
    name: "Retry the same schedule request",
  });
  const first = savedCall(0);
  expect(
    (screen.getByLabelText("Backup frequency") as HTMLInputElement).disabled,
  ).toBe(true);
  expect(screen.queryByText("private_network_canary")).toBeNull();
  fireEvent.click(
    screen.getByRole("button", { name: "Retry the same schedule request" }),
  );
  await screen.findByText(/Saved schedule schedule revision 1/);
  expect(savedCall(1)[3]).toEqual(first[3]);
});
test("definitive validation rejection allows correction while retaining input values", async () => {
  vi.mocked(api.saveDatabaseBackupSchedule).mockRejectedValueOnce(
    new ApiError(400, "rejected", null),
  );
  mount();
  await enabledButton();
  fireEvent.change(screen.getByLabelText("Backup frequency"), {
    target: { value: "1 * * * *" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Create backup schedule" }),
  );
  await screen.findByRole("alert");
  expect(
    (screen.getByLabelText("Backup frequency") as HTMLInputElement).value,
  ).toBe("1 * * * *");
  expect(
    (screen.getByLabelText("Backup frequency") as HTMLInputElement).disabled,
  ).toBe(false);
  const first = savedCall(0)[3];
  fireEvent.change(screen.getByLabelText("Backup frequency"), {
    target: { value: "hourly" },
  });
  fireEvent.click(await enabledButton());
  await screen.findByText(/Saved schedule schedule revision 1/);
  const corrected = savedCall(1)[3];
  expect(corrected.request_id).not.toBe(first.request_id);
  expect(corrected.frequency).toBe("hourly");
});
test("version conflict requires refreshed explicit review without automatic resubmission", async () => {
  values = [{ ...saved, revision: 3 }];
  vi.mocked(api.saveDatabaseBackupSchedule).mockRejectedValueOnce(
    new ApiError(409, "conflict", null),
  );
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Edit schedule" }));
  fireEvent.change(screen.getByLabelText("Backup frequency"), {
    target: { value: "hourly" },
  });
  fireEvent.click(await enabledButton("Save schedule changes"));
  await screen.findByRole("alert");
  values = [{ ...saved, revision: 4, frequency: "weekly" }];
  fireEvent.click(screen.getByRole("button", { name: "Refresh schedules" }));
  await screen.findByText(/revision 4 · Enabled/);
  expect(screen.getByText(/reviewed revision 3/)).toBeTruthy();
  expect(
    (screen.getByLabelText("Backup frequency") as HTMLInputElement).value,
  ).toBe("hourly");
  expect(api.saveDatabaseBackupSchedule).toHaveBeenCalledTimes(1);
  fireEvent.click(await enabledButton("Edit schedule"));
  expect(
    (screen.getByLabelText("Backup frequency") as HTMLInputElement).value,
  ).toBe("weekly");
  fireEvent.click(await enabledButton("Save schedule changes"));
  await screen.findByText(/Saved schedule schedule revision 5/);
  expect(savedCall(1)[3].expected_version).toBe(4);
});
test("scope identity remount cancels old request and rejects its late state update", async () => {
  let resolve!: (value: api.DatabaseBackupSchedule) => void;
  vi.mocked(api.saveDatabaseBackupSchedule).mockReturnValueOnce(
    new Promise((done) => {
      resolve = done;
    }),
  );
  const view = mount();
  fireEvent.click(await enabledButton());
  await waitFor(() =>
    expect(api.saveDatabaseBackupSchedule).toHaveBeenCalledTimes(1),
  );
  const signal = savedCall(0)[4];
  view.rerender(
    view.element({
      ...database,
      resource: { ...database.resource, id: "other_database" },
    }),
  );
  expect(signal.aborted).toBe(true);
  await act(async () => resolve(saved));
  expect(screen.queryByText(/Saved schedule schedule/)).toBeNull();
  expect(
    (screen.getByLabelText("Backup frequency") as HTMLInputElement).value,
  ).toBe("daily");
  expect(
    screen.getByRole("button", { name: "Create backup schedule" }),
  ).toBeTruthy();
});
test("denial removes scoped cached metadata and hides owner actions", async () => {
  vi.mocked(api.listDatabaseBackupSchedules).mockRejectedValue(
    new ApiError(403, "denied", null),
  );
  const view = mount();
  await screen.findByText("Backup schedule access is unavailable.");
  await waitFor(() => expect(view.denied).toHaveBeenCalled());
  expect(
    screen.queryByRole("button", { name: "Create backup schedule" }),
  ).toBeNull();
});
