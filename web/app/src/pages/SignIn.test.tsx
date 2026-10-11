import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { SignIn } from "./SignIn";

afterEach(cleanup);

test("pending sign-in prevents repeated submissions and becomes usable after completion", async () => {
  let resolve!: () => void;
  const onSubmit = vi.fn(() => new Promise<void>((done) => { resolve = done; }));
  const { container, rerender } = render(<SignIn error="" onSubmit={onSubmit} />);
  fireEvent.change(screen.getByLabelText("Email"), { target: { value: "operator@example.test" } });
  fireEvent.change(screen.getByLabelText("Password"), { target: { value: "test-password" } });
  const form = container.querySelector("form")!;
  fireEvent.submit(form);
  fireEvent.submit(form);
  expect(onSubmit).toHaveBeenCalledTimes(1);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Signing in..." }).disabled).toBe(true);
  expect(screen.getByLabelText<HTMLInputElement>("Email").readOnly).toBe(true);
  await act(async () => resolve());
  rerender(<SignIn error="Temporary sign-in failure" onSubmit={onSubmit} />);
  expect(screen.getByRole("alert").textContent).toBe("Temporary sign-in failure");
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Sign in" }).disabled).toBe(false);
  expect(screen.getByLabelText<HTMLInputElement>("Email").value).toBe("operator@example.test");
  expect(screen.getByLabelText<HTMLInputElement>("Password").readOnly).toBe(false);
  fireEvent.submit(form);
  expect(onSubmit).toHaveBeenCalledTimes(2);
  await act(async () => resolve());
});

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
