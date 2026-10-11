import { QueryClient, useQuery, useQueryClient } from "@tanstack/react-query";
import { RunnerDetailView } from "@/pages/RunnersView";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComponentType, ReactNode } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";

const router = vi.hoisted(() => ({
  params: { runnerId: "runner_1" },
  search: { q: "" } as { q: string; offset?: number },
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
  useQueryClient: vi.fn(),
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

beforeEach(() => {
  vi.mocked(useQueryClient).mockReturnValue(new QueryClient());
});

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
  vi.mocked(revokeRunnerToken).mockResolvedValue({ ...runner, status: "revoked" } as never);
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
  const client = useQueryClient();
  const telemetry = { observed_at: runner.last_heartbeat_at, journal_depth: 3, retry_count: 1, renew_failures: 0 };
  client.setQueryData(["runner", runner.id], { ...runner, telemetry });
  client.setQueryData(["runners", {}], { items: [runner], count: 1, total: 1, offset: 0, limit: 50 });
  const RunnerDetailComponent = RunnerDetailRoute.options.component!;
  render(<RunnerDetailComponent />);
  await user.click(screen.getByRole("button", { name: "Revoke credential" }));
  const dialog = screen.getByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: "Revoke credential" }));
  await waitFor(() => expect(revokeRunnerToken).toHaveBeenCalledWith({ runner_id: runner.id }));
  await waitFor(() => expect(client.getQueryData(["runner", runner.id])).toEqual({ ...runner, status: "revoked", telemetry }));
  expect(client.getQueryData(["runners", {}])).toEqual({ items: [{ ...runner, status: "revoked" }], count: 1, total: 1, offset: 0, limit: 50 });
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
  expect(screen.getByRole("link", { name: "Back to runners" })).toBeTruthy();
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

test("runner inventory does not report an empty list while loading", () => {
  vi.mocked(useQuery).mockReturnValue({ data: undefined, isError: false, isPending: true } as never);
  const Component = RunnersRoute.options.component!;
  render(<Component />);
  expect(screen.getByRole("status").textContent).toBe("Loading runners...");
  expect(screen.queryByText("No runners match this view")).toBeNull();
});

test("runner inventory and details accept null tag and capability arrays", () => {
  const record = { ...runner, tags: null, capabilities: null };
  vi.mocked(useQuery).mockImplementation((options) => {
    const key = (options as { queryKey?: readonly string[] }).queryKey?.[0];
    return { data: key === "runners" ? { items: [record] } : key === "runner" ? record : { roles: ["runner_admin"] }, isError: false, isPending: false } as never;
  });
  const List = RunnersRoute.options.component!;
  const list = render(<List />);
  expect(screen.getByText("Edge runner")).toBeTruthy();
  expect(screen.getByText(/no tags/)).toBeTruthy();
  list.unmount();
  const Detail = RunnerDetailRoute.options.component!;
  render(<Detail />);
  expect(screen.getAllByText("None")).toHaveLength(2);
});


test("an open revocation dialog closes when the runner is revoked externally", async () => {
  const onRevoke = vi.fn();
  const view = render(<RunnerDetailView runner={runner as never} canAdmin onRevoke={onRevoke} />);
  await userEvent.setup().click(screen.getByRole("button", { name: "Revoke credential" }));
  expect(screen.getByRole("dialog")).toBeTruthy();
  view.rerender(<RunnerDetailView runner={{ ...runner, status: "revoked" } as never} canAdmin onRevoke={onRevoke} />);
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(screen.getByText("Credential is revoked.")).toBeTruthy();
  expect(onRevoke).not.toHaveBeenCalled();
});


test("pending enrollment locks the submitted draft and restores it after failure", async () => {
  let reject!: (error: Error) => void;
  vi.mocked(createRunnerEnrollment).mockImplementationOnce(() => new Promise((_, fail) => { reject = fail; }));
  queryData(["runner_admin"]);
  const Component = RunnersRoute.options.component!;
  render(<Component />);
  fireEvent.change(screen.getByLabelText("Runner ID"), { target: { value: "runner_pending" } });
  fireEvent.change(screen.getByLabelText("Runner name"), { target: { value: "Pending runner" } });
  const form = screen.getByRole("form", { name: "Create runner enrollment" });
  fireEvent.submit(form);
  expect(form.getAttribute("aria-busy")).toBe("true");
  for (const field of [...screen.getAllByRole("textbox"), screen.getByRole("combobox")]) {
    expect((field as HTMLInputElement).disabled).toBe(true);
  }
  fireEvent.submit(form);
  expect(createRunnerEnrollment).toHaveBeenCalledOnce();
  await act(async () => reject(new Error("Temporary failure")));
  expect(form.getAttribute("aria-busy")).toBe("false");
  expect((screen.getByLabelText("Runner ID") as HTMLInputElement).value).toBe("runner_pending");
  expect((screen.getByLabelText("Runner name") as HTMLInputElement).value).toBe("Pending runner");
  expect((screen.getByLabelText("Runner ID") as HTMLInputElement).disabled).toBe(false);
  expect(screen.getByRole("alert").textContent).toContain("Enrollment could not be created");
});


