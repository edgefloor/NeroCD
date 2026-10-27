import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { StorageArchiveTransfers } from "./StorageArchiveTransfers";
import type { StorageBackupTransfer } from "@/api/hosting-storage-backup-remote";
afterEach(cleanup);
const upload = {
  id: "upload",
  mode: "upload",
  state: "failed",
  failure_code: "access_denied",
} as StorageBackupTransfer;
const download = {
  id: "download",
  mode: "download",
  state: "uncertain",
  failure_code: "interrupted",
} as StorageBackupTransfer;
test("existing image caller retains independent upload and held download retry controls", () => {
  const retry = vi.fn();
  render(
    <StorageArchiveTransfers
      transfers={[upload, download]}
      selected="backup"
      owner
      blocked={false}
      remoteBusy={false}
      onRetry={retry}
      allowDownload
    />,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Retry remote download" }),
  );
  expect(retry).toHaveBeenLastCalledWith(download);
  fireEvent.click(
    screen.getByRole("button", {
      name: "Retry remote upload of the same archive",
    }),
  );
  expect(retry).toHaveBeenLastCalledWith(upload);
  expect(screen.getByText(/Restore remains held/)).toBeTruthy();
});
test("Compose caller exposes upload retry and keeps identical retry available after refreshed transfer succeeds", () => {
  const saved = vi.fn();
  render(
    <StorageArchiveTransfers
      transfers={[{ ...upload, state: "succeeded" }, download]}
      selected="backup"
      owner
      blocked={false}
      remoteBusy={false}
      remoteRetry={{
        backup: "backup",
        input: { request_id: "a".repeat(32), expected_transfer_id: "upload" },
      }}
      onRetry={() => {}}
      onRetrySaved={saved}
    />,
  );
  expect(
    screen.queryByRole("button", { name: "Retry remote download" }),
  ).toBeNull();
  fireEvent.click(
    screen.getByRole("button", {
      name: "Retry identical remote archive request",
    }),
  );
  expect(saved).toHaveBeenCalledOnce();
});
test("viewer and blocked caller cannot retry", () => {
  const view = render(
    <StorageArchiveTransfers
      transfers={[upload]}
      selected="backup"
      owner={false}
      blocked={false}
      remoteBusy={false}
      onRetry={() => {}}
    />,
  );
  expect(screen.queryByRole("button")).toBeNull();
  view.rerender(
    <StorageArchiveTransfers
      transfers={[upload]}
      selected="backup"
      owner
      blocked
      remoteBusy={false}
      onRetry={() => {}}
    />,
  );
  expect(
    screen.getByRole<HTMLButtonElement>("button", {
      name: "Retry remote upload of the same archive",
    }).disabled,
  ).toBe(true);
});
