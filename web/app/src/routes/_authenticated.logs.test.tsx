import type { ComponentType, ReactNode } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { Route } from "./_authenticated.logs";

vi.mock("@tanstack/react-router", () => ({
  createFileRoute: () => (options: { component: ComponentType }) => ({ options, useSearch: () => ({}) }),
  useSearch: () => ({}),
  useNavigate: () => vi.fn(),
  Link: ({ children }: { children: ReactNode }) => <a>{children}</a>,
}));
vi.mock("@tanstack/react-query", async (original) => ({
  ...await original<typeof import("@tanstack/react-query")>(), useQuery: vi.fn(), useQueries: vi.fn(),
}));
afterEach(() => { cleanup(); vi.clearAllMocks(); });
const LogsRoute = Route.options.component as ComponentType;
const data = { items: [{ id: "log_one", run_id: "run_one", sequence: 1, stream: "stdout", message: "Preserved output" }], count: 1, total: 1, limit: 50, offset: 0 };

test("a failed log refresh keeps cached output and retries only logs", () => {
  const refetch = vi.fn();
  vi.mocked(useQuery).mockReturnValue({ data, isError: true, isFetching: false, isPending: false, refetch } as never);
  vi.mocked(useQueries).mockReturnValue([]);
  render(<LogsRoute />);
  expect(screen.getByRole("alert").textContent).toBe("Unable to refresh logs. Showing previously loaded output.");
  expect(screen.getByText("Preserved output")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Retry logs" }));
  expect(refetch).toHaveBeenCalledOnce();
});

test("pending status metadata does not block log output", () => {
  vi.mocked(useQuery).mockReturnValue({ data, isError: false, isFetching: false, isPending: false } as never);
  vi.mocked(useQueries).mockReturnValue([{ isPending: true, isError: false }] as never);
  render(<LogsRoute />);
  expect(screen.getByText("Preserved output")).toBeTruthy();
});

test("status recovery retries failed lookups and omits stale statuses", () => {
  const failed = vi.fn();
  const healthy = vi.fn();
  vi.mocked(useQuery).mockReturnValue({ data, isError: false, isPending: false } as never);
  vi.mocked(useQueries).mockReturnValue([
    { data: { id: "run_one", status: "running" }, isError: true, isFetching: false, refetch: failed },
    { data: { id: "run_other", status: "queued" }, isError: false, refetch: healthy },
  ] as never);
  render(<LogsRoute />);
  expect(screen.getByText("Preserved output")).toBeTruthy();
  expect(screen.queryByText(/running/)).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Retry run statuses" }));
  expect(failed).toHaveBeenCalledOnce();
  expect(healthy).not.toHaveBeenCalled();
});
