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
test("image hosting preset is explicit and includes versioned release ports without modifying custom grants silently", async () => {
  const onEnroll = vi.fn().mockResolvedValue(created()); render(<RunnersView runners={[]} canAdmin onEnroll={onEnroll} />);
  expect(screen.getByLabelText<HTMLInputElement>("Runner capabilities").value).toBe("compose-deploy");
  fireEvent.change(screen.getByLabelText("Runner capabilities"), { target: { value: "shell,custom" } });
  expect(screen.getByLabelText<HTMLInputElement>("Runner capabilities").value).toBe("shell,custom");
  fireEvent.click(screen.getByRole("button", { name: "Use image hosting capability preset" })); submit();
  await screen.findByRole("dialog");
  expect(onEnroll).toHaveBeenCalledWith(expect.objectContaining({ capabilities: ["compose-deploy", "image_deploy", "image_release_ports_v1"] }));
});

test("runtime profile submits its grants but returned metadata alone determines the command", async () => {
  const response = created();
  response.enrollment.capabilities = ["compose-deploy", "image_deploy", "image_release_ports_v1", "hosting_runtime_env_v1", "hosting_observation_v1"];
  const onEnroll = vi.fn().mockResolvedValue(response);
  render(<RunnersView runners={[]} canAdmin onEnroll={onEnroll} />);
  // The submitted draft differs from the persisted response.
  submit();
  const dialog = await screen.findByRole("dialog");
  fireEvent.change(within(dialog).getByLabelText("Runner-reachable server URL"), { target: { value: "https://control.example.test" } });
  expect(onEnroll).toHaveBeenCalledWith(expect.objectContaining({ capabilities: ["compose-deploy"] }));
  expect(within(dialog).getByLabelText("Manual runner command").textContent).toContain("--hosting-runtime-env --hosting-observation --execute");
  expect(dialog.textContent).toContain("Persisted capabilities: compose-deploy, image_deploy");
});
test("runtime profile replaces the draft explicitly without adding flags to a basic returned enrollment", async () => {
  const onEnroll = vi.fn().mockResolvedValue(created());
  render(<RunnersView runners={[]} canAdmin onEnroll={onEnroll} />);
  fireEvent.click(screen.getByRole("button", { name: "Use image hosting with runtime configuration, observations and logs" })); submit();
  const dialog = await screen.findByRole("dialog");
  expect(onEnroll).toHaveBeenCalledWith(expect.objectContaining({ capabilities: ["compose-deploy", "image_deploy", "image_release_ports_v1", "hosting_runtime_env_v1", "hosting_observation_v1", "hosting_runtime_logs_v1"] }));
  expect(within(dialog).getByLabelText("Manual runner command").textContent).not.toContain("--hosting-observation");
});
test("advanced enrollment still discloses its token once but withholds an incomplete command", async () => {
  const response = created(); response.enrollment.capabilities = ["compose-deploy", "hosting_build_v1", "proxy_apply", "local_build_deploy_v1"];
  render(<RunnersView runners={[]} canAdmin onEnroll={vi.fn().mockResolvedValue(response)} />); submit();
  const dialog = await screen.findByRole("dialog");
  expect(within(dialog).queryByLabelText("Manual runner command")).toBeNull();
  expect(within(dialog).getByRole("alert").textContent).toContain("--build-config-file");
  expect(within(dialog).getByRole("alert").textContent).toContain("--proxy-state-root");
  expect(within(dialog).getByLabelText("Enrollment secret").textContent).toBe(response.token);
  expect(within(dialog).getByRole("button", { name: "Download once" })).toBeTruthy();
  fireEvent.keyDown(dialog, { key: "Escape" }); await waitFor(() => expect(screen.queryByLabelText("Enrollment secret")).toBeNull());
});

test("returned runtime log grants enable the collector independently of the submitted draft", async () => {
  const response = created(); response.enrollment.capabilities = ["hosting_runtime_logs_v1"];
  render(<RunnersView runners={[]} canAdmin onEnroll={vi.fn().mockResolvedValue(response)} />); submit();
  const dialog = await screen.findByRole("dialog");
  fireEvent.change(within(dialog).getByLabelText("Runner-reachable server URL"), { target: { value: "http://127.0.0.1:18182" } });
  expect(within(dialog).queryByLabelText("Manual runner command")).toBeNull();
  expect(within(dialog).getByRole("alert").textContent).toContain("HTTPS");
  fireEvent.change(within(dialog).getByLabelText("Runner-reachable server URL"), { target: { value: "https://control.example.test" } });
  const command = within(dialog).getByLabelText("Manual runner command").textContent!;
  expect(command).toContain("--capabilities 'hosting_runtime_logs_v1'");
  expect(command).toContain("--hosting-runtime-logs --execute");
  expect(command).not.toContain("--hosting-observation");
  expect(command).not.toContain(response.token);
});
