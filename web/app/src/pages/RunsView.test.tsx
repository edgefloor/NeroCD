import type { ComponentProps, ReactNode } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useEffect, useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { apiSnapshot } from "@/api/compat";
import { RunsView } from "./RunsView";
import { TerminalLogDialog } from "@/components/runs/TerminalLogDialog";

vi.mock("@tanstack/react-router", () => ({ Link: ({ children, params, search = {}, ...props }: { children: ReactNode; params: { runId: string }; search?: { q?: string; offset?: number } } & ComponentProps<"a">) => {
  const query = new URLSearchParams();
  if (search.q) query.set("q", search.q);
  if (search.offset) query.set("offset", String(search.offset));
  return <a {...props} href={`/runs/${params.runId}${query.size ? `?${query}` : ""}`}>{children}</a>;
} }));

afterEach(cleanup);

const run = {
  id: "run_logs",
  project_id: "proj_logs",
  template_id: "tmpl_logs",
  status: "running",
  started_at: "2026-09-04T12:00:00Z",
} as never;

function renderRuns(selectedRunID = "", selectedLogs: unknown[] = [], logsLoading = false, logsError?: Error) {
  const onSelectRun = vi.fn();
  const onCloseLogs = vi.fn();
  const view = render(
    <RunsView
      snapshot={apiSnapshot({ runs: [run], projects: [{ id: "proj_logs", name: "Logs project" } as never], templates: [{ id: "tmpl_logs", name: "Logs template" } as never] })}
      token=""
      busy=""
      mutate={vi.fn() as never}
      selectedRunID={selectedRunID}
      selectedLogs={selectedLogs as never}
      logsLoading={logsLoading}
      logsError={logsError}
      onSelectRun={onSelectRun}
      onCloseLogs={onCloseLogs}
    />,
  );
  return { ...view, onSelectRun, onCloseLogs };
}

test("runs list selects a run before mounting its terminal output", () => {
  const { onSelectRun } = renderRuns();
  fireEvent.click(screen.getAllByRole("button", { name: "Logs" })[0]!);
  expect(onSelectRun).toHaveBeenCalledWith("run_logs");
});

test("runs list exposes ordinary detail links for each run", () => {
  renderRuns();
  const details = screen.getAllByRole("link", { name: "Details" });
  expect(details.length).toBeGreaterThanOrEqual(2);
  expect(details.every((detail) => detail.getAttribute("href") === "/runs/run_logs")).toBe(true);
});

test("selected run exposes loading, output, errors, and close behavior", () => {
  const { rerender, onCloseLogs } = renderRuns("run_logs", [], true);
  expect(screen.getByRole("status").textContent).toContain("Loading run output");
  rerender(
    <RunsView
      snapshot={apiSnapshot({ runs: [run] })}
      token=""
      busy=""
      mutate={vi.fn() as never}
      selectedRunID="run_logs"
      selectedLogs={[{ id: "log_1", run_id: "run_logs", sequence: 1, stream: "system", message: "Run requested" } as never]}
      onSelectRun={vi.fn()}
      onCloseLogs={onCloseLogs}
    />,
  );
  expect(screen.getByText("Run requested")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Close" }));
  expect(onCloseLogs).toHaveBeenCalledOnce();
});

test("selected run remains identifiable when the refreshed list drops it", () => {
  function Harness() {
    const [runs, setRuns] = useState([run]);
    const [selectedRunID, setSelectedRunID] = useState("");
    useEffect(() => {
      if (selectedRunID) setRuns([]);
    }, [selectedRunID]);
    return <>
      <RunsView snapshot={apiSnapshot({ runs, projects: [{ id: "proj_logs", name: "Logs project" } as never], templates: [{ id: "tmpl_logs", name: "Logs template" } as never] })} token="" busy="" mutate={vi.fn() as never} selectedRunID={selectedRunID} selectedLogs={[]} logsFollowing={false} onSelectRun={setSelectedRunID} onCloseLogs={() => setSelectedRunID("")} />
    </>;
  }
  render(<Harness />);
  fireEvent.click(screen.getAllByRole("button", { name: "Logs" })[0]!);
  expect(screen.getByRole("dialog", { name: "run run_logs" })).toBeTruthy();
  expect(screen.queryByText("Following active run output.")).toBeNull();
});

test("selected run keeps the dialog clear when fetching output fails", () => {
  renderRuns("run_logs", [], false, new Error("network unavailable"));
  expect(screen.getByRole("alert").textContent).toContain("Unable to load run output: network unavailable");
});

test("a failed log refresh keeps previously loaded output readable", () => {
  renderRuns("run_logs", [{ id: "log_1", run_id: "run_logs", sequence: 1, stream: "stdout", message: "Completed build step" }], false, new Error("network unavailable"));
  expect(screen.getByRole("alert").textContent).toContain("network unavailable");
  expect(screen.getByText("Completed build step")).toBeTruthy();
});

test.each(["succeeded", "failed", "canceled"])("%s runs with no logs do not promise future output", (status) => {
  render(<TerminalLogDialog run={{ id: "run_empty", status } as never} logs={[]} open onOpenChange={vi.fn()} />);
  expect(screen.getByText("No run output was recorded.")).toBeTruthy();
  expect(screen.queryByText(/waiting for runner output/)).toBeNull();
});

test("active runs with no logs explain that output is pending", () => {
  render(<TerminalLogDialog run={{ id: "run_empty", status: "queued" } as never} logs={[]} open onOpenChange={vi.fn()} />);
  expect(screen.getByText(/waiting for runner output/)).toBeTruthy();
});


const filterLogs = [
  { id: "l1", run_id: "run_logs", sequence: 1, stream: "stdout", message: "Build ready" },
  { id: "l2", run_id: "run_logs", sequence: 2, stream: "stderr", message: "Deploy failure" },
] as never;

test.each(["FAILURE", "stderr", "#2"])("log filter matches text, stream, and sequence: %s", (query) => {
  render(<TerminalLogDialog run={run} logs={filterLogs} open onOpenChange={vi.fn()} />);
  fireEvent.change(screen.getByRole("searchbox", { name: "Filter loaded output" }), { target: { value: query } });
  expect(screen.getByText("Deploy failure")).toBeTruthy();
  expect(screen.queryByText("Build ready")).toBeNull();
  expect(screen.getByRole("status").textContent).toBe("1 of 2 loaded entries match.");
  fireEvent.click(screen.getByRole("button", { name: "Clear output filter" }));
  expect(screen.getByText("Build ready")).toBeTruthy();
  expect(document.activeElement).toBe(screen.getByRole("searchbox", { name: "Filter loaded output" }));
});

test("log filtering retains history access and resets when switching runs", () => {
  const view = render(<TerminalLogDialog run={run} logs={filterLogs} open hasPrevious onLoadPrevious={vi.fn()} onOpenChange={vi.fn()} />);
  fireEvent.change(screen.getByRole("searchbox", { name: "Filter loaded output" }), { target: { value: "absent" } });
  expect(screen.getByText("No loaded output matches this filter.")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Load earlier output" })).toBeTruthy();
  view.rerender(<TerminalLogDialog run={{ id: "another", status: "running" } as never} logs={filterLogs} open onOpenChange={vi.fn()} />);
  expect((screen.getByRole("searchbox", { name: "Filter loaded output" }) as HTMLInputElement).value).toBe("");
  expect(screen.getByText("Build ready")).toBeTruthy();
});
