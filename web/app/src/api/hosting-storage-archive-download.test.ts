import { afterEach, expect, test, vi } from "vitest";
import {
  archiveDownloadPath,
  getArchiveDownloadOptions,
} from "./hosting-storage-archive-download";
import type { Backup } from "./hosting-storage-archives";
const scope = {
  actor: "u",
  project: "p",
  resource: "r",
  destination: "d",
  binding: "b",
};
const blob = {
  backup_id: "backup",
  destination_id: "d",
  daemon_id: "daemon",
  sha256: `sha256:${"a".repeat(64)}`,
  size_bytes: 12,
  format: "nerocd-storage-v1",
};
const backup = {
  id: "backup",
  project_id: "p",
  resource_id: "r",
  state: "succeeded",
  blob,
} as Backup;
const options = { available: true, reason_code: "", backup_id: "backup", blob };
afterEach(() => vi.unstubAllGlobals());
test.each([
  { ...options, backup_id: "other" },
  { ...options, reason_code: "disabled" },
  { ...options, blob: undefined },
  { ...options, available: false },
  { ...options, available: "yes" },
  { ...options, extra: "secret" },
  { ...options, blob: { ...blob, destination_id: "other" } },
  { ...options, blob: { ...blob, daemon_id: "other" } },
  { ...options, blob: { ...blob, sha256: `sha256:${"b".repeat(64)}` } },
  { ...options, blob: { ...blob, size_bytes: 13 } },
  { ...options, blob: { ...blob, format: "gzip" } },
  { ...options, blob: { ...blob, path: "/private" } },
  null,
])("rejects malformed or mismatched availability %j", async (value) => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(JSON.stringify(value))),
  );
  await expect(getArchiveDownloadOptions(scope, backup)).rejects.toThrow();
});
test("rejects foreign scope before network", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  await expect(
    getArchiveDownloadOptions({ ...scope, project: "foreign" }, backup),
  ).rejects.toThrow("scope mismatch");
  expect(fetch).not.toHaveBeenCalled();
});
test.each(["../x", "bad/id", "", "a?secret=x"])(
  "rejects unsafe archive identity %s",
  (id) => {
    expect(() => archiveDownloadPath(id)).toThrow();
  },
);
