import "./styles.css";
import { createRoot } from "react-dom/client";
import { RouterProvider, createRouter } from "@tanstack/react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { routeTree } from "./routeTree.gen";
import { useAuth } from "./hooks/useApi";
import { AuthContext } from "./router/context";
import { retryRemote } from "./api/queries";

const queryClient = new QueryClient({ defaultOptions: { queries: { retry: retryRemote }, mutations: { retry: false } } });
const router = createRouter({ routeTree, defaultPreload: "intent", context: { queryClient } });
declare module "@tanstack/react-router" { interface Register { router: typeof router } }
function App() { const auth = useAuth(); return <AuthContext.Provider value={auth}><RouterProvider router={router} /></AuthContext.Provider>; }
const root = document.querySelector<HTMLDivElement>("#app");
if (!root) throw new Error("missing #app root");
createRoot(root).render(<QueryClientProvider client={queryClient}><App /></QueryClientProvider>);
