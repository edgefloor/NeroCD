import { Button } from "@/components/ui/button";
import type { StorageBackupTransfer } from "@/api/hosting-storage-backup-remote";
export type StorageTransferRetry = {
  backup: string;
  input: { request_id: string; expected_transfer_id: string };
};
export function StorageArchiveTransfers({
  transfers,
  selected,
  owner,
  blocked,
  remoteBusy,
  remoteRetry,
  onRetry,
  allowDownload = false,
  onRetrySaved,
}: {
  transfers?: StorageBackupTransfer[];
  selected: string;
  owner: boolean;
  blocked: boolean;
  remoteBusy: boolean;
  remoteRetry?: StorageTransferRetry;
  onRetry: (v: StorageBackupTransfer) => void;
  allowDownload?: boolean;
  onRetrySaved?: () => void;
}) {
  const remoteUpload = transfers?.find((v) => v.mode === "upload");
  return (
    <section
      aria-label="Remote archive transfers"
      className="space-y-2 break-all"
    >
      <h5>Remote archive transfers</h5>
      {transfers?.length === 0 ? <p>No remote transfer recorded.</p> : null}
      {transfers?.map((v) => (
        <p key={v.id}>
          {v.mode} {v.id}: {v.state}
          {v.failure_code ? ` · ${v.failure_code}` : ""}. Local archive remains
          independent.
          {owner &&
          allowDownload &&
          v.mode === "download" &&
          ["failed", "uncertain"].includes(v.state) ? (
            <>
              <span>
                {" "}
                Restore remains held until a verified download completes.
              </span>
              <Button
                className="h-auto max-w-full whitespace-normal break-all"
                disabled={
                  blocked ||
                  remoteBusy ||
                  (!!remoteRetry &&
                    (remoteRetry.backup !== selected ||
                      remoteRetry.input.expected_transfer_id !== v.id))
                }
                onClick={() => void onRetry(v)}
              >
                Retry remote download
              </Button>
            </>
          ) : null}
        </p>
      ))}
      {owner &&
      remoteUpload &&
      ["failed", "uncertain"].includes(remoteUpload.state) ? (
        <Button
          className="h-auto max-w-full whitespace-normal break-all"
          disabled={
            blocked ||
            remoteBusy ||
            (!!remoteRetry &&
              (remoteRetry.backup !== selected ||
                remoteRetry.input.expected_transfer_id !== remoteUpload.id))
          }
          onClick={() => void onRetry(remoteUpload)}
        >
          Retry remote upload of the same archive
        </Button>
      ) : null}
      {remoteRetry && owner && onRetrySaved ? (
        <Button
          className="h-auto max-w-full whitespace-normal break-all"
          disabled={blocked || remoteBusy}
          onClick={onRetrySaved}
        >
          Retry identical remote archive request
        </Button>
      ) : null}
      {remoteRetry ? (
        <p>
          Saved remote retry {remoteRetry.input.request_id} for transfer{" "}
          {remoteRetry.input.expected_transfer_id} in backup{" "}
          {remoteRetry.backup}. Retry that transfer to keep the same archive and
          request identity.
        </p>
      ) : null}
    </section>
  );
}
