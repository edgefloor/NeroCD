import { createContext, useContext } from "react";
import type { QueryClient } from "@tanstack/react-query";

export type AuthState = { authenticated: boolean | null; authError: string; signIn: (email: string, password: string) => Promise<void>; signOut: () => Promise<void> };
export const AuthContext = createContext<AuthState | null>(null);
export function useAuthState(): AuthState { const value = useContext(AuthContext); if (!value) throw new Error("missing auth provider"); return value; }

export type RouterContext = { queryClient: QueryClient };
