import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { TerminalLogDialog } from "./TerminalLogDialog";

afterEach(cleanup);
const run = { id: "run_one", status: "running" } as never;
const logs = [{ id: "log_one", run_id: "run_one", sequence: 1, stream: "stdout", message: "Loaded output" }] as never;

test("filter resets on reopening and changing runs while the dialog shell stays mounted", () => {
  const props = { run, logs, open: true, onOpenChange: vi.fn() };
  const view = render(<TerminalLogDialog {...props} />);
  const dialog = screen.getByRole("dialog");
  fireEvent.change(screen.getByRole("searchbox", { name: "Filter loaded output" }), { target: { value: "missing" } });
  expect(screen.getByText("No loaded output matches this filter.")).toBeTruthy();
  view.rerender(<TerminalLogDialog {...props} run={{ id: "run_two", status: "running" } as never} />);
  expect(screen.getByRole("dialog")).toBe(dialog);
  expect((screen.getByRole("searchbox", { name: "Filter loaded output" }) as HTMLInputElement).value).toBe("");
  expect(screen.getByText("Loaded output")).toBeTruthy();
  fireEvent.change(screen.getByRole("searchbox", { name: "Filter loaded output" }), { target: { value: "missing" } });
  view.rerender(<TerminalLogDialog {...props} open={false} />);
  view.rerender(<TerminalLogDialog {...props} />);
  expect((screen.getByRole("searchbox", { name: "Filter loaded output" }) as HTMLInputElement).value).toBe("");
});
