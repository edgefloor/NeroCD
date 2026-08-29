import { render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { SignIn } from "./SignIn";

test("pre-bootstrap sign-in exposes only CLI bootstrap guidance", () => {
  render(<SignIn error="" bootstrapRequired onSubmit={vi.fn()} />);

  expect(screen.getByRole("status", { name: "Administrator bootstrap required" })).toBeTruthy();
  expect(screen.getByText("Bootstrap is intentionally CLI-only.")).toBeTruthy();
  expect(screen.queryByLabelText("Email")).toBeNull();
  expect(screen.queryByLabelText("Password")).toBeNull();
  expect(screen.queryByRole("button", { name: "Sign in" })).toBeNull();
});
