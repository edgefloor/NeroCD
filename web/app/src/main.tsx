import "./styles.css";
import { createRoot } from "react-dom/client";
import { useEffect } from "react";
import { RouterProvider, createRouter, useRouterState } from "@tanstack/react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { routeTree } from "./routeTree.gen";
import { useAuth } from "./hooks/useApi";
import { ThemeProvider } from "./hooks/useTheme";
import { AuthContext } from "./router/context";
import { navigationForView, viewForPath } from "./router/metadata";
import { retryRemote } from "./api/queries";
import { PageUnavailable } from "./components/common/PageUnavailable";
import { PageNotFound } from "./components/common/PageNotFound";

const queryClient = new QueryClient({ defaultOptions: { queries: { retry: retryRemote }, mutations: { retry: false } } });
const router = createRouter({ routeTree, defaultPreload: "intent", scrollRestoration: true, defaultErrorComponent: PageUnavailable, defaultNotFoundComponent: PageNotFound, context: { queryClient } });
declare module "@tanstack/react-router" { interface Register { router: typeof router } }
function App() {
  const auth = useAuth();
  const pageTitle = useRouterState({ router, select: (state) => {
    if (state.matches.some((match) => match._notFound || match.status === "notFound")) return "Page not found";
    const pathname = state.location.pathname.replace(/\/+$/, "") || "/";
    if (pathname === "/sign-in") return "Sign in";
    return navigationForView(viewForPath(pathname)).label;
  } });
  useEffect(() => { document.title = `${pageTitle} · NeroCD`; }, [pageTitle]);
  return <AuthContext.Provider value={auth}><RouterProvider router={router} /></AuthContext.Provider>;
}
const root = document.querySelector<HTMLDivElement>("#app");
if (!root) throw new Error("missing #app root");
createRoot(root).render(<ThemeProvider><QueryClientProvider client={queryClient}><App /></QueryClientProvider></ThemeProvider>);
