import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { ImageRegistryCredential } from "./ImageRegistryCredential";
const secret = { id: "secret", name: "Registry", project_id: "project", status: "active", current_version: 2 };
const page = (items: unknown[]) => ({ items, limit: 20, offset: 0, count: items.length, total: items.length });
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
let client: QueryClient;
afterEach(() => { cleanup(); client?.clear(); vi.unstubAllGlobals(); });
function setup(handler?: (url: string, init: RequestInit) => Response | Promise<Response> | undefined) {
  const fetch = vi.fn(async (url: string, init: RequestInit) => (await handler?.(url, init)) ?? (url.includes("/versions") ? json(page([{ secret_id: "secret", version: 1 }, { secret_id: "secret", version: 2 }])) : json(page([secret, { ...secret, id: "revoked", status: "revoked" }, { ...secret, id: "foreign", project_id: "other" }]))));
  vi.stubGlobal("fetch", fetch);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const changed = vi.fn(), denied = vi.fn();
  const props = { actor: "actor", project: "project", image: "ghcr.io/team/app:1", disabled: false, onChange: changed, onDenied: denied };
  const view = render(<QueryClientProvider client={client}><ImageRegistryCredential {...props} /></QueryClientProvider>);
  return { fetch, changed, denied, ...view, retire: () => view.rerender(<QueryClientProvider client={client}><ImageRegistryCredential {...props} disabled /></QueryClientProvider>) };
}
async function choose() {
  fireEvent.change(screen.getByLabelText("Image access"), { target: { value: "private" } });
  await screen.findByRole("option", { name: "Registry · secret" });
  fireEvent.change(screen.getByLabelText("Registry credential secret"), { target: { value: "secret" } });
  await screen.findByRole("option", { name: "Version 1" });
  fireEvent.change(screen.getByLabelText("Registry credential version"), { target: { value: "1" } });
}
test("public default reads no secrets; private selection uses explicit version and excludes foreign or revoked secrets", async () => {
  const { fetch, changed } = setup();
  expect(fetch).not.toHaveBeenCalled();
  await choose();
  expect(screen.queryByRole("option", { name: /revoked|foreign/ })).toBeNull();
  await waitFor(() => expect(changed).toHaveBeenLastCalledWith({ valid: true, credential: { secret_id: "secret", version: 1 } }));
  expect(fetch.mock.calls.some(([url]) => url.includes("reveal"))).toBe(false);
  fireEvent.change(screen.getByLabelText("Image access"), { target: { value: "public" } });
  await waitFor(() => expect(changed).toHaveBeenLastCalledWith({ valid: true }));
});
test.each([201, 503])("protected create clears plaintext for HTTP %s and never caches or reveals it", async (status) => {
  const { fetch } = setup((_url, init) => init.method === "POST" ? json(secret, status) : undefined);
  await choose();
  fireEvent.change(screen.getByLabelText("Registry secret name"), { target: { value: "Private pull" } });
  fireEvent.change(screen.getByLabelText("Registry host"), { target: { value: "ghcr.io" } });
  fireEvent.change(screen.getByLabelText("Registry username"), { target: { value: "operator" } });
  fireEvent.paste(screen.getByLabelText("Registry password or token"), { clipboardData: { getData: () => "secret-canary" } });
  if (status === 201) fireEvent.keyDown(screen.getByLabelText("Registry host"), { key: "Enter" });
  else fireEvent.click(screen.getByRole("button", { name: "Save registry credential" }));
  await waitFor(() => expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(1));
  const body = JSON.parse(fetch.mock.calls.find(([, init]) => init.method === "POST")![1].body as string);
  expect(body).toEqual({ project_id: "project", name: "Private pull", value: JSON.stringify({ registry: "ghcr.io", username: "operator", password: "secret-canary" }) });
  await screen.findByText(status === 201 ? /Registry credential saved/ : /Credential creation is unconfirmed/);
  expect(screen.getByLabelText<HTMLTextAreaElement>("Registry password or token").value).toBe("");
  expect(JSON.stringify(client.getQueryCache().getAll().map((q) => q.state.data))).not.toContain("secret-canary");
  expect(fetch.mock.calls.some(([url]) => url.includes("reveal"))).toBe(false);
});
test("retirement clears protected input and locks controls", async () => {
  const { retire } = setup(); await choose();
  fireEvent.paste(screen.getByLabelText("Registry password or token"), { clipboardData: { getData: () => "secret-canary" } });
  retire();
  expect(screen.getByLabelText<HTMLTextAreaElement>("Registry password or token").value).toBe("");
  expect(screen.getByLabelText<HTMLSelectElement>("Image access").disabled).toBe(true);
});

test("metadata permission failure retires access and never selects a credential", async () => {
  const { denied, changed } = setup(() => json({ error: "denied" }, 403));
  fireEvent.change(screen.getByLabelText("Image access"), { target: { value: "private" } });
  await waitFor(() => expect(denied).toHaveBeenCalled());
  expect(changed).toHaveBeenLastCalledWith({ valid: false });
});
