import { useState, useEffect, useCallback, useLayoutEffect, useRef } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createBrowserSession,
  revokeBrowserSession,
  principalQuery,
  ApiError,
} from "@/api";

const pendingSignOutKey = "nerocd.sign-out-pending";
const signOutFailure = "Sign-out is not confirmed. Your server session may still be active. Retry sign-out before leaving this browser.";
type SignOutState = "idle" | "pending" | "failed";

function initialSignOutState(): SignOutState {
  try { return sessionStorage.getItem(pendingSignOutKey) ? "failed" : "idle"; }
  catch { return "idle"; }
}

function rememberPendingSignOut(pending: boolean) {
  try {
    if (pending) sessionStorage.setItem(pendingSignOutKey, "1");
    else sessionStorage.removeItem(pendingSignOutKey);
  } catch {
    // Storage can be unavailable in restricted browsers. Keep the live UI
    // locked and the failure visible until the server confirms revocation.
  }
}

export function useAuth() {
  const client = useQueryClient();
  const [authError, setAuthError] = useState("");
  const [sessionRevoked, setSessionRevoked] = useState(false);
  const [unauthorized, setUnauthorized] = useState(false);
  const [signOutState, setSignOutState] = useState<SignOutState>(initialSignOutState);
  const signOutInFlight = useRef(false);
  const principal = useQuery({ ...principalQuery(), retry: false, enabled: signOutState === "idle" && !sessionRevoked && !unauthorized });
  const authenticated = signOutState !== "idle" ? null : sessionRevoked || unauthorized ? false : principal.isPending || principal.isError ? null : true;
  const clearedUnauthorized = useRef(false);
  useLayoutEffect(() => {
    if (principal.isError && principal.error instanceof ApiError && principal.error.status === 401) {
      if (!clearedUnauthorized.current) {
        clearedUnauthorized.current = true;
        queueMicrotask(() => {
          client.clear();
          setUnauthorized(true);
        });
      }
    } else {
      clearedUnauthorized.current = false;
    }
  }, [client, principal.error, principal.isError]);
  useEffect(() => { if (principal.isError && authenticated !== false && !(principal.error instanceof ApiError && principal.error.status === 401)) setAuthError(principal.error instanceof Error ? principal.error.message : String(principal.error)); }, [authenticated, principal.error, principal.isError]);
  useEffect(() => { if (sessionRevoked) client.clear(); }, [client, sessionRevoked]);

  const signIn = useCallback(async (email: string, password: string) => {
    if (signOutState !== "idle") throw new Error(signOutFailure);
    try {
      await createBrowserSession({ email, password });
      setSessionRevoked(false);
      // This fetch shares the sole principal query key with the mounted observer.
      // It verifies the new cookie without a second post-login /me request.
      await client.fetchQuery(principalQuery());
      setUnauthorized(false);
      setAuthError("");
    } catch (err) {
      setAuthError(err instanceof Error ? err.message : String(err));
      throw err;
    }
  }, [client, signOutState]);

  const signOut = useCallback(async () => {
    if (signOutInFlight.current) return;
    signOutInFlight.current = true;
    rememberPendingSignOut(true);
    setSignOutState("pending");
    await client.cancelQueries();
    client.clear();
    try {
      await revokeBrowserSession();
    } catch (err) {
      if (!(err instanceof ApiError && err.status === 401)) {
        setSignOutState("failed");
        return;
      }
    } finally {
      signOutInFlight.current = false;
      client.clear();
    }
    rememberPendingSignOut(false);
    setSessionRevoked(true);
    setUnauthorized(false);
    setAuthError("");
    setSignOutState("idle");
  }, [client]);

  return { authenticated, authError: signOutState === "failed" ? signOutFailure : authError, signOutState, signIn, signOut };
}
