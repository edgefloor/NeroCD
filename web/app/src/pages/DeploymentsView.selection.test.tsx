import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { DeploymentsView } from "./DeploymentsView";

vi.mock("@tanstack/react-router", () => ({ Link: ({ children }: { children: React.ReactNode }) => <a>{children}</a> }));
afterEach(cleanup);
const props = {
  deployments: [],
  projects: [{ id: "project_a", name: "First" }, { id: "project_b", name: "Second" }] as never,
  services: [{ id: "service_a", project_id: "project_a", name: "Service A" }, { id: "service_b", project_id: "project_b", name: "Service B" }] as never,
  environments: [{ id: "env_a", service_id: "service_a", name: "Environment A" }, { id: "env_b", service_id: "service_b", name: "Environment B" }] as never,
  revisions: [{ id: "revision_a", service_id: "service_a", requested_ref: "main", git_commit: "abc" }] as never,
  canOperate: true, onCreate: vi.fn(), onCancel: vi.fn(), onServiceChange: vi.fn(), onEnvironmentChange: vi.fn(),
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
