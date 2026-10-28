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
import type { Resource } from "../api/hosting";
import { ApiError } from "../api/errors";
import * as api from "../api/hosting-database-backup-schedules";
import * as backup from "../api/hosting-database-backups";
import { ComposeDatabaseBackupSchedules } from "./ComposeDatabaseBackupSchedules";
vi.mock("../api/hosting-database-backup-schedules", async (original) => ({
  ...(await original<typeof api>()),
  listDatabaseBackupSchedules: vi.fn(),
  listDatabaseBackupOccurrences: vi.fn(),
  saveDatabaseBackupSchedule: vi.fn(),
}));
vi.mock("../api/hosting-database-backups", async (original) => ({
  ...(await original<typeof backup>()),
  getDatabaseBackupComponents: vi.fn(),
}));
const scope = { actor: "owner", project: "project", environment: "env" };
const resource = {
  id: "compose",
  generation: 3,
  source: { kind: "compose" },
} as Resource;
const saved: api.DatabaseBackupSchedule = {
  id: "schedule",
  version: "postgresql.compose-logical-local.schedule.v1",
  component: "postgresql",
  project_id: "project",
  resource_id: "compose",
  revision: 1,
  frequency: "daily",
  timezone: "UTC",
  enabled: true,
  created_at: "2026-10-27T08:00:00Z",
  updated_at: "2026-10-27T08:00:00Z",
};
let values: api.DatabaseBackupSchedule[];
beforeEach(() => {
  values = [];
  vi.mocked(api.listDatabaseBackupSchedules).mockImplementation(
    async () => values,
  );
  vi.mocked(api.listDatabaseBackupOccurrences).mockResolvedValue([]);
  vi.mocked(backup.getDatabaseBackupComponents).mockResolvedValue({
    resource_id: "compose",
    resource_generation: 3,
    operation_id: "current_deployment",
    available: true,
    reason: "",
    components: [
      {
        component: "postgresql",
        engine: "postgresql",
        major_version: 16,
        running: true,
        eligible: true,
        reason: "",
      },
    ],
  });
  vi.mocked(api.saveDatabaseBackupSchedule).mockImplementation(
    async (_scope, _context, _id, input) => {
      const out = { ...saved, ...input, revision: input.expected_version + 1 };
      values = [out];
      return out;
    },
  );
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});
function mount(owner = true) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const onDenied = vi.fn();
  const element = (r = resource, canWrite = owner) => (
    <QueryClientProvider client={client}>
      <ComposeDatabaseBackupSchedules
        resource={r}
        scope={scope}
        owner={canWrite}
        onDenied={onDenied}
      />
    </QueryClientProvider>
  );
  return {
    ...render(<StrictMode>{element()}</StrictMode>),
    client,
    onDenied,
    element,
  };
}
async function click(name: string) {
  const button = await screen.findByRole("button", { name });
  await waitFor(() =>
    expect((button as HTMLButtonElement).disabled).toBe(false),
  );
  fireEvent.click(button);
}
function call(index = 0) {
  const value = vi.mocked(api.saveDatabaseBackupSchedule).mock.calls[index];
  if (!value) throw new Error("Expected saved schedule request");
  return value;
}
async function choose() {
  await screen.findByRole("option", { name: "postgresql" });
  fireEvent.change(screen.getByLabelText("Scheduled database component"), {
    target: { value: "postgresql" },
  });
}
test("owner creates and edits fixed component against current deployment then disables without runtime authority", async () => {
  mount();
  await choose();
  await click("Create backup schedule");
  await screen.findByText("Saved schedule schedule revision 1.");
  expect(call()[1]).toEqual({ resource });
  expect(call()[3]).toMatchObject({
    component: "postgresql",
    expected_deployed_operation_id: "current_deployment",
    expected_resource_generation: 3,
    expected_version: 0,
  });
  expect(
    (screen.getByLabelText("Scheduled database component") as HTMLSelectElement)
      .disabled,
  ).toBe(true);
  fireEvent.change(screen.getByLabelText("Backup frequency"), {
    target: { value: "hourly" },
  });
  await click("Save schedule changes");
  await screen.findByText("Saved schedule schedule revision 2.");
  expect(call(1)[3]).toMatchObject({
    expected_version: 1,
    frequency: "hourly",
    component: "postgresql",
  });
  await click("Disable schedule");
  await screen.findByText("Saved schedule schedule revision 3.");
  expect(call(2)[3]).toMatchObject({
    expected_version: 2,
    enabled: false,
    component: "postgresql",
  });
  expect(call(2)[3].expected_deployed_operation_id).toBeUndefined();
  expect(screen.queryByLabelText("Scheduled backup destination")).toBeNull();
});
test.each(["failed", "missing", "held"])(
  "%s component evidence does not block history or disabling",
  async (reason) => {
    values = [saved];
    if (reason === "failed")
      vi.mocked(backup.getDatabaseBackupComponents).mockRejectedValue(
        new TypeError("offline"),
      );
    else
      vi.mocked(backup.getDatabaseBackupComponents).mockResolvedValue({
        resource_id: "compose",
        resource_generation: 3,
        available: false,
        reason,
        components: [],
      });
    mount();
    await screen.findByText(/Existing schedules can still be inspected/);
    await click("View occurrences");
    await screen.findByText("No occurrences yet.");
    await click("Disable schedule");
    await screen.findByText("Saved schedule schedule revision 2.");
    expect(call()[3].expected_deployed_operation_id).toBeUndefined();
    expect(call()[3].enabled).toBe(false);
  },
);
test("viewer reads historical operation without eligibility request or owner controls", async () => {
  values = [saved];
  vi.mocked(api.listDatabaseBackupOccurrences).mockResolvedValue([
    {
      id: "occurrence",
      schedule_id: "schedule",
      project_id: "project",
      resource_id: "compose",
      schedule_revision: 1,
      component: "postgresql",
      frequency: "daily",
      timezone: "UTC",
      scheduled_at: saved.created_at,
      created_at: saved.created_at,
      state: "uncertain",
      reason: "interrupted",
      ownership_held: true,
      operation_id: "backup_operation",
    },
  ]);
  mount(false);
  await click("View occurrences");
  await screen.findByText("Backup operation: backup_operation");
  expect(backup.getDatabaseBackupComponents).not.toHaveBeenCalled();
  expect(screen.queryByRole("button", { name: "Disable schedule" })).toBeNull();
});
test("ambiguous response replays identical identity and freezes edits", async () => {
  vi.mocked(api.saveDatabaseBackupSchedule).mockRejectedValueOnce(
    new TypeError("private_canary"),
  );
  mount();
  await choose();
  await click("Create backup schedule");
  await screen.findByRole("button", {
    name: "Retry the same schedule request",
  });
  const first = call()[3];
  expect(
    (screen.getByLabelText("Backup frequency") as HTMLInputElement).disabled,
  ).toBe(true);
  expect(screen.queryByText("private_canary")).toBeNull();
  await click("Retry the same schedule request");
  await screen.findByText("Saved schedule schedule revision 1.");
  expect(call(1)[3]).toEqual(first);
});
test("scope remount aborts mutation and removes old schedule metadata", async () => {
  let resolve!: (value: api.DatabaseBackupSchedule) => void;
  vi.mocked(api.saveDatabaseBackupSchedule).mockReturnValueOnce(
    new Promise((done) => {
      resolve = done;
    }),
  );
  const view = mount();
  await choose();
  await click("Create backup schedule");
  await waitFor(() =>
    expect(api.saveDatabaseBackupSchedule).toHaveBeenCalledTimes(1),
  );
  const signal = call()[4];
  view.rerender(view.element({ ...resource, id: "other" }));
  expect(signal.aborted).toBe(true);
  await act(async () => resolve(saved));
  expect(screen.queryByText("Saved schedule schedule revision 1.")).toBeNull();
  expect(
    view.client.getQueryData(
      api.databaseBackupSchedulesKey(scope, resource.id),
    ),
  ).toBeUndefined();
});
test("denial removes cached schedule data and owner controls", async () => {
  values = [saved];
  vi.mocked(backup.getDatabaseBackupComponents).mockRejectedValue(
    new ApiError(403, "denied", null),
  );
  const view = mount();
  await screen.findByText("Backup schedule access is unavailable.");
  await waitFor(() => expect(view.onDenied).toHaveBeenCalled());
  expect(
    screen.queryByRole("button", { name: "Create backup schedule" }),
  ).toBeNull();
  expect(
    view.client.getQueryData(
      api.databaseBackupSchedulesKey(scope, resource.id),
    ),
  ).toBeUndefined();
});

