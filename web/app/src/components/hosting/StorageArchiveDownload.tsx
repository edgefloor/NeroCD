import { useEffect, useRef, useState } from "react";
import { ApiError } from "@/api/errors";
import {
  archiveDownloadPath,
  getArchiveDownloadOptions,
} from "@/api/hosting-storage-archive-download";
import type { ArchiveScope, Backup } from "@/api/hosting-storage-archives";
import { Button } from "@/components/ui/button";

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
    backup.state,
    backup.blob,
  ]);
  return <DownloadOptions key={identity} {...props} />;
}
function DownloadOptions({ scope, backup, onDenied }: Props) {
  const identity = JSON.stringify([
    scope.actor,
    scope.project,
    scope.resource,
    scope.destination,
    scope.binding,
    backup.id,
    backup.state,
    backup.blob,
  ]);
  const [result, setResult] = useState<{
    identity: string;
    available: boolean;
    message: string;
  }>();
  const [refresh, setRefresh] = useState(0);
  const deny = useRef(onDenied);
  deny.current = onDenied;
  useEffect(() => {
    const controller = new AbortController();
    setResult(undefined);
    void getArchiveDownloadOptions(scope, backup, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted)
          setResult({
            identity,
            available: value.available,
            message: reasons[value.reason_code] ?? "",
          });
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        if (error instanceof ApiError && [401, 403].includes(error.status))
          deny.current();
        const message =
          error instanceof ApiError && [401, 403].includes(error.status)
            ? "Archive download access is unavailable. Reload to verify access."
            : error instanceof ApiError && error.status === 404
              ? "The local archive copy is unavailable."
              : "Archive download availability could not be verified. Try again.";
        setResult({ identity, available: false, message });
      });
    return () => controller.abort();
    // Primitive identity includes every authority and archive field used by the request.
  }, [identity, refresh]);
  const current = result?.identity === identity ? result : undefined;
  return (
    <div className="space-y-2 text-sm">
      {current?.available ? (
        <>
          <Button
            asChild
            variant="outline"
            className="h-auto max-w-full whitespace-normal"
          >
            <a
              href={archiveDownloadPath(backup.id)}
              target="_blank"
              rel="noopener"
            >
              Download archive
            </a>
          </Button>
          <p>
            The browser saves the archive. If its local copy or access changes,
            the download opens an error in a new tab.
          </p>
        </>
      ) : (
        <>
          <Button
            variant="outline"
            disabled
            className="h-auto max-w-full whitespace-normal"
          >
            Download archive
          </Button>
          <p role="status">
            {current?.message ?? "Checking archive download availability…"}
          </p>
        </>
      )}
      {current ? (
        <Button
          variant="outline"
          className="h-auto max-w-full whitespace-normal"
          onClick={() => {
            setResult(undefined);
            setRefresh((value) => value + 1);
          }}
        >
          Refresh download availability
        </Button>
      ) : null}
    </div>
  );
}
