import { ApiError } from "@/api/errors";
import type { Resource } from "@/api/hosting";
import { storageRequestID } from "@/api/hosting-storage";
import type { ArchiveScope } from "@/api/hosting-storage-archives";
import type { DeletionExecution } from "@/api/hosting-storage-backup-deletion";
import {
  executeDeletion,
  listDeletions,
  previewDeletion,
  type DeletionInput,
  type DeletionPreview,
  type DeletionPreviewInput,
} from "@/api/hosting-storage-backup-deletion";
import type { RetentionCopy } from "@/api/hosting-storage-backup-retention";
import { BackupAccessBoundary } from "@/components/hosting/BackupAccessBoundary";
import { Button } from "@/components/ui/button";
import type { UseQueryResult } from "@tanstack/react-query";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
type Attempt =
  | { kind: "preview"; body: DeletionPreviewInput }
  | { kind: "execute"; body: DeletionInput };
type StorageBackupDeletionProps = {
  scope: ArchiveScope;
  resource: Resource;
  backup: string;
  copies: (Omit<RetentionCopy, "protected_reasons"> & {
    protected_reasons: string[];
  })[];
  owner: boolean;
  blocked: boolean;
  onDenied: () => void;
};
export function StorageBackupDeletion(props: StorageBackupDeletionProps) {
  return (
    <BackupAccessBoundary
      key={JSON.stringify([props.scope, props.backup])}
      onDenied={props.onDenied}
      message="Deletion access denied. Reload to verify current access."
    >
      {(onDenied) => (
        <StorageBackupDeletionSession {...props} onDenied={onDenied} />
      )}
    </BackupAccessBoundary>
  );
}
function StorageBackupDeletionSession({
  scope,
  resource,
  backup,
  copies,
  owner,
  blocked,
  onDenied,
}: StorageBackupDeletionProps) {
  const client = useQueryClient();
  const [selected, setSelected] = useState<string[]>([]);
  const [preview, setPreview] = useState<DeletionPreview>();
  const [attempt, setAttempt] = useState<Attempt>();
  const [resumeConfirmed, setResumeConfirmed] = useState<
    Record<string, boolean>
  >({});
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const lock = useRef(false);
  const active = useRef(true);
  const controller = useRef<AbortController | undefined>(undefined);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      controller.current?.abort();
    };
  }, []);
  const history = useQuery({
    queryKey: [
      "storageBackupDeletions",
      scope.actor,
      scope.project,
      scope.resource,
      backup,
    ],
    enabled: !blocked,
    queryFn: ({ signal }) => listDeletions(scope, resource, backup, signal),
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    refetchInterval: blocked ? false : 3000,
  });
  useEffect(() => {
    if (
      history.error instanceof ApiError &&
      [401, 403].includes(history.error.status)
    ) {
      onDenied();
    }
  }, [history.error, onDenied]);
  async function submit(next: Attempt) {
    if (!owner || blocked || lock.current) return;
    lock.current = true;
    setBusy(true);
    setAttempt(next);
    setError("");
    const c = new AbortController();
    controller.current = c;
    try {
      if (next.kind === "preview") {
        const result = await previewDeletion(
          scope,
          resource,
          backup,
          next.body,
          c.signal,
        );
        if (active.current) {
          setPreview(result);
          setConfirmed(false);
          setAttempt(undefined);
        }
      } else {
        await executeDeletion(scope, resource, backup, next.body, c.signal);
        if (active.current) {
          setAttempt(undefined);
          setPreview(undefined);
          setSelected([]);
          await client.invalidateQueries({ queryKey: ["storageArchives"] });
          await history.refetch();
        }
      }
    } catch (e) {
      if (!active.current) return;
      if (e instanceof ApiError && [401, 403].includes(e.status)) {
        setPreview(undefined);
        onDenied();
        setError("Deletion access denied. Reload to verify current access.");
      } else if (e instanceof ApiError && e.status === 409)
        setError(
          "Copy protection, recovery availability or authority changed. Review current history before a separate request.",
        );
      else
        setError(
          "Delivery is unknown. Retry the identical saved request to recover its result.",
        );
    } finally {
      lock.current = false;
      if (active.current) setBusy(false);
    }
  }
  const selectedIDs = new Set(selected);
  const disabled = blocked || busy || !!history.error;
  return (
    <section
      aria-label="Delete selected backup copies"
      className="min-w-0 space-y-2 break-all"
    >
      <h5>Delete backup copies</h5>
      <p>
        Choose physical copies from this backup. Backup history, transfer
        records and audit evidence remain available.
      </p>
      <BackupDeletionActions
        owner={owner}
        copies={copies}
        disabled={disabled}
        attempt={attempt}
        preview={preview}
        selectedIDs={selectedIDs}
        setSelected={setSelected}
        selected={selected}
        submit={submit}
        resource={resource}
        confirmed={confirmed}
        setConfirmed={setConfirmed}
        busy={busy}
        setAttempt={setAttempt}
        setPreview={setPreview}
        setError={setError}
      />
      {error ? <p role="alert">{error}</p> : null}
      {history.error ? (
        <p role="alert">
          Deletion history is unavailable. Refresh current access before
          proceeding.
        </p>
      ) : null}
      <BackupDeletionHistory
        history={history}
        owner={owner}
        resumeConfirmed={resumeConfirmed}
        disabled={disabled}
        attempt={attempt}
        setResumeConfirmed={setResumeConfirmed}
        preview={preview}
        submit={submit}
      />
    </section>
  );
}

