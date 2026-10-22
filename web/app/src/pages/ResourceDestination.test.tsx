import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import type { Resource } from "@/api/hosting";
import { ResourceDestination } from "./ResourceDestination";
const resource: Resource = { id: "resource", project_id: "project", project_environment_id: "env", name: "Web", kind: "application", source: { kind: "image", image: { reference: "team/web:1" } }, generation: 1, created_by: "user", created_at: "", updated_at: "" };
const destination = { id: "dest", server_id: "server", name: "default", runtime_kind: "docker_compose", created_at: "" };
const binding = { id: "binding", resource_id: "resource", destination_id: "dest", runtime_namespace: "nerocd_resource", created_by: "user", created_at: "" };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const page = { items: [destination], limit: 20, offset: 0, count: 1, total: 1 };
let client: QueryClient;
afterEach(() => { cleanup(); client?.clear(); vi.unstubAllGlobals(); });
function setup(writable = true) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(<QueryClientProvider client={client}><ResourceDestination resource={resource} writable={writable} blocked={false} /></QueryClientProvider>);
  return view;
}
test("viewers read saved placement without deployment or replacement controls", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json(binding))); setup(false);
  await screen.findByText("nerocd_resource");
  expect(screen.queryByRole("button", { name: "Save desired destination" })).toBeNull();
  expect(screen.queryByRole("combobox")).toBeNull();
});
test("maintainer saves only resource-scoped destination intent and reloads the immutable binding", async () => {
  let saved = false;
  const fetch = vi.fn(async (url: string, options: RequestInit) => url.includes("/binding") ? options.method === "POST" ? (saved = true, json(binding, 201)) : saved ? json(binding) : json({}, 404) : json(page));
  vi.stubGlobal("fetch", fetch); setup();
  await screen.findByRole("option", { name: /default/ });
  fireEvent.change(screen.getByLabelText("Available destination"), { target: { value: "dest" } });
  fireEvent.submit(screen.getByRole("form", { name: "Save destination for Web" }));
  await screen.findByText("nerocd_resource");
  expect(fetch.mock.calls.find(([, options]) => options.method === "POST")?.[1].body).toBe(JSON.stringify({ destination_id: "dest" }));
  expect(screen.queryByRole("combobox")).toBeNull();
});
test("409 refetches saved binding without auto resubmission or overwriting placement", async () => {
  let conflict = false;
  const fetch = vi.fn(async (url: string, options: RequestInit) => url.includes("/binding") ? options.method === "POST" ? (conflict = true, json({}, 409)) : conflict ? json({ ...binding, destination_id: "other" }) : json({}, 404) : json(page));
  vi.stubGlobal("fetch", fetch); setup(); await screen.findByRole("option", { name: /default/ });
  fireEvent.change(screen.getByLabelText("Available destination"), { target: { value: "dest" } });
  fireEvent.submit(screen.getByRole("form"));
  await screen.findByText("other"); await screen.findByText(/no placement was overwritten/);
  expect(fetch.mock.calls.filter(([, options]) => options.method === "POST")).toHaveLength(1);
});
test("503 refresh error retains draft and blocks writes until explicit retry", async () => {
  let failing = false;
  vi.stubGlobal("fetch", vi.fn(async (url: string) => url.includes("/binding") ? json({}, 404) : failing ? json({}, 503) : json(page)));
  setup(); await screen.findByRole("option", { name: /default/ });
  fireEvent.change(screen.getByLabelText("Available destination"), { target: { value: "dest" } });
  failing = true; await act(async () => { await client.invalidateQueries({ queryKey: ["hostingDestinations"] }); });
  await screen.findByRole("button", { name: "Retry placement" });
  expect(screen.getByLabelText<HTMLSelectElement>("Available destination").value).toBe("dest");
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Save desired destination" }).disabled).toBe(true);
  failing = false; fireEvent.click(screen.getByRole("button", { name: "Retry placement" }));
  await waitFor(() => expect(screen.getByRole<HTMLButtonElement>("button", { name: "Save desired destination" }).disabled).toBe(false));
});
test("late replies after resource/project change cannot populate the new resource", async () => {
  let resolve!: (response: Response) => void;
  vi.stubGlobal("fetch", vi.fn((url: string) => url.includes("/resources/resource/") ? new Promise<Response>((done) => { resolve = done; }) : Promise.resolve(json({}, 404))));
  const view = setup();
  view.rerender(<QueryClientProvider client={client}><ResourceDestination resource={{ ...resource, id: "other", project_id: "other-project" }} writable blocked={false} /></QueryClientProvider>);
  await act(async () => resolve(json(binding)));
  expect(screen.queryByText("nerocd_resource")).toBeNull();
});
