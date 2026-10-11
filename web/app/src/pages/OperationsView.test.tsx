import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { OperationsView } from "./OperationsView";

afterEach(cleanup);

const status = { readiness: "ready", snapshot: { queue_depth: 0, queue_oldest_age_seconds: 0, active_leases: 0, expired_leases: 0, oldest_runner_heartbeat_seconds: 0, deployments: { applying: 0, verifying: 0, rolling_back: 0 }, deployment_health_passed: 0, deployment_health_failed: 0, rollback_succeeded: 0, rollback_failed: 0, runner_journal_depth: 0, runner_retry_count: 0, runner_renew_failures: 0, backup_outcome: "none", backup_age_seconds: 0, backup_reason: "none", backup_schedule_status: "backoff", backup_schedule_next_seconds: 60, backup_schedule_failures: 1, pool: { acquired: 0, total: 0, idle: 0 } } } as never;
const retention = { policy: { enabled: true, keep_days: 30, batch_size: 25, version: 4, updated_at: "2026-08-21T00:00:00Z" }, preview: { cutoff: "2026-07-22T00:00:00Z", eligible_logs: 12, eligible_bytes: 345 } } as never;

test("retention confirmation exposes aggregate-only bounds and keeps one client request identity", async () => {
  const execute = vi.fn();
  render(<OperationsView status={status} loading={false} unavailable={false} canAdmin retention={retention} onRetentionExecute={execute} />);
  expect(screen.getByText(/eligible logs/i).parentElement?.textContent).not.toMatch(/run_|lease_|token/i);
  fireEvent.click(screen.getByRole("button", { name: /previewed delete batch/i }));
  expect(screen.getByRole("dialog").textContent).toContain("12");
  fireEvent.click(screen.getByRole("button", { name: /execute retained batch/i }));
  expect(execute).toHaveBeenCalledWith(4, expect.any(String));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("operations status renders only bounded backup-scheduler state", () => {
  render(<OperationsView status={status} loading={false} unavailable={false} canAdmin />);
  expect(screen.getAllByText(/backoff; next 1m/i).length).toBeGreaterThan(0);
  expect(screen.getAllByText(/backup schedule: backoff/i).length).toBeGreaterThan(0);
  expect(screen.queryByText(/backup-/i)).toBeNull();
});


test("pending retention saves lock fields and keep the draft available after failure", () => {
  const update = vi.fn();
  const props = { status, loading: false, unavailable: false, canAdmin: true, retention, onRetentionUpdate: update };
  const view = render(<OperationsView {...props} />);
  fireEvent.change(screen.getByRole("spinbutton", { name: "Keep days" }), { target: { value: "21" } });
  view.rerender(<OperationsView {...props} retentionBusy />);
  expect((screen.getByRole("checkbox") as HTMLInputElement).disabled).toBe(true);
  for (const field of screen.getAllByRole("spinbutton")) expect((field as HTMLInputElement).disabled).toBe(true);
  const form = screen.getByRole("form", { name: "Run-log retention policy" });
  expect(form.getAttribute("aria-busy")).toBe("true");
  fireEvent.submit(form);
  expect(update).not.toHaveBeenCalled();
  view.rerender(<OperationsView {...props} retentionBusy={false} />);
  expect((screen.getByRole("spinbutton", { name: "Keep days" }) as HTMLInputElement).value).toBe("21");
  fireEvent.submit(form);
  expect(update).toHaveBeenCalledWith({ enabled: true, keep_days: 21, batch_size: 25 });
});


test("retention execution preserves confirmation and request identity through a failed attempt", async () => {
  let reject!: (cause: Error) => void;
  const execute = vi.fn().mockImplementationOnce(() => new Promise<void>((_, fail) => { reject = fail; })).mockResolvedValueOnce(undefined);
  const props = { status, loading: false, unavailable: false, canAdmin: true, retention, onRetentionExecute: execute };
  const view = render(<OperationsView {...props} />);
  fireEvent.click(screen.getByRole("button", { name: "Previewed delete batch" }));
  const confirm = screen.getByRole("button", { name: "Execute retained batch" });
  fireEvent.click(confirm);
  fireEvent.click(confirm);
  expect(execute).toHaveBeenCalledTimes(1);
  fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
  expect(screen.queryByRole("button", { name: "Close" })).toBeNull();
  expect(screen.getByRole("dialog")).toBeTruthy();
  await act(async () => { reject(new Error("Temporary execution failure")); });
  expect(screen.getByRole("alert").textContent).toContain("Temporary execution failure");
  view.rerender(<OperationsView {...props} retention={{ policy: { enabled: true, version: 5, batch_size: 99 }, preview: { eligible_logs: 99 } } as never} />);
  expect(screen.getByRole("dialog").textContent).toContain("up to 25");
  fireEvent.click(screen.getByRole("button", { name: "Execute retained batch" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(execute.mock.calls[1]).toEqual(execute.mock.calls[0]);
});


test("retention save failures stay beside the draft and clear while retrying", () => {
  const props = { status, loading: false, unavailable: false, canAdmin: true, retention };
  const view = render(<OperationsView {...props} />);
  fireEvent.change(screen.getByRole("spinbutton", { name: "Keep days" }), { target: { value: "21" } });
  view.rerender(<OperationsView {...props} retentionSaveError="Temporary policy save failure" />);
  expect(screen.getByRole("alert").textContent).toContain("Unable to save retention policy: Temporary policy save failure");
  expect((screen.getByRole("spinbutton", { name: "Keep days" }) as HTMLInputElement).value).toBe("21");
  view.rerender(<OperationsView {...props} retentionSaveError="Temporary policy save failure" retentionBusy />);
  expect(screen.queryByRole("alert")).toBeNull();
  view.rerender(<OperationsView {...props} />);
  expect(screen.queryByRole("alert")).toBeNull();
});


test("reset restores the latest loaded retention policy without saving", () => {
  const update = vi.fn();
  const reset = vi.fn();
  const props = { status, loading: false, unavailable: false, canAdmin: true, retention, onRetentionUpdate: update, onRetentionReset: reset };
  const view = render(<OperationsView {...props} />);
  const days = screen.getByRole("spinbutton", { name: "Keep days" }) as HTMLInputElement;
  fireEvent.change(days, { target: { value: "21" } });
  const refreshed = { policy: { enabled: false, keep_days: 60, batch_size: 50, version: 5 }, preview: { eligible_logs: 0 } } as never;
  view.rerender(<OperationsView {...props} retention={refreshed} />);
  expect(days.value).toBe("21");
  fireEvent.click(screen.getByRole("button", { name: "Reset changes" }));
  expect(days.value).toBe("60");
  expect((screen.getByRole("spinbutton", { name: "Batch size" }) as HTMLInputElement).value).toBe("50");
  expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(false);
  expect(update).not.toHaveBeenCalled();
  expect(reset).toHaveBeenCalledOnce();
});


test("unsaved retention changes cannot open a deletion preview", () => {
  render(<OperationsView status={status} loading={false} unavailable={false} canAdmin retention={retention} />);
  const preview = screen.getByRole("button", { name: "Previewed delete batch" }) as HTMLButtonElement;
  const days = screen.getByRole("spinbutton", { name: "Keep days" });
  fireEvent.change(days, { target: { value: "21" } });
  expect(preview.disabled).toBe(true);
  fireEvent.click(preview);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.getByText("Save or reset your changes before previewing a deletion.")).toBeTruthy();
  fireEvent.change(days, { target: { value: "30" } });
  expect(preview.disabled).toBe(false);
  fireEvent.click(screen.getByRole("checkbox"));
  expect(preview.disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Reset changes" }));
  expect(preview.disabled).toBe(false);
});
