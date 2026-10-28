import { StrictMode } from "react";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { ApiError } from "../api/errors";
import type { HostingDatabase } from "../api/hosting-databases";
import { DatabaseBackupRetention } from "./DatabaseBackupRetention";
import * as api from "../api/hosting-database-backup-retention";
vi.mock("../api/hosting-database-backup-retention", async (original) => ({
  ...(await original<
    typeof import("../api/hosting-database-backup-retention")
  >()),
  getRetentionPolicy: vi.fn(),
  listRetentionExecutions: vi.fn(),
  saveRetentionPolicy: vi.fn(),
  createRetentionPreview: vi.fn(),
  executeRetention: vi.fn(),
}));
const db = {
  resource: { id: "database", generation: 2 },
  engine: "postgresql",
} as HostingDatabase;
const scope = {
  actor: "owner",
  project: "project",
  environment: "environment",
};
const at = "2026-10-25T12:00:00Z",
  hash = `sha256:${"a".repeat(64)}`;
const policy: api.RetentionPolicy = {
  version: api.retentionVersion,
  project_id: "project",
  resource_id: "database",
  revision: 1,
  resource_generation: 2,
  enabled: true,
  automatic: false,
  local: { keep_count: 5, keep_days: 7, max_bytes: 1073741824 },
  remote: { keep_count: 0, keep_days: 0, max_bytes: 0 },
  created_at: at,
  updated_at: at,
};
const preview: api.RetentionPreview = {
  id: "preview",
  version: api.retentionVersion,
  project_id: "project",
  resource_id: "database",
  policy_revision: 1,
  plan_hash: hash,
  local_reference_at: at,
  remote_reference_at: null,
  selection_truncated: true,
  items: [
    {
      copy_id: "local_copy",
      backup_id: "backup_old",
      location: "local",
      digest: hash,
      bytes: 1024,
      created_at: at,
      availability: "available",
      selected: true,
      protected_reasons: [],
    },
    {
      copy_id: "new_copy",
      backup_id: "backup_new",
      location: "remote",
      digest: hash,
      bytes: 2048,
      created_at: at,
      availability: "available",
      selected: false,
      protected_reasons: ["newest", "shared"],
    },
  ],
  created_at: at,
};
const execution: api.RetentionExecution = {
  id: "execution",
  version: api.retentionVersion,
  project_id: "project",
  resource_id: "database",
  preview_id: "preview",
  policy_revision: 1,
  plan_hash: hash,
  state: "uncertain",
  reservation_held: true,
  local_state: "succeeded",
  remote_state: "uncertain",
  items: [
    {
      copy_id: "local_copy",
      backup_id: "backup_old",
      location: "local",
      state: "deleted",
      failure_code: "",
      reservation_held: false,
    },
    {
      copy_id: "remote_copy",
      backup_id: "backup_old",
      location: "remote",
      state: "uncertain",
      failure_code: "remote_unavailable",
      reservation_held: true,
    },
  ],
  created_at: at,
  updated_at: at,
};
beforeEach(() => {
  vi.mocked(api.getRetentionPolicy).mockResolvedValue(policy);
  vi.mocked(api.listRetentionExecutions).mockResolvedValue([]);
  vi.mocked(api.createRetentionPreview).mockResolvedValue(preview);
  vi.mocked(api.executeRetention).mockResolvedValue(execution);
  vi.mocked(api.saveRetentionPolicy).mockImplementation(
    async (_scope, _db, input) => ({
      ...policy,
      ...input,
      revision: input.expected_revision + 1,
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.useRealTimers();
});
function mount(owner = true, strict = false, initialDatabase = db) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const denied = vi.fn();
  const element = (
    database = initialDatabase,
    actor = "owner",
    isOwner = owner,
    blocked = false,
  ) => (
    <QueryClientProvider client={client}>
      <DatabaseBackupRetention
        database={database}
        scope={{ ...scope, actor }}
        owner={isOwner}
        blocked={blocked}
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
async function button(name: string) {
  await waitFor(() =>
    expect(
      (screen.getByRole("button", { name }) as HTMLButtonElement).disabled,
    ).toBe(false),
  );
  return screen.getByRole("button", { name });
}
async function showPreview() {
  fireEvent.click(await button("Preview retention deletion"));
  await screen.findByRole("region", { name: "Retention deletion preview" });
}
const confirmation = () =>
  screen.getByRole("checkbox", { name: /I confirm permanent deletion/ });
test("StrictMode owner edits six independent limits with exact bytes and reviewed revision", async () => {
  mount(true, true);
  await button("Save retention policy");
  for (const [label, value] of [
    ["Local backups to keep", "0"],
    ["Local days to keep", "36500"],
    ["Local maximum storage in GiB", "0.5"],
    ["Remote backups to keep", "10000"],
    ["Remote days to keep", "0"],
    ["Remote maximum storage in GiB", "1048576"],
  ])
    fireEvent.change(screen.getByLabelText(label!), { target: { value } });
  fireEvent.click(await button("Save retention policy"));
  await screen.findByText("Saved retention revision 2.");
  expect(api.saveRetentionPolicy).toHaveBeenCalledTimes(1);
  expect(vi.mocked(api.saveRetentionPolicy).mock.calls[0]![2]).toMatchObject({
    request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
    expected_revision: 1,
    expected_resource_generation: 2,
    local: { keep_count: 0, keep_days: 36500, max_bytes: 536870912 },
    remote: { keep_count: 10000, keep_days: 0, max_bytes: 2 ** 50 },
  });
});
test("viewer sees saved limits and independent uncertain history without mutation controls", async () => {
  vi.mocked(api.listRetentionExecutions).mockResolvedValue([execution]);
  mount(false);
  await screen.findByText(/Local result: succeeded. Remote result: uncertain/);
  expect(screen.getByText(/local: count 5/)).toBeTruthy();
  expect(screen.getByText(/Reservation remains held/)).toBeTruthy();
  expect(
    screen.queryByRole("button", {
      name: /Save retention|Preview retention|Delete selected/,
    }),
  ).toBeNull();
  expect(api.saveRetentionPolicy).not.toHaveBeenCalled();
  expect(api.createRetentionPreview).not.toHaveBeenCalled();
  expect(api.executeRetention).not.toHaveBeenCalled();
});
test("preview displays protected copies but exact deletion requires explicit keyboard confirmation", async () => {
  mount();
  await showPreview();
  expect(screen.getByText("Protected: newest, shared")).toBeTruthy();
  expect(screen.getByText(/Not selected: backup_new · remote/)).toBeTruthy();
  expect(
    screen.getByText(/Selected for deletion: backup_old · local/),
  ).toBeTruthy();
  expect(screen.getByText(/Selection is truncated/)).toBeTruthy();
  const remove = screen.getByRole("button", {
    name: "Delete selected backup copies",
  });
  expect((remove as HTMLButtonElement).disabled).toBe(true);
  const user = userEvent.setup();
  confirmation().focus();
  await user.keyboard(" ");
  expect((confirmation() as HTMLInputElement).checked).toBe(true);
  remove.focus();
  await user.keyboard("{Enter}");
  await screen.findByText(/Accepted retention execution execution/);
  expect(vi.mocked(api.executeRetention).mock.calls[0]![2]).toMatchObject({
    preview_id: "preview",
    expected_policy_revision: 1,
    expected_plan_hash: hash,
  });
  expect(api.createRetentionPreview).toHaveBeenCalledTimes(1);
  expect(
    screen.queryByRole("region", { name: "Retention deletion preview" }),
  ).toBeNull();
});
test("lost execution response retains the exact request and never requests another batch", async () => {
  vi.mocked(api.executeRetention)
    .mockRejectedValueOnce(new Error("lost"))
    .mockResolvedValueOnce(execution);
  mount();
  await showPreview();
  fireEvent.click(confirmation());
  fireEvent.click(await button("Delete selected backup copies"));
  await screen.findByText(/Retained request/);
  expect(
    (
      screen.getByRole("button", {
        name: "Preview retention deletion",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  const first = vi.mocked(api.executeRetention).mock.calls[0]![2];
  fireEvent.click(await button("Retry the same retention request"));
  await screen.findByText(/Accepted retention execution execution/);
  expect(vi.mocked(api.executeRetention).mock.calls[1]![2]).toBe(first);
  expect(api.createRetentionPreview).toHaveBeenCalledTimes(1);
});
test.each(["save", "preview"])(
  "lost %s response retains exact identity and input",
  async (kind) => {
    if (kind === "save")
      vi.mocked(api.saveRetentionPolicy).mockRejectedValueOnce(
        new Error("lost"),
      );
    else
      vi.mocked(api.createRetentionPreview).mockRejectedValueOnce(
        new Error("lost"),
      );
    mount();
    fireEvent.click(
      await button(
        kind === "save"
          ? "Save retention policy"
          : "Preview retention deletion",
      ),
    );
    await screen.findByText(/Retained request/);
    const fn =
      kind === "save"
        ? vi.mocked(api.saveRetentionPolicy)
        : vi.mocked(api.createRetentionPreview);
    const first = fn.mock.calls[0]![2];
    fireEvent.click(await button("Retry the same retention request"));
    await waitFor(() => expect(fn).toHaveBeenCalledTimes(2));
    expect(fn.mock.calls[1]![2]).toBe(first);
  },
);
test("validation and rejection preserve typed values and require explicit saved-policy review", async () => {
  mount();
  await button("Save retention policy");
  fireEvent.change(screen.getByLabelText("Local maximum storage in GiB"), {
    target: { value: "0.1" },
  });
  expect(
    (
      screen.getByRole("button", {
        name: "Save retention policy",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  fireEvent.change(screen.getByLabelText("Local maximum storage in GiB"), {
    target: { value: "0.5" },
  });
  vi.mocked(api.saveRetentionPolicy).mockRejectedValueOnce(
    new ApiError(409, "conflict", null),
  );
  fireEvent.click(await button("Save retention policy"));
  await screen.findByText(/Input is preserved/);
  expect(
    (screen.getByLabelText("Local maximum storage in GiB") as HTMLInputElement)
      .value,
  ).toBe("0.5");
  expect(
    screen.queryByRole("button", { name: "Retry the same retention request" }),
  ).toBeNull();
  expect(
    (
      screen.getByRole("button", {
        name: "Preview retention deletion",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
});
test("editing any limit invalidates the preview and confirmation", async () => {
  mount();
  await showPreview();
  fireEvent.click(confirmation());
  fireEvent.change(screen.getByLabelText("Remote days to keep"), {
    target: { value: "1" },
  });
  expect(
    screen.queryByRole("region", { name: "Retention deletion preview" }),
  ).toBeNull();
  expect(
    (
      screen.getByRole("button", {
        name: "Preview retention deletion",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
});
test.each(["actor", "resource", "generation", "role", "blocked"])(
  "%s change aborts old scope and prevents a late response",
  async (field) => {
    let resolve!: (value: api.RetentionPreview) => void;
    vi.mocked(api.createRetentionPreview).mockImplementationOnce(
      () =>
        new Promise((done) => {
          resolve = done;
        }),
    );
    const view = mount();
    fireEvent.click(await button("Preview retention deletion"));
    await waitFor(() =>
      expect(api.createRetentionPreview).toHaveBeenCalledTimes(1),
    );
    const signal = vi.mocked(api.createRetentionPreview).mock.calls[0]![3];
    const changed = {
      ...db,
      resource: {
        ...db.resource,
        ...(field === "resource" ? { id: "other" } : {}),
        ...(field === "generation" ? { generation: 3 } : {}),
      },
    };
    view.rerender(
      view.element(
        changed,
        field === "actor" ? "other_actor" : "owner",
        field !== "role",
        field === "blocked",
      ),
    );
    expect(signal?.aborted).toBe(true);
    await act(async () => resolve(preview));
    expect(
      screen.queryByRole("region", { name: "Retention deletion preview" }),
    ).toBeNull();
  },
);
test("denial retires mutation and polling authority and removes scoped cache", async () => {
  vi.mocked(api.createRetentionPreview).mockRejectedValueOnce(
    new ApiError(403, "denied", null),
  );
  const view = mount();
  fireEvent.click(await button("Preview retention deletion"));
  await screen.findByText("Backup retention access is unavailable.");
  expect(view.denied).toHaveBeenCalled();
  expect(
    view.client
      .getQueriesData({ queryKey: api.retentionKey(scope, "database") })
      .every(([, data]) => data === undefined),
  ).toBe(true);
  expect(vi.mocked(api.createRetentionPreview).mock.calls[0]![3]?.aborted).toBe(
    true,
  );
});
test("active execution polls at three seconds and terminal uncertain history stops polling", async () => {
  vi.useFakeTimers();
  vi.mocked(api.listRetentionExecutions).mockResolvedValue([
    { ...execution, state: "applying" },
  ]);
  mount();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10);
  });
  expect(api.listRetentionExecutions).toHaveBeenCalledTimes(1);
  vi.mocked(api.listRetentionExecutions).mockResolvedValue([execution]);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000);
  });
  expect(api.listRetentionExecutions).toHaveBeenCalledTimes(2);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(9000);
  });
  expect(api.listRetentionExecutions).toHaveBeenCalledTimes(2);
});
test("all-protected previews cannot execute and Redis does not request retention", async () => {
  vi.mocked(api.createRetentionPreview).mockResolvedValue({
    ...preview,
    items: preview.items.filter((v) => !v.selected),
  });
  const view = mount();
  await showPreview();
  expect(screen.getByText("No copies are selected for deletion.")).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Delete selected backup copies" }),
  ).toBeNull();
  view.unmount();
  vi.clearAllMocks();
  render(
    <QueryClientProvider client={new QueryClient()}>
      <DatabaseBackupRetention
        database={{ ...db, engine: "redis" }}
        scope={scope}
        owner
        blocked={false}
        onDenied={vi.fn()}
      />
    </QueryClientProvider>,
  );
  expect(api.getRetentionPolicy).not.toHaveBeenCalled();
  expect(api.listRetentionExecutions).not.toHaveBeenCalled();
});

test("manual batches require confirmation while automatic cleanup uses saved policy authorization", async () => {
  mount();
  await button("Save retention policy");
  expect(
    screen.getByText(
      /Every manual batch needs a new explicit preview and confirmation\./,
    ),
  ).toBeTruthy();
  expect(
    screen.getByText(
      /Automatic cleanup acts under the saved enabled policy authorization when automatic retention is enabled\./,
    ),
  ).toBeTruthy();
  expect(
    screen.getByText(
      /When enabled, automatic retention authorizes cleanup under the saved enabled policy when supported by the runner\./,
    ),
  ).toBeTruthy();
  expect(screen.queryByText(/A later batch always needs/)).toBeNull();
});

async function confirmSavedBatch() {
  fireEvent.click(await button("Retry this batch"));
  await screen.findByRole("region", { name: "Retry saved retention batch" });
  fireEvent.click(
    screen.getByRole("checkbox", {
      name: "I confirm continuing only this saved batch",
    }),
  );
  fireEvent.click(await button("Confirm retry of this batch"));
}
test("reloaded history can recover only its exact saved execution without a fresh preview", async () => {
  vi.mocked(api.listRetentionExecutions).mockResolvedValue([execution]);
  mount();
  fireEvent.click(await button("Retry this batch"));
  const region = await screen.findByRole("region", {
    name: "Retry saved retention batch",
  });
  expect(region.textContent).toContain("backup_old · local · copy local_copy");
  expect(region.textContent).toContain(
    "backup_old · remote · copy remote_copy",
  );
  expect(region.textContent).toContain(
    "Copies with unknown prior effects may remain blocked",
  );
  expect(
    (
      screen.getByRole("button", {
        name: "Confirm retry of this batch",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  fireEvent.click(
    screen.getByRole("checkbox", {
      name: "I confirm continuing only this saved batch",
    }),
  );
  fireEvent.click(await button("Confirm retry of this batch"));
  await screen.findByText(/Accepted retention execution execution/);
  const call = vi.mocked(api.executeRetention).mock.calls[0]!;
  expect(call[2]).toEqual({
    request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
    preview_id: execution.preview_id,
    expected_policy_revision: execution.policy_revision,
    expected_plan_hash: execution.plan_hash,
  });
  expect(call[4]).toBe(execution.id);
  expect(api.createRetentionPreview).not.toHaveBeenCalled();
});
test("lost recovery response replays the identical new request and expected execution", async () => {
  vi.mocked(api.listRetentionExecutions).mockResolvedValue([execution]);
  vi.mocked(api.executeRetention)
    .mockRejectedValueOnce(new Error("lost"))
    .mockResolvedValueOnce(execution);
  mount();
  await confirmSavedBatch();
  await screen.findByText(/Retained request/);
  const first = vi.mocked(api.executeRetention).mock.calls[0]!;
  expect(
    (
      screen.getByRole("button", {
        name: "Retry this batch",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  fireEvent.click(await button("Retry the same retention request"));
  await screen.findByText(/Accepted retention execution execution/);
  const second = vi.mocked(api.executeRetention).mock.calls[1]!;
  expect(second[2]).toBe(first[2]);
  expect(second[4]).toBe(execution.id);
  expect(api.createRetentionPreview).not.toHaveBeenCalled();
});
test.each(["dispatched", "applying", "uncertain"] as const)(
  "held %s batch is explicitly recoverable",
  async (state) => {
    vi.mocked(api.listRetentionExecutions).mockResolvedValue([
      { ...execution, state },
    ]);
    mount();
    await button("Retry this batch");
    expect(api.executeRetention).not.toHaveBeenCalled();
  },
);
test.each(["queued", "succeeded", "failed"] as const)(
  "%s history cannot start a recovery deletion",
  async (state) => {
    vi.mocked(api.listRetentionExecutions).mockResolvedValue([
      { ...execution, state },
    ]);
    mount();
    await screen.findByText(/Local result: succeeded/);
    expect(
      screen.queryByRole("button", { name: "Retry this batch" }),
    ).toBeNull();
  },
);
test("released reservations and viewers cannot recover history", async () => {
  vi.mocked(api.listRetentionExecutions).mockResolvedValue([
    { ...execution, reservation_held: false },
  ]);
  const view = mount();
  await screen.findByText(/Local result: succeeded/);
  expect(screen.queryByRole("button", { name: "Retry this batch" })).toBeNull();
  view.unmount();
  vi.mocked(api.listRetentionExecutions).mockResolvedValue([execution]);
  mount(false);
  await screen.findByText(/Local result: succeeded/);
  expect(screen.queryByRole("button", { name: "Retry this batch" })).toBeNull();
});
test.each(["actor", "resource", "generation", "role", "blocked"])(
  "%s change retires recovery confirmation and request",
  async (field) => {
    vi.mocked(api.listRetentionExecutions).mockResolvedValue([execution]);
    let resolve!: (value: api.RetentionExecution) => void;
    vi.mocked(api.executeRetention).mockImplementationOnce(
      () =>
        new Promise((done) => {
          resolve = done;
        }),
    );
    const view = mount();
    await confirmSavedBatch();
    await waitFor(() => expect(api.executeRetention).toHaveBeenCalledTimes(1));
    const signal = vi.mocked(api.executeRetention).mock.calls[0]![3];
    view.rerender(
      view.element(
        {
          ...db,
          resource: {
            ...db.resource,
            ...(field === "resource" ? { id: "other" } : {}),
            ...(field === "generation" ? { generation: 3 } : {}),
          },
        },
        field === "actor" ? "other_actor" : "owner",
        field !== "role",
        field === "blocked",
      ),
    );
    expect(signal?.aborted).toBe(true);
    await act(async () => resolve(execution));
    expect(
      screen.queryByRole("region", { name: "Retry saved retention batch" }),
    ).toBeNull();
    expect(
      screen.queryByText(/Accepted retention execution execution/),
    ).toBeNull();
  },
);
test("stale resource generation blocks saved batch recovery", async () => {
  vi.mocked(api.listRetentionExecutions).mockResolvedValue([execution]);
  const view = mount();
  view.rerender(
    view.element({ ...db, resource: { ...db.resource, generation: 3 } }),
  );
  await screen.findByText(/Local result: succeeded/);
  expect(
    (
      screen.getByRole("button", {
        name: "Retry this batch",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
});
test("recovery denial revokes authority and an unsafe conflict keeps history bounded", async () => {
  vi.mocked(api.listRetentionExecutions).mockResolvedValue([execution]);
  vi.mocked(api.executeRetention).mockRejectedValueOnce(
    new ApiError(409, "unsafe", null),
  );
  const view = mount();
  await confirmSavedBatch();
  await screen.findByText(/Input is preserved/);
  expect(api.createRetentionPreview).not.toHaveBeenCalled();
  expect(screen.getByText(/Reservation remains held/)).toBeTruthy();
  vi.mocked(api.executeRetention).mockRejectedValueOnce(
    new ApiError(403, "denied", null),
  );
  await confirmSavedBatch();
  await screen.findByText("Backup retention access is unavailable.");
  expect(view.denied).toHaveBeenCalled();
  expect(screen.queryByRole("button", { name: "Retry this batch" })).toBeNull();
});


test("native MySQL owner uses existing policy, protected preview, and independent local/remote deletion results", async () => {
 const mysql = { ...db, engine: "mysql" as const };
 const version = api.mysqlRetentionVersion;
 vi.mocked(api.getRetentionPolicy).mockResolvedValue({ ...policy, version });
 vi.mocked(api.saveRetentionPolicy).mockImplementation(async (_scope, _db, input) => {
   const out = { ...policy, ...input, version, revision: input.expected_revision + 1 };
   vi.mocked(api.getRetentionPolicy).mockResolvedValue(out);
   return out;
 });
 vi.mocked(api.createRetentionPreview).mockResolvedValue({ ...preview, version, policy_revision: 2 });
 vi.mocked(api.executeRetention).mockImplementation(async () => {
   vi.mocked(api.listRetentionExecutions).mockResolvedValue([{ ...execution, version, policy_revision: 2 }]);
   return { ...execution, version, policy_revision: 2 };
 });
 mount(true, true, mysql);
 await button("Save retention policy");
 expect(screen.getByText(/Native MySQL retention requires hosting_mysql_backup_retention_v1/)).toBeTruthy();
 fireEvent.change(screen.getByLabelText("Local backups to keep"), { target: { value: "0" } });
 fireEvent.change(screen.getByLabelText("Remote days to keep"), { target: { value: "5" } });
 fireEvent.click(await button("Save retention policy"));
 await screen.findByText("Saved retention revision 2.");
 expect(vi.mocked(api.saveRetentionPolicy).mock.calls[0]![2]).toMatchObject({ expected_revision: 1, expected_resource_generation: 2, local: { keep_count: 0 }, remote: { keep_days: 5 } });
 await showPreview();
 expect(screen.getByText("Protected: newest, shared")).toBeTruthy();
 fireEvent.click(confirmation());
 fireEvent.click(screen.getByRole("button", { name: "Delete selected backup copies" }));
 await screen.findByText(/Local result: succeeded. Remote result: uncertain/);
 expect(vi.mocked(api.executeRetention).mock.calls[0]![2]).toMatchObject({ expected_policy_revision: 2, expected_plan_hash: hash, preview_id: "preview" });
 expect(screen.getByText(/backup_old · local · copy local_copy · deleted/)).toBeTruthy();
 expect(screen.getByText(/backup_old · remote · copy remote_copy · uncertain/)).toBeTruthy();
 expect(screen.getByText(/Reservation remains held/)).toBeTruthy();
});
