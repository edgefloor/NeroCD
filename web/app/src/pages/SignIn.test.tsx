import { render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { SignIn } from "./SignIn";

test("pre-bootstrap sign-in exposes only CLI bootstrap guidance", () => {
  render(<SignIn error="" bootstrapRequired oidcEnabled onSubmit={vi.fn()} />);

  expect(screen.getByRole("status", { name: "Administrator bootstrap required" })).toBeTruthy();
  expect(screen.getByText("Bootstrap is intentionally CLI-only.")).toBeTruthy();
  expect(screen.queryByLabelText("Email")).toBeNull();
  expect(screen.queryByLabelText("Password")).toBeNull();
  expect(screen.queryByRole("button", { name: "Sign in" })).toBeNull();
  expect(screen.queryByRole("link", { name: "Continue with SSO" })).toBeNull();
});

test("configured SSO is offered alongside local recovery", () => {
  render(<SignIn error="" oidcEnabled oidcHref="/api/v1/oidc/login?redirect=%2Fruns" onSubmit={vi.fn()} />);

  expect(screen.getByRole("link", { name: "Continue with SSO" }).getAttribute("href")).toBe("/api/v1/oidc/login?redirect=%2Fruns");
  expect(screen.getByText("or use local recovery")).toBeTruthy();
  expect(screen.getByLabelText("Email")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Sign in" })).toBeTruthy();
});

test("provider failure is generic and local recovery remains usable", () => {
  render(<SignIn error="" oidcEnabled oidcError onSubmit={vi.fn()} />);

  expect(screen.getByRole("alert").textContent).toBe("Single sign-on failed. Try again or use local sign-in.");
  expect(screen.getByLabelText("Password")).toBeTruthy();
});
