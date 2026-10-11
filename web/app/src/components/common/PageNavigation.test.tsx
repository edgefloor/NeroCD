import { cleanup, fireEvent, render, renderHook, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { PageNavigation, usePage } from "./PageNavigation";

const navigate = vi.hoisted(() => vi.fn());
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate, useSearch: () => ({ offset: 10000 }) }));
afterEach(() => { cleanup(); vi.clearAllMocks(); });

test("an empty page keeps backward navigation available after totals shrink", () => {
  const previous = vi.fn();
  render(<PageNavigation page={{ count: 0, limit: 50, offset: 100, total: 80 }} label="projects" onFirst={vi.fn()} onPrevious={previous} onNext={vi.fn()} />);
  expect(screen.getByText("Showing 0-0 of 80")).toBeTruthy();
  const previousButton = screen.getByRole<HTMLButtonElement>("button", { name: "Previous projects" });
  expect(previousButton.disabled).toBe(false);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Next projects" }).disabled).toBe(true);
  fireEvent.click(previousButton);
  expect(previous).toHaveBeenCalledOnce();
});


test("first-page navigation removes only the offset", () => {
  const { result } = renderHook(() => usePage());
  result.current.first();
  const navigation = navigate.mock.calls[0]![0];
  expect(navigation.to).toBe(".");
  expect(navigation.search({ offset: 10000, q: "release", service_id: "service_one", environment_id: "env_one" })).toEqual({ offset: undefined, q: "release", service_id: "service_one", environment_id: "env_one" });
});

test("first-page recovery is available on empty distant pages and disabled during loading", () => {
  const first = vi.fn();
  const props = { page: { count: 0, limit: 50, offset: 10000, total: 3 }, label: "logs", onFirst: first, onPrevious: vi.fn(), onNext: vi.fn() };
  const view = render(<PageNavigation {...props} />);
  fireEvent.click(screen.getByRole("button", { name: "First logs page" }));
  expect(first).toHaveBeenCalledOnce();
  view.rerender(<PageNavigation {...props} loading />);
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "First logs page" }).disabled).toBe(true);
  view.rerender(<PageNavigation {...props} page={{ ...props.page, offset: 0 }} />);
  expect(screen.queryByRole("button", { name: "First logs page" })).toBeNull();
});
