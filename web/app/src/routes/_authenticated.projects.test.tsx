import type { ComponentType } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import { useQuery } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { Route } from "./_authenticated.projects";
const locator = {
  project_id: "project",
  environment_id: "env",
  resource_id: "db",
  backup_id: "backup",
  transfer_id: "upload",
};
const hosting = vi.hoisted(() => vi.fn());
const notifications = vi.hoisted(() => vi.fn());
vi.mock("@tanstack/react-router", () => ({
  createFileRoute: () => (options: { component: ComponentType }) => ({
    options,
    useSearch: () => locator,
  }),
  useNavigate: () => vi.fn(),
  useSearch: () => ({}),
}));
vi.mock("@tanstack/react-query", async (original) => ({
  ...(await original<typeof import("@tanstack/react-query")>()),
  useQuery: vi.fn(),
}));
vi.mock("@/api/mutations", () => ({
  useProjectActions: () => ({ actions: {}, busy: false, error: "" }),
}));
vi.mock("@/components/common/PageNavigation", () => ({
  usePage: () => ({ limit: 50, offset: 0 }),
  PageNavigation: () => null,
}));
vi.mock("@/pages/ProjectHosting", () => ({
  ProjectHosting: (props: unknown) => {
    hosting(props);
    return <div>Scoped hosting</div>;
  },
}));
vi.mock("@/pages/ProjectNotifications", () => ({
  ProjectNotifications: (props: unknown) => {
    notifications(props);
    return <div>Backup notification settings</div>;
  },
}));
vi.mock("@/pages/ProjectSecrets", () => ({ ProjectSecrets: () => null }));
vi.mock("@/pages/ProjectsView", () => ({ ProjectsView: () => null }));
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
const Component = Route.options.component as ComponentType;
function queries(fetching = false) {
  const project = {
    id: "project",
    name: "Production",
    description: "",
    created_at: "2026-10-26T12:00:00Z",
  };
  const principal = { id: "current", provider: "local", roles: [] };
  const members = [
    { user_id: "current", project_id: "project", role: "owner" },
  ];
  const values = [
    { items: [project], total: 1, count: 1, limit: 50, offset: 0 },
    [project],
    [],
    [],
    { items: [] },
    principal,
    members,
  ];
  let n = 0;
  vi.mocked(useQuery).mockImplementation(
    () =>
      ({
        data: values[n++ % values.length],
        isError: false,
        isPending: false,
        isFetching: fetching,
        refetch: vi.fn(),
      }) as never,
  );
  return { principal, members, project };
}
test("projects route forwards validated locator and current authorization data to hosting and notifications", () => {
  const { principal, members, project } = queries();
  render(<Component />);
  expect(screen.getByText("Backup notification settings")).toBeTruthy();
  expect(hosting).toHaveBeenCalledWith(
    expect.objectContaining({ recoverySearch: locator, projects: [project] }),
  );
  expect(notifications).toHaveBeenCalledWith(
    expect.objectContaining({
      principal,
      members,
      projects: [project],
      unavailable: false,
    }),
  );
});
test("refreshing authorization blocks notification mutations", () => {
  queries(true);
  render(<Component />);
  expect(notifications).toHaveBeenCalledWith(
    expect.objectContaining({ unavailable: true }),
  );
});
