import { StrictMode } from "react";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { DatabaseBackupDestinations } from "./DatabaseBackupDestinations";
import { ApiError } from "../api/errors";
import type { HostingDatabase } from "../api/hosting-databases";
import * as storageRemote from "../api/hosting-storage-backup-remote";
import * as api from "../api/hosting-database-backup-targets";
vi.mock("../api/hosting-database-backup-targets", async (original) => ({
  ...(await original<
    typeof import("../api/hosting-database-backup-targets")
  >()),
  listDatabaseBackupTargets: vi.fn(),
  listDatabaseBackupValidations: vi.fn(),
  saveDatabaseBackupTarget: vi.fn(),
  validateDatabaseBackupTarget: vi.fn(),
}));
vi.mock("../api/hosting-storage-backup-remote", () => ({
  listStorageValidations: vi.fn(async () => []),
  validateStorageTarget: vi.fn(),
}));
const scope = { actor: "owner", project: "project", environment: "env" };
const database = {
  resource: { id: "db", generation: 2 },
  binding: { destination_id: "runner" },
  engine: "postgresql",
} as HostingDatabase;
const target: api.DatabaseBackupTarget = {
  id: "storage",
  project_id: "project",
  version: "postgresql.backup-target.v1",
  revision: 1,
  name: "Daily archive",
  endpoint: "https://s3.example.invalid",
  bucket: "backups",
  region: "us-east-1",
  enabled: true,
  created_at: "2026-10-25T12:00:00Z",
  updated_at: "2026-10-25T12:00:00Z",
};
let targets: api.DatabaseBackupTarget[];
beforeEach(() => {
  targets = [];
  vi.mocked(api.listDatabaseBackupTargets).mockImplementation(
    async () => targets,
  );
  vi.mocked(api.listDatabaseBackupValidations).mockResolvedValue([]);
  vi.mocked(api.saveDatabaseBackupTarget).mockImplementation(
    async (_scope, _id, input) => {
      const out = {
        ...target,
        revision: input.expected_revision + 1,
        name: input.name,
        endpoint: input.endpoint,
        bucket: input.bucket,
        region: input.region,
        enabled: input.enabled,
      };
      targets = [out];
      return out;
    },
  );
  vi.mocked(api.validateDatabaseBackupTarget).mockResolvedValue({
    id: "validation",
    version: "postgresql.logical-s3-transfer.v1",
    project_id: "project",
    target_id: "storage",
    target_revision: 1,
    destination_id: "runner",
    mode: "validate",
    state: "queued",
    failure_code: "",
    created_at: target.created_at,
    updated_at: target.updated_at,
  });
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});
function mount(owner = true) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const onDenied = vi.fn();
  const view = render(
    <StrictMode>
      <QueryClientProvider client={client}>
        <DatabaseBackupDestinations
          database={database}
          scope={scope}
          owner={owner}
          blocked={false}
          onDenied={onDenied}
        />
      </QueryClientProvider>
    </StrictMode>,
  );
  return { ...view, client, onDenied };
}
async function fill() {
  await waitFor(() =>
    expect(
      (screen.getByLabelText("Destination name") as HTMLInputElement).disabled,
    ).toBe(false),
  );
  for (const [label, value] of [
    ["Destination name", "Daily archive"],
    ["S3 endpoint", "https://s3.example.invalid"],
    ["S3 bucket", "backups"],
    ["S3 access key", "access_canary"],
    ["S3 secret key", "secret_canary"],
  ])
    fireEvent.change(screen.getByLabelText(label!), { target: { value } });
}
test("owner creates with write-only keys, clears accepted keys, then omits credentials on edit", async () => {
  const view = mount();
  await fill();
  expect(screen.queryByText(/Replacing keys revokes/)).toBeNull();
  fireEvent.click(
    screen.getByRole("button", { name: "Create backup destination" }),
  );
  await screen.findByText(/Saved destination Daily archive, revision 1/);
  expect(
    screen.getByText(
      "Replacing keys revokes the previous credentials. Older backups cannot use the replacement keys, so retrying their upload or restoring them remotely may fail. Local backups remain available.",
    ),
  ).toBeTruthy();
  expect(
    (screen.getByLabelText("S3 access key") as HTMLInputElement).value,
  ).toBe("");
  expect(
    (screen.getByLabelText("S3 secret key") as HTMLInputElement).value,
  ).toBe("");
  expect(
    JSON.stringify(
      view.client
        .getQueryCache()
        .getAll()
        .map((q) => q.state.data),
    ),
  ).not.toContain("canary");
  await waitFor(() =>
    expect(
      (
        screen.getByRole("button", {
          name: "Save destination changes",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Save destination changes" }),
  );
  await screen.findByText(/Saved destination Daily archive, revision 2/);
  expect(api.saveDatabaseBackupTarget).toHaveBeenCalledTimes(2);
  expect(
    vi.mocked(api.saveDatabaseBackupTarget).mock.calls[1]![2],
  ).not.toHaveProperty("credentials");
});
test("response loss retains the exact credential-bearing request only in component memory", async () => {
  vi.mocked(api.saveDatabaseBackupTarget).mockRejectedValueOnce(
    new TypeError("secret_private_network"),
  );
  const view = mount();
  await fill();
  fireEvent.click(
    screen.getByRole("button", { name: "Create backup destination" }),
  );
  await screen.findByRole("button", {
    name: "Retry the same destination request",
  });
  const first = vi.mocked(api.saveDatabaseBackupTarget).mock.calls[0]![2];
  expect(
    (screen.getByLabelText("S3 secret key") as HTMLInputElement).disabled,
  ).toBe(true);
  expect(screen.queryByText(/secret_private_network/)).toBeNull();
  expect(
    JSON.stringify(
      view.client
        .getQueryCache()
        .getAll()
        .map((q) => q.state.data),
    ),
  ).not.toContain("canary");
  fireEvent.click(
    screen.getByRole("button", { name: "Retry the same destination request" }),
  );
  await screen.findByText(/Saved destination Daily archive/);
  expect(vi.mocked(api.saveDatabaseBackupTarget).mock.calls[1]![2]).toEqual(
    first,
  );
});
test("conflict preserves nonsecret edits and requires explicit current revision review", async () => {
  targets = [target];
  vi.mocked(api.saveDatabaseBackupTarget).mockRejectedValueOnce(
    new ApiError(409, "conflict", null),
  );
  mount();
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Edit destination Daily archive",
    }),
  );
  fireEvent.change(screen.getByLabelText("Destination name"), {
    target: { value: "My edit" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Save destination changes" }),
  );
  await screen.findByRole("alert");
  targets = [{ ...target, revision: 2 }];
  fireEvent.click(screen.getByRole("button", { name: "Refresh destinations" }));
  await screen.findByText(/reviewed destination revision changed/);
  expect(
    (screen.getByLabelText("Destination name") as HTMLInputElement).value,
  ).toBe("My edit");
  expect(
    (
      screen.getByRole("button", {
        name: "Save destination changes",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  expect(api.saveDatabaseBackupTarget).toHaveBeenCalledTimes(1);
  fireEvent.click(
    screen.getByRole("button", { name: "Edit destination Daily archive" }),
  );
  expect(screen.getByText(/reviewed revision 2/)).toBeTruthy();
});
test("validation is bound to this database's runner and exact reviewed target revision", async () => {
  targets = [target];
  mount();
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Validate Daily archive from this runner",
    }),
  );
  await screen.findByText(/Accepted destination validation validation/);
  expect(vi.mocked(api.validateDatabaseBackupTarget).mock.calls[0]![2]).toEqual(
    {
      request_id: expect.stringMatching(/^[a-f0-9]{32}$/),
      expected_revision: 1,
      destination_id: "runner",
    },
  );
});
test("viewer sees safe configuration but has no credential or mutation controls", async () => {
  targets = [target];
  mount(false);
  await screen.findByText(/Daily archive · revision 1/);
  expect(screen.queryByLabelText("S3 secret key")).toBeNull();
  expect(screen.queryByRole("button", { name: /Validate Daily/ })).toBeNull();
  expect(api.saveDatabaseBackupTarget).not.toHaveBeenCalled();
});
test("permission denial clears destination data and removes owner actions", async () => {
  vi.mocked(api.listDatabaseBackupTargets).mockRejectedValue(
    new ApiError(403, "forbidden", null),
  );
  const view = mount();
  await screen.findByText("Backup destination access is unavailable.");
  await waitFor(() => expect(view.onDenied).toHaveBeenCalled());
  expect(screen.queryByLabelText("S3 secret key")).toBeNull();
});

async function useRealSaveHelper() {
  const actual = await vi.importActual<
    typeof import("../api/hosting-database-backup-targets")
  >("../api/hosting-database-backup-targets");
  vi.mocked(api.saveDatabaseBackupTarget).mockImplementation(
    actual.saveDatabaseBackupTarget,
  );
  const fetch = vi.fn(
    async (_url: RequestInfo | URL, _options?: RequestInit) => {
      targets = [target];
      return new Response(JSON.stringify(target), { status: 200 });
    },
  );
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
test("padded keys are rejected before submit and remain correctable through the real save helper", async () => {
  const fetch = await useRealSaveHelper();
  mount();
  await fill();
  fireEvent.change(screen.getByLabelText("S3 access key"), {
    target: { value: " access_canary " },
  });
  const save = screen.getByRole("button", {
    name: "Create backup destination",
  }) as HTMLButtonElement;
  expect(save.disabled).toBe(true);
  expect(
    (screen.getByLabelText("S3 access key") as HTMLInputElement).disabled,
  ).toBe(false);
  expect(fetch).not.toHaveBeenCalled();
  fireEvent.change(screen.getByLabelText("S3 access key"), {
    target: { value: "access_canary" },
  });
  expect(save.disabled).toBe(false);
  fireEvent.click(save);
  await screen.findByText(/Saved destination Daily archive, revision 1/);
  expect(fetch).toHaveBeenCalledTimes(1);
});
test("never-sent preflight errors release the form without replaying an invalid request", async () => {
  const fetch = await useRealSaveHelper();
  mount();
  await fill();
  fireEvent.change(screen.getByLabelText("Destination name"), {
    target: { value: "x".repeat(129) },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Create backup destination" }),
  );
  await screen.findByText(
    /Correct the destination fields. This request was not sent/,
  );
  expect(fetch).not.toHaveBeenCalled();
  expect(
    (screen.getByLabelText("Destination name") as HTMLInputElement).disabled,
  ).toBe(false);
  expect(
    (screen.getByLabelText("S3 secret key") as HTMLInputElement).value,
  ).toBe("secret_canary");
  expect(
    screen.queryByRole("button", {
      name: "Retry the same destination request",
    }),
  ).toBeNull();
  fireEvent.change(screen.getByLabelText("Destination name"), {
    target: { value: "Daily archive" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Create backup destination" }),
  );
  await screen.findByText(/Saved destination Daily archive, revision 1/);
  expect(fetch).toHaveBeenCalledTimes(1);
});
test("real helper preserves one sent request after transport loss", async () => {
  const fetch = await useRealSaveHelper();
  fetch.mockRejectedValueOnce(new TypeError("network"));
  mount();
  await fill();
  fireEvent.click(
    screen.getByRole("button", { name: "Create backup destination" }),
  );
  await screen.findByRole("button", {
    name: "Retry the same destination request",
  });
  const first = fetch.mock.calls[0];
  expect(
    (screen.getByLabelText("S3 access key") as HTMLInputElement).disabled,
  ).toBe(true);
  fireEvent.click(
    screen.getByRole("button", { name: "Retry the same destination request" }),
  );
  await screen.findByText(/Saved destination Daily archive, revision 1/);
  expect(fetch.mock.calls[1]?.[0]).toEqual(first?.[0]);
  expect(fetch.mock.calls[1]?.[1]?.body).toEqual(first?.[1]?.body);
  expect(fetch.mock.calls[1]?.[1]?.headers).toEqual(first?.[1]?.headers);
  expect(fetch.mock.calls[1]?.[1]?.method).toEqual(first?.[1]?.method);
});

test("uploads-only applications configure and validate a target without a database", async () => {
  targets = [target];
  const validation = {
    id: "storage_validation",
    version: "owned-upload.s3-transfer.v1",
    project_id: "project",
    target_id: target.id,
    target_revision: 1,
    destination_id: "uploads_destination",
    mode: "validate",
    state: "queued",
    failure_code: "",
    created_at: target.created_at,
    updated_at: target.updated_at,
  } as Extract<storageRemote.StorageBackupTransfer,{mode:"validate"}>;
  vi.mocked(storageRemote.validateStorageTarget).mockResolvedValue(validation);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <DatabaseBackupDestinations
        storage={{
          resource: { id: "uploads_app" },
          destination_id: "uploads_destination",
        }}
        scope={scope}
        owner
        blocked={false}
        onDenied={() => {}}
      />
    </QueryClientProvider>,
  );
  const validate = await screen.findByRole("button", {
    name: `Validate ${target.name} from this runner`,
  });
  await waitFor(() => expect(validate.hasAttribute("disabled")).toBe(false));
  fireEvent.click(validate);
  await waitFor(() =>
    expect(storageRemote.validateStorageTarget).toHaveBeenCalledTimes(1),
  );
  expect(
    vi.mocked(storageRemote.validateStorageTarget).mock.calls[0]![2]
      .destination_id,
  ).toBe("uploads_destination");
  expect(api.validateDatabaseBackupTarget).not.toHaveBeenCalled();
  expect(screen.getByRole("region", { name: "Backup targets" })).toBeTruthy();
  client.clear();
});
