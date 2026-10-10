import { fireEvent, render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { PageNavigation } from "./PageNavigation";

test("an empty page keeps backward navigation available after totals shrink", () => {
  const previous = vi.fn();
  render(<PageNavigation page={{ count: 0, limit: 50, offset: 100, total: 80 }} label="projects" onPrevious={previous} onNext={vi.fn()} />);
  expect(screen.getByText("Showing 0-0 of 80")).toBeTruthy();
  const previousButton = screen.getByRole<HTMLButtonElement>("button", { name: "Previous projects" });
  expect(previousButton.disabled).toBe(false);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Next projects" }).disabled).toBe(true);
  fireEvent.click(previousButton);
  expect(previous).toHaveBeenCalledOnce();
});
