import { readFileSync } from "node:fs";
import { afterEach, expect, test, vi } from "vitest";
import { waitFor } from "@testing-library/react";

afterEach(() => { document.body.replaceChildren(); });

test("fallback dashboard renders repository-controlled fields as text", async () => {
  document.body.innerHTML = ["principal", "templates", "runs", "capabilities", "refresh", "metric-projects", "metric-templates", "metric-approvals", "metric-runs", "metric-api"].map((id) => `<div id="${id}"></div>`).join("");
  const markup = '<meta http-equiv="refresh" content="0;url=https://attacker.invalid"><style>body{display:none}</style><img src=x onerror=alert(1)>';
  const responses: Record<string, unknown> = {
    health: { status: "ok" }, me: { name: "Operator", roles: [] }, projects: { items: [] },
    templates: { items: [{ name: markup, kind: markup, runner_tags: [markup], requires_ack: true }] },
    runs: { items: [] }, capabilities: { items: [] },
  };
  const fetchMock = vi.fn(async (path: string) => new Response(JSON.stringify(responses[path.split("/").at(-1)!]), { headers: { "Content-Type": "application/json" } }));
  const script = readFileSync("../static/app.js", "utf8");
  new Function("fetch", script)(fetchMock);
  await waitFor(() => expect(document.querySelector("#templates strong")?.textContent).toBe(markup));
  expect(document.querySelector("#templates .kind")?.textContent).toBe(markup);
  expect(document.querySelector("#templates .tag")?.textContent).toBe(markup);
  expect(document.querySelectorAll("#templates meta, #templates style, #templates img")).toHaveLength(0);
});
