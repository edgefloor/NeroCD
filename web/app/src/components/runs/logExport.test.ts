import { expect, test } from "vitest";
import { formatLoadedRunLogs } from "./logExport";

test("loaded output exports retain sequence, streams, Unicode, and multiline messages", () => {
  const logs = [
    { id: "second", run_id: "run_export", sequence: 8, stream: "stderr", message: "Failure\n  details: ő" },
    { id: "first", run_id: "run_export", sequence: 7, stream: "stdout", message: "Build ready" },
  ];
  expect(formatLoadedRunLogs("run_export", logs)).toBe([
    "Run: run_export",
    "Loaded entries: 2. This snapshot may not include the complete run history.",
    "",
    "#7 [stdout] Build ready",
    "#8 [stderr] Failure\n  details: ő",
    "",
  ].join("\n"));
  expect(logs.map((log) => log.sequence)).toEqual([8, 7]);
});
