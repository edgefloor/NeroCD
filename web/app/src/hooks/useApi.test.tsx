import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";

import { useAuth } from "./useApi";

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; localStorage.clear(); });
function wrapper({ children }: { children: ReactNode }) { return <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{children}</QueryClientProvider>; }

test("startup uses cookie-backed /me and recognizes authentication state", async () => {
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ id: "usr_1", email: "admin@example.local", name: "Admin", roles: [], provider: "local" }), { headers: { "Content-Type": "application/json" } }));
  globalThis.fetch = fetchMock;
  const { result } = renderHook(() => useAuth(), { wrapper });
  await waitFor(() => expect(result.current.authenticated).toBe(true));
  expect(fetchMock).toHaveBeenCalledWith("/api/v1/me", expect.objectContaining({ credentials: "include" }));
});

test("startup recognizes an unauthenticated browser", async () => {
  globalThis.fetch = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ error: "unauthenticated" }), { status: 401, headers: { "Content-Type": "application/json" } }));
  const { result } = renderHook(() => useAuth(), { wrapper });
  await waitFor(() => expect(result.current.authenticated).toBe(false));
  expect(result.current.authError).toBe("");
});

test("startup preserves server failures as a retryable error instead of signing out", async () => {
  globalThis.fetch = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify({ error: "service unavailable" }), { status: 500, headers: { "Content-Type": "application/json" } }));
  const { result } = renderHook(() => useAuth(), { wrapper });

  await waitFor(() => expect(result.current.authError).toBe("service unavailable"));
  expect(result.current.authenticated).toBeNull();
});

test("startup preserves network failures as a retryable error instead of signing out", async () => {
  globalThis.fetch = vi.fn<typeof fetch>().mockRejectedValue(new TypeError("network offline"));
  const { result } = renderHook(() => useAuth(), { wrapper });

  await waitFor(() => expect(result.current.authError).toBe("network offline"));
  expect(result.current.authenticated).toBeNull();
});

test("browser sign-in never persists auth material and sign-out revokes its endpoint", async () => {
  const fetchMock = vi.fn<typeof fetch>()
    .mockResolvedValueOnce(new Response(null, { status: 401 }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ session: { id: "ses_1", user_id: "usr_1", expires_at: "2026-01-01T00:00:00Z", created_at: "2025-01-01T00:00:00Z" } }), { status: 201, headers: { "Content-Type": "application/json" } }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ id: "usr_1", email: "admin@example.local", name: "Admin", roles: [], provider: "local" }), { headers: { "Content-Type": "application/json" } }))
    .mockResolvedValueOnce(new Response(null, { status: 204 }));
  globalThis.fetch = fetchMock;
  const { result } = renderHook(() => useAuth(), { wrapper });
  await waitFor(() => expect(result.current.authenticated).toBe(false));
  await act(() => result.current.signIn("admin@example.local", "admin"));
  expect(result.current.authenticated).toBe(true);
  expect(localStorage).toHaveLength(0);
  expect(fetchMock).toHaveBeenCalledWith("/api/v1/browser-sessions", expect.objectContaining({ method: "POST", credentials: "include" }));
  await act(() => result.current.signOut());
  expect(result.current.authenticated).toBe(false);
  expect(fetchMock).toHaveBeenCalledWith("/api/v1/browser-sessions", expect.objectContaining({ method: "DELETE", credentials: "include" }));
});

test("post-login principal refetch is singular and a later 401 revokes authentication", async () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const localWrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  const fetchMock = vi.fn<typeof fetch>()
    .mockResolvedValueOnce(new Response(null, { status: 401 }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ session: { id: "ses_1" } }), { status: 201, headers: { "Content-Type": "application/json" } }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ id: "usr_1", email: "admin@example.local", name: "Admin", roles: [], provider: "local" }), { headers: { "Content-Type": "application/json" } }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ error: "expired" }), { status: 401, headers: { "Content-Type": "application/json" } }));
  globalThis.fetch = fetchMock;
  const { result } = renderHook(() => useAuth(), { wrapper: localWrapper });
  await waitFor(() => expect(result.current.authenticated).toBe(false));
  await act(() => result.current.signIn("admin@example.local", "admin"));
  await waitFor(() => expect(result.current.authenticated).toBe(true));
  expect(fetchMock.mock.calls.filter(([path]) => path === "/api/v1/me")).toHaveLength(2);
  queryClient.setQueryData(["projects"], [{ id: "proj_1" }]);
  queryClient.setQueryData(["runs"], [{ id: "run_1" }]);
  await act(async () => { await queryClient.refetchQueries({ queryKey: ["principal"] }); });
  await waitFor(() => expect(result.current.authenticated).toBe(false));
  expect(fetchMock.mock.calls.filter(([path]) => path === "/api/v1/me")).toHaveLength(3);
  await waitFor(() => expect(queryClient.getQueryCache().getAll()).toHaveLength(0));
});

test("sign-out clears every in-memory authenticated query before redirect state changes", async () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const localWrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  globalThis.fetch = vi.fn<typeof fetch>()
    .mockResolvedValueOnce(new Response(JSON.stringify({ id: "usr_1", email: "admin@example.local", name: "Admin", roles: [], provider: "local" }), { headers: { "Content-Type": "application/json" } }))
    .mockResolvedValueOnce(new Response(null, { status: 204 }));
  const { result } = renderHook(() => useAuth(), { wrapper: localWrapper });
  await waitFor(() => expect(result.current.authenticated).toBe(true));
  queryClient.setQueryData(["projects"], [{ id: "proj_1" }]);
  await act(() => result.current.signOut());
  await waitFor(() => expect(result.current.authenticated).toBe(false));
  await waitFor(() => expect(queryClient.getQueryCache().getAll()).toHaveLength(0));
});

test("failed browser sign-in rejects for router control while retaining the rendered error", async () => {
  globalThis.fetch = vi.fn<typeof fetch>()
    .mockResolvedValueOnce(new Response(null, { status: 401 }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ error: "invalid credentials" }), { status: 401, headers: { "Content-Type": "application/json" } }));
  const { result } = renderHook(() => useAuth(), { wrapper });
  await waitFor(() => expect(result.current.authenticated).toBe(false));
  await expect(result.current.signIn("bad@example.local", "wrong")).rejects.toThrow("invalid credentials");
  expect(result.current.authenticated).toBe(false);
  await waitFor(() => expect(result.current.authError).toBe("invalid credentials"));
});
