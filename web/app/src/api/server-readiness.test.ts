import { afterEach, expect, test, vi } from "vitest";
import { currentRead, getServerReadiness, validateServerReadiness } from "./server-readiness";
const unknown = { server_id: "a/b", profile: "basic_image_hosting.v1", status: "unknown", reason: "no_sample" };
afterEach(() => vi.unstubAllGlobals());
test("encodes the exact server ID, forwards cancellation, and disables caches", async () => {
  const fetch = vi.fn(async () => new Response(JSON.stringify(unknown))); vi.stubGlobal("fetch", fetch);
  const controller = new AbortController(); expect(await getServerReadiness("a/b", { signal: controller.signal })).toEqual(unknown);
  expect(fetch).toHaveBeenCalledWith("/api/v1/servers/a%2Fb/readiness", expect.objectContaining({ method: "GET", signal: controller.signal, cache: "no-store" }));
});
test.each([{ ...unknown, server_id: "other" }, { ...unknown, status: "ready" }, { ...unknown, reason: "custom" }, { ...unknown, reason: "stale" }, { ...unknown, sample_id: "invalid" }])("rejects malformed, mismatched or incomplete evidence %j", (value) => expect(() => validateServerReadiness(value, "a/b")).toThrow());
test("local receipt age bounds display without using observed_at or trusting wall-clock rollback", () => {
  const snapshot = { status: unknown as Parameters<typeof currentRead>[0]["status"], monotonic: 100, wall: 1000 };
  expect(currentRead(snapshot, 5099, -99999)).toBe(true); expect(currentRead(snapshot, 5100, -99999)).toBe(false); expect(currentRead(snapshot, 101, 6000)).toBe(false); expect(currentRead(snapshot, 99, 1000)).toBe(false);
});
