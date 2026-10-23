import { expect, test } from "vitest";
import { navigationForRoles, navigationForView, navigationGroups, viewForPath } from "./metadata";

test("nested routes select their parent without matching unrelated prefixes", () => {
  expect(viewForPath("/runs/run_123")).toBe("runs");
  expect(viewForPath("/deployments/deploy_123")).toBe("deployments");
  expect(viewForPath("/runners/runner_123")).toBe("runners");
  expect(viewForPath("/runners-unrelated")).toBe("home");
});

test("all navigation consumers use the same role-filtered destinations", () => {
  expect(navigationForRoles([]).some((item) => item.key === "operations")).toBe(false);
  expect(navigationForRoles(["runner_admin"]).some((item) => item.key === "operations")).toBe(false);
  expect(navigationForRoles(["system_admin"]).some((item) => item.key === "operations")).toBe(true);
  for (const item of navigationForRoles(["system_admin"])) {
    expect(navigationForView(item.key).to).toBe(item.to);
    expect(viewForPath(item.to)).toBe(item.key);
  }
});


test("hosting destinations lead navigation without changing route keys or URLs", () => {
  expect(navigationGroups).toEqual(["Hosting", "Advanced", "Observe", "Account"]);
  const hosting = navigationForRoles([]).filter((item) => item.group === "Hosting");
  expect(hosting.map(({ key, to, label }) => ({ key, to, label }))).toEqual([
    { key: "home", to: "/", label: "Home" }, { key: "projects", to: "/projects", label: "Projects" }, { key: "runners", to: "/runners", label: "Servers and runners" },
  ]);
  expect(navigationForRoles([]).filter((item) => item.group === "Advanced").map((item) => item.key)).toEqual(["deployments", "runs", "approvals", "templates"]);
});
