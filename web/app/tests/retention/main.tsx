import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StorageBackupDeletion } from "../../src/components/hosting/StorageBackupDeletion";
import type { Resource } from "../../src/api/hosting";
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
      {new URLSearchParams(location.search).get("panel") === "deletion" ? (
        <StorageBackupDeletion
          scope={{
            actor: owner ? "owner" : "viewer",
            project: "project",
            resource: "resource",
            binding: "binding",
            destination: "destination",
          }}
          resource={
            { id: "resource", project_id: "project", generation: 2 } as Resource
          }
          backup="backup"
          copies={[
            {
              copy_id: "local",
              backup_id: "backup",
              location: "local",
              digest: `sha256:${"a".repeat(64)}`,
              bytes: 1024,
              created_at: "2026-10-26T12:00:00Z",
              availability: "available",
              selected: false,
              protected_reasons: [],
            },
          ]}
          owner={owner}
          blocked={false}
          onDenied={() => {}}
        />
      ) : (
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
      )}
    </div>
  </QueryClientProvider>,
);
