import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { AuditView } from "./AuditView";

vi.mock("@tanstack/react-router", () => ({ Link: ({ children, params }: { children: ReactNode; params: { runId?: string; deploymentId?: string } }) => <a href={params.deploymentId ? `/deployments/${params.deploymentId}` : `/runs/${params.runId}`}>{children}</a> }));

afterEach(cleanup);
const event = { id: "audit_one", actor_id: "user_operator", target_id: "run_one", action: "run.requested", metadata: { reason: "Release requested", nested: { value: "Complete evidence" } }, created_at: "2026-10-11T12:34:56Z" };

test("audit entries expose actor, exact time, event identity, and complete metadata", () => {
  const { container } = render(<AuditView events={[event]} />);
  expect(screen.getByText(event.actor_id)).toBeTruthy();
  expect(container.querySelector("time")?.dateTime).toBe(event.created_at);
  expect(screen.getByText(event.id)).toBeTruthy();
  expect(container.querySelector("pre")?.textContent).toBe(JSON.stringify(event.metadata, null, 2));
});

test("audit filtering includes actor and distinguishes a miss from an empty page", () => {
  const view = render(<AuditView events={[event]} q="USER_OPERATOR" />);
  expect(screen.getByText(event.action)).toBeTruthy();
  expect(screen.getByRole("status").textContent).toBe("1 of 1 loaded audit events match this filter.");
  view.rerender(<AuditView events={[event]} q="missing" />);
  expect(screen.getByText("No audit events match this filter on the loaded page.")).toBeTruthy();
  view.rerender(<AuditView events={[]} />);
  expect(screen.getByText("No audit events on this page.")).toBeTruthy();
});


test.each(["run.request", "run.approve", "run.reject", "run.cancel"])("%s links to its recorded run target", (action) => {
  render(<AuditView events={[{ ...event, action }]} />);
  expect(screen.getByRole("link", { name: event.target_id }).getAttribute("href")).toBe(`/runs/${event.target_id}`);
});

test.each(["project.create", "run_log_retention.execute", "run.unknown", "deployment.unknown", "runner.deployment.unknown"])("%s does not guess a target route", (action) => {
  render(<AuditView events={[{ ...event, action }]} />);
  expect(screen.queryByRole("link")).toBeNull();
  expect(screen.getByText(event.target_id)).toBeTruthy();
});


test.each(["deployment.create", "deployment.confirm", "deployment.cancel", "deployment.preassignment_fail", "runner.deployment.failed", "runner.deployment.cancellation_rollback", "runner.deployment.rollback_queued", "runner.deployment.transition", "runner.deployment.provenance.resolve"])("%s links to its recorded deployment target", (action) => {
  render(<AuditView events={[{ ...event, action, target_id: "deployment_one" }]} />);
  expect(screen.getByRole("link", { name: "deployment_one" }).getAttribute("href")).toBe("/deployments/deployment_one");
});
