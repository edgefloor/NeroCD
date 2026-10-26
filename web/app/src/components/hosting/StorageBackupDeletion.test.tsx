import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { StorageBackupDeletion } from "./StorageBackupDeletion";
import * as api from "@/api/hosting-storage-backup-deletion";
import { ApiError } from "@/api/errors";
import type { Resource } from "@/api/hosting";
vi.mock("@/api/hosting-storage-backup-deletion", () => ({
  previewDeletion: vi.fn(),
  executeDeletion: vi.fn(),
  listDeletions: vi.fn(),
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
  generation: 2,
} as Resource;
const at = "2026-10-26T12:00:00Z",
  hash = `sha256:${"a".repeat(64)}`;
const copy = {
  copy_id: "local",
  backup_id: "backup",
  location: "local" as const,
  digest: hash,
  bytes: 100,
  created_at: at,
  availability: "available" as const,
  selected: true,
  protected_reasons: [],
};
const preview = {
  id: "preview",
  origin: "manual_selection",
  version: "owned-upload.backup-deletion.v1",
  project_id: "project",
  resource_id: "resource",
  backup_id: "backup",
  resource_generation: 2,
  final_copy: true,
  policy_revision: 0,
  plan_hash: hash,
  evaluated_at: at,
  local_reference_at: null,
  remote_reference_at: null,
  selection_truncated: false,
  items: [copy],
  created_at: at,
} as api.DeletionPreview;
function setup(owner = true) {
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <StorageBackupDeletion
        scope={scope}
        resource={resource}
        backup="backup"
        copies={[copy]}
        owner={owner}
        blocked={false}
        onDenied={vi.fn()}
      />
    </QueryClientProvider>,
  );
}
beforeEach(() => {
  vi.mocked(api.listDeletions).mockResolvedValue([]);
  vi.mocked(api.previewDeletion).mockResolvedValue(preview);
});
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
test("reviews selection and requires final-copy confirmation, then retries exact saved request", async () => {
  vi.mocked(api.executeDeletion)
    .mockRejectedValueOnce(new Error("response lost"))
    .mockResolvedValue({} as api.DeletionExecution);
  setup();
  fireEvent.click(screen.getByLabelText("Delete local copy local"));
  fireEvent.click(screen.getByText("Review selected copy deletion"));
  await screen.findByLabelText("Review exact deletion selection");
  const execute = screen.getByText("Permanently delete reviewed copies");
  expect((execute as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(
    screen.getByLabelText(
      "I understand this deletes the final recoverable copy of this backup.",
    ),
  );
  fireEvent.click(execute);
  await screen.findByText("Retry identical deletion request");
  const first = vi.mocked(api.executeDeletion).mock.calls[0]![3];
  fireEvent.click(screen.getByText("Retry identical deletion request"));
  await waitFor(() => expect(api.executeDeletion).toHaveBeenCalledTimes(2));
  expect(vi.mocked(api.executeDeletion).mock.calls[1]![3]).toEqual(first);
  expect(first.confirm_final_copy).toBe(true);
});
test("viewer has deletion history without mutation controls", async () => {
  setup(false);
  await waitFor(() => expect(api.listDeletions).toHaveBeenCalled());
  expect(screen.queryByText("Review selected copy deletion")).toBeNull();
  expect(api.previewDeletion).not.toHaveBeenCalled();
});
test("denial retires review and prevents further mutations", async () => {
  vi.mocked(api.previewDeletion).mockRejectedValue(new ApiError(403, "denied", null));
  setup();
  fireEvent.click(screen.getByLabelText("Delete local copy local"));
  fireEvent.click(screen.getByText("Review selected copy deletion"));
  await screen.findByRole("alert");
  expect(screen.queryByText("Permanently delete reviewed copies")).toBeNull();
  expect(api.executeDeletion).not.toHaveBeenCalled();
});
test("resuming a held final-copy deletion requires fresh explicit confirmation", async () => {
  vi.mocked(api.listDeletions).mockResolvedValue([
    {
      ...preview,
      id: "execution",
      preview_id: "preview",
      state: "uncertain",
      reservation_held: true,
      local_state: "uncertain",
      remote_state: "not_selected",
      updated_at: at,
      items: [
        {
          copy_id: "local",
          backup_id: "backup",
          location: "local",
          state: "intent",
          failure_code: "",
          reservation_held: true,
        },
      ],
    } as api.DeletionExecution,
  ]);
  vi.mocked(api.executeDeletion).mockResolvedValue({} as api.DeletionExecution);
  setup();
  const resume = await screen.findByText("Resume the same deletion selection");
  expect((resume as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(
    screen.getByLabelText(
      "I confirm resuming deletion of the final recoverable copy for execution.",
    ),
  );
  fireEvent.click(resume);
  await waitFor(() => expect(api.executeDeletion).toHaveBeenCalledTimes(1));
  expect(vi.mocked(api.executeDeletion).mock.calls[0]![3]).toMatchObject({
    preview_id: "preview",
    expected_plan_hash: hash,
    confirm_final_copy: true,
  });
});
