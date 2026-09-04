import { fireEvent, render, screen } from "@testing-library/react";
import { useEffect, useState } from "react";
import { expect, test, vi } from "vitest";
import { apiSnapshot } from "@/api/compat";
import { RunsView } from "./RunsView";

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
      <RunsView snapshot={apiSnapshot({ runs, projects: [{ id: "proj_logs", name: "Logs project" } as never], templates: [{ id: "tmpl_logs", name: "Logs template" } as never] })} token="" busy="" mutate={vi.fn() as never} selectedRunID={selectedRunID} selectedLogs={[]} onSelectRun={setSelectedRunID} onCloseLogs={() => setSelectedRunID("")} />
    </>;
  }
  render(<Harness />);
  fireEvent.click(screen.getAllByRole("button", { name: "Logs" })[0]!);
  expect(screen.getByRole("dialog", { name: "run run_logs" })).toBeTruthy();
});

test("selected run keeps the dialog clear when fetching output fails", () => {
  renderRuns("run_logs", [], false, new Error("network unavailable"));
  expect(screen.getByRole("alert").textContent).toContain("Unable to load run output: network unavailable");
});