function BackupDeletionHistory({
  history,
  owner,
  resumeConfirmed,
  disabled,
  attempt,
  setResumeConfirmed,
  preview,
  submit,
}: {
  history: UseQueryResult<DeletionExecution[], Error>;
  owner: boolean;
  resumeConfirmed: Record<string, boolean>;
  disabled: boolean;
  attempt: Attempt | undefined;
  setResumeConfirmed: React.Dispatch<
    React.SetStateAction<Record<string, boolean>>
  >;
  preview: DeletionPreview | undefined;
  submit: (next: Attempt) => Promise<void>;
}) {
  return (
    <div aria-label="Backup copy deletion history">
      {history.data?.map((v) => (
        <div key={v.id} className="space-y-1">
          <p>
            Deletion {v.id}: {v.state}. Local: {v.local_state}. Remote:{" "}
            {v.remote_state}.
          </p>
          {v.items.map((c) => (
            <p key={c.copy_id}>
              {c.location} {c.copy_id}: {c.state}
              {c.failure_code ? ` · ${c.failure_code}` : ""}
              {c.reservation_held ? " · recovery hold retained" : ""}
            </p>
          ))}
          {owner &&
          v.reservation_held &&
          ["failed", "uncertain"].includes(v.state) ? (
            <>
              {v.final_copy ? (
                <label className="flex gap-2">
                  <input
                    type="checkbox"
                    checked={!!resumeConfirmed[v.id]}
                    disabled={disabled || !!attempt}
                    onChange={(e) =>
                      setResumeConfirmed((values) => ({
                        ...values,
                        [v.id]: e.target.checked,
                      }))
                    }
                  />
                  <span>
                    I confirm resuming deletion of the final recoverable copy
                    for {v.id}.
                  </span>
                </label>
              ) : null}
              <Button
                disabled={
                  disabled ||
                  !!attempt ||
                  !!preview ||
                  (!!v.final_copy && !resumeConfirmed[v.id])
                }
                onClick={() =>
                  void submit({
                    kind: "execute",
                    body: {
                      request_id: storageRequestID(),
                      preview_id: v.preview_id,
                      expected_plan_hash: v.plan_hash,
                      confirm_final_copy: !!resumeConfirmed[v.id],
                    },
                  })
                }
              >
                Resume the same deletion selection
              </Button>
            </>
          ) : null}
        </div>
      ))}
    </div>
  );
}

function BackupDeletionPreview({
  preview,
  confirmed,
  disabled,
  attempt,
  setConfirmed,
  submit,
}: {
  preview: DeletionPreview;
  confirmed: boolean;
  disabled: boolean;
  attempt: Attempt | undefined;
  setConfirmed: React.Dispatch<React.SetStateAction<boolean>>;
  submit: (next: Attempt) => Promise<void>;
}) {
  return (
    <div aria-label="Review exact deletion selection" className="space-y-2">
      {preview.items.map((c) => (
        <p key={c.copy_id}>
          {c.location} copy {c.copy_id} · {c.digest} · {c.bytes} bytes
        </p>
      ))}
      <p>Plan {preview.plan_hash}</p>
      {preview.final_copy ? (
        <label className="flex gap-2">
          <input
            type="checkbox"
            checked={confirmed}
            disabled={disabled || !!attempt}
            onChange={(e) => setConfirmed(e.target.checked)}
          />
          <span>
            I understand this deletes the final recoverable copy of this backup.
          </span>
        </label>
      ) : (
        <p>A recoverable copy remains according to the reviewed inventory.</p>
      )}
      <Button
        disabled={disabled || !!attempt || (!!preview.final_copy && !confirmed)}
        onClick={() =>
          void submit({
            kind: "execute",
            body: {
              request_id: storageRequestID(),
              preview_id: preview.id,
              expected_plan_hash: preview.plan_hash,
              confirm_final_copy: confirmed,
            },
          })
        }
      >
        Permanently delete reviewed copies
      </Button>
    </div>
  );
}

