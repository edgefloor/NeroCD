import { Profiler, StrictMode } from "react";
import { DeploymentsView } from "./DeploymentsView";
import { afterEach, expect, test, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { DeploymentDetailView } from "./DeploymentDetailView";
import { deploymentStateMessage, newDeploymentIntentKey } from "@/lib/deploymentPresentation";

test("deployment lifecycle has explicit safe operator messages", () => {
  const fixture = (status: string, failure_code?: string) => ({ status, failure_code }) as never;
  expect(deploymentStateMessage(fixture("waiting_confirmation"))).toMatch(/confirmation/i);
  expect(deploymentStateMessage(fixture("applying"))).toMatch(/applying/i);
  expect(deploymentStateMessage(fixture("cancel_requested"))).toMatch(/cancellation/i);
  expect(deploymentStateMessage(fixture("rolled_back"))).toMatch(/rollback/i);
  expect(deploymentStateMessage(fixture("rollback_failed"))).toMatch(/intervention/i);
  expect(deploymentStateMessage(fixture("manual_intervention"))).toMatch(/intervention/i);
  expect(deploymentStateMessage(fixture("failed", "missing_secret"))).toMatch(/secret/i);
  expect(deploymentStateMessage(fixture("failed", "policy_denied"))).toMatch(/policy/i);
  expect(deploymentStateMessage(fixture("failed", "runner_expired"))).toMatch(/runner/i);
});

test("operator intent keys are fresh, opaque browser-generated values", () => {
  expect(newDeploymentIntentKey()).not.toBe(newDeploymentIntentKey());
});


afterEach(cleanup);

test.each(["confirm", "cancel"] as const)("deployment %s feedback remains beside disabled actions while pending", (action) => {
  const onConfirm = vi.fn();
  const onCancel = vi.fn();
  const props = {
    deployment: { id: "deployment_1", status: "waiting_confirmation", created_at: "2026-10-11T00:00:00Z" } as never,
    related: [], logs: [], canOperate: true, onConfirm, onCancel,
  };
  const view = render(<DeploymentDetailView {...props} pendingAction={action} actionError="Earlier failure" />);
  const pending = screen.getByRole("button", { name: action === "confirm" ? "Confirming…" : "Requesting cancellation…" });
  expect((pending as HTMLButtonElement).disabled).toBe(true);
  for (const button of screen.getAllByRole("button")) expect((button as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(pending);
  expect(onConfirm).not.toHaveBeenCalled();
  expect(onCancel).not.toHaveBeenCalled();
  expect(screen.queryByRole("alert")).toBeNull();
  view.rerender(<DeploymentDetailView {...props} actionError="Request failed. Try again." />);
  expect(screen.getByRole("alert").textContent).toBe("Request failed. Try again.");
  fireEvent.click(screen.getByRole("button", { name: action === "confirm" ? "Confirm" : "Cancel" }));
  expect(action === "confirm" ? onConfirm : onCancel).toHaveBeenCalledOnce();
  view.rerender(<DeploymentDetailView {...props} />);
  expect(screen.queryByRole("alert")).toBeNull();
});


test.each([
  ["queued", true], ["waiting_confirmation", true], ["assigned", true],
  ["preparing", true], ["applying", true], ["verifying", true],
  ["cancel_requested", false], ["rolling_back", false], ["rolled_back", false],
  ["rollback_failed", false], ["manual_intervention", false], ["succeeded", false],
  ["failed", false], ["canceled", false],
] as const)("deployment state %s exposes cancellation only when supported", (status, allowed) => {
  render(<DeploymentDetailView deployment={{ id: "deployment_1", status } as never} related={[]} logs={[]} canOperate onConfirm={vi.fn()} onCancel={vi.fn()} />);
  expect(Boolean(screen.queryByRole("button", { name: "Cancel" }))).toBe(allowed);
});

test("rollback deployments do not offer cancellation even while queued", () => {
  render(<DeploymentDetailView deployment={{ id: "rollback_1", status: "queued", rollback_of_id: "deployment_1" } as never} related={[]} logs={[]} canOperate onConfirm={vi.fn()} onCancel={vi.fn()} />);
  expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
});


test.each(["queued", "waiting_confirmation", "assigned", "preparing", "applying", "verifying", "cancel_requested", "rolling_back", "rolled_back", "rollback_failed", "manual_intervention", "succeeded", "canceled"])("%s state takes precedence over historical failure guidance", (status) => {
  const expected = deploymentStateMessage({ status } as never);
  for (const failure_code of ["missing_secret", "policy_denied", "runner_expired"]) {
    expect(deploymentStateMessage({ status, failure_code } as never)).toBe(expected);
  }
});

test.each(["missing_secret", "policy_denied", "runner_expired"])("failed deployment with %s does not promise automatic recovery", (failure_code) => {
  const message = deploymentStateMessage({ status: "failed", failure_code } as never);
  expect(message).toMatch(/^Deployment failed/);
  expect(message).not.toMatch(/waiting|automatically/i);
  expect(message).toContain("before retrying");
});


test("cancellation intent stays stable for retries and changes with deployment identity", () => {
  const onCancel = vi.fn();
  const props = { deployment: { id: "deployment_first", status: "queued" } as never, related: [], logs: [], canOperate: true, onConfirm: vi.fn(), onCancel };
  const view = render(<DeploymentDetailView {...props} />);
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  const first = onCancel.mock.calls[0]![0];
  expect(first.deployment_id).toBe("deployment_first");
  expect(first.request_id).toEqual(expect.any(String));
  view.rerender(<DeploymentDetailView {...props} actionError="Retry cancellation" />);
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(onCancel.mock.calls[1]![0]).toEqual(first);
  view.rerender(<DeploymentDetailView {...props} deployment={{ id: "deployment_second", status: "queued" } as never} />);
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  const second = onCancel.mock.calls[2]![0];
  expect(second.deployment_id).toBe("deployment_second");
  expect(second.request_id).not.toBe(first.request_id);
});


test("StrictMode retires missing revisions before commit without resurrecting them or submitting", () => {
  const props = { deployments: [], projects: [{ id: "project", name: "Project" }] as never,
    services: [{ id: "service", project_id: "project", name: "Service" }] as never,
    environments: [{ id: "environment", service_id: "service", name: "Production" }] as never,
    selectedServiceID: "service", selectedEnvironmentID: "environment", canOperate: true,
    onCreate: vi.fn(), onServiceChange: vi.fn(), onEnvironmentChange: vi.fn() };
  const revisions = [{ id: "revision", service_id: "service", requested_ref: "main", git_commit: "abc" }] as never;
  const enabledCommits: boolean[] = [];
  const observe = () => { const button = document.querySelector<HTMLButtonElement>('form[aria-label="Request deployment"] button[type="submit"]'); if (button) enabledCommits.push(!button.disabled); };
  const ui = (choices: typeof revisions) => <StrictMode><Profiler id="deployment" onRender={observe}><DeploymentsView {...props} revisions={choices} /></Profiler></StrictMode>;
  const view = render(ui(revisions));
  fireEvent.change(screen.getByRole("combobox", { name: "Immutable revision" }), { target: { value: "revision" } });
  enabledCommits.length = 0;
  view.rerender(ui([] as never));
  expect(enabledCommits.length).toBeGreaterThan(0);
  expect(enabledCommits.every((enabled) => !enabled)).toBe(true);
  view.rerender(ui(revisions)); view.rerender(ui(revisions));
  expect(screen.getByRole<HTMLSelectElement>("combobox", { name: "Immutable revision" }).value).toBe("");
  fireEvent.submit(screen.getByRole("form", { name: "Request deployment" }));
  expect(props.onCreate).not.toHaveBeenCalled();
});