test("pending runner revocation stays visible and rejects duplicate submissions", async () => {
  let reject!: (error: Error) => void;
  const onRevoke = vi.fn().mockImplementationOnce(() => new Promise<void>((_, fail) => { reject = fail; })).mockResolvedValueOnce(undefined);
  render(<RunnerDetailView runner={runner as never} canAdmin onRevoke={onRevoke} />);
  fireEvent.click(screen.getByRole("button", { name: "Revoke credential" }));
  const dialog = screen.getByRole("dialog");
  const confirm = within(dialog).getByRole("button", { name: "Revoke credential" });
  fireEvent.click(confirm);
  fireEvent.click(confirm);
  expect(onRevoke).toHaveBeenCalledOnce();
  expect(dialog.getAttribute("aria-busy")).toBe("true");
  expect((within(dialog).getByRole("button", { name: "Keep credential" }) as HTMLButtonElement).disabled).toBe(true);
  expect(within(dialog).queryByRole("button", { name: "Close" })).toBeNull();
  fireEvent.keyDown(dialog, { key: "Escape" });
  expect(screen.getByRole("dialog")).toBe(dialog);
  await act(async () => reject(new Error("Temporary revocation failure")));
  expect(dialog.getAttribute("aria-busy")).toBe("false");
  expect(within(dialog).getByRole("alert").textContent).toContain("Unable to revoke credential");
  expect((within(dialog).getByRole("button", { name: "Keep credential" }) as HTMLButtonElement).disabled).toBe(false);
  fireEvent.click(within(dialog).getByRole("button", { name: "Revoke credential" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(onRevoke).toHaveBeenCalledTimes(2);
});


test("failed later runner pages offer a return to the first page", () => {
  queryFailure();
  router.search = { q: "edge", offset: 50 };
  const Component = RunnersRoute.options.component!;
  const view = render(<Component />);
  expect(screen.getByRole("button", { name: "First runners page" })).toBeTruthy();
  expect(screen.queryByText("No runners match this view")).toBeNull();
  router.search = { q: "edge" };
  view.rerender(<Component />);
  expect(screen.queryByRole("button", { name: "First runners page" })).toBeNull();
});


test.each(["", "not-a-date"])("runner details do not report invalid heartbeat %j as recent", (heartbeat) => {
  render(<RunnerDetailView runner={{ ...runner, last_heartbeat_at: heartbeat } as never} canAdmin onRevoke={vi.fn()} />);
  expect(screen.getByText("Heartbeat unavailable; runner connectivity cannot be determined.")).toBeTruthy();
  expect(screen.queryByText("Active and recently observed.")).toBeNull();
  expect(screen.getByRole("button", { name: "Revoke credential" })).toBeTruthy();
});

test("enrollment validates tag arrays and preserves their boundaries", async () => {
  vi.mocked(createRunnerEnrollment).mockResolvedValue({ enrollment: { runner_id: "runner_tags" }, token: "one-time-token" } as never);
  queryData(["runner_admin"]);
  const Component = RunnersRoute.options.component!;
  render(<Component />);
  fireEvent.change(screen.getByLabelText("Runner ID"), { target: { value: "runner_tags" } });
  fireEvent.change(screen.getByLabelText("Runner name"), { target: { value: "Tagged runner" } });
  const input = screen.getByLabelText("Runner tags");
  fireEvent.change(input, { target: { value: '["linux", null]' } });
  const form = screen.getByRole("form", { name: "Create runner enrollment" });
  await act(async () => { fireEvent.submit(form); });
  expect(createRunnerEnrollment).not.toHaveBeenCalled();
  expect(screen.getByRole("alert").textContent).toBe("Enter comma-separated tags or a JSON array of strings.");
  expect(document.activeElement).toBe(input);
  const tags = ["linux", "zone=west,east", "line\nbreak"];
  fireEvent.change(input, { target: { value: JSON.stringify(tags) } });
  await act(async () => { fireEvent.submit(form); });
  expect(createRunnerEnrollment).toHaveBeenCalledWith(expect.objectContaining({ runner_id: "runner_tags", tags }));
});
