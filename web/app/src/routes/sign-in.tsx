import { useEffect, useMemo, useRef } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { SignIn } from "@/pages/SignIn";
import { bootstrapStatusQuery, oidcStatusQuery } from "@/api";
import { useAuthState } from "@/router/context";
import { validateSearch } from "@/router/search";
type InternalTo = "/" | "/runs" | "/deployments" | "/runners" | "/operations" | "/approvals" | "/projects" | "/templates" | "/logs" | "/audit" | "/settings" | "/runs/$runId" | "/deployments/$deploymentId" | "/runners/$runnerId";
export type InternalDestination = { to: InternalTo; params?: { runId?: string; deploymentId?: string; runnerId?: string }; search?: { q: string } };
export function parseInternalDestination(value: string | undefined): InternalDestination {
  if (!value || !value.startsWith("/") || value.startsWith("//") || value.includes("\\") || value.includes("%")) return { to: "/" };
  const parsed = new URL(value, window.location.origin);
  if (parsed.origin !== window.location.origin || parsed.pathname === "/sign-in") return { to: "/" };
  const query = parsed.searchParams.get("q");
  const search = query && query.trim() ? { q: query.slice(0, 200) } : undefined;
  const staticPaths = new Set<InternalTo>(["/", "/runs", "/deployments", "/runners", "/operations", "/approvals", "/projects", "/templates", "/logs", "/audit", "/settings"]);
  if (staticPaths.has(parsed.pathname as InternalTo)) return { to: parsed.pathname as InternalTo, search };
  const run = /^\/runs\/([A-Za-z0-9._:-]+)$/.exec(parsed.pathname);
  if (run) return { to: "/runs/$runId", params: { runId: run[1]! }, search };
  const deployment = /^\/deployments\/([A-Za-z0-9._:-]+)$/.exec(parsed.pathname);
  if (deployment) return { to: "/deployments/$deploymentId", params: { deploymentId: deployment[1]! }, search };
  const runner = /^\/runners\/([A-Za-z0-9._:-]+)$/.exec(parsed.pathname);
  return runner ? { to: "/runners/$runnerId", params: { runnerId: runner[1]! }, search } : { to: "/" };
}
export function validatedInternalRedirect(value: string | undefined): string {
  if (!value || !value.startsWith("/") || value.startsWith("//") || value.includes("\\") || value.includes("%")) return "/";
  const parsed = new URL(value, window.location.origin);
  const destination = parseInternalDestination(value);
  if (destination.to === "/" && parsed.pathname !== "/") return "/";
  return value;
}
export const Route = createFileRoute("/sign-in")({ validateSearch, component: SignInRoute });
function SignInRoute() {
  const auth = useAuthState();
  const navigate = useNavigate();
  const { redirect, oidc_error: oidcError } = Route.useSearch() as { redirect?: string; oidc_error?: "failed" };
  const destination = useMemo(() => parseInternalDestination(redirect), [redirect]);
  const bootstrap = useQuery(bootstrapStatusQuery());
  const oidc = useQuery(oidcStatusQuery());
  const redirected = useRef(false);
  useEffect(() => {
    if (auth.authenticated === true && !redirected.current) {
      redirected.current = true;
      void navigate(destination as never);
    }
  }, [auth.authenticated, destination, navigate]);
  if (auth.authenticated === null) return <main className="min-h-screen bg-background" aria-label="Checking session" />;
  if (auth.authenticated === true) return <main className="min-h-screen bg-background" aria-label="Redirecting after sign in" />;
  // Until the tiny public status is known, retain the CLI-only guidance. A
  // transient status failure must never imply that browser bootstrap exists.
  const bootstrapRequired = bootstrap.data?.status !== "complete";
  const oidcHref = `/api/v1/oidc/login?redirect=${encodeURIComponent(validatedInternalRedirect(redirect))}`;
  return <SignIn error={auth.authError} bootstrapRequired={bootstrapRequired} oidcEnabled={!bootstrapRequired && oidc.data?.enabled === true} oidcError={oidcError === "failed"} oidcHref={oidcHref} onSubmit={async (email, password) => { try { await auth.signIn(email, password); } catch { /* useAuth retains the rendered authError and route stays put */ } }} />;
}
