import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { SessionCheck } from "./SessionCheck";

afterEach(cleanup);
test("session checking becomes actionable recovery only after failure", () => {
  const view = render(<SessionCheck failed={false} />);
  expect(screen.getByRole("status").textContent).toBe("Checking session...");
  expect(screen.queryByRole("button")).toBeNull();
  view.rerender(<SessionCheck failed />);
  expect(screen.getByRole("heading").textContent).toBe("Session unavailable");
  expect(screen.getByRole("alert").textContent).toBe("Unable to check your session. Reload this page to try again.");
  expect(screen.getByRole("button", { name: "Reload page" })).toBeTruthy();
});
