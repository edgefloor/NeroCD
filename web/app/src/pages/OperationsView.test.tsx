import { fireEvent, render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { OperationsView } from "./OperationsView";

const status = { readiness: "ready", snapshot: { queue_depth: 0, queue_oldest_age_seconds: 0, active_leases: 0, expired_leases: 0, oldest_runner_heartbeat_seconds: 0, deployments: { applying: 0, verifying: 0, rolling_back: 0 }, deployment_health_passed: 0, deployment_health_failed: 0, rollback_succeeded: 0, rollback_failed: 0, runner_journal_depth: 0, runner_retry_count: 0, runner_renew_failures: 0, backup_outcome: "none", backup_age_seconds: 0, backup_reason: "none", backup_schedule_status: "backoff", backup_schedule_next_seconds: 60, backup_schedule_failures: 1, pool: { acquired: 0, total: 0, idle: 0 } } } as never;
const retention = { policy: { enabled: true, keep_days: 30, batch_size: 25, version: 4, updated_at: "2026-08-21T00:00:00Z" }, preview: { cutoff: "2026-07-22T00:00:00Z", eligible_logs: 12, eligible_bytes: 345 } } as never;

test("retention confirmation exposes aggregate-only bounds and keeps one client request identity", () => {
  const execute = vi.fn();
  render(<OperationsView status={status} loading={false} unavailable={false} canAdmin retention={retention} onRetentionExecute={execute} />);
  expect(screen.getByText(/eligible logs/i).parentElement?.textContent).not.toMatch(/run_|lease_|token/i);
  fireEvent.click(screen.getByRole("button", { name: /previewed delete batch/i }));
  expect(screen.getByRole("dialog").textContent).toContain("12");
  fireEvent.click(screen.getByRole("button", { name: /execute retained batch/i }));
  expect(execute).toHaveBeenCalledWith(4, expect.any(String));
});

test("operations status renders only bounded backup-scheduler state", () => {
  render(<OperationsView status={status} loading={false} unavailable={false} canAdmin />);
  expect(screen.getAllByText(/backoff; next 1m/i).length).toBeGreaterThan(0);
  expect(screen.getAllByText(/backup schedule: backoff/i).length).toBeGreaterThan(0);
  expect(screen.queryByText(/backup-/i)).toBeNull();
});
