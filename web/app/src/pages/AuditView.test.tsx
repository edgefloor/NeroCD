import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { AuditView } from "./AuditView";

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
