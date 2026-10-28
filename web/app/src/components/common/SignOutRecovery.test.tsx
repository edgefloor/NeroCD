import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { SignOutRecovery } from "./SignOutRecovery";

afterEach(cleanup);

test("sign-out recovery explains the unconfirmed session and offers a retry", () => {
  const retry = vi.fn().mockResolvedValue(undefined);
  const view = render(<SignOutRecovery pending={false} onRetry={retry} />);
  expect(screen.getByRole("heading").textContent).toBe("Sign-out not confirmed");
  expect(screen.getByRole("alert").textContent).toContain("session may still be active");
  fireEvent.click(screen.getByRole("button", { name: "Retry sign-out" }));
  expect(retry).toHaveBeenCalledTimes(1);
  view.rerender(<SignOutRecovery pending onRetry={retry} />);
  expect(screen.getByRole("status").textContent).toContain("Waiting for the server");
  expect(screen.queryByRole("button")).toBeNull();
});
