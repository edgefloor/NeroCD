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
import { ComposeDatabaseExternalImport } from "./ComposeDatabaseExternalImport";
import * as imports from "@/api/hosting-database-imports";
import * as destinations from "@/api/destinations";
import * as remote from "@/api/hosting-database-backup-targets";
import { ApiError } from "@/api/errors";
vi.mock("@/api/hosting-database-imports", async (original) => ({
  ...(await original<typeof import("@/api/hosting-database-imports")>()),
  inspectDatabaseImport: vi.fn(),
  getDatabaseImportStage: vi.fn(),
  requestDatabaseImport: vi.fn(),
  getDatabaseImport: vi.fn(),
}));
vi.mock("@/api/destinations", async (original) => ({
  ...(await original<typeof import("@/api/destinations")>()),
  getResourceBinding: vi.fn(),
}));
vi.mock("@/api/hosting-database-backup-targets", async (original) => ({
  ...(await original<typeof import("@/api/hosting-database-backup-targets")>()),
  listDatabaseBackupTargets: vi.fn(),
  listDatabaseBackupValidations: vi.fn(),
}));
const scope = { actor: "owner", project: "project", environment: "env" };
const target = {
  resource_id: "target",
  component: "mysql",
  expected_deployed_operation_id: "deployment",
  expected_resource_generation: 3,
};
const now = "2026-10-30T12:00:00Z";
const stage: imports.DatabaseImportStage = {
  id: "stage",
  version: "mysql.compose-external-s3-stage.v1",
  target_resource_id: "target",
  component: "mysql",
  expected_deployed_operation_id: "deployment",
  expected_resource_generation: 3,
  s3_source: {
    target_id: "storage",
    revision: 2,
    object_key: "/incoming/é%2F.sql.gz",
  },
  state: "ready",
  source_bytes: 42,
  compression: "gzip",
  staged_blob: {
    digest: "sha256:" + "a".repeat(64),
    bytes: 100,
    format: "mysql_sql",
  },
  stage_hash: "sha256:" + "b".repeat(64),
  expires_at: "2099-01-01T01:00:00Z",
  created_at: now,
  updated_at: now,
};
const operation: imports.DatabaseImportOperation = {
  id: "import",
  version: "mysql.compose-external-import.v1",
  stage_id: "stage",
  target_resource_id: "target",
  target_component: "mysql",
  target_deployed_operation_id: "deployment",
  state: "succeeded",
  ownership_held: false,
  writers_resumed: true,
  created_at: now,
  updated_at: now,
};
const configured: remote.DatabaseBackupTarget = {
  id: "storage",
  project_id: "project",
  version: "postgresql.backup-target.v1",
  revision: 2,
  name: "SQL source",
  endpoint: "https://s3.example.invalid",
  bucket: "incoming",
  region: "us-east-1",
  enabled: true,
  created_at: now,
  updated_at: now,
};
beforeEach(() => {
  vi.mocked(destinations.getResourceBinding).mockImplementation(async (id) => ({
    id: "binding",
    resource_id: id,
    destination_id: "runner",
    runtime_namespace: "ns",
    created_by: "owner",
    created_at: now,
  }));
  vi.mocked(remote.listDatabaseBackupTargets).mockResolvedValue([configured]);
  vi.mocked(remote.listDatabaseBackupValidations).mockResolvedValue([
    {
      id: "validation",
      version: "mysql.logical-s3-transfer.v1",
      project_id: "project",
      target_id: "storage",
      target_revision: 2,
      destination_id: "runner",
      mode: "validate",
      state: "succeeded",
      failure_code: "",
      created_at: now,
      updated_at: now,
    },
  ]);
  vi.mocked(imports.inspectDatabaseImport).mockResolvedValue(stage);
  vi.mocked(imports.getDatabaseImportStage).mockResolvedValue(stage);
  vi.mocked(imports.requestDatabaseImport).mockResolvedValue(operation);
  vi.mocked(imports.getDatabaseImport).mockResolvedValue(operation);
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
function mount(
  props: Partial<Parameters<typeof ComposeDatabaseExternalImport>[0]> = {},
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const onBusy = vi.fn(),
    onDenied = vi.fn();
  const view = (extra = props) => (
    <QueryClientProvider client={client}>
      <ComposeDatabaseExternalImport
        scope={scope}
        target={target}
        owner
        blocked={false}
        onBusy={onBusy}
        onDenied={onDenied}
        {...extra}
      />
    </QueryClientProvider>
  );
  const mounted = render(view());
  return {
    client,
    onBusy,
    onDenied,
    ...mounted,
    update: (extra: typeof props) => mounted.rerender(view(extra)),
  };
}
async function select() {
  await screen.findByRole("option", { name: "SQL source · revision 2" });
  fireEvent.change(screen.getByLabelText("S3 import destination"), {
    target: { value: "storage:2" },
  });
  fireEvent.change(screen.getByLabelText("S3 object key"), {
    target: { value: stage.s3_source.object_key },
  });
}
async function inspect() {
  await select();
  fireEvent.click(screen.getByRole("button", { name: "Inspect S3 object" }));
  await screen.findByText(/Inspection stage · ready/);
  await waitFor(() =>
    expect((screen.getByRole("checkbox") as HTMLInputElement).disabled).toBe(
      false,
    ),
  );
}
test("reviews actual inspected SQL receipt and imports only after explicit replacement consent", async () => {
  mount();
  await inspect();
  expect(imports.requestDatabaseImport).not.toHaveBeenCalled();
  expect(imports.inspectDatabaseImport).toHaveBeenCalledWith(
    "target",
    {
      request_id: expect.any(String),
      component: "mysql",
      expected_deployed_operation_id: "deployment",
      expected_resource_generation: 3,
      s3_source: stage.s3_source,
    },
    expect.any(AbortSignal),
  );
  expect(
    screen.getByText(
      /Source bytes 42 · compression gzip. Verified SQL bytes 100/,
    ),
  ).toBeTruthy();
  expect(
    (
      screen.getByRole("button", {
        name: "Import inspected SQL",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(screen.getByRole("button", { name: "Import inspected SQL" }));
  await screen.findByText(/External import import · succeeded/);
  expect(imports.requestDatabaseImport).toHaveBeenCalledWith(
    {
      request_id: expect.any(String),
      stage_id: "stage",
      expected_stage_hash: stage.stage_hash,
      component_target: { ...target, acknowledge_replacement: true },
    },
    expect.any(AbortSignal),
  );
});
test.each(["inspect", "import"] as const)(
  "ambiguous %s freezes source and retains exact request across changed live target",
  async (mode) => {
    const view = mount();
    if (mode === "inspect") await select();
    else await inspect();
    const method =
      mode === "inspect"
        ? vi.mocked(imports.inspectDatabaseImport)
        : vi.mocked(imports.requestDatabaseImport);
    method.mockRejectedValueOnce(new Error("response lost"));
    if (mode === "import") fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(
      screen.getByRole("button", {
        name: mode === "inspect" ? "Inspect S3 object" : "Import inspected SQL",
      }),
    );
    const retry = await screen.findByRole("button", {
      name: "Retry exact external import request",
    });
    const first = method.mock.calls.at(-1)!;
    expect(
      (screen.getByLabelText("S3 object key") as HTMLInputElement).disabled,
    ).toBe(true);
    view.update({
      target: {
        ...target,
        expected_deployed_operation_id: "changed",
        expected_resource_generation: 4,
      },
    });
    fireEvent.click(retry);
    await waitFor(() => expect(method.mock.calls.length).toBe(2));
    const second = method.mock.calls.at(-1)!;
    expect(second[mode === "inspect" ? 1 : 0]).toBe(
      first[mode === "inspect" ? 1 : 0],
    );
  },
);
test("source A to B to A retires unsubmitted consent", async () => {
  mount();
  await inspect();
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.change(screen.getByLabelText("S3 object key"), {
    target: { value: "other.sql" },
  });
  fireEvent.change(screen.getByLabelText("S3 object key"), {
    target: { value: stage.s3_source.object_key },
  });
  expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(
    false,
  );
  expect(imports.requestDatabaseImport).not.toHaveBeenCalled();
});
test("target deployment A to B to A retires unsubmitted consent", async () => {
  const view = mount();
  await inspect();
  fireEvent.click(screen.getByRole("checkbox"));
  view.update({
    target: { ...target, expected_deployed_operation_id: "changed" },
  });
  view.update({ target });
  expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(
    false,
  );
});
test("stale target revision blocks import and retires consent", async () => {
  const view = mount();
  await inspect();
  fireEvent.click(screen.getByRole("checkbox"));
  vi.mocked(remote.listDatabaseBackupTargets).mockResolvedValue([
    { ...configured, revision: 3 },
  ]);
  await act(async () => {
    await view.client.invalidateQueries({
      queryKey: remote.databaseBackupTargetsKey(scope),
    });
  });
  await waitFor(() =>
    expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(
      false,
    ),
  );
  expect(
    (
      screen.getByRole("button", {
        name: "Import inspected SQL",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
});
test("identical evidence refresh retains inspection and permits explicit renewed confirmation", async () => {
  const view = mount();
  await inspect();
  fireEvent.click(screen.getByRole("checkbox"));
  await act(async () => {
    await view.client.invalidateQueries({
      queryKey: remote.databaseBackupTargetsKey(scope),
    });
  });
  await waitFor(() =>
    expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(
      false,
    ),
  );
  expect(imports.inspectDatabaseImport).toHaveBeenCalledTimes(1);
  await waitFor(() =>
    expect((screen.getByRole("checkbox") as HTMLInputElement).disabled).toBe(
      false,
    ),
  );
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(screen.getByRole("button", { name: "Import inspected SQL" }));
  await screen.findByText(/External import import · succeeded/);
  expect(imports.requestDatabaseImport).toHaveBeenCalledOnce();
});
test("batched S3 revision A to B to A evidence reads retire consent", async () => {
  const view = mount();
  await inspect();
  fireEvent.click(screen.getByRole("checkbox"));
  await act(async () => {
    vi.mocked(remote.listDatabaseBackupTargets).mockResolvedValueOnce([
      { ...configured, revision: 3 },
    ]);
    await view.client.invalidateQueries({
      queryKey: remote.databaseBackupTargetsKey(scope),
    });
    await view.client.invalidateQueries({
      queryKey: remote.databaseBackupTargetsKey(scope),
    });
  });
  await waitFor(() =>
    expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(
      false,
    ),
  );
  expect(imports.requestDatabaseImport).not.toHaveBeenCalled();
});
test.each(["failed", "expired", "consumed"] as const)(
  "%s stage requires a fresh explicit inspection",
  async (state) => {
    vi.mocked(imports.inspectDatabaseImport).mockResolvedValue({
      ...stage,
      state,
    });
    vi.mocked(imports.getDatabaseImportStage).mockResolvedValue({
      ...stage,
      state,
    });
    mount();
    await select();
    fireEvent.click(screen.getByRole("button", { name: "Inspect S3 object" }));
    await screen.findByText(new RegExp(`Inspection stage · ${state}`));
    expect(
      (
        screen.getByRole("button", {
          name: "Import inspected SQL",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(imports.requestDatabaseImport).not.toHaveBeenCalled();
  },
);
test("expired receipt is not silently inspected again", async () => {
  vi.mocked(imports.inspectDatabaseImport).mockResolvedValue({
    ...stage,
    expires_at: now,
  });
  vi.mocked(imports.getDatabaseImportStage).mockResolvedValue({
    ...stage,
    expires_at: now,
  });
  mount();
  await select();
  fireEvent.click(screen.getByRole("button", { name: "Inspect S3 object" }));
  await screen.findByText(/Fresh inspection is required/);
  expect(
    (
      screen.getByRole("button", {
        name: "Import inspected SQL",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  expect(imports.inspectDatabaseImport).toHaveBeenCalledTimes(1);
});
test.each(["failed", "uncertain"] as const)(
  "%s import with ownership held cannot launch another import",
  async (state) => {
    const held = {
      ...operation,
      state,
      ownership_held: true,
      writers_resumed: false,
    };
    vi.mocked(imports.requestDatabaseImport).mockResolvedValue(held);
    vi.mocked(imports.getDatabaseImport).mockResolvedValue(held);
    const view = mount();
    await inspect();
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(
      screen.getByRole("button", { name: "Import inspected SQL" }),
    );
    await screen.findByText(
      /Mutation ownership retained. Writer resume not confirmed/,
    );
    expect(
      (
        screen.getByRole("button", {
          name: "Inspect S3 object",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(view.onBusy).toHaveBeenLastCalledWith(true);
    expect(
      screen.queryByRole("button", {
        name: "Retry exact external import request",
      }),
    ).toBeNull();
  },
);
test("owner and intended target are required before inspection", async () => {
  const view = mount({ owner: false });
  await select();
  expect(
    (
      screen.getByRole("button", {
        name: "Inspect S3 object",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  view.update({ target: undefined });
  expect(
    screen.getByText(/Select a distinct deployed MySQL 8 target/),
  ).toBeTruthy();
  expect(imports.inspectDatabaseImport).not.toHaveBeenCalled();
});
test("definitive stale deployment rejection allows fresh target review without inventing a retry", async () => {
  vi.mocked(imports.inspectDatabaseImport).mockRejectedValue(
    new ApiError(409, "conflict", null),
  );
  mount();
  await select();
  fireEvent.click(screen.getByRole("button", { name: "Inspect S3 object" }));
  await screen.findByText(/HTTP 409/);
  expect(
    screen.queryByRole("button", {
      name: "Retry exact external import request",
    }),
  ).toBeNull();
  expect(
    (screen.getByLabelText("S3 object key") as HTMLInputElement).disabled,
  ).toBe(false);
});
test("authorization denial retires current request session", async () => {
  vi.mocked(imports.inspectDatabaseImport).mockRejectedValue(
    new ApiError(403, "forbidden", null),
  );
  const view = mount();
  await select();
  fireEvent.click(screen.getByRole("button", { name: "Inspect S3 object" }));
  await screen.findByText("External database import access is unavailable.");
  expect(view.onDenied).toHaveBeenCalledOnce();
});
test("unmounted old actor inspection cannot write new actor state or propagate late denial", async () => {
  let reject!: (error: unknown) => void;
  vi.mocked(imports.inspectDatabaseImport).mockImplementationOnce(
    () =>
      new Promise((_, r) => {
        reject = r;
      }),
  );
  const view = mount();
  await select();
  fireEvent.click(screen.getByRole("button", { name: "Inspect S3 object" }));
  await waitFor(() => expect(imports.inspectDatabaseImport).toHaveBeenCalled());
  const signal = vi.mocked(imports.inspectDatabaseImport).mock.calls[0]![2];
  view.update({ scope: { ...scope, actor: "new_owner" } });
  expect(signal.aborted).toBe(true);
  await act(async () => reject(new ApiError(403, "old denial", null)));
  expect(view.onDenied).not.toHaveBeenCalled();
  expect(
    screen.queryByText(/External database import access is unavailable/),
  ).toBeNull();
});
