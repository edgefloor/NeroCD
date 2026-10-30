import {
  archiveDownloadPath,
  getArchiveDownloadOptions,
} from "@/api/hosting-storage-archive-download";
import type { ArchiveScope, Backup } from "@/api/hosting-storage-archives";
import { SavedBackupDownload } from "./SavedBackupDownload";

const reasons: Record<string, string> = {
  disabled: "Archive downloads are disabled.",
  runner_unavailable: "The archive runner is unavailable. Try again later.",
  local_copy_unavailable: "The local archive copy is unavailable.",
  backup_not_succeeded: "Only successful backups can be downloaded.",
};
type Props = {
  scope: ArchiveScope;
  backup: Backup;
  owner: boolean;
  blocked: boolean;
  onDenied: () => void;
};
export function StorageArchiveDownload(props: Props) {
  const { scope, backup, owner, blocked } = props;
  if (!owner || blocked) return null;
  if (backup.state !== "succeeded")
    return (
      <p className="text-sm">Only successful backups can be downloaded.</p>
    );
  const identity = JSON.stringify([
    scope.actor,
    scope.project,
    scope.resource,
    scope.destination,
    scope.binding,
    backup.id,
    backup.project_id,
    backup.resource_id,
    backup.state,
    backup.blob,
  ]);
  return (
    <SavedBackupDownload
      key={identity}
      identity={identity}
      path={archiveDownloadPath(backup.id)}
      noun="archive"
      title="Archive"
      reasons={reasons}
      load={(signal) => getArchiveDownloadOptions(scope, backup, signal)}
      onDenied={props.onDenied}
    />
  );
}
