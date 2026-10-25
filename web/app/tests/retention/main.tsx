import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { DatabaseBackupRetention } from "../../src/pages/DatabaseBackupRetention";
import type { HostingDatabase } from "../../src/api/hosting-databases";
import "../../src/styles.css";
const database = {
  resource: { id: "database", generation: 2 },
  engine: "postgresql",
} as HostingDatabase;
const owner = new URLSearchParams(location.search).get("role") !== "viewer";
createRoot(document.getElementById("root")!).render(
  <QueryClientProvider client={new QueryClient()}>
    <div className="min-w-0 p-4">
      <DatabaseBackupRetention
        database={database}
        scope={{
          actor: owner ? "owner" : "viewer",
          project: "project",
          environment: "environment",
        }}
        owner={owner}
        blocked={false}
        onDenied={() => {}}
      />
    </div>
  </QueryClientProvider>,
);