function BackupCopySelection({
  copies,
  disabled,
  attempt,
  preview,
  selectedIDs,
  setSelected,
}: {
  copies: (Omit<RetentionCopy, "protected_reasons"> & {
    protected_reasons: string[];
  })[];
  disabled: boolean;
  attempt: Attempt | undefined;
  preview: DeletionPreview | undefined;
  selectedIDs: Set<string>;
  setSelected: React.Dispatch<React.SetStateAction<string[]>>;
}) {
  return (
    <>
      {copies.map((c) => (
        <label key={c.copy_id} className="flex items-start gap-2">
          <input
            type="checkbox"
            aria-label={`Delete ${c.location} copy ${c.copy_id}`}
            disabled={
              disabled ||
              !!attempt ||
              !!preview ||
              c.availability !== "available" ||
              !!c.protected_reasons.length
            }
            checked={selectedIDs.has(c.copy_id)}
            onChange={(e) =>
              setSelected((ids) =>
                e.target.checked
                  ? [...ids, c.copy_id]
                  : ids.filter((id) => id !== c.copy_id),
              )
            }
          />
          <span>
            {c.location} copy {c.copy_id}: {c.availability}
            {c.protected_reasons.length
              ? ` · ${c.protected_reasons.join(", ")}`
              : ""}
          </span>
        </label>
      ))}
    </>
  );
}

function BackupDeletionActions({
  owner,
  copies,
  disabled,
  attempt,
  preview,
  selectedIDs,
  setSelected,
  selected,
  submit,
  resource,
  confirmed,
  setConfirmed,
  busy,
  setAttempt,
  setPreview,
  setError,
}: {
  owner: boolean;
  copies: (Omit<RetentionCopy, "protected_reasons"> & {
    protected_reasons: string[];
  })[];
  disabled: boolean;
  attempt: Attempt | undefined;
  preview: DeletionPreview | undefined;
  selectedIDs: Set<string>;
  setSelected: React.Dispatch<React.SetStateAction<string[]>>;
  selected: string[];
  submit: (next: Attempt) => Promise<void>;
  resource: Resource;
  confirmed: boolean;
  setConfirmed: React.Dispatch<React.SetStateAction<boolean>>;
  busy: boolean;
  setAttempt: React.Dispatch<React.SetStateAction<Attempt | undefined>>;
  setPreview: React.Dispatch<React.SetStateAction<DeletionPreview | undefined>>;
  setError: React.Dispatch<React.SetStateAction<string>>;
}) {
  return (
    <>
      {owner ? (
        <>
          <BackupCopySelection
            copies={copies}
            disabled={disabled}
            attempt={attempt}
            preview={preview}
            selectedIDs={selectedIDs}
            setSelected={setSelected}
          />
          <Button
            disabled={disabled || !!attempt || !!preview || !selected.length}
            onClick={() =>
              void submit({
                kind: "preview",
                body: {
                  request_id: storageRequestID(),
                  expected_resource_generation: resource.generation,
                  copy_ids: selected,
                },
              })
            }
          >
            Review selected copy deletion
          </Button>
          {preview ? (
            <BackupDeletionPreview
              preview={preview}
              confirmed={confirmed}
              disabled={disabled}
              attempt={attempt}
              setConfirmed={setConfirmed}
              submit={submit}
            />
          ) : null}
          {attempt ? (
            <>
              <p>
                Saved {attempt.kind} request {attempt.body.request_id}.
                Selection is frozen for retry.
              </p>
              <Button disabled={disabled} onClick={() => void submit(attempt)}>
                Retry identical deletion request
              </Button>
            </>
          ) : null}
          {preview || attempt ? (
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => {
                setAttempt(undefined);
                setPreview(undefined);
                setConfirmed(false);
                setSelected([]);
                setError("");
              }}
            >
              Discard saved request and review current copies
            </Button>
          ) : null}
          {preview || attempt ? (
            <p>
              Discarding this saved request does not cancel work already
              accepted by the server. Check deletion history before submitting
              again.
            </p>
          ) : null}
        </>
      ) : null}
    </>
  );
}
