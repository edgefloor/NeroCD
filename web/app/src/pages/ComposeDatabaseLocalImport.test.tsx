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
  admitDatabaseImportUpload: vi.fn(),
  getDatabaseImportUploadStage: vi.fn(),
  sendDatabaseImportFile: vi.fn(),
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
const file = new File(["SELECT 1;"], "proof.sql");
const created = new Date().toISOString();
const stage: imports.DatabaseImportUploadStage = {
  id: "upload_stage",
  version: "mysql.compose-local-upload-stage.v1",
  target_resource_id: "target",
  component: "mysql",
  expected_deployed_operation_id: "deployment",
  expected_resource_generation: 3,
  upload_source: { filename: file.name, bytes: file.size },
  upload_hash: "sha256:" + "a".repeat(64),
  upload_deadline: new Date(Date.parse(created) + 3600000).toISOString(),
  state: "queued",
  created_at: created,
  updated_at: created,
};
const ready: imports.DatabaseImportUploadStage = {
  ...stage,
  state: "ready",
  source_bytes: file.size,
  compression: "plain",
  staged_blob: {
    digest: "sha256:" + "b".repeat(64),
    bytes: file.size,
    format: "mysql_sql",
  },
  stage_hash: "sha256:" + "c".repeat(64),
  expires_at: new Date(Date.parse(created) + 3600000).toISOString(),
};
const operation: imports.DatabaseImportOperation = {
  id: "import",
  version: "mysql.compose-external-import.v1",
  stage_id: stage.id,
  target_resource_id: "target",
  target_component: "mysql",
  target_deployed_operation_id: "deployment",
  state: "succeeded",
  ownership_held: false,
  writers_resumed: true,
  created_at: created,
  updated_at: created,
};
beforeEach(() => {
  vi.mocked(destinations.getResourceBinding).mockImplementation(async (id) => ({
    id: "binding",
    resource_id: id,
    destination_id: "runner",
    runtime_namespace: "ns",
    created_by: "owner",
    created_at: created,
  }));
  vi.mocked(imports.admitDatabaseImportUpload).mockResolvedValue(stage);
  vi.mocked(imports.getDatabaseImportUploadStage).mockResolvedValue(stage);
  vi.mocked(imports.sendDatabaseImportFile).mockResolvedValue();
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
        sourceKind="upload"
        scope={scope}
        target={target}
        targetResourceID="target"
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
async function admit() {
  fireEvent.change(screen.getByLabelText("Local SQL file"), {
    target: { files: [file] },
  });
  await waitFor(() =>
    expect(
      (
        screen.getByRole("button", {
          name: "Prepare local file upload",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare local file upload" }),
  );
  await screen.findByText(/Inspection upload_stage · queued/);
  await waitFor(() =>
    expect(
      (
        screen.getByRole("button", {
          name: "Send file for inspection",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false),
  );
}
async function inspectReady() {
  await admit();
  vi.mocked(imports.getDatabaseImportUploadStage).mockResolvedValue(ready);
  fireEvent.click(
    screen.getByRole("button", { name: "Send file for inspection" }),
  );
  await screen.findByText(/Inspection upload_stage · ready/);
  await waitFor(() =>
    expect((screen.getByRole("checkbox") as HTMLInputElement).disabled).toBe(
      false,
    ),
  );
}
test("admission precedes one native transfer and only ready SQL enables confirmation", async () => {
  mount();
  await admit();
  expect(imports.sendDatabaseImportFile).not.toHaveBeenCalled();
  expect(remote.listDatabaseBackupTargets).not.toHaveBeenCalled();
  expect(imports.admitDatabaseImportUpload).toHaveBeenCalledWith(
    "target",
    {
      request_id: expect.any(String),
      component: "mysql",
      expected_deployed_operation_id: "deployment",
      expected_resource_generation: 3,
      filename: file.name,
      source_bytes: file.size,
    },
    expect.any(AbortSignal),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Send file for inspection" }),
  );
  await waitFor(() =>
    expect(imports.sendDatabaseImportFile).toHaveBeenCalledOnce(),
  );
  expect(vi.mocked(imports.sendDatabaseImportFile).mock.calls[0]![1]).toBe(
    file,
  );
  expect((screen.getByRole("checkbox") as HTMLInputElement).disabled).toBe(
    true,
  );
  expect(screen.getByText(/File upload attempted/)).toBeTruthy();
  expect(
    (
      screen.getByRole("button", {
        name: "Send file for inspection",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  vi.mocked(imports.getDatabaseImportUploadStage).mockResolvedValue(ready);
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh inspection status" }),
  );
  await screen.findByText(/Inspection upload_stage · ready/);
  await waitFor(() =>
    expect((screen.getByRole("checkbox") as HTMLInputElement).disabled).toBe(
      false,
    ),
  );
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(screen.getByRole("button", { name: "Import inspected SQL" }));
  await screen.findByText(/External import import · succeeded/);
  expect(imports.requestDatabaseImport).toHaveBeenCalledWith(
    {
      request_id: expect.any(String),
      stage_id: stage.id,
      expected_stage_hash: ready.stage_hash,
      component_target: { ...target, acknowledge_replacement: true },
    },
    expect.any(AbortSignal),
  );
});
test("ambiguous metadata admission retains exact identity and never sends a body automatically", async () => {
  vi.mocked(imports.admitDatabaseImportUpload).mockRejectedValueOnce(
    new Error("metadata response lost"),
  );
  const view = mount();
  fireEvent.change(screen.getByLabelText("Local SQL file"), {
    target: { files: [file] },
  });
  await waitFor(() =>
    expect(
      (
        screen.getByRole("button", {
          name: "Prepare local file upload",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare local file upload" }),
  );
  const retry = await screen.findByRole("button", {
    name: "Retry exact external import request",
  });
  const first = vi.mocked(imports.admitDatabaseImportUpload).mock.calls[0]![1];
  expect(
    (screen.getByLabelText("Local SQL file") as HTMLInputElement).disabled,
  ).toBe(true);
  view.update({
    target: { ...target, expected_deployed_operation_id: "new_deployment" },
  });
  fireEvent.click(retry);
  await waitFor(() =>
    expect(imports.admitDatabaseImportUpload).toHaveBeenCalledTimes(2),
  );
  expect(vi.mocked(imports.admitDatabaseImportUpload).mock.calls[1]![1]).toBe(
    first,
  );
  expect(imports.sendDatabaseImportFile).not.toHaveBeenCalled();
});
test.each([
  new Error("body response lost"),
  new ApiError(409, "already attempted", null),
])(
  "ambiguous or duplicate body resolves only through GET without resending: %s",
  async (cause) => {
    vi.mocked(imports.sendDatabaseImportFile).mockRejectedValue(cause);
    mount();
    await admit();
    const reads = vi.mocked(imports.getDatabaseImportUploadStage).mock.calls
      .length;
    fireEvent.click(
      screen.getByRole("button", { name: "Send file for inspection" }),
    );
    await screen.findByText(/File upload result needs checking/);
    await waitFor(() =>
      expect(
        vi.mocked(imports.getDatabaseImportUploadStage).mock.calls.length,
      ).toBeGreaterThan(reads),
    );
    expect(imports.sendDatabaseImportFile).toHaveBeenCalledOnce();
    expect(
      screen.queryByRole("button", {
        name: "Retry exact external import request",
      }),
    ).toBeNull();
    vi.mocked(imports.getDatabaseImportUploadStage).mockResolvedValue(ready);
    fireEvent.click(
      screen.getByRole("button", { name: "Refresh inspection status" }),
    );
    await screen.findByText(/Inspection upload_stage · ready/);
    expect(imports.sendDatabaseImportFile).toHaveBeenCalledOnce();
  },
);
test.each([412, 429])(
  "definitive HTTP %s rejection allows a fresh upload but never resends the old file",
  async (status) => {
    vi.mocked(imports.sendDatabaseImportFile).mockRejectedValue(
      new ApiError(status, "upload not started", null),
    );
    mount();
    await admit();
    const original = vi.mocked(imports.admitDatabaseImportUpload).mock
      .calls[0]![1];
    fireEvent.click(
      screen.getByRole("button", { name: "Send file for inspection" }),
    );
    await screen.findByText(/File upload could not start/);
    expect(imports.sendDatabaseImportFile).toHaveBeenCalledOnce();
    expect(
      (
        screen.getByRole("button", {
          name: "Send file for inspection",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(
      (screen.getByLabelText("Local SQL file") as HTMLInputElement).disabled,
    ).toBe(false);
    vi.mocked(imports.admitDatabaseImportUpload).mockResolvedValue({
      ...stage,
      id: "fresh_stage",
    });
    vi.mocked(imports.getDatabaseImportUploadStage).mockResolvedValue({
      ...stage,
      id: "fresh_stage",
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Prepare local file upload" }),
    );
    await waitFor(() =>
      expect(imports.admitDatabaseImportUpload).toHaveBeenCalledTimes(2),
    );
    expect(
      vi.mocked(imports.admitDatabaseImportUpload).mock.calls[1]![1].request_id,
    ).not.toBe(original.request_id);
    await screen.findByText(/Inspection fresh_stage · queued/);
    expect(
      (
        screen.getByRole("button", {
          name: "Send file for inspection",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false);
    expect(imports.sendDatabaseImportFile).toHaveBeenCalledOnce();
    expect(imports.requestDatabaseImport).not.toHaveBeenCalled();
  },
);
test("sending-byte progress is separate from persistence and Stop aborts transfer then checks stage", async () => {
  vi.mocked(imports.sendDatabaseImportFile).mockImplementation(
    (_stage, _file, signal, progress) =>
      new Promise((_, reject) => {
        progress(4);
        signal.addEventListener(
          "abort",
          () => reject(new DOMException("Stopped", "AbortError")),
          { once: true },
        );
      }),
  );
  mount();
  await admit();
  fireEvent.click(
    screen.getByRole("button", { name: "Send file for inspection" }),
  );
  await screen.findByText(/Sending file bytes: 4 of 9/);
  expect(
    screen.getByText(/does not confirm persisted or inspected bytes/),
  ).toBeTruthy();
  const failed = {
    ...stage,
    state: "failed" as const,
    failure_code: "interrupted" as const,
  };
  vi.mocked(imports.getDatabaseImportUploadStage).mockResolvedValue(failed);
  fireEvent.click(screen.getByRole("button", { name: "Stop sending file" }));
  await screen.findByText(/Inspection upload_stage · failed/);
  expect(imports.sendDatabaseImportFile).toHaveBeenCalledOnce();
  expect(
    (
      screen.getByRole("button", {
        name: "Prepare local file upload",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(false);
  expect((screen.getByRole("checkbox") as HTMLInputElement).disabled).toBe(
    true,
  );
});
test("file A to B to A retires unsubmitted consent without another body attempt", async () => {
  mount();
  await inspectReady();
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.change(screen.getByLabelText("Local SQL file"), {
    target: { files: [new File(["SELECT 2;"], "other.sql")] },
  });
  fireEvent.change(screen.getByLabelText("Local SQL file"), {
    target: { files: [file] },
  });
  expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(
    false,
  );
  expect(imports.sendDatabaseImportFile).toHaveBeenCalledOnce();
});
test("same filename and size do not authorize a different File's bytes", async () => {
  mount();
  await inspectReady();
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.change(screen.getByLabelText("Local SQL file"), {
    target: { files: [new File(["SELECT 2;"], file.name)] },
  });
  expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(
    false,
  );
  expect(
    (
      screen.getByRole("button", {
        name: "Import inspected SQL",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
});
test("an active transfer aborts on target-resource change and late denial cannot retire the new scope", async () => {
  let reject!: (cause: unknown) => void;
  vi.mocked(imports.sendDatabaseImportFile).mockImplementation(
    () =>
      new Promise((_, r) => {
        reject = r;
      }),
  );
  const view = mount();
  await admit();
  fireEvent.click(
    screen.getByRole("button", { name: "Send file for inspection" }),
  );
  await waitFor(() =>
    expect(imports.sendDatabaseImportFile).toHaveBeenCalled(),
  );
  const signal = vi.mocked(imports.sendDatabaseImportFile).mock.calls[0]![2];
  view.update({
    targetResourceID: "new_target",
    target: { ...target, resource_id: "new_target" },
  });
  expect(signal.aborted).toBe(true);
  await act(async () => reject(new ApiError(403, "old_scope", null)));
  expect(view.onDenied).not.toHaveBeenCalled();
  expect(screen.queryByText(/Inspection upload_stage/)).toBeNull();
});
test("local import ambiguity retains exact stage and target request but never resends bytes", async () => {
  vi.mocked(imports.requestDatabaseImport).mockRejectedValueOnce(
    new Error("import response lost"),
  );
  mount();
  await inspectReady();
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(screen.getByRole("button", { name: "Import inspected SQL" }));
  const retry = await screen.findByRole("button", {
    name: "Retry exact external import request",
  });
  const first = vi.mocked(imports.requestDatabaseImport).mock.calls[0]![0];
  fireEvent.click(retry);
  await waitFor(() =>
    expect(imports.requestDatabaseImport).toHaveBeenCalledTimes(2),
  );
  expect(vi.mocked(imports.requestDatabaseImport).mock.calls[1]![0]).toBe(
    first,
  );
  expect(imports.sendDatabaseImportFile).toHaveBeenCalledOnce();
});
test.each([401, 403])("body HTTP %s retires upload access", async (status) => {
  vi.mocked(imports.sendDatabaseImportFile).mockRejectedValue(
    new ApiError(status, "denied", null),
  );
  const view = mount();
  await admit();
  fireEvent.click(
    screen.getByRole("button", { name: "Send file for inspection" }),
  );
  await screen.findByText("External database import access is unavailable.");
  expect(view.onDenied).toHaveBeenCalledOnce();
});
test("empty file and non-owner do not create upload admission", async () => {
  const view = mount();
  fireEvent.change(screen.getByLabelText("Local SQL file"), {
    target: { files: [new File([], "empty.sql")] },
  });
  expect(
    (
      screen.getByRole("button", {
        name: "Prepare local file upload",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  view.update({ owner: false });
  fireEvent.change(screen.getByLabelText("Local SQL file"), {
    target: { files: [file] },
  });
  expect(
    (
      screen.getByRole("button", {
        name: "Prepare local file upload",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  expect(imports.admitDatabaseImportUpload).not.toHaveBeenCalled();
});