test("owner downgrade aborts pending mutation while preserving viewer history", async () => {
  let resolve!: (value: api.DatabaseBackupSchedule) => void;
  vi.mocked(api.saveDatabaseBackupSchedule).mockReturnValueOnce(
    new Promise((done) => {
      resolve = done;
    }),
  );
  const view = mount();
  await choose();
  await click("Create backup schedule");
  await waitFor(() =>
    expect(api.saveDatabaseBackupSchedule).toHaveBeenCalledTimes(1),
  );
  const signal = call()[4];
  view.rerender(view.element(resource, false));
  expect(signal.aborted).toBe(true);
  await act(async () => resolve(saved));
  expect(screen.queryByText("Saved schedule schedule revision 1.")).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Create backup schedule" }),
  ).toBeNull();
  expect(
    screen.getByText(/You can inspect schedules and occurrence history/),
  ).toBeTruthy();
});

test.each(["Create backup schedule", "Enable schedule"])(
  "%s waits for matching generation and settled component refresh while disable and history stay available",
  async (action) => {
    values = [saved];
    const view = mount();
    await choose();
    const current = view.client.getQueryData<backup.DatabaseBackupComponents>([
      "database-backup-components",
      scope,
      resource.id,
    ]);
    if (!current) throw new Error("Expected reviewed component evidence");
    let resolve!: (value: backup.DatabaseBackupComponents) => void;
    vi.mocked(backup.getDatabaseBackupComponents).mockImplementationOnce(
      () =>
        new Promise((done) => {
          resolve = done;
        }),
    );
    await click("Refresh schedule components");
    await waitFor(() =>
      expect(
        (
          screen.getByRole("button", {
            name: "Create backup schedule",
          }) as HTMLButtonElement
        ).disabled,
      ).toBe(true),
    );
    await click("View occurrences");
    await screen.findByText("No occurrences yet.");
    await click("Disable schedule");
    await screen.findByText("Saved schedule schedule revision 2.");
    expect(call()[3].expected_deployed_operation_id).toBeUndefined();
    expect(
      (
        screen.getByRole("button", {
          name: "Enable schedule",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);

    await act(async () =>
      resolve({
        ...current,
        resource_generation: 2,
        operation_id: "stale_deployment",
      }),
    );
    await waitFor(() =>
      expect(
        (
          screen.getByRole("button", {
            name: "Refresh schedule components",
          }) as HTMLButtonElement
        ).disabled,
      ).toBe(false),
    );
    for (const name of ["Create backup schedule", "Enable schedule"]) {
      const button = screen.getByRole("button", { name }) as HTMLButtonElement;
      expect(button.disabled).toBe(true);
      fireEvent.click(button);
    }
    expect(api.saveDatabaseBackupSchedule).toHaveBeenCalledTimes(1);
    expect(screen.getByText("No occurrences yet.")).toBeTruthy();

    vi.mocked(backup.getDatabaseBackupComponents).mockImplementationOnce(
      () =>
        new Promise((done) => {
          resolve = done;
        }),
    );
    await click("Refresh schedule components");
    expect(
      (screen.getByRole("button", { name: action }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);
    await act(async () =>
      resolve({ ...current, operation_id: "newest_deployment" }),
    );
    await click(action);
    await waitFor(() =>
      expect(api.saveDatabaseBackupSchedule).toHaveBeenCalledTimes(2),
    );
    expect(call(1)[3]).toMatchObject({
      component: "postgresql",
      expected_resource_generation: 3,
      expected_deployed_operation_id: "newest_deployment",
      enabled: true,
      expected_version: action === "Enable schedule" ? 2 : 0,
    });
  },
);


test("expanded discovery never offers MySQL components for PostgreSQL schedules",async()=>{
 vi.mocked(backup.getDatabaseBackupComponents).mockResolvedValue({resource_id:"compose",resource_generation:3,operation_id:"current_deployment",available:true,reason:"",components:[{component:"mysql",engine:"mysql",major_version:8,running:true,eligible:true,reason:""},{component:"postgresql",engine:"postgresql",major_version:16,running:true,eligible:true,reason:""}]});
 mount();
 const select=await screen.findByLabelText("Scheduled database component");
 await waitFor(()=>expect(select.querySelectorAll("option")).toHaveLength(2));
 expect(select.textContent).toContain("postgresql");
 expect(select.textContent).not.toContain("mysql");
});
