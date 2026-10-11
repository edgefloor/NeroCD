import type { RunLog } from "@/api";

export function formatLoadedRunLogs(runID: string, logs: readonly RunLog[]): string {
  const entries = [...logs].sort((left, right) => left.sequence - right.sequence);
  return [
    `Run: ${runID}`,
    `Loaded entries: ${entries.length}. This snapshot may not include the complete run history.`,
    "",
    ...entries.map((log) => `#${log.sequence} [${log.stream}] ${log.message}`),
    "",
  ].join("\n");
}
