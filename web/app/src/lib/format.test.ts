import { afterEach, expect, test, vi } from "vitest";
import type { TaskRun } from "@/api";
import { formatDuration } from "./format";

afterEach(() => vi.useRealTimers());
const run = (started_at: string, finished_at?: string) => ({ started_at, finished_at }) as TaskRun;

test("completed run durations retain seconds rather than rounding up to a minute", () => {
  const start = "2026-10-11T12:00:00Z";
  expect(formatDuration(run(start, "2026-10-11T12:00:00Z"))).toBe("0s");
  expect(formatDuration(run(start, "2026-10-11T12:00:07Z"))).toBe("7s");
  expect(formatDuration(run(start, "2026-10-11T12:00:59.999Z"))).toBe("59s");
  expect(formatDuration(run(start, "2026-10-11T12:01:00Z"))).toBe("1m 0s");
  expect(formatDuration(run(start, "2026-10-11T12:02:05Z"))).toBe("2m 5s");
  expect(formatDuration(run(start, "2026-10-11T13:02:05Z"))).toBe("1h 2m");
});

test("unfinished runs use elapsed time and invalid intervals are explicit", () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-10-11T12:00:07Z"));
  expect(formatDuration(run("2026-10-11T12:00:00Z"))).toBe("7s");
  expect(formatDuration(run("invalid"))).toBe("Unavailable");
  expect(formatDuration(run("2026-10-11T12:00:00Z", "invalid"))).toBe("Unavailable");
  expect(formatDuration(run("2026-10-11T13:00:00Z"))).toBe("Unavailable");
});
