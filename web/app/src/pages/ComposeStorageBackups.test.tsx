import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { ComposeStorageBackups } from "./ComposeStorageBackups";
const scope = {
  actor: "u",
  project: "p",
  resource: "r",
  binding: "b",
  destination: "d",
};
const evidence = {
  resource_id: "r",
  resource_generation: 2,
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
const backup = {
  id: "backup",
  project_id: "p",
  resource_id: "r",
  operation_id: "op",
  state: "queued",
  created_by: "u",
  created_at: "2026-10-27T00:00:00Z",
  volume: "uploads",
  source_deployed_operation_id: "deploy",
};
let client: QueryClient;
afterEach(() => {
  cleanup();
  client?.clear();
  vi.unstubAllGlobals();
});
function setup(
  handler?: (
    u: string,
    o: RequestInit,
  ) => Response | Promise<Response> | undefined,
  owner = true,
) {
  const fetch = vi.fn(
    async (u: string, o: RequestInit) =>
      (await handler?.(u, o)) ??
      new Response(
        JSON.stringify(
          u.includes("/transfers") || u.includes("database-backup-targets")
            ? []
            : u.includes("storage-backup-schedules")
              ? []
              : u.endsWith("storage-backup-volumes")
                ? evidence
                : o.method === "POST"
                  ? backup
                  : u.includes("?limit=")
                    ? { items: [], limit: 20, offset: 0, count: 0, total: 0 }
                    : backup,
        ),
      ),
  );
  vi.stubGlobal("fetch", fetch);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onDenied = vi.fn();
  const view = render(
    <QueryClientProvider client={client}>
      <ComposeStorageBackups
        scope={scope}
        generation={2}
        owner={owner}
        blocked={false}
        onDenied={onDenied}
      />
    </QueryClientProvider>,
  );
  return { fetch, onDenied, view };
}
async function review() {
  await screen.findByRole("option", { name: "uploads" });
  fireEvent.change(
    screen.getByRole("combobox", { name: "Named volume to back up" }),
    {
      target: { value: "uploads" },
    },
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Review Compose volume backup" }),
  );
  await screen.findByRole("region", { name: "Reviewed Compose volume backup" });
}
test("fresh review requires interruption consent before sending", async () => {
  const { fetch } = setup();
  await review();
  const submit = screen.getByRole<HTMLButtonElement>("button", {
    name: "Confirm Compose volume backup",
  });
  expect(submit.disabled).toBe(true);
  expect(fetch.mock.calls.filter(([, o]) => o.method === "POST")).toHaveLength(
    0,
  );
  fireEvent.click(
    screen.getByRole("checkbox", {
      name: /I acknowledge interruption of this volume.s writers while/,
    }),
  );
  fireEvent.click(submit);
  await screen.findByText(/Volume backup backup/);
  expect(
    JSON.parse(
      String(fetch.mock.calls.find(([, o]) => o.method === "POST")![1].body),
    ),
  ).toMatchObject({
    volume: "uploads",
    expected_resource_generation: 2,
    expected_deployed_operation_id: "deploy",
    acknowledge_interruption: true,
  });
});
test("lost response retries identical body", async () => {
  let n = 0;
  const { fetch } = setup((_u, o) =>
    o.method === "POST" && ++n === 1
      ? Promise.reject(Error("lost"))
      : undefined,
  );
  await review();
  fireEvent.click(
    screen.getByRole("checkbox", {
      name: /I acknowledge interruption of this volume.s writers while/,
    }),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Confirm Compose volume backup" }),
  );
  await screen.findByRole("alert");
  fireEvent.click(
    screen.getByRole("button", {
      name: "Retry identical Compose backup request",
    }),
  );
  await screen.findByText(/Volume backup backup/);
  const writes = fetch.mock.calls.filter(([, o]) => o.method === "POST");
  expect(writes).toHaveLength(2);
  expect(writes[0]![1].body).toBe(writes[1]![1].body);
});
test("fresh generation mismatch blocks submission", async () => {
  let n = 0;
  const { fetch } = setup((u) =>
    u.endsWith("storage-backup-volumes")
      ? new Response(
          JSON.stringify({
            ...evidence,
            resource_generation: ++n === 1 ? 2 : 3,
          }),
        )
      : undefined,
  );
  await screen.findByRole("option", { name: "uploads" });
  fireEvent.change(
    screen.getByRole("combobox", { name: "Named volume to back up" }),
    {
      target: { value: "uploads" },
    },
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Review Compose volume backup" }),
  );
  await screen.findByRole("alert");
  expect(
    screen.queryByRole("button", { name: "Confirm Compose volume backup" }),
  ).toBeNull();
  expect(fetch.mock.calls.filter(([, o]) => o.method === "POST")).toHaveLength(
    0,
  );
});
test("viewer retains history", async () => {
  setup(undefined, false);
  await screen.findByRole("option", { name: "uploads" });
  expect(
    screen.queryByRole("button", { name: "Review Compose volume backup" }),
  ).toBeNull();
  expect(
    screen.getByRole("list", { name: "Compose volume backup history" }),
  ).toBeTruthy();
});
test("denial retires review", async () => {
  const { onDenied } = setup((_u, o) =>
    o.method === "POST" ? new Response("{}", { status: 403 }) : undefined,
  );
  await review();
  fireEvent.click(
    screen.getByRole("checkbox", {
      name: /I acknowledge interruption of this volume.s writers while/,
    }),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Confirm Compose volume backup" }),
  );
  await waitFor(() =>
    expect(
      screen.queryByRole("region", { name: "Reviewed Compose volume backup" }),
    ).toBeNull(),
  );
  expect(onDenied).toHaveBeenCalled();
});

test("unmount aborts a pending mutation and ignores its late receipt", async () => {
  let signal: AbortSignal | undefined;
  let resolve!: (response: Response) => void;
  const pending = new Promise<Response>((done) => {
    resolve = done;
  });
  const { fetch, view, onDenied } = setup((_url, options) => {
    if (options.method === "POST") {
      signal = options.signal as AbortSignal;
      return pending;
    }
  });
  await review();
  fireEvent.click(
    screen.getByRole("checkbox", {
      name: /I acknowledge interruption of this volume.s writers while/,
    }),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Confirm Compose volume backup" }),
  );
  await waitFor(() => expect(signal).toBeDefined());
  expect(signal!.aborted).toBe(false);
  const readsBefore = fetch.mock.calls.length;
  view.unmount();
  expect(signal!.aborted).toBe(true);
  await act(async () => {
    resolve(new Response(JSON.stringify(backup)));
    await pending;
  });
  await waitFor(() =>
    expect(
      screen.queryByRole("region", { name: "Compose volume backups" }),
    ).toBeNull(),
  );
  expect(fetch.mock.calls).toHaveLength(readsBefore);
  expect(onDenied).not.toHaveBeenCalled();
});

test("held Compose archive requires consent and retries the exact resume recovery after response loss", async () => {
  const hash = `sha256:${"a".repeat(64)}`;
  const snapshot = { resource_id: "r", binding_id: "b", destination_id: "d" };
  const volume = { name: "uploads", volume_name: "owned-uploads" };
  const source = {
    healthy: { snapshot, operation_id: "deploy", plan_hash: hash, stack: {} },
    plan: { snapshot, hash, stack: { volumes: [volume] } },
    volume,
    retained: [],
  };
  const held = {
    id: "op",
    project_id: "p",
    resource_id: "r",
    binding_id: "b",
    state: "reconciliation_required",
    task_mode: "backup",
    mutation_hold: true,
    restore_ownership_held: false,
    storage_archive: {
      mode: "backup",
      backup_id: "backup",
      plan: {
        operation_id: "op",
        project_id: "p",
        destination_id: "d",
        mode: "backup",
        backup_id: "backup",
        hash,
        compiler_version: "managed-compose.local-volume-archive.v1",
        policy_version: "managed-compose.local-volume-archive.v1",
        compose_source: source,
        source_storage: {},
        source_healthy: {},
        source_materialization: {},
        writers: [],
      },
      progress: [],
      result: {
        archive_outcome: "failed",
        resume_outcome: "uncertain",
        restore_verified: false,
        uncertain: true,
      },
    },
  };
  const recovered = {
    ...held,
    id: "recover",
    state: "succeeded",
    task_mode: "recover",
    mutation_hold: false,
    storage_archive: {
      ...held.storage_archive,
      mode: "recover",
      recovery_of_operation_id: "op",
      plan: {
        ...held.storage_archive.plan,
        operation_id: "recover",
        mode: "recover",
        recovery_of_operation_id: "op",
        recovery_evidence: { original_plan_hash: hash },
      },
      result: {
        archive_outcome: "not_attempted",
        resume_outcome: "resumed",
        restore_verified: false,
        uncertain: false,
      },
    },
  };
  const json = (value: unknown) => new Response(JSON.stringify(value));
  let attempts = 0;
  const { fetch } = setup((url, options) => {
    if (url.includes("?limit="))
      return json({
        items: [{ ...backup, state: "failed" }],
        limit: 20,
        offset: 0,
        count: 1,
        total: 1,
      });
    if (url.endsWith("/hosting-operations/op")) return json(held);
    if (url.endsWith("/hosting-operations/recover")) return json(recovered);
    if (url.endsWith("/storage-archive-recovery")) {
      if (++attempts === 1)
        return Promise.reject(new Error("accepted receipt lost"));
      return json(recovered);
    }
  });
  fireEvent.click(
    await screen.findByRole("button", { name: "View volume backup backup" }),
  );
  const submit = await screen.findByRole<HTMLButtonElement>("button", {
    name: "Request Compose writer resume recovery",
  });
  expect(submit.disabled).toBe(true);
  expect(
    fetch.mock.calls.filter(([, options]) => options.method === "POST"),
  ).toHaveLength(0);
  fireEvent.click(
    screen.getByRole("checkbox", {
      name: "I reviewed the held operation and request writer resumption.",
    }),
  );
  fireEvent.click(submit);
  await screen.findByRole("alert");
  fireEvent.click(
    screen.getByRole("button", {
      name: "Retry identical Compose resume recovery",
    }),
  );
  await screen.findByRole("region", { name: "recover operation recover" });
  const writes = fetch.mock.calls.filter(
    ([, options]) => options.method === "POST",
  );
  expect(writes).toHaveLength(2);
  expect(writes[0]![0]).toBe(
    "/api/v1/hosting-operations/op/storage-archive-recovery",
  );
  expect(writes[1]![0]).toBe(writes[0]![0]);
  expect(writes[1]![1].body).toBe(writes[0]![1].body);
  expect(JSON.parse(String(writes[0]![1].body))).toEqual({
    request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
    expected_plan_hash: hash,
  });
  expect(screen.getByText("Writer resumption: resumed")).toBeTruthy();
});

const remoteTarget = {
  id: "target",
  project_id: "p",
  version: "postgresql.backup-target.v1",
  revision: 3,
  name: "S3 target",
  endpoint: "https://s3.example.test",
  bucket: "backups",
  region: "us-east-1",
  enabled: true,
  created_at: "2026-10-27T00:00:00Z",
  updated_at: "2026-10-27T00:00:00Z",
};
const remoteValidation = {
  id: "validation",
  project_id: "p",
  version: "owned-upload.s3-transfer.v1",
  target_id: "target",
  target_revision: 3,
  destination_id: "d",
  mode: "validate",
  state: "succeeded",
  failure_code: "",
  created_at: "2026-10-27T00:00:00Z",
  updated_at: "2026-10-27T00:00:00Z",
};
test("immediate remote selection is frozen into review and exact retry while schedule stays local", async () => {
  let writes = 0;
  const { fetch } = setup((url, options) => {
    if (url.endsWith("/database-backup-targets"))
      return new Response(JSON.stringify([remoteTarget]));
    if (url.endsWith("/storage-validations"))
      return new Response(JSON.stringify([remoteValidation]));
    if (
      options.method === "POST" &&
      url.endsWith("/storage-backups") &&
      ++writes === 1
    )
      return Promise.reject(Error("lost response"));
  });
  await screen.findAllByRole("option", { name: /S3 target/ });
  fireEvent.change(
    screen.getByRole("combobox", { name: "Remote Compose backup target" }),
    { target: { value: "target:3" } },
  );
  await review();
  expect(
    screen.getByText(
      /Remote target for this immediate backup: target revision 3/,
    ),
  ).toBeTruthy();
  expect(
    screen.getByRole("combobox", { name: "Remote Compose backup target" }),
  ).toHaveProperty("disabled", true);
  fireEvent.click(
    screen.getByRole("checkbox", {
      name: /I acknowledge interruption of this volume.s writers while/,
    }),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Confirm Compose volume backup" }),
  );
  await screen.findByRole("button", {
    name: "Retry identical Compose backup request",
  });
  await waitFor(() =>
    expect(
      screen.getByRole<HTMLButtonElement>("button", {
        name: "Retry identical Compose backup request",
      }).disabled,
    ).toBe(false),
  );
  fireEvent.click(
    screen.getByRole("button", {
      name: "Retry identical Compose backup request",
    }),
  );
  await screen.findByText(/Volume backup backup/);
  const admissions = fetch.mock.calls.filter(
    ([u, o]) => u.endsWith("/storage-backups") && o.method === "POST",
  );
  expect(admissions).toHaveLength(2);
  expect(admissions[0]![1].body).toBe(admissions[1]![1].body);
  expect(JSON.parse(String(admissions[0]![1].body)).remote_target).toEqual({
    target_id: "target",
    revision: 3,
  });
  expect(
    screen.getByRole("region", { name: "Recurring Compose volume backups" })
      .textContent,
  ).toContain("Remote target for this submission: local only");
});

const transferBlob = {
  backup_id: "backup",
  destination_id: "d",
  daemon_id: "daemon",
  sha256: `sha256:${"a".repeat(64)}`,
  size_bytes: 10,
  format: "nerocd-storage-v1",
};
const failedUpload = {
  ...remoteValidation,
  id: "upload",
  mode: "upload",
  backup_id: "backup",
  blob: transferBlob,
  state: "failed",
  failure_code: "access_denied",
};
test("remote upload retry preserves archive and request identity after lost response and successful history refresh", async () => {
  let retries = 0;
  const { fetch } = setup((url, options) => {
    if (url.endsWith("/transfers/retry") && options.method === "POST") {
      if (++retries === 1) return Promise.reject(Error("lost retry response"));
      return new Response(
        JSON.stringify({
          ...failedUpload,
          id: "next-upload",
          state: "succeeded",
          failure_code: "",
        }),
        { status: 202 },
      );
    }
    if (url.endsWith("/transfers"))
      return new Response(
        JSON.stringify([
          {
            ...failedUpload,
            ...(retries
              ? { id: "next-upload", state: "succeeded", failure_code: "" }
              : {}),
          },
        ]),
      );
    if (url.includes("/storage-backups?"))
      return new Response(
        JSON.stringify({
          items: [{ ...backup, state: "succeeded", blob: transferBlob }],
          count: 1,
          total: 1,
          limit: 20,
          offset: 0,
        }),
      );
    if (url.endsWith("/storage-backups/backup"))
      return new Response(
        JSON.stringify({ ...backup, state: "succeeded", blob: transferBlob }),
      );
  });
  fireEvent.click(
    await screen.findByRole("button", { name: "View volume backup backup" }),
  );
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Retry remote upload of the same archive",
    }),
  );
  await screen.findByRole("button", {
    name: "Retry identical remote archive request",
  });
  await waitFor(() =>
    expect(
      screen.getByRole<HTMLButtonElement>("button", {
        name: "Refresh Compose backup evidence and history",
      }).disabled,
    ).toBe(false),
  );
  fireEvent.click(
    screen.getByRole("button", {
      name: "Refresh Compose backup evidence and history",
    }),
  );
  await screen.findByText(/upload next-upload: succeeded/);
  fireEvent.click(
    screen.getByRole("button", {
      name: "Retry identical remote archive request",
    }),
  );
  await waitFor(() =>
    expect(
      fetch.mock.calls.filter(
        ([u, o]) => u.endsWith("/transfers/retry") && o.method === "POST",
      ),
    ).toHaveLength(2),
  );
  const writes = fetch.mock.calls.filter(
    ([u, o]) => u.endsWith("/transfers/retry") && o.method === "POST",
  );
  expect(writes[0]![1].body).toBe(writes[1]![1].body);
  expect(JSON.parse(String(writes[0]![1].body)).expected_transfer_id).toBe(
    "upload",
  );
  expect(
    fetch.mock.calls.filter(
      ([u, o]) => u.endsWith("/storage-backups") && o.method === "POST",
    ),
  ).toHaveLength(0);
});


