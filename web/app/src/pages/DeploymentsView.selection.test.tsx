import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { DeploymentDetailView, DeploymentsView } from "./DeploymentsView";

vi.mock("@tanstack/react-router", () => ({ Link: ({ children }: { children: React.ReactNode }) => <a>{children}</a> }));
afterEach(cleanup);
const props = {
  deployments: [],
  projects: [{ id: "project_a", name: "First" }, { id: "project_b", name: "Second" }] as never,
  services: [{ id: "service_a", project_id: "project_a", name: "Service A" }, { id: "service_b", project_id: "project_b", name: "Service B" }] as never,
  environments: [{ id: "env_a", service_id: "service_a", name: "Environment A" }, { id: "env_b", service_id: "service_b", name: "Environment B" }] as never,
  revisions: [{ id: "revision_a", service_id: "service_a", requested_ref: "main", git_commit: "abc" }] as never,
  canOperate: true, onCreate: vi.fn(), onServiceChange: vi.fn(), onEnvironmentChange: vi.fn(),
};
const value = (name: string) => (screen.getByRole("combobox", { name }) as HTMLSelectElement).value;

test("deep links restore project, service and environment from the URL", () => {
  render(<DeploymentsView {...props} selectedServiceID="service_a" selectedEnvironmentID="env_a" />);
  expect(value("Project")).toBe("project_a");
  expect(value("Service")).toBe("service_a");
  expect(value("Environment")).toBe("env_a");
});

test("history changes restore selection and invalidate a revision from another service", () => {
  const view = render(<DeploymentsView {...props} selectedServiceID="service_a" selectedEnvironmentID="env_a" />);
  fireEvent.change(screen.getByRole("combobox", { name: "Immutable revision" }), { target: { value: "revision_a" } });
  view.rerender(<DeploymentsView {...props} selectedServiceID="service_b" selectedEnvironmentID="env_b" />);
  expect(value("Project")).toBe("project_b");
  expect(value("Service")).toBe("service_b");
  expect(value("Environment")).toBe("env_b");
  expect(value("Immutable revision")).toBe("");
  expect((screen.getByRole("button", { name: "Request deployment" }) as HTMLButtonElement).disabled).toBe(true);
  view.rerender(<DeploymentsView {...props} />);
  expect(value("Service")).toBe("");
  expect(value("Environment")).toBe("");
});

test("changing project clears service and environment route context", () => {
  const onServiceChange = vi.fn();
  render(<DeploymentsView {...props} selectedServiceID="service_a" selectedEnvironmentID="env_a" onServiceChange={onServiceChange} />);
  fireEvent.change(screen.getByRole("combobox", { name: "Project" }), { target: { value: "project_b" } });
  expect(onServiceChange).toHaveBeenCalledWith("");
});

test("retrying an unchanged deployment preserves its intent key while changing the revision renews it", () => {
  const onCreate = vi.fn();
  render(<DeploymentsView {...props} selectedServiceID="service_a" selectedEnvironmentID="env_a" onCreate={onCreate} />);
  const revision = screen.getByRole("combobox", { name: "Immutable revision" });
  fireEvent.change(revision, { target: { value: "revision_a" } });
  const form = screen.getByRole("form", { name: "Request deployment" });
  fireEvent.submit(form);
  fireEvent.submit(form);
  expect(onCreate.mock.calls[0]![0]).toEqual(onCreate.mock.calls[1]![0]);
  expect(onCreate.mock.calls[0]![0]).toMatchObject({ environment_id: "env_a", desired_revision_id: "revision_a" });
  fireEvent.change(revision, { target: { value: "" } });
  fireEvent.change(revision, { target: { value: "revision_a" } });
  fireEvent.submit(form);
  expect(onCreate.mock.calls[2]![0].idempotency_key).not.toBe(onCreate.mock.calls[0]![0].idempotency_key);
});

