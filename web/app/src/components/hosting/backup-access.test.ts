import { ApiError } from "@/api/errors";
import { expect, test, vi } from "vitest";
import { readBackupMetadata } from "./backup-access";

test.each([401, 403])(
  "a live %i response revokes access before forwarding the error",
  async (status) => {
    const error = new ApiError(status, "denied", null);
    const onDenied = vi.fn();
    await expect(
      readBackupMetadata(
        new AbortController().signal,
        () => Promise.reject(error),
        onDenied,
      ),
    ).rejects.toBe(error);
    expect(onDenied).toHaveBeenCalledTimes(1);
  },
);

test("a late denial from a retired read cannot revoke the current session", async () => {
  const controller = new AbortController();
  const onDenied = vi.fn();
  let reject!: (error: unknown) => void;
  const pending = readBackupMetadata(
    controller.signal,
    () =>
      new Promise((_, no) => {
        reject = no;
      }),
    onDenied,
  );
  controller.abort();
  const error = new ApiError(403, "old authority", null);
  reject(error);
  await expect(pending).rejects.toBe(error);
  expect(onDenied).not.toHaveBeenCalled();
});

test("transient metadata errors leave the authorization session intact", async () => {
  const onDenied = vi.fn();
  const error = new ApiError(503, "unavailable", null);
  await expect(
    readBackupMetadata(
      new AbortController().signal,
      () => Promise.reject(error),
      onDenied,
    ),
  ).rejects.toBe(error);
  expect(onDenied).not.toHaveBeenCalled();
});
