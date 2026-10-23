import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import type { ReactNode } from "react";
import { useSelectedRunLogsPollingQuery } from "./polling";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

test("the terminal transition reads the final tail once and repeated terminal renders do not refetch it", async () => {
  let completed = false;
  const offsets: number[] = [];
  const row = (sequence: number) => ({ id: `log_${sequence}`, run_id: "run_one", sequence, stream: "stdout", message: `Output ${sequence}` });
  vi.stubGlobal("fetch", vi.fn(async (input: string) => {
    const url = new URL(input, "http://localhost");
    const offset = Number(url.searchParams.get("offset"));
    const limit = Number(url.searchParams.get("limit"));
    if (limit !== 1) offsets.push(offset);
    const items = offset === 0 ? [row(1)] : offset === 1 && completed ? [row(2)] : [];
    return new Response(JSON.stringify({ items, count: items.length, total: completed ? 2 : 1, limit, offset }), { headers: { "Content-Type": "application/json" } });
  }));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0, refetchOnWindowFocus: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  const hook = renderHook(({ follow }) => useSelectedRunLogsPollingQuery("run_one", follow), { initialProps: { follow: true }, wrapper });
  await waitFor(() => expect(hook.result.current.data.map((row) => row.sequence)).toEqual([1]));
  await waitFor(() => expect(offsets.filter((offset) => offset === 1)).toHaveLength(1));
  completed = true;
  hook.rerender({ follow: false });
  await waitFor(() => expect(hook.result.current.data.map((row) => row.sequence)).toEqual([1, 2]));
  await waitFor(() => expect(hook.result.current.isFetching).toBe(false));
  expect(offsets.filter((offset) => offset === 1)).toHaveLength(2);
  const settledReads = offsets.length;
  hook.rerender({ follow: false });
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 20)); });
  expect(offsets).toHaveLength(settledReads);
  hook.unmount(); client.clear();
});