test("viewers can select deployment history without receiving write controls", () => {
  const onEnvironmentChange = vi.fn();
  render(<DeploymentsView {...props} canOperate={false} selectedServiceID="service_a" selectedEnvironmentID="env_a" onEnvironmentChange={onEnvironmentChange} />);
  expect(value("Project")).toBe("project_a");
  expect(value("Service")).toBe("service_a");
  expect(value("Environment")).toBe("env_a");
  fireEvent.change(screen.getByRole("combobox", { name: "Environment" }), { target: { value: "" } });
  expect(onEnvironmentChange).toHaveBeenCalledWith("");
  expect(screen.queryByRole("combobox", { name: "Immutable revision" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Request deployment" })).toBeNull();
  expect(screen.queryByRole("form")).toBeNull();
});


test("deployment revision choices identify missing commits", () => {
  render(<DeploymentsView {...props} revisions={[{ id: "revision_a", service_id: "service_a", requested_ref: "main", git_commit: "" }] as never} selectedServiceID="service_a" selectedEnvironmentID="env_a" />);
  expect(screen.getByRole("option", { name: "main (commit unavailable)" })).toBeTruthy();
});

test("deployment details identify missing provenance instead of rendering blanks", () => {
  render(<DeploymentDetailView deployment={{ id: "dep_empty", status: "queued", desired_revision_id: "revision_a", previous_healthy_revision_id: "", created_at: "2026-01-01T00:00:00Z" } as never} revision={{ id: "revision_a", requested_ref: "main", git_commit: "", compose_hash: "", content_identity: "" } as never} related={[]} logs={[]} canOperate={false} onConfirm={vi.fn()} onCancel={vi.fn()} />);
  expect(screen.getByText("main (commit unavailable)")).toBeTruthy();
  expect(screen.getAllByText("Not recorded")).toHaveLength(2);
  expect(screen.getByText("None")).toBeTruthy();
});


test("deployment recovery preserves the revision and blocks submission until ready", () => {
  const onCreate = vi.fn();
  const selected = { ...props, selectedServiceID: "service_a", selectedEnvironmentID: "env_a", onCreate };
  const view = render(<DeploymentsView {...selected} />);
  fireEvent.change(screen.getByRole("combobox", { name: "Immutable revision" }), { target: { value: "revision_a" } });
  view.rerender(<DeploymentsView {...selected} busy />);
  expect(value("Immutable revision")).toBe("revision_a");
  fireEvent.submit(screen.getByRole("form", { name: "Request deployment" }));
  expect(onCreate).not.toHaveBeenCalled();
  view.rerender(<DeploymentsView {...selected} busy={false} />);
  fireEvent.submit(screen.getByRole("form", { name: "Request deployment" }));
  expect(onCreate).toHaveBeenCalledWith(expect.objectContaining({ environment_id: "env_a", desired_revision_id: "revision_a" }));
});


test("pending deployment submission locks its target and restores the draft on failure", () => {
  const onCreate = vi.fn();
  const selected = { ...props, selectedServiceID: "service_a", selectedEnvironmentID: "env_a", onCreate };
  const view = render(<DeploymentsView {...selected} />);
  fireEvent.change(screen.getByRole("combobox", { name: "Immutable revision" }), { target: { value: "revision_a" } });
  view.rerender(<DeploymentsView {...selected} submitting />);
  for (const field of screen.getAllByRole("combobox")) expect((field as HTMLSelectElement).disabled).toBe(true);
  expect(screen.getByRole("form", { name: "Request deployment" }).getAttribute("aria-busy")).toBe("true");
  fireEvent.submit(screen.getByRole("form", { name: "Request deployment" }));
  expect(onCreate).not.toHaveBeenCalled();
  view.rerender(<DeploymentsView {...selected} />);
  expect(value("Immutable revision")).toBe("revision_a");
  for (const field of screen.getAllByRole("combobox")) expect((field as HTMLSelectElement).disabled).toBe(false);
});
