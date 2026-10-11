import type { ReactNode } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { LogsView } from "./LogsView";

vi.mock("@tanstack/react-router", () => ({ Link: ({ children }: { children: ReactNode }) => <a>{children}</a> }));
afterEach(cleanup);
const logs = [{ id: "log_one", run_id: "run_one", sequence: 1, stream: "stdout", message: "Artifact uploaded", created_at: "2026-01-01T00:00:00Z" }];

test("filter misses distinguish hidden entries from an empty page", () => {
  const view = render(<LogsView logs={logs} q="missing" />);
  expect(screen.getByRole("status").textContent).toBe("0 of 1 loaded log entries match this filter.");
  expect(screen.getByText("No logs match this filter on the loaded page. Clear the filter or try another page.")).toBeTruthy();
  view.rerender(<LogsView logs={logs} q="UPLOADED" />);
  expect(screen.getByRole("status").textContent).toBe("1 of 1 loaded log entries match this filter.");
  expect(screen.getByText("Artifact uploaded")).toBeTruthy();
  view.rerender(<LogsView logs={[]} />);
  expect(screen.getByText("No log output on this page.")).toBeTruthy();
  expect(screen.queryByRole("status")).toBeNull();
});

test("pending logs do not announce an empty filtered result", () => {
  render(<LogsView logs={[]} q="missing" loading />);
  expect(screen.queryByText(/No logs match/)).toBeNull();
  expect(screen.queryByText(/loaded log entries/)).toBeNull();
});
