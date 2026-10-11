import { useQuery } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComponentType, ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";

const router = vi.hoisted(() => ({
  params: { runnerId: "runner_1" },
  search: { q: "" },
}));

vi.mock("@tanstack/react-router", () => ({
  createFileRoute: (_path: string) => (options: { component: ComponentType }) => ({
    options,
    useParams: () => router.params,
    useSearch: () => router.search,
  }),
  useNavigate: () => vi.fn(),
  useSearch: () => router.search,
  Link: ({ children }: { children: ReactNode }) => <a href="#runner">{children}</a>,
}));

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...await importOriginal<typeof import("@tanstack/react-query")>(),
  useQuery: vi.fn(),
}));

vi.mock("@/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/api")>(),
  createRunnerEnrollment: vi.fn(),
  revokeRunnerToken: vi.fn(),
}));

import { createRunnerEnrollment, revokeRunnerToken } from "@/api";
import { Route as RunnerDetailRoute } from "./_authenticated.runners.$runnerId";
import { Route as RunnersRoute } from "./_authenticated.runners.index";

const runner = {
  id: "runner_1",
  name: "Edge runner",
  status: "active",
  registered_at: "2026-09-09T00:00:00Z",
  last_heartbeat_at: "2026-09-09T00:00:00Z",
  tags: [],
  capabilities: [],
};

function queryData(roles: string[]) {
  vi.mocked(useQuery).mockImplementation((options) => {
    const key = (options as { queryKey?: readonly string[] }).queryKey?.[0];
    const data = key === "runners" ? [runner] : key === "runner" ? runner : { roles };
    return { data, isError: false, isPending: false, refetch: vi.fn() } as never;
  });
}

function queryFailure() {
  vi.mocked(useQuery).mockReturnValue({ data: undefined, isError: true, isPending: false, refetch: vi.fn() } as never);
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  router.search = { q: "" };
  router.params = { runnerId: "runner_1" };
});

test("runner_admin can enroll and revoke runners", async () => {
  vi.mocked(createRunnerEnrollment).mockResolvedValue({
    enrollment: { runner_id: runner.id },
    token: "one-time-token",
  } as never);
  vi.mocked(revokeRunnerToken).mockResolvedValue(runner as never);
  const user = userEvent.setup();

  queryData(["runner_admin"]);
  const RunnersComponent = RunnersRoute.options.component!;
  const list = render(<RunnersComponent />);
  await user.type(screen.getByRole("textbox", { name: "Runner ID" }), "runner_edge_02");
  await user.type(screen.getByRole("textbox", { name: "Runner name" }), "Edge runner 2");
  await user.click(screen.getByRole("button", { name: "Create enrollment" }));
  await waitFor(() => expect(createRunnerEnrollment).toHaveBeenCalledWith({
    runner_id: "runner_edge_02",
    runner_name: "Edge runner 2",
    tags: ["compose-runtime"],
    capabilities: ["compose-deploy"],
    ttl_seconds: 600,
  }));
  list.unmount();

  queryData(["runner_admin"]);
  const RunnerDetailComponent = RunnerDetailRoute.options.component!;
  render(<RunnerDetailComponent />);
  await user.click(screen.getByRole("button", { name: "Revoke credential" }));
  const dialog = screen.getByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: "Revoke credential" }));
  await waitFor(() => expect(revokeRunnerToken).toHaveBeenCalledWith({ runner_id: runner.id }));
});

test("system_admin retains list and detail runner controls while a principal without admin roles cannot operate runners", () => {
  queryData(["system_admin"]);
  const RunnersComponent = RunnersRoute.options.component!;
  const list = render(<RunnersComponent />);
  expect(screen.getByRole("button", { name: "Create enrollment" })).toBeTruthy();
  list.unmount();

  queryData(["system_admin"]);
  const RunnerDetailComponent = RunnerDetailRoute.options.component!;
  const detail = render(<RunnerDetailComponent />);
  expect(screen.getByRole("button", { name: "Revoke credential" })).toBeTruthy();
  detail.unmount();

  queryData([]);
  const viewerList = render(<RunnersComponent />);
  expect(screen.queryByRole("button", { name: "Create enrollment" })).toBeNull();
  expect(screen.getByRole("status").textContent).toBe("Runner operations require runner administrator permissions.");
  viewerList.unmount();

  queryData([]);
  render(<RunnerDetailComponent />);
  expect(screen.queryByRole("button", { name: "Revoke credential" })).toBeNull();
});

test("runner controls show the existing unavailable state when their queries fail", () => {
  queryFailure();
  const RunnersComponent = RunnersRoute.options.component!;
  const list = render(<RunnersComponent />);
  expect(screen.getByRole("alert").textContent).toContain("Runner inventory is unavailable.");
  list.unmount();

  const RunnerDetailComponent = RunnerDetailRoute.options.component!;
  render(<RunnerDetailComponent />);
  expect(screen.getByRole("alert").textContent).toContain("Runner is unavailable.");
});

test("runner details distinguish pending requests from unavailable records", () => {
  vi.mocked(useQuery).mockReturnValue({ data: undefined, isError: false, isPending: true } as never);
  const Component = RunnerDetailRoute.options.component!;
  render(<Component />);
  expect(screen.getByRole("status").textContent).toBe("Loading runner...");
  expect(screen.queryByRole("alert")).toBeNull();
});

test("runner detail retry refetches failed queries", async () => {
  const refetch = vi.fn();
  vi.mocked(useQuery).mockImplementation((options) => {
    const failed = (options as { queryKey?: readonly string[] }).queryKey?.[0] === "runner";
    return { data: failed ? undefined : { roles: ["runner_admin"] }, isError: failed, isPending: false, isFetching: false, refetch } as never;
  });
  const Component = RunnerDetailRoute.options.component!;
  render(<Component />);
  await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
  expect(refetch).toHaveBeenCalledOnce();
  expect(screen.queryByRole("button", { name: "Revoke credential" })).toBeNull();
});

test("failed credential revocation stays open for retry without an unhandled rejection", async () => {
  vi.mocked(revokeRunnerToken).mockRejectedValueOnce(new Error("service unavailable")).mockResolvedValueOnce(runner as never);
  queryData(["runner_admin"]);
  const Component = RunnerDetailRoute.options.component!;
  const user = userEvent.setup();
  render(<Component />);
  await user.click(screen.getByRole("button", { name: "Revoke credential" }));
  const dialog = screen.getByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: "Revoke credential" }));
  await waitFor(() => expect(within(dialog).getByRole("alert").textContent).toBe("Unable to revoke credential. Try again."));
  await user.click(within(dialog).getByRole("button", { name: "Revoke credential" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(revokeRunnerToken).toHaveBeenCalledTimes(2);
});
