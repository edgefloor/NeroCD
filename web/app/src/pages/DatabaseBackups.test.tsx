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
import * as hosting from "../api/hosting";
import * as destinations from "../api/destinations";
vi.mock("../api/destinations", async original => ({ ...(await original<typeof import("../api/destinations")>()), getResourceBinding: vi.fn() }));
vi.mock("../api/hosting", async original => ({...(await original<typeof import("../api/hosting")>()), listResources:vi.fn()}));
vi.mock("./ResourceDatabase",()=>({useDatabaseAccess:()=>({ready:true,refreshing:false,viewer:true,owner:true,scope:{actor:"owner",project:"project",environment:"env"}})}));
import { DatabaseBackups, ResourceComposeDatabaseBackups } from "./DatabaseBackups";
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
    getDatabaseBackupComponents: vi.fn(),
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
  vi.mocked(destinations.getResourceBinding).mockResolvedValue({ id: "binding", resource_id: "db", destination_id: "runner", runtime_namespace: "test", created_by: "owner", created_at: "2026-10-25T12:00:00Z" });
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
function mount(owner = true, strict = false, savedDatabase = database) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const denied = vi.fn();
  const element = (
    <QueryClientProvider client={client}>
      <DatabaseBackups
        database={savedDatabase}
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
test.each(["postgresql", "mysql"] as const)("%s current runner validation enables immediate remote choice and freezes the selected revision", async (engine) => {
  remoteFixture();
  if (engine === "mysql") vi.mocked(api.listDatabaseBackups).mockResolvedValue([{ ...completed, version: "mysql.logical-local.v1", blob: { ...completed.blob!, format: "mysql_sql" } }]);
  mount(true, false, { ...database, engine });
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
test.each(["postgresql", "mysql"] as const)("%s successful dump and failed upload persist as separate outcomes and retry replays the same archive fence", async (engine) => {
  remoteFixture();
  if (engine === "mysql") vi.mocked(api.listDatabaseBackups).mockResolvedValue([{ ...completed, version: "mysql.logical-local.v1", blob: { ...completed.blob!, format: "mysql_sql" } }]);
  const engineUpload = engine === "mysql" ? { ...upload, version: "mysql.logical-s3-transfer.v1" as const, blob: { ...completed.blob!, format: "mysql_sql" as const } } : upload;
  vi.mocked(remote.listDatabaseBackupTransfers).mockResolvedValue([
    { ...engineUpload, state: "failed", failure_code: "access_denied" },
  ]);
  vi.mocked(remote.retryDatabaseBackupUpload)
    .mockRejectedValueOnce(new TypeError("network"))
    .mockResolvedValueOnce({ ...engineUpload, id: "upload_retry", state: "queued" });
  mount(true, false, { ...database, engine });
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
test.each(["postgresql", "mysql"] as const)("%s restore requires an explicit saved remote object choice and sends no arbitrary URL or key", async (engine) => {
  remoteFixture();
  vi.mocked(remote.listDatabaseBackupTargets).mockResolvedValue([
    { ...target, revision: 3 },
  ]);
  vi.mocked(remote.listDatabaseBackupTransfers).mockResolvedValue([
    upload,
    { ...upload, id: "failed", state: "failed" },
  ]);
  mount(true, false, { ...database, engine });
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


function mountComposeBackupPanel(sourceKind: "compose"|"git"="compose",targetKind: "compose"|"git"="compose",engine: "postgresql"|"mysql"="postgresql"){
 const source=(kind:"compose"|"git")=>kind==="compose"?{kind}:{kind,git:{source_id:"source",generation:2,repository_url:"https://github.com/example/app",requested_ref:"changed-desired-ref",credential_kind:"public",settings:{strategy:"dockerfile"}}};
 const componentBackup={...completed,version:engine==="mysql"?"mysql.compose-logical-local.v1" as const:"postgresql.compose-logical-local.v1" as const,source_component:engine,source_deployed_operation_id:"hop_source",blob:{...completed.blob!,format:engine==="mysql"?"mysql_sql" as const:"postgresql_custom" as const}};
 vi.mocked(api.listDatabaseBackups).mockResolvedValue([componentBackup]);
 vi.mocked(api.getDatabaseBackupComponents).mockImplementation(async(_scope,id)=>({resource_id:id,resource_generation:id==="db"?2:3,operation_id:id==="db"?"hop_source":"hop_target",available:true,reason:"available",components:[{component:engine,...(engine==="mysql"?{engine:"mysql" as const,major_version:8 as const}:{engine:"postgresql" as const,major_version:16 as const}),running:true,eligible:true,reason:"requires_runtime_profile_verification"},...(engine==="mysql"?[{component:"wrong_engine",engine:"postgresql" as const,major_version:16 as const,running:true,eligible:true,reason:"requires_runtime_profile_verification"}]:[])]}));
 vi.mocked(hosting.listResources).mockResolvedValue({items:[{...database.resource,id:"target",name:"Distinct target",source:source(targetKind)}],total:1,limit:100,offset:0} as Awaited<ReturnType<typeof hosting.listResources>>);
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
 render(<QueryClientProvider client={client}><ResourceComposeDatabaseBackups resource={{...database.resource,source:source(sourceKind)} as hosting.Resource} blocked={false}/></QueryClientProvider>);
 return client;
}

test("Compose selection creates no demand and import carries exact distinct settled target",async()=>{
 mountComposeBackupPanel();
 const selector=await screen.findByRole("combobox",{name:"Database backup component"});
 await waitFor(()=>expect(selector.querySelectorAll("option")).toHaveLength(2));
 fireEvent.change(selector,{target:{value:"postgresql"}});
 expect(api.requestDatabaseBackup).not.toHaveBeenCalled();
 fireEvent.click(screen.getByRole("button",{name:"Create logical backup"}));
 await waitFor(()=>expect(api.requestDatabaseBackup).toHaveBeenCalled());
 expect(vi.mocked(api.requestDatabaseBackup).mock.calls[0]?.[2]).toMatchObject({component:"postgresql",expected_deployed_operation_id:"hop_source",expected_resource_generation:2});
 fireEvent.change(screen.getByRole("combobox",{name:"Backup to restore"}),{target:{value:"backup"}});
 fireEvent.change(screen.getByRole("combobox",{name:"Restore target application"}),{target:{value:"target"}});
 const target=screen.getByRole("combobox",{name:"Restore target database component"});
 await waitFor(()=>expect(target.querySelectorAll("option")).toHaveLength(2));
 fireEvent.change(target,{target:{value:"postgresql"}});
 expect((screen.getByRole("button",{name:"Restore database backup"}) as HTMLButtonElement).disabled).toBe(true);
 fireEvent.click(screen.getByRole("checkbox",{name:"Replace target database and interrupt its writers"}));
 await waitFor(()=>expect((screen.getByRole("button",{name:"Restore database backup"}) as HTMLButtonElement).disabled).toBe(false));
 fireEvent.click(screen.getByRole("button",{name:"Restore database backup"}));
 await waitFor(()=>expect(api.restoreDatabaseBackup).toHaveBeenCalled());
 expect(vi.mocked(api.restoreDatabaseBackup).mock.calls[0]?.[3]).toEqual({request_id:expect.any(String),component_target:{resource_id:"target",component:"postgresql",expected_deployed_operation_id:"hop_target",expected_resource_generation:3,acknowledge_replacement:true}});
 expect(screen.getByText(/not uploaded files/)).toBeTruthy();
});


test.each(["postgresql","mysql"] as const)("Compose %s lost response freezes selection and preserves identical retry authority",async(engine)=>{
 mountComposeBackupPanel("compose","compose",engine);
 vi.mocked(api.requestDatabaseBackup).mockRejectedValueOnce(new Error("response lost"));
 const selector=await screen.findByRole("combobox",{name:"Database backup component"});
 await waitFor(()=>expect(selector.querySelectorAll("option")).toHaveLength(engine==="mysql"?3:2));
 fireEvent.change(selector,{target:{value:engine}});
 fireEvent.click(screen.getByRole("button",{name:"Create logical backup"}));
 const retry=await screen.findByRole("button",{name:"Retry exact database request"});
 const first=vi.mocked(api.requestDatabaseBackup).mock.calls[0]?.[2];
 expect(first?.request_id).toBeTruthy();
 for(const label of ["Database backup component","Backup to restore","Restore target application","Restore target database component"]){expect((screen.getByRole("combobox",{name:label}) as HTMLSelectElement).disabled).toBe(true)}
 for(const name of ["Create logical backup","Restore database backup","Refresh database components","Refresh database backup history"]){expect((screen.getByRole("button",{name}) as HTMLButtonElement).disabled).toBe(true)}
 fireEvent.change(selector,{target:{value:""}});
 expect((selector as HTMLSelectElement).value).toBe(engine);
 fireEvent.click(retry);
 await waitFor(()=>expect(api.requestDatabaseBackup).toHaveBeenCalledTimes(2));
 expect(vi.mocked(api.requestDatabaseBackup).mock.calls[1]?.[2]).toBe(first);
 await waitFor(()=>expect(screen.queryByRole("button",{name:"Retry exact database request"})).toBeNull());
});


test("committed Git Compose exposes logical backups and verifies Git restore target through component evidence",async()=>{
 const hash=`sha256:${"a".repeat(64)}`;
 const fetch=vi.fn(async(url:string)=>new Response(JSON.stringify(url.endsWith("/stack-lifecycle")?{resource_id:"db",resource_generation:2,state:"running",deployed_operation_id:"current"}:{id:"current",project_id:"project",resource_id:"db",binding_id:"binding",state:"succeeded",snapshot:{resource_id:"db",binding_id:"binding",stack:{generation:1,hash},git_compose:{build_id:"built",source_generation:1,result_hash:hash}}})));
 vi.stubGlobal("fetch",fetch);
 mountComposeBackupPanel("git","git");
 const components=await screen.findByRole("combobox",{name:"Database backup component"});
 await waitFor(()=>expect(components.querySelectorAll("option")).toHaveLength(2));
 fireEvent.change(screen.getByRole("combobox",{name:"Restore target application"}),{target:{value:"target"}});
 await waitFor(()=>expect(vi.mocked(api.getDatabaseBackupComponents).mock.calls.some(([,id])=>id==="target")).toBe(true));
 expect(fetch.mock.calls.map(([url])=>url)).toEqual(["/api/v1/resources/db/stack-lifecycle","/api/v1/hosting-operations/current"]);
 expect(api.restoreDatabaseBackup).not.toHaveBeenCalled();
});

test.each([200,403])("Git database controls fail closed without current stack proof HTTP%s",async status=>{
 const fetch=vi.fn(async()=>new Response(JSON.stringify(status===200?{resource_id:"db",resource_generation:2,state:"unavailable"}:{}),{status}));
 vi.stubGlobal("fetch",fetch);
 mountComposeBackupPanel("git");
 await waitFor(()=>expect(fetch).toHaveBeenCalled());
 if(status===403)await screen.findByText(/Component database backups are paused/);
 else await waitFor(()=>expect(screen.queryByText(/Component database backups are paused/)).toBeNull());
 expect(screen.queryByRole("region",{name:"Component database backups"})).toBeNull();
 expect(api.getDatabaseBackupComponents).not.toHaveBeenCalled();
 expect(api.listDatabaseBackups).not.toHaveBeenCalled();
});


test("MySQL exposes immediate local backup and a separate new-resource restore with an explicit archive source choice", async () => {
 const mysql = { ...database, engine: "mysql" as const };
 const backup: DatabaseBackup = { ...completed, version: "mysql.logical-local.v1", blob: { ...completed.blob!, format: "mysql_sql" } };
 vi.mocked(api.listDatabaseBackups).mockResolvedValue([backup]);
 vi.mocked(api.requestDatabaseBackup).mockResolvedValue({ ...backup, id: "new_backup", backup_id: "new_backup", state: "queued", ownership_held: true });
 vi.mocked(api.restoreDatabaseBackup).mockResolvedValue({ ...backup, id: "restore", mode: "restore", target_resource_id: "new_mysql", state: "waiting_target", ownership_held: true });
 mount(true, false, mysql);
 await screen.findByText("MySQL logical backups");
 await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Back up now" }).disabled).toBe(false));
 expect(screen.getByLabelText("Immediate backup destination")).toBeTruthy();
 expect(screen.getByLabelText("Restore archive source")).toBeTruthy();
 expect(screen.getByText(/bytes · MySQL SQL dump/)).toBeTruthy();
 fireEvent.click(screen.getByRole("button", { name: "Back up now" }));
 await waitFor(() => expect(api.requestDatabaseBackup).toHaveBeenCalledOnce());
 expect(vi.mocked(api.requestDatabaseBackup).mock.calls[0]![2]).not.toHaveProperty("remote_target");
 await selectRestore();
 fireEvent.click(screen.getByRole("button", { name: "Restore into new database" }));
 await waitFor(() => expect(api.restoreDatabaseBackup).toHaveBeenCalledOnce());
 expect(vi.mocked(api.restoreDatabaseBackup).mock.calls[0]![3]).toMatchObject({ name: "Restored data" });
 expect(remote.listDatabaseBackupTransfers).toHaveBeenCalled();
});


test.each(["failed", "waiting_target", "uncertain"] as const)("MySQL download success is shown separately from %s restore import", async (state) => {
 const backup: DatabaseBackup = { ...completed, version: "mysql.logical-local.v1", blob: { ...completed.blob!, format: "mysql_sql" } };
 vi.mocked(api.listDatabaseBackups).mockResolvedValue([backup, { ...backup, id: "restore", mode: "restore", target_resource_id: "target", state, ownership_held: true, failure_code: "execution_failed" }]);
 vi.mocked(remote.listDatabaseBackupTransfers).mockResolvedValue([{ ...upload, id: "download", mode: "download", version: "mysql.logical-s3-transfer.v1", blob: backup.blob, restore_operation_id: "restore", state: "succeeded" }]);
 mount(true, false, { ...database, engine: "mysql" });
 await screen.findByText(/Remote archive download: succeeded/);
 expect(screen.getByText(/Download success does not prove import completion/)).toBeTruthy();
 expect(screen.getByText(new RegExp(`New-database restore · ${state} · restore`))).toBeTruthy();
 expect(screen.queryByText(/Import and readiness succeeded/)).toBeNull();
});


test("Compose MySQL imports a local archive only into a matching separate component",async()=>{
 mountComposeBackupPanel("compose","compose","mysql");
 const source=await screen.findByRole("combobox",{name:"Database backup component"});
 await screen.findByRole("option",{name:"mysql · mysql 8"});
 fireEvent.change(source,{target:{value:"mysql"}});
 fireEvent.click(screen.getByRole("button",{name:"Create logical backup"}));
 await waitFor(()=>expect(api.requestDatabaseBackup).toHaveBeenCalled());
 expect(vi.mocked(api.requestDatabaseBackup).mock.calls[0]![2]).toEqual({request_id:expect.any(String),expected_resource_generation:2,component:"mysql",expected_deployed_operation_id:"hop_source"});
 fireEvent.change(screen.getByRole("combobox",{name:"Backup to restore"}),{target:{value:"backup"}});
 fireEvent.change(screen.getByRole("combobox",{name:"Restore target application"}),{target:{value:"target"}});
 const target=screen.getByRole("combobox",{name:"Restore target database component"});
 await waitFor(()=>expect(target.querySelectorAll("option")).toHaveLength(2));
 expect(target.textContent).not.toContain("wrong_engine");
 fireEvent.change(target,{target:{value:"mysql"}});
 fireEvent.click(screen.getByRole("checkbox",{name:"Replace target database and interrupt its writers"}));
 await waitFor(()=>expect((screen.getByRole("button",{name:"Restore database backup"}) as HTMLButtonElement).disabled).toBe(false));
 vi.mocked(api.listDatabaseBackups).mockResolvedValue([{...completed,id:"held_restore",backup_id:"backup",mode:"restore",state:"failed",ownership_held:true,target_resource_id:"target",version:"mysql.compose-logical-local.v1",source_component:"mysql",source_deployed_operation_id:"hop_source",target_component:"mysql",target_deployed_operation_id:"hop_target",blob:{...completed.blob!,format:"mysql_sql"}}]);
 fireEvent.click(screen.getByRole("button",{name:"Restore database backup"}));
 await waitFor(()=>expect(api.restoreDatabaseBackup).toHaveBeenCalled());
 expect(vi.mocked(api.restoreDatabaseBackup).mock.calls[0]![3]).toEqual({request_id:expect.any(String),component_target:{resource_id:"target",component:"mysql",expected_deployed_operation_id:"hop_target",expected_resource_generation:3,acknowledge_replacement:true}});
 await screen.findByText(/held_restore · restore · failed · mutation ownership retained/);
 expect(screen.queryByRole("button",{name:"Retry exact database request"})).toBeNull();
 expect(screen.getByText(/Failed or uncertain import/)).toBeTruthy();
 expect(screen.queryByLabelText("Restore archive source")).toBeNull();
});


test.each(["postgresql", "mysql"] as const)("Compose %s target A to B to A retires unsubmitted destructive consent", async engine => {
 mountComposeBackupPanel("compose", "compose", engine);
 await screen.findByRole("option", {name:/ · backup$/});
 fireEvent.change(screen.getByRole("combobox", {name:"Backup to restore"}), {target:{value:"backup"}});
 const applications = screen.getByRole("combobox", {name:"Restore target application"});
 fireEvent.change(applications, {target:{value:"target"}});
 const components = screen.getByRole("combobox", {name:"Restore target database component"});
 await waitFor(()=>expect(components.querySelectorAll("option")).toHaveLength(2));
 fireEvent.change(components, {target:{value:engine}});
 const consent = screen.getByRole("checkbox", {name:"Replace target database and interrupt its writers"});
 fireEvent.click(consent);
 const restore = screen.getByRole("button", {name:"Restore database backup"});
 await waitFor(()=>expect((restore as HTMLButtonElement).disabled).toBe(false));
 fireEvent.change(applications, {target:{value:""}});
 fireEvent.change(applications, {target:{value:"target"}});
 await waitFor(()=>expect(components.querySelectorAll("option")).toHaveLength(2));
 fireEvent.change(components, {target:{value:engine}});
 expect((consent as HTMLInputElement).checked).toBe(false);
 expect((restore as HTMLButtonElement).disabled).toBe(true);
 expect(api.restoreDatabaseBackup).not.toHaveBeenCalled();
});


test.each(["postgresql", "mysql"] as const)("Compose %s metadata refresh retires consent across deployment A to B to A", async engine => {
 const client = mountComposeBackupPanel("compose", "compose", engine);
 await screen.findByRole("option", {name:/ · backup$/});
 fireEvent.change(screen.getByLabelText("Backup to restore"), {target:{value:"backup"}});
 fireEvent.change(screen.getByLabelText("Restore target application"), {target:{value:"target"}});
 const selector=screen.getByLabelText("Restore target database component");
 await waitFor(()=>expect(selector.querySelectorAll("option")).toHaveLength(2));
 fireEvent.change(selector, {target:{value:engine}});
 const consent=screen.getByRole("checkbox", {name:"Replace target database and interrupt its writers"});
 fireEvent.click(consent);
 const restore=screen.getByRole("button", {name:"Restore database backup"});
 await waitFor(()=>expect((restore as HTMLButtonElement).disabled).toBe(false));
 const actualKey=client.getQueryCache().getAll().find(q=>q.queryKey[0]==="database-backup-components" && q.queryKey[2]==="target")!.queryKey;
 const original=client.getQueryData<api.DatabaseBackupComponents>(actualKey)!;
 let resolve!: (value: api.DatabaseBackupComponents)=>void;
 vi.mocked(api.getDatabaseBackupComponents).mockImplementationOnce(()=>new Promise(done=>{resolve=done}));
 let refresh!: Promise<void>;
 act(()=>{refresh=client.refetchQueries({queryKey:actualKey,exact:true});});
 await waitFor(()=>expect((restore as HTMLButtonElement).disabled).toBe(true));
 await act(async()=>{resolve({...original,operation_id:"deployment_B",resource_generation:original.resource_generation+1});await refresh;});
 await screen.findByText(/Reviewed target deployment deployment_B/);
 expect((consent as HTMLInputElement).checked).toBe(false);
 expect((restore as HTMLButtonElement).disabled).toBe(true);
 vi.mocked(api.getDatabaseBackupComponents).mockResolvedValueOnce(original);
 await act(async()=>{await client.refetchQueries({queryKey:actualKey,exact:true});});
 await screen.findByText(/Reviewed target deployment hop_target/);
 expect((consent as HTMLInputElement).checked).toBe(false);
 expect((restore as HTMLButtonElement).disabled).toBe(true);
 expect(api.restoreDatabaseBackup).not.toHaveBeenCalled();
 fireEvent.change(selector, {target:{value:engine}});
 fireEvent.click(consent);
 await waitFor(()=>expect((restore as HTMLButtonElement).disabled).toBe(false));
 vi.mocked(api.restoreDatabaseBackup).mockRejectedValueOnce(new Error("response lost"));
 fireEvent.click(restore);
 const retry=await screen.findByRole("button", {name:"Retry exact database request"});
 const first=vi.mocked(api.restoreDatabaseBackup).mock.calls[0]![3];
 act(()=>client.setQueryData(actualKey,{...original,operation_id:"deployment_C",resource_generation:original.resource_generation+2}));
 fireEvent.click(retry);
 await waitFor(()=>expect(api.restoreDatabaseBackup).toHaveBeenCalledTimes(2));
 expect(vi.mocked(api.restoreDatabaseBackup).mock.calls[1]![3]).toBe(first);
 expect(first.component_target).toMatchObject({expected_deployed_operation_id:original.operation_id,expected_resource_generation:original.resource_generation});
});

test("Compose MySQL uses a currently validated remote destination and freezes it after an ambiguous backup response", async () => {
 remoteFixture();
 mountComposeBackupPanel("compose", "compose", "mysql");
 vi.mocked(api.requestDatabaseBackup).mockRejectedValueOnce(new Error("response lost"));
 const source = await screen.findByRole("combobox", { name: "Database backup component" });
 await screen.findByRole("option", { name: "mysql · mysql 8" });
 fireEvent.change(source, { target: { value: "mysql" } });
 const destination = screen.getByRole("combobox", { name: "Immediate backup destination" });
 await screen.findByRole("option", { name: "Daily archive · revision 2" });
 fireEvent.change(destination, { target: { value: "storage:2" } });
 fireEvent.click(screen.getByRole("button", { name: "Create logical backup" }));
 const retry = await screen.findByRole("button", { name: "Retry exact database request" });
 const first = vi.mocked(api.requestDatabaseBackup).mock.calls[0]![2];
 expect(first).toEqual({ request_id: expect.any(String), expected_resource_generation: 2, component: "mysql", expected_deployed_operation_id: "hop_source", remote_target: { target_id: "storage", revision: 2 } });
 expect((destination as HTMLSelectElement).disabled).toBe(true);
 fireEvent.change(destination, { target: { value: "" } });
 expect((destination as HTMLSelectElement).value).toBe("storage:2");
 fireEvent.click(retry);
 await waitFor(() => expect(api.requestDatabaseBackup).toHaveBeenCalledTimes(2));
 expect(vi.mocked(api.requestDatabaseBackup).mock.calls[1]![2]).toBe(first);
});

test.each([{ destination_id: "other" }, { target_revision: 1 }])("Compose MySQL excludes a destination with stale or foreign validation %j", async patch => {
 remoteFixture();
 vi.mocked(remote.listDatabaseBackupValidations).mockResolvedValue([{ ...validation, ...patch }]);
 mountComposeBackupPanel("compose", "compose", "mysql");
 await screen.findByRole("option", { name: "mysql · mysql 8" });
 await waitFor(() => expect(remote.listDatabaseBackupValidations).toHaveBeenCalled());
 expect(screen.queryByRole("option", { name: "Daily archive · revision 2" })).toBeNull();
});

test("Compose MySQL preserves successful local backup when upload fails and retries only the exact upload", async () => {
 const failed = { ...upload, version: "mysql.logical-s3-transfer.v1" as const, state: "failed" as const, failure_code: "access_denied" as const, blob: { ...completed.blob!, format: "mysql_sql" as const } };
 vi.mocked(remote.listDatabaseBackupTransfers).mockResolvedValue([failed]);
 vi.mocked(remote.retryDatabaseBackupUpload).mockRejectedValueOnce(new Error("response lost")).mockResolvedValueOnce({ ...failed, id: "retry", state: "queued" });
 mountComposeBackupPanel("compose", "compose", "mysql");
 await screen.findByText("Local dump: succeeded. Latest remote upload: failed.");
 expect(screen.getByText(/The local dump result is unchanged/)).toBeTruthy();
 fireEvent.click(screen.getByRole("button", { name: "Retry upload for backup" }));
 const retry = await screen.findByRole("button", { name: "Retry the same upload request" });
 const first = vi.mocked(remote.retryDatabaseBackupUpload).mock.calls[0]![2];
 expect(first).toEqual({ request_id: expect.any(String), expected_transfer_id: "upload" });
 fireEvent.click(retry);
 await waitFor(() => expect(remote.retryDatabaseBackupUpload).toHaveBeenCalledTimes(2));
 expect(vi.mocked(remote.retryDatabaseBackupUpload).mock.calls[1]![2]).toBe(first);
 expect(api.requestDatabaseBackup).not.toHaveBeenCalled();
 expect(screen.queryByRole("combobox", { name: "Restore source" })).toBeNull();
});

test("Compose remote destinations require a binding for the current resource", async () => {
 remoteFixture();
 vi.mocked(destinations.getResourceBinding).mockResolvedValue({ id: "binding", resource_id: "other", destination_id: "runner", runtime_namespace: "test", created_by: "owner", created_at: completed.created_at });
 mountComposeBackupPanel("compose", "compose", "mysql");
 await screen.findByRole("option", { name: "mysql · mysql 8" });
 await waitFor(() => expect(destinations.getResourceBinding).toHaveBeenCalled());
 expect(screen.queryByRole("option", { name: "Daily archive · revision 2" })).toBeNull();
 expect(remote.listDatabaseBackupTargets).not.toHaveBeenCalled();
});

test("Compose remote binding denial retires backup controls", async () => {
 vi.mocked(destinations.getResourceBinding).mockRejectedValue(new ApiError(403, "forbidden", "Denied"));
 mountComposeBackupPanel("compose", "compose", "mysql");
 await screen.findByText("Component database backup access is unavailable.");
 expect(screen.queryByRole("button", { name: "Create logical backup" })).toBeNull();
 expect(remote.listDatabaseBackupTargets).not.toHaveBeenCalled();
});

test("Compose selected remote revision is rejected after destination metadata changes", async () => {
 remoteFixture();
 const client = mountComposeBackupPanel("compose", "compose", "mysql");
 await screen.findByRole("option", { name: "mysql · mysql 8" });
 fireEvent.change(screen.getByRole("combobox", { name: "Database backup component" }), { target: { value: "mysql" } });
 await screen.findByRole("option", { name: "Daily archive · revision 2" });
 fireEvent.change(screen.getByRole("combobox", { name: "Immediate backup destination" }), { target: { value: "storage:2" } });
 expect((screen.getByRole("button", { name: "Create logical backup" }) as HTMLButtonElement).disabled).toBe(false);
 vi.mocked(remote.listDatabaseBackupTargets).mockResolvedValue([{ ...target, revision: 3 }]);
 await act(async () => { await client.invalidateQueries({ queryKey: remote.databaseBackupTargetsKey(scope) }); });
 await screen.findByText("Selected destination requires current validation");
 expect((screen.getByRole("button", { name: "Create logical backup" }) as HTMLButtonElement).disabled).toBe(true);
 expect(api.requestDatabaseBackup).not.toHaveBeenCalled();
});
