import { useState, useEffect, useCallback, useLayoutEffect, useRef } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createBrowserSession,
  revokeBrowserSession,
  principalQuery,
  ApiError,
} from "@/api";
export type { MutateFn } from "@/api/compat";

export function useAuth() {
  const client = useQueryClient();
  const [authError, setAuthError] = useState("");
  const [sessionRevoked, setSessionRevoked] = useState(false);
  const [unauthorized, setUnauthorized] = useState(false);
  const unauthorizedRef = useRef(false);
  const principal = useQuery({ ...principalQuery(), retry: false, enabled: !sessionRevoked && !unauthorized && !unauthorizedRef.current });
  const authenticated = sessionRevoked || unauthorized ? false : principal.isPending ? null : principal.isError && principal.error instanceof ApiError && principal.error.status === 401 ? false : principal.isError ? null : true;
  const clearedUnauthorized = useRef(false);
  useLayoutEffect(() => {
    if (principal.isError && principal.error instanceof ApiError && principal.error.status === 401) {
      if (!clearedUnauthorized.current) {
        clearedUnauthorized.current = true;
        unauthorizedRef.current = true;
        setUnauthorized(true);
      }
    } else {
      clearedUnauthorized.current = false;
    }
  }, [client, principal.error, principal.isError]);
  useLayoutEffect(() => {
    if (!unauthorized) return;
    queueMicrotask(() => client.clear());
  }, [client, unauthorized]);
  useEffect(() => { if (principal.isError && authenticated !== false) setAuthError(principal.error instanceof Error ? principal.error.message : String(principal.error)); }, [authenticated, principal.error, principal.isError]);
  useEffect(() => { if (sessionRevoked) client.clear(); }, [client, sessionRevoked]);

  const signIn = useCallback(async (email: string, password: string) => {
    try {
      await createBrowserSession({ email, password });
      setSessionRevoked(false);
      // This fetch shares the sole principal query key with the mounted observer.
      // It verifies the new cookie without a second post-login /me request.
      await client.fetchQuery(principalQuery());
      unauthorizedRef.current = false;
      setUnauthorized(false);
      setAuthError("");
    } catch (err) {
      setAuthError(err instanceof Error ? err.message : String(err));
      throw err;
    }
  }, [client]);

  const signOut = useCallback(async () => {
    try {
      await revokeBrowserSession();
    } catch {
      // Local sign-out still completes when the browser session is already gone.
    }
    client.clear();
    setSessionRevoked(true);
    unauthorizedRef.current = true;
    setUnauthorized(false);
    setAuthError("");
  }, [client]);

  return { authenticated, authError, signIn, signOut };
}

const THEME_STORAGE_KEY = "nerocd.theme.v2";

export function useTheme() {
  const [theme, setTheme] = useState<"light" | "dark">(() => {
    const saved = localStorage.getItem(THEME_STORAGE_KEY);
    if (saved === "dark" || saved === "light") return saved;
    return "dark";
  });

  useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
    localStorage.setItem(THEME_STORAGE_KEY, theme);
  }, [theme]);

  const toggleTheme = useCallback(() => {
    setTheme((current) => (current === "dark" ? "light" : "dark"));
  }, []);

  return { theme, toggleTheme };
}
