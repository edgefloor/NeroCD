import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { StorageBackupRetention } from "./StorageBackupRetention";
import * as api from "@/api/hosting-storage-backup-retention";
import { ApiError } from "@/api/errors";
import type { Resource } from "@/api/hosting";
vi.mock("@/api/hosting-storage-backup-retention", () => ({
  getRetentionPolicy: vi.fn(),
  listRetentionExecutions: vi.fn(),
  saveRetentionPolicy: vi.fn(),
  createRetentionPreview: vi.fn(),
  executeRetention: vi.fn(),
}));
const scope = {
  actor: "owner",
  project: "project",
  resource: "resource",
  destination: "destination",
  binding: "binding",
};
const resource = {
  id: "resource",
  project_id: "project",
  generation: 4,
} as Resource;
const limits = { keep_count: 7, keep_days: 0, max_bytes: 0 };
const policy = {
  version: "owned-upload.backup-retention.v1",
  project_id: "project",
  resource_id: "resource",
  revision: 2,
  resource_generation: 4,
  enabled: true,
  automatic: false,
  local: limits,
  remote: limits,
  created_at: "2026-10-26T12:00:00Z",
  updated_at: "2026-10-26T12:00:00Z",
  storage: {
    storage_id: "storage",
    generation: 1,
    hash: `sha256:${"a".repeat(64)}`,
  },
  binding_id: "binding",
  destination_id: "destination",
} as const;
function setup(owner = true) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <StorageBackupRetention
        scope={scope}
        resource={resource}
        owner={owner}
        blocked={false}
        onDenied={vi.fn()}
      />
    </QueryClientProvider>,
  );
  return client;
}
beforeEach(() => {
  vi.mocked(api.getRetentionPolicy).mockResolvedValue(null);
  vi.mocked(api.listRetentionExecutions).mockResolvedValue([]);
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
test("configuration defaults remain disabled and independent", async () => {
  setup();
  await waitFor(() =>
    expect(
      (
        screen.getByRole("button", {
          name: "Save retention policy",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false),
  );
  expect(
    (
      screen.getByRole("checkbox", {
        name: "Enable retention",
      }) as HTMLInputElement
    ).checked,
  ).toBe(false);
  expect(
    (screen.getByLabelText("local keep_count") as HTMLInputElement).value,
  ).toBe("7");
  expect(
    (screen.getByLabelText("remote keep_days") as HTMLInputElement).value,
  ).toBe("0");
  fireEvent.change(screen.getByLabelText("remote keep_count"), {
    target: { value: "2" },
  });
  vi.mocked(api.saveRetentionPolicy).mockResolvedValue(policy);
  fireEvent.click(
    screen.getByRole("button", { name: "Save retention policy" }),
  );
  await waitFor(() => expect(api.saveRetentionPolicy).toHaveBeenCalledOnce());
  const sent = vi.mocked(api.saveRetentionPolicy).mock.calls[0]![2];
  expect(sent.local.keep_count).toBe(7);
  expect(sent.remote.keep_count).toBe(2);
  expect(sent.enabled).toBe(false);
  expect(sent.expected_resource_generation).toBe(4);
});
test("viewer can read status but cannot mutate", async () => {
  setup(false);
  await waitFor(() => expect(api.getRetentionPolicy).toHaveBeenCalled());
  expect(
    screen.queryByRole("button", { name: "Save retention policy" }),
  ).toBeNull();
  expect(api.executeRetention).not.toHaveBeenCalled();
});
test("review sends exactly the immutable preview and retries the same request", async () => {
  vi.mocked(api.getRetentionPolicy).mockResolvedValue(policy);
  const preview = {
    id: "preview",
    version: policy.version,
    project_id: "project",
    resource_id: "resource",
    policy_revision: 2,
    plan_hash: `sha256:${"b".repeat(64)}`,
    evaluated_at: policy.created_at,
    local_reference_at: null,
    remote_reference_at: null,
    selection_truncated: false,
    created_at: policy.created_at,
    items: [
      {
        copy_id: "copy",
        backup_id: "backup",
        location: "remote",
        digest: `sha256:${"c".repeat(64)}`,
        bytes: 192,
        created_at: policy.created_at,
        availability: "available",
        selected: true,
        protected_reasons: [],
      },
    ],
  } as api.RetentionPreview;
  vi.mocked(api.createRetentionPreview).mockResolvedValue(preview);
  vi.mocked(api.executeRetention).mockRejectedValue(new Error("response lost"));
  setup();
  await waitFor(() =>
    expect(
      (
        screen.getByRole("button", {
          name: "Review deletion preview",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Review deletion preview" }),
  );
  await screen.findByRole("button", { name: "Delete selected copies" });
  expect(api.executeRetention).not.toHaveBeenCalled();
  fireEvent.click(
    screen.getByRole("button", { name: "Delete selected copies" }),
  );
  await screen.findByRole("button", { name: "Retry saved retention request" });
  const original = vi.mocked(api.executeRetention).mock.calls[0]![2];
  fireEvent.click(
    screen.getByRole("button", { name: "Retry saved retention request" }),
  );
  await waitFor(() => expect(api.executeRetention).toHaveBeenCalledTimes(2));
  expect(vi.mocked(api.executeRetention).mock.calls[1]![2]).toBe(original);
  expect(original.preview_id).toBe("preview");
  expect(original.expected_plan_hash).toBe(preview.plan_hash);
  expect(screen.getByText(/Selected for deletion/)).not.toBeNull();
});

test("invalid numeric limits remain editable and do not create a saved attempt", async () => {
  setup();
  const save = await screen.findByRole("button", {
    name: "Save retention policy",
  });
  await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
  fireEvent.change(screen.getByLabelText("local keep_count"), {
    target: { value: "-1" },
  });
  fireEvent.click(save);
  await screen.findByText(/Enter whole nonnegative limits/);
  expect(api.saveRetentionPolicy).not.toHaveBeenCalled();
  expect(
    (screen.getByLabelText("local keep_count") as HTMLInputElement).disabled,
  ).toBe(false);
  fireEvent.change(screen.getByLabelText("local keep_count"), {
    target: { value: "2" },
  });
  vi.mocked(api.saveRetentionPolicy).mockResolvedValue(policy);
  fireEvent.click(save);
  await waitFor(() => expect(api.saveRetentionPolicy).toHaveBeenCalledOnce());
  expect(
    vi.mocked(api.saveRetentionPolicy).mock.calls[0]![2].local.keep_count,
  ).toBe(2);
});

test("definitive stale policy rejection refreshes revision and allows correction", async () => {
  vi.mocked(api.getRetentionPolicy).mockResolvedValue(policy);
  vi.mocked(api.saveRetentionPolicy)
    .mockRejectedValueOnce(new ApiError(409, "stale", null))
    .mockResolvedValue(policy);
  setup();
  const save = await screen.findByRole("button", {
    name: "Save retention policy",
  });
  await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
  vi.mocked(api.getRetentionPolicy).mockResolvedValue({
    ...policy,
    revision: 3,
  });
  fireEvent.click(save);
  await screen.findByText(/The policy or copies changed/);
  await waitFor(() =>
    expect(vi.mocked(api.getRetentionPolicy).mock.calls.length).toBeGreaterThan(
      1,
    ),
  );
  expect(
    screen.queryByRole("button", { name: "Retry saved retention request" }),
  ).toBeNull();
  await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
  fireEvent.change(screen.getByLabelText("remote keep_count"), {
    target: { value: "1" },
  });
  fireEvent.click(save);
  await waitFor(() => expect(api.saveRetentionPolicy).toHaveBeenCalledTimes(2));
  const first = vi.mocked(api.saveRetentionPolicy).mock.calls[0]![2];
  const corrected = vi.mocked(api.saveRetentionPolicy).mock.calls[1]![2];
  expect(corrected.request_id).not.toBe(first.request_id);
  expect(corrected.expected_revision).toBe(3);
  expect(corrected.remote.keep_count).toBe(1);
});

test("ambiguous saved policy retry stays identical and can be discarded without claiming cancellation", async () => {
  vi.mocked(api.saveRetentionPolicy).mockRejectedValue(
    new Error("response lost"),
  );
  setup();
  const save = await screen.findByRole("button", {
    name: "Save retention policy",
  });
  await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(save);
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Retry saved retention request",
    }),
  );
  await waitFor(() => expect(api.saveRetentionPolicy).toHaveBeenCalledTimes(2));
  expect(vi.mocked(api.saveRetentionPolicy).mock.calls[1]![2]).toBe(
    vi.mocked(api.saveRetentionPolicy).mock.calls[0]![2],
  );
  fireEvent.click(
    await screen.findByRole("button", {
      name: "Discard saved request locally",
    }),
  );
  await screen.findByText(
    /Saved request discarded locally. This does not cancel work/,
  );
  expect(
    screen.queryByRole("button", { name: "Retry saved retention request" }),
  ).toBeNull();
  await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
});
