import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { BuildLogs } from "./BuildLogs";
import type { HostingBuildLogPage } from "@/api/hosting-builds";

const props = { actor: "actor", project: "project", resource: "resource", build: "build/one", blocked: false };
const row = (sequence: number, message = `output ${sequence}`) => ({ sequence, component: "buildkit" as const, stream: "stdout", message, created_at: "2026-10-22T10:00:00Z" });
const page = (overrides: Partial<HostingBuildLogPage> = {}): HostingBuildLogPage => ({ items: [row(1)], next_sequence: 1, earliest_available_sequence: 1, deleted_through_sequence: 0, gap: false, terminal: true, truncated: false, history_complete: true, ...overrides });
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const open = () => fireEvent.click(screen.getByRole("button", { name: "Show build output" }));
const refresh = () => fireEvent.click(screen.getByRole("button", { name: "Refresh build output" }));
afterEach(() => { cleanup(); vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

test("output is opt-in, escaped plaintext with component and stream labels, and close discards it", async () => {
  const fetch = vi.fn(async () => json(page({ items: [row(1, '<img src=x onerror="alert(1)">\u001b[31mred')] }))); vi.stubGlobal("fetch", fetch);
  render(<BuildLogs {...props} />); expect(fetch).not.toHaveBeenCalled(); open();
  await screen.findByText(/<img src=x/); expect(document.querySelector("img")).toBeNull();
  expect(screen.getByText(/component buildkit · stream stdout/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Close build output" })); expect(screen.queryByText(/<img src=x/)).toBeNull();
  open(); await screen.findByText(/<img src=x/); expect(fetch.mock.calls).toHaveLength(2);
});
test("one visible poll reads one page using the server cursor and terminal polling stops", async () => {
  const fetch = vi.fn().mockResolvedValueOnce(json(page({ terminal: false, truncated: true }))).mockResolvedValueOnce(json(page({ items: [row(2)], next_sequence: 2 }))); vi.stubGlobal("fetch", fetch);
  vi.useFakeTimers(); render(<BuildLogs {...props} />); open(); await act(async () => {});
  expect(fetch).toHaveBeenCalledTimes(1);
  await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
  expect(fetch).toHaveBeenCalledTimes(2); expect(fetch.mock.calls[1]![0]).toContain("after_sequence=1&limit=200");
  await act(async () => { await vi.advanceTimersByTimeAsync(15000); }); expect(fetch).toHaveBeenCalledTimes(2);
  fireEvent.focus(window); await act(async () => {}); expect(fetch).toHaveBeenCalledTimes(3);
});
test("hidden output waits for visibility, and concurrent refresh/focus never overlaps", async () => {
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
  let resolve!: (response: Response) => void;
  const fetch = vi.fn(() => new Promise<Response>((done) => { resolve = done; })); vi.stubGlobal("fetch", fetch);
  render(<BuildLogs {...props} />); open(); expect(fetch).not.toHaveBeenCalled();
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible"); fireEvent(document, new Event("visibilitychange"));
  fireEvent.focus(window); refresh(); expect(fetch).toHaveBeenCalledTimes(1);
  await act(async () => resolve(json(page()))); await screen.findByText("output 1"); vi.restoreAllMocks();
});
test("transient errors preserve cursor and pause focus/poll until explicit retry", async () => {
  const fetch = vi.fn().mockResolvedValueOnce(json(page({ terminal: false }))).mockRejectedValueOnce(new TypeError("offline")).mockResolvedValueOnce(json(page({ items: [row(2)], next_sequence: 2 }))); vi.stubGlobal("fetch", fetch);
  render(<BuildLogs {...props} />); open(); await screen.findByText("output 1"); refresh();
  await screen.findByRole("button", { name: "Retry build output" }); fireEvent.focus(window);
  expect(fetch).toHaveBeenCalledTimes(2); expect(screen.getByText("output 1")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Retry build output" })); await screen.findByText("output 2");
  expect(fetch.mock.calls[2]![0]).toContain("after_sequence=1");
});
test.each([401, 403, 404])("%s immediately erases retained output and disables retry", async (status) => {
  const fetch = vi.fn().mockResolvedValueOnce(json(page())).mockResolvedValueOnce(json({}, status)); vi.stubGlobal("fetch", fetch);
  render(<BuildLogs {...props} />); open(); await screen.findByText("output 1"); refresh();
  await screen.findByText(/unavailable under current access/); expect(screen.queryByText("output 1")).toBeNull();
  expect(screen.getByRole<HTMLButtonElement>("button", { name: "Refresh build output" }).disabled).toBe(true);
});
test.each(["actor", "project", "resource", "build", "blocked"])("%s change aborts and prevents late plaintext from mounting", async (field) => {
  let resolve!: (response: Response) => void; const fetch = vi.fn((_url: string, _options: RequestInit) => new Promise<Response>((done) => { resolve = done; })); vi.stubGlobal("fetch", fetch);
  const view = render(<BuildLogs {...props} />); open();
  const signal = fetch.mock.calls[0]![1].signal;
  view.rerender(<BuildLogs {...props} {...(field === "blocked" ? { blocked: true } : { [field]: "other" })} />);
  expect(signal?.aborted).toBe(true); await act(async () => resolve(json(page())));
  expect(screen.queryByText("output 1")).toBeNull(); expect(screen.queryByRole("list", { name: "Build output rows" })).toBeNull();
});
test("history completeness, gaps, message clipping and page truncation have distinct notices", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => json(page({ history_complete: false, gap: true, truncated: true, items: [{ ...row(1), message_truncated: true }] }))));
  render(<BuildLogs {...props} />); open(); await screen.findByText(/Build history is incomplete/);
  expect(screen.getByText(/A server history gap/)).toBeTruthy(); expect(screen.getByText(/More retained server output/)).toBeTruthy(); expect(screen.getByText("Server shortened this message.")).toBeTruthy();
});
test.each([
  { next_sequence: 0 }, { next_sequence: 1, truncated: true, items: [] },
  { next_sequence: 2, items: [row(1)] }, { next_sequence: 2, items: [] },
  { next_sequence: 2, items: [row(2), row(2)] }, { next_sequence: 2, gap: "false" },
])("malformed or nonadvancing page pauses without changing retained cursor: %j", async (invalid) => {
  const fetch = vi.fn().mockResolvedValueOnce(json(page())).mockResolvedValueOnce(json(page(invalid as Partial<HostingBuildLogPage>))).mockResolvedValueOnce(json(page({ items: [], next_sequence: 1 })));
  vi.stubGlobal("fetch", fetch); render(<BuildLogs {...props} />); open(); await screen.findByText("output 1"); refresh();
  fireEvent.click(await screen.findByRole("button", { name: "Retry build output" })); await waitFor(() => expect(fetch).toHaveBeenCalledTimes(3));
  expect(fetch.mock.calls[2]![0]).toContain("after_sequence=1"); expect(screen.getByText("output 1")).toBeTruthy();
});
test("local retention counts UTF-8 bytes as well as rows without losing the server cursor", async () => {
  let cursor = 0; const fetch = vi.fn(async (_url: string, _options: RequestInit) => { const items = Array.from({ length: 30 }, () => row(++cursor, "🦊".repeat(2000))); return json(page({ items, next_sequence: cursor, truncated: true })); }); vi.stubGlobal("fetch", fetch);
  render(<BuildLogs {...props} />); open();
  for (const last of [30, 60, 90, 120, 150]) {
    await waitFor(() => expect(screen.getAllByRole("listitem").at(-1)?.textContent).toContain(`Sequence ${last} ·`));
    if (last < 150) refresh();
  }
  expect(screen.getByText(/local output window was trimmed/)).toBeTruthy();
  const retained = screen.getAllByRole("listitem"); expect(retained.length).toBeLessThan(140); expect(fetch.mock.calls[4]![0]).toContain("after_sequence=120");
});
test("local row limit removes only the oldest rows and close aborts pending reads", async () => {
  let cursor = 0; const fetch = vi.fn(async (_url: string, _options: RequestInit) => {
    const items = Array.from({ length: 200 }, () => row(++cursor)); return json(page({ items, next_sequence: cursor, truncated: true }));
  }); vi.stubGlobal("fetch", fetch); render(<BuildLogs {...props} />); open();
  for (const last of [200, 400, 600]) {
    await waitFor(() => expect(screen.getAllByRole("listitem").at(-1)?.textContent).toContain(`Sequence ${last} ·`));
    if (last < 600) refresh();
  }
  expect(screen.getAllByRole("listitem")).toHaveLength(500); expect(screen.getAllByRole("listitem")[0]?.textContent).toContain("Sequence 101 ·");
  fetch.mockImplementationOnce(() => new Promise<Response>(() => {})); refresh();
  const signal = fetch.mock.calls[3]![1].signal;
  fireEvent.click(screen.getByRole("button", { name: "Close build output" }));
  expect(signal?.aborted).toBe(true); expect(screen.queryByRole("list", { name: "Build output rows" })).toBeNull();
});