test("scheduled remote selection is independent and locked across review and identical retry", async () => {
  let writes = 0;
  const { fetch } = setup((url, options) => {
    if (url.endsWith("/database-backup-targets")) return new Response(JSON.stringify([remoteTarget]));
    if (url.endsWith("/storage-validations")) return new Response(JSON.stringify([remoteValidation]));
    if (options.method === "POST" && url.endsWith("/storage-backup-schedules")) {
      if (++writes === 1) return Promise.reject(Error("lost response"));
      const body = JSON.parse(String(options.body));
      return new Response(JSON.stringify({ id: "schedule", version: "managed-compose.local-volume-archive.schedule.v1", project_id: "p", resource_id: "r", revision: 1, frequency: body.frequency, timezone: body.timezone, enabled: body.enabled, volume: body.volume, acknowledge_interruption: true, remote_target: body.remote_target, created_at: "2026-10-27T00:00:00Z", updated_at: "2026-10-27T00:00:00Z" }));
    }
  });
  await screen.findAllByRole("option", { name: /S3 target/ });
  const schedules = within(screen.getByRole("region", { name: "Recurring Compose volume backups" }));
  fireEvent.change(schedules.getByRole("combobox", { name: "Remote scheduled Compose backup target" }), { target: { value: "target:3" } });
  expect(screen.getByRole("combobox", { name: "Remote Compose backup target" })).toHaveProperty("value", "");
  await screen.findByRole("option", { name: "uploads" });
  fireEvent.change(screen.getByRole("combobox", { name: "Named volume to back up" }), { target: { value: "uploads" } });
  fireEvent.click(schedules.getByRole("button", { name: "Review Compose volume schedule" }));
  await schedules.findByRole("region", { name: "Reviewed Compose volume schedule" });
  expect(schedules.getByRole("combobox", { name: "Remote scheduled Compose backup target" })).toHaveProperty("disabled", true);
  fireEvent.click(schedules.getByRole("checkbox", { name: /I acknowledge interruption/ }));
  fireEvent.click(schedules.getByRole("button", { name: "Confirm Compose volume schedule" }));
  await schedules.findByRole("button", { name: "Retry identical schedule request" });
  expect(schedules.getByRole("combobox", { name: "Remote scheduled Compose backup target" })).toHaveProperty("disabled", true);
  fireEvent.click(schedules.getByRole("button", { name: "Retry identical schedule request" }));
  await waitFor(() => expect(writes).toBe(2));
  const admissions = fetch.mock.calls.filter(([u, o]) => u.endsWith("/storage-backup-schedules") && o.method === "POST");
  expect(admissions[1]![1].body).toBe(admissions[0]![1].body);
  expect(JSON.parse(String(admissions[0]![1].body))).toMatchObject({ remote_target: { target_id: "target", revision: 3 }, volume: "uploads", expected_deployed_operation_id: "deploy", acknowledge_interruption: true });
});

test("selected Compose backup downloads through options without retention copy metadata", async () => {
  const blob = {
    backup_id: "backup",
    destination_id: "d",
    daemon_id: "daemon",
    sha256: `sha256:${"a".repeat(64)}`,
    size_bytes: 123,
    format: "nerocd-storage-v1",
  };
  const saved = { ...backup, state: "succeeded", blob };
  setup((url) => {
    if (url.endsWith("/archive-options"))
      return new Response(
        JSON.stringify({
          available: true,
          reason_code: "",
          backup_id: "backup",
          blob,
        }),
      );
    if (url.includes("storage-backups?"))
      return new Response(
        JSON.stringify({
          items: [saved],
          limit: 20,
          offset: 0,
          count: 1,
          total: 1,
        }),
      );
    if (url === "/api/v1/storage-backups/backup")
      return new Response(JSON.stringify(saved));
    return undefined;
  });
  fireEvent.click(
    await screen.findByRole("button", { name: "View volume backup backup" }),
  );
  const link = await screen.findByRole("link", { name: "Download archive" });
  expect(link.getAttribute("href")).toBe(
    "/api/v1/storage-backups/backup/archive",
  );
});
