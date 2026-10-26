import { cleanup, render, screen } from "@testing-library/react";
import type { ComponentType } from "react";
import { afterEach, expect, test, vi } from "vitest";
vi.mock("@tanstack/react-router", () => ({ createFileRoute: () => (options: { component: ComponentType }) => ({ options, useSearch: () => ({ q: "" }) }) }));
vi.mock("@tanstack/react-query", () => ({ queryOptions: (value: unknown) => value, useQuery: () => ({ data: { items: [], total: 0 }, isPending: false, isError: false }) }));
vi.mock("@/api/mutations", () => ({ useProjectActions: () => ({ busy: false, actions: {} }) }));
vi.mock("@/components/common/PageNavigation", () => ({ PageNavigation: () => <p>Project paging</p>, usePage: () => ({ limit: 50, offset: 0 }) }));
vi.mock("@/pages/ProjectsView", () => ({ ProjectsView: () => <section aria-label="Project creation and automation summaries" /> }));
vi.mock("@/pages/ProjectHosting", () => ({ ProjectHosting: () => <section aria-label="Project hosting" /> }));
vi.mock("@/pages/ProjectNotifications", () => ({ ProjectNotifications: () => <section aria-label="Project backup notifications" /> }));
vi.mock("@/pages/ProjectSecrets", () => ({ ProjectSecrets: () => <section aria-label="Project secrets" /> }));
import { Route } from "./_authenticated.projects";
afterEach(cleanup);
test("application hosting precedes project creation and automation summaries without replacing them", () => {
  const Component = Route.options.component as ComponentType; render(<Component />);
  const hosting = screen.getByRole("region", { name: "Project hosting" }), summary = screen.getByRole("region", { name: "Project creation and automation summaries" });
  expect(hosting.compareDocumentPosition(summary) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy(); expect(screen.getByText("Project paging")).toBeTruthy(); expect(screen.getByRole("region", { name: "Project secrets" })).toBeTruthy();
});
