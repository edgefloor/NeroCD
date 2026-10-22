import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { RunnersView } from "./RunnersView";
import type { CreatedRunnerEnrollment } from "@/api";
afterEach(() => { cleanup(); vi.useRealTimers(); vi.unstubAllGlobals(); });
const created = (expires = Date.now() + 600_000): CreatedRunnerEnrollment => ({ enrollment: { id: "enrollment", runner_id: "runner_manual", runner_name: "Manual", tags: [], capabilities: ["compose-deploy"], created_by: "admin", created_at: new Date().toISOString(), expires_at: new Date(expires).toISOString() }, token: "test-only-bootstrap" });
function submit() { fireEvent.change(screen.getByLabelText("Runner ID"), { target: { value: "runner_manual" } }); fireEvent.change(screen.getByLabelText("Runner name"), { target: { value: "Manual" } }); fireEvent.submit(screen.getByRole("form", { name: "Create runner enrollment" })); }
test("manual onboarding command uses exact runner identity, explicit execution, and paths without inline secrets", async () => {
  const onEnroll = vi.fn().mockResolvedValue(created()); render(<RunnersView runners={[]} canAdmin onEnroll={onEnroll} />); submit();
  const dialog = await screen.findByRole("dialog");
  fireEvent.change(within(dialog).getByLabelText("Runner-reachable server URL"), { target: { value: "https://control.example.test" } });
  const command = within(dialog).getByLabelText("Manual runner command").textContent!;
  expect(command).toContain("--server 'https://control.example.test'"); expect(command).toContain("--execute"); expect(command).toContain("--enrollment-file /secure/nerocd-runner-enrollment.token"); expect(command).toContain("--credential-file /secure/nerocd-runner.credential");
  expect(command).not.toContain("test-only-bootstrap"); expect(command).not.toContain("--token");
  expect(dialog.textContent).toContain("Runner ID: runner_manual");
  expect(onEnroll).toHaveBeenCalledWith(expect.objectContaining({ capabilities: ["compose-deploy"] }));
  fireEvent.keyDown(dialog, { key: "Escape" }); await waitFor(() => expect(screen.queryByText("test-only-bootstrap")).toBeNull());
});
test("expiry clears the secret and removes copy/download controls", async () => {
  vi.useFakeTimers(); const onEnroll = vi.fn().mockResolvedValue(created(Date.now() + 1000)); render(<RunnersView runners={[]} canAdmin onEnroll={onEnroll} />);
  await act(async () => submit()); expect(screen.getByLabelText("Enrollment secret").textContent).toBe("test-only-bootstrap");
  await act(async () => vi.advanceTimersByTime(1001));
  expect(screen.queryByLabelText("Enrollment secret")).toBeNull(); expect(screen.queryByRole("button", { name: "Download once" })).toBeNull(); expect(screen.getByRole("alert").textContent).toContain("Enrollment expired");
});
test("copy rejection is reported honestly and no secret goes to browser storage", async () => {
  vi.stubGlobal("navigator", { clipboard: { writeText: vi.fn().mockRejectedValue(new Error("denied")) } });
  render(<RunnersView runners={[]} canAdmin onEnroll={vi.fn().mockResolvedValue(created())} />); submit();
  const dialog = await screen.findByRole("dialog"); fireEvent.click(within(dialog).getByRole("button", { name: "Copy" }));
  await screen.findByText("Unable to copy. Download the secret instead, or try again.");
  expect(JSON.stringify(localStorage)).not.toContain("test-only-bootstrap"); expect(JSON.stringify(sessionStorage)).not.toContain("test-only-bootstrap");
});
test.each(["permission", "unmount"])("late enrollment replies cannot expose a secret after %s context ends", async (context) => {
  let resolve!: (value: CreatedRunnerEnrollment) => void;
  const onEnroll = vi.fn(() => new Promise<CreatedRunnerEnrollment>((done) => { resolve = done; }));
  const view = render(<RunnersView runners={[]} canAdmin onEnroll={onEnroll} />); submit();
  if (context === "permission") view.rerender(<RunnersView runners={[]} canAdmin={false} onEnroll={onEnroll} />); else view.unmount();
  await act(async () => resolve(created())); expect(screen.queryByText("test-only-bootstrap")).toBeNull();
});
