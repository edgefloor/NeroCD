import {
  databaseBackupDownloadPath,
  getDatabaseBackupDownloadOptions,
  type DatabaseDownloadContext,
} from "@/api/hosting-database-backup-download";
import type { DatabaseBackup } from "@/api/hosting-database-backups";
import { SavedBackupDownload } from "./SavedBackupDownload";
const reasons = {
  disabled: "Database backup downloads are disabled.",
  backup_not_succeeded: "Only successful backups can be downloaded.",
  local_copy_unavailable:
    "The local database backup copy is unavailable. A remote copy is not downloaded automatically.",
  copy_busy: "The local database backup copy is busy. Try again later.",
  runner_unavailable: "The backup runner is unavailable. Try again later.",
  placement_changed:
    "The backup placement has changed. Its local copy is unavailable here.",
};
type Props = {
  context: DatabaseDownloadContext;
  backup: DatabaseBackup;
  owner: boolean;
  blocked: boolean;
  onDenied: () => void;
};
export function DatabaseBackupDownload({
  context,
  backup,
  owner,
  blocked,
  onDenied,
}: Props) {
  if (!owner || blocked || backup.mode !== "backup") return null;
  if (backup.state !== "succeeded")
    return (
      <p className="text-sm">Only successful backups can be downloaded.</p>
    );
  const identity = JSON.stringify([
    context,
    backup.id,
    backup.project_id,
    backup.source_resource_id,
    backup.backup_id,
    backup.version,
    backup.mode,
    backup.state,
    backup.blob,
  ]);
  return (
    <section aria-label={"Download for database backup " + backup.id}>
      <SavedBackupDownload
        key={identity}
        identity={identity}
        path={databaseBackupDownloadPath(backup.id)}
        noun="database backup"
        title="Database backup"
        reasons={reasons}
        load={(signal) =>
          getDatabaseBackupDownloadOptions(context, backup, signal)
        }
        onDenied={onDenied}
      />
    </section>
  );
}
