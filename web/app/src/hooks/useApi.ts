import { useState, useEffect, useCallback } from "react";
import {
  loadSnapshot,
  createSession,
  revokeSession,
  createProject,
  createRepository,
  createTemplate,
  requestRun,
  approveRun,
  rejectRun,
  updateProject,
  archiveProject,
  updateTemplate,
  type ApiSnapshot,
  type ProjectInput,
  type RepositoryInput,
  type TemplateInput,
  type RunRequestInput,
  type CreatedSession,
  type Project,
  type Repository,
  type TaskTemplate,
  type TaskRun,
  type RunLog,
  type Approval,
  type AuditEvent,
  type Capability,
} from "@/api";

export function useSnapshot(token: string | null) {
  const [data, setData] = useState<ApiSnapshot | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(
    async (message = "") => {
      if (!token) return;
      setLoading(true);
      setError(null);
      try {
        const next = await loadSnapshot(token);
        setData(next);
        return message;
      } catch (err) {
        if (err instanceof Error && err.message.startsWith("401 ")) {
          throw new Error("UNAUTHORIZED");
        }
        setError(err instanceof Error ? err.message : String(err));
        throw err;
      } finally {
        setLoading(false);
      }
    },
    [token],
  );

  useEffect(() => {
    if (token) {
      refresh();
    }
  }, [token, refresh]);

  return { data, loading, error, refresh };
}

export function useAuth() {
  const [token, setToken] = useState(() => localStorage.getItem("nerocd.sessionToken") ?? "");
  const [authError, setAuthError] = useState("");

  const signIn = useCallback(async (email: string, password: string) => {
    try {
      const created: CreatedSession = await createSession(email, password);
      localStorage.setItem("nerocd.sessionToken", created.token);
      setToken(created.token);
      setAuthError("");
    } catch (err) {
      setAuthError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  const signOut = useCallback(() => {
    const currentToken = localStorage.getItem("nerocd.sessionToken") ?? token;
    if (currentToken) {
      void revokeSession(currentToken).catch(() => {
        // Local sign-out should still complete if the session is already gone.
      });
    }
    localStorage.removeItem("nerocd.sessionToken");
    setToken("");
    setAuthError("");
  }, [token]);

  return { token, authError, signIn, signOut };
}

export type MutateState = {
  busy: string;
  notice: string;
};

export type MutateFn = <T>(action: string, work: () => Promise<T>, message: string) => Promise<T | undefined>;

export function useMutate(refresh: (message?: string) => Promise<string | undefined>) {
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState("");

  const mutate = useCallback(
    async <T,>(action: string, work: () => Promise<T>, message: string): Promise<T | undefined> => {
      setBusy(action);
      setNotice("");
      try {
        const result = await work();
        await refresh();
        setNotice(message);
        return result;
      } catch (err) {
        setNotice(err instanceof Error ? err.message : String(err));
        return undefined;
      } finally {
        setBusy("");
      }
    },
    [refresh],
  );

  return { busy, notice, mutate, setNotice };
}

export function useTheme() {
  const [theme, setTheme] = useState<"light" | "dark">(() => {
    const saved = localStorage.getItem("nerocd.theme");
    if (saved === "dark" || saved === "light") return saved;
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  });

  useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
    localStorage.setItem("nerocd.theme", theme);
  }, [theme]);

  const toggleTheme = useCallback(() => {
    setTheme((current) => (current === "dark" ? "light" : "dark"));
  }, []);

  return { theme, toggleTheme };
}

export function useSearch() {
  const [query, setQuery] = useState("");

  return { query, setQuery };
}

export function useLocalStorage<T>(key: string, initialValue: T) {
  const [value, setValue] = useState<T>(() => {
    try {
      const item = localStorage.getItem(key);
      return item ? (JSON.parse(item) as T) : initialValue;
    } catch {
      return initialValue;
    }
  });

  useEffect(() => {
    localStorage.setItem(key, JSON.stringify(value));
  }, [key, value]);

  return [value, setValue] as const;
}
