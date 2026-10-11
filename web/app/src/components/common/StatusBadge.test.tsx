import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { StatusBadge, statusTone } from "./StatusBadge";

afterEach(cleanup);

test.each([
  ["queued", "pending"], ["waiting_confirmation", "pending"],
  ["assigned", "pending"], ["preparing", "pending"], ["applying", "pending"],
  ["verifying", "pending"], ["cancel_requested", "pending"], ["rolling_back", "pending"],
  ["succeeded", "good"], ["rolled_back", "good"],
  ["failed", "danger"], ["canceled", "danger"], ["rollback_failed", "danger"],
  ["manual_intervention", "danger"], ["unknown_future_state", "neutral"],
] as const)("deployment badge %s communicates %s severity", (status, tone) => {
  expect(statusTone(status)).toBe(tone);
});

test.each([
  ["manual_intervention", "manual intervention", "destructive"],
  ["rolling_back", "rolling back", "warning"],
  ["rolled_back", "rolled back", "success"],
] as const)("%s retains a readable label alongside its semantic treatment", (status, label, variant) => {
  render(<StatusBadge status={status} />);
  const badge = screen.getByText(label);
  expect(badge.getAttribute("data-variant")).toBe(variant);
  expect(badge.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
});
