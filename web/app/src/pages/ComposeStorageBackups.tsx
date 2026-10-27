import { useEffect, useId, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ApiError } from "@/api/errors";
import { storageRequestID } from "@/api/hosting-storage";
import {
  getComposeBackupVolumes,
  requestComposeBackup,
  type ComposeBackupInput,
} from "@/api/hosting-compose-storage-backups";
import {
  getArchiveBackup,
  getArchiveOperation,
  listArchiveBackups,
  requestArchiveRecovery,
  type ArchiveScope,
  type RecoveryInput,
} from "@/api/hosting-storage-archives";
import { OperationDetails } from "@/components/hosting/StorageArchives";
import { Button } from "@/components/ui/button";

const button = "h-auto max-w-full whitespace-normal break-all";
const terminal = (state?: string) =>
  ["succeeded", "failed", "canceled"].includes(state ?? "");
const denied = (e: unknown) =>
  e instanceof ApiError && [401, 403, 404].includes(e.status);
export function ComposeStorageBackups({
  scope,
  generation,
  owner,
  blocked,
  onDenied,
}: {
  scope: ArchiveScope;
  generation: number;
  owner: boolean;
  blocked: boolean;
  onDenied: () => void;
}) {
  const id = useId();
  const [offset, setOffset] = useState(0),
    [selected, setSelected] = useState(""),
    [volume, setVolume] = useState("");
  const [review, setReview] = useState<ComposeBackupInput>(),
    [sent, setSent] = useState(false),
    [ack, setAck] = useState(false),
    [conflict, setConflict] = useState(false);
  const [recovery, setRecovery] = useState<{
      operation: string;
      backup: string;
      body: RecoveryInput;
    }>(),
    [recovered, setRecovered] = useState(""),
    [resumeAck, setResumeAck] = useState(false);
  const [busy, setBusy] = useState(false),
    [lost, setLost] = useState(false),
    [error, setError] = useState("");
  const active = useRef(true),
    controller = useRef<AbortController | null>(null);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      controller.current?.abort();
    };
  }, []);
  useEffect(() => {
    if (blocked) controller.current?.abort();
  }, [blocked]);
  const key = [
    "composeStorageBackups",
    scope.actor,
    scope.project,
    scope.resource,
    scope.binding,
    scope.destination,
  ];
  const base = {
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  } as const;
  const choices = useQuery({
    ...base,
    queryKey: [...key, "volumes"],
    enabled: !lost,
    queryFn: ({ signal }) => getComposeBackupVolumes(scope, signal),
  });
  const history = useQuery({
    ...base,
    queryKey: [...key, "history", offset],
    enabled: !lost,
    queryFn: ({ signal }) => listArchiveBackups(scope, offset, signal),
  });
  const backup = useQuery({
    ...base,
    queryKey: [...key, "backup", selected],
    enabled: !!selected && !lost,
    queryFn: ({ signal }) => getArchiveBackup(scope, selected, signal),
    refetchInterval: (q) =>
      !q.state.error && !terminal(q.state.data?.state) ? 2000 : false,
  });
  const operationID = recovered || backup.data?.operation_id;
  const detail = useQuery({
    ...base,
    queryKey: [...key, "operation", operationID],
    enabled: !!operationID && !lost,
    queryFn: ({ signal }) =>
      getArchiveOperation(scope, operationID!, selected, signal),
    refetchInterval: (q) =>
      !q.state.error && !terminal(q.state.data?.state) ? 2000 : false,
  });
  function lose() {
    controller.current?.abort();
    setLost(true);
    onDenied();
  }
  useEffect(() => {
    if ([choices.error, history.error, backup.error, detail.error].some(denied))
      lose();
  }, [choices.error, history.error, backup.error, detail.error]);
  async function run(work: (signal: AbortSignal) => Promise<void>) {
    if (controller.current || lost || blocked) return;
    const c = new AbortController();
    controller.current = c;
    setBusy(true);
    setError("");
    try {
      await work(c.signal);
    } catch (e) {
      if (active.current) {
        if (denied(e)) lose();
        else {
          if (e instanceof ApiError && [400, 409].includes(e.status))
            setConflict(true);
          setError(
            e instanceof ApiError && [400, 409].includes(e.status)
              ? "Backup evidence changed or the request was rejected. Refresh and review a separate request. Check history first."
              : "Delivery or status is unconfirmed. Retry the identical saved request to recover its result. No automatic recovery will run.",
          );
        }
      }
    } finally {
      if (controller.current === c) controller.current = null;
      if (active.current) setBusy(false);
    }
  }
  async function prepare() {
    if (!owner || sent || blocked) return;
    await run(async (signal) => {
      const current = await getComposeBackupVolumes(scope, signal);
      if (!active.current || signal.aborted) return;
      const chosen = current.volumes.find(
        (v) => v.volume === volume && v.eligible,
      );
      if (
        !current.available ||
        !chosen ||
        !current.operation_id ||
        current.resource_generation !== generation
      ) {
        setReview(undefined);
        throw new ApiError(409, "Compose changed", null);
      }
      setConflict(false);
      setAck(false);
      setReview(
        Object.freeze({
          request_id: storageRequestID(),
          expected_resource_generation: current.resource_generation,
          expected_deployed_operation_id: current.operation_id,
          volume,
          acknowledge_interruption: true,
        }),
      );
    });
  }
  async function submit() {
    if (!review || !owner || !ack || conflict || blocked) return;
    await run(async (signal) => {
      setSent(true);
      const value = await requestComposeBackup(scope, review, signal);
      if (!active.current || signal.aborted) return;
      setSelected(value.id);
      setRecovered("");
      setReview(undefined);
      setSent(false);
      setAck(false);
      await history.refetch();
    });
  }
  async function resume() {
    const op = detail.data;
    if (!owner || !op || !resumeAck || blocked) return;
    const attempt = recovery ?? {
      operation: op.id,
      backup: selected,
      body: { request_id: storageRequestID(), expected_plan_hash: op.planHash },
    };
    setRecovery(attempt);
    await run(async (signal) => {
      const value = await requestArchiveRecovery(
        scope,
        attempt.operation,
        attempt.backup,
        attempt.body,
        signal,
      );
      if (!active.current || signal.aborted) return;
      setRecovered(value.id);
      setRecovery(undefined);
      setResumeAck(false);
    });
  }
  const disabled = busy || blocked || lost;
  if (lost)
    return (
      <p role="alert">
        Compose backup access is unavailable. Reload to verify access.
      </p>
    );
  return (
    <section
      aria-label="Compose volume backups"
      className="min-w-0 space-y-3 rounded-lg border p-3"
    >
      <h6 className="font-medium">Compose volume backups</h6>
      <p>
        Back up one local named volume. Its running writers stop while the
        archive is created, then resume. This interrupts service. Other volumes
        are excluded; this is not a coordinated database and uploads snapshot.
        No automatic rollback runs.
      </p>
      <Button
        className={button}
        variant="outline"
        disabled={busy}
        onClick={() => {
          if (!sent) {
            setReview(undefined);
            setConflict(false);
          }
          void choices.refetch();
          void history.refetch();
        }}
      >
        Refresh Compose backup evidence and history
      </Button>
      {choices.isError ? (
        <p role="alert">
          Current volume evidence is unavailable. Backup history remains
          accessible.
        </p>
      ) : null}
      {choices.data && !choices.isError ? (
        <>
          <p>
            Backup availability:{" "}
            {choices.data.available
              ? "available"
              : choices.data.reason || "unavailable"}
          </p>
          <label htmlFor={`${id}-volume`}>Named volume to back up</label>
          <select
            id={`${id}-volume`}
            className="w-full min-w-0 rounded border bg-card p-2"
            disabled={disabled || !!review}
            value={volume}
            onChange={(e) => setVolume(e.target.value)}
          >
            <option value="">Select a named volume</option>
            {choices.data.volumes.map((v) => (
              <option key={v.volume} value={v.volume} disabled={!v.eligible}>
                {v.volume}
                {!v.eligible ? ` · ${v.reason || "ineligible"}` : ""}
              </option>
            ))}
          </select>
          <ul className="min-w-0 space-y-1 break-all text-sm">
            {choices.data.volumes
              .filter((v) => v.volume === volume)
              .flatMap((v) =>
                v.mounts.map((m, i) => (
                  <li key={i}>
                    {m.component} · {m.mount_path} ·{" "}
                    {m.read_only ? "read only" : "writer"}
                  </li>
                )),
              )}
          </ul>
          {owner ? (
            <Button
              className={button}
              disabled={
                disabled ||
                sent ||
                !!review ||
                !volume ||
                !choices.data.available ||
                choices.isFetching
              }
              onClick={() => void prepare()}
            >
              Review Compose volume backup
            </Button>
          ) : (
            <p>
              Project owner access is required to request a backup or resume
              recovery.
            </p>
          )}
        </>
      ) : null}
      {review ? (
        <section
          aria-label="Reviewed Compose volume backup"
          className="min-w-0 space-y-2 break-all"
        >
          <p>
            Volume {review.volume} · generation{" "}
            {review.expected_resource_generation} · deployed operation{" "}
            {review.expected_deployed_operation_id}
          </p>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={ack}
              disabled={disabled || sent}
              onChange={(e) => setAck(e.target.checked)}
            />
            <span>
              I acknowledge interruption of this volume's writers while the
              local archive is created.
            </span>
          </label>
          <Button
            className={button}
            disabled={disabled || !owner || !ack || conflict}
            onClick={() => void submit()}
          >
            {sent
              ? "Retry identical Compose backup request"
              : "Confirm Compose volume backup"}
          </Button>
          <p className="text-xs">Saved request {review.request_id}</p>
          {!sent || conflict ? (
            <Button
              className={button}
              variant="outline"
              disabled={busy}
              onClick={() => {
                setReview(undefined);
                setSent(false);
                setConflict(false);
                setAck(false);
              }}
            >
              Discard review for a separate request
            </Button>
          ) : null}
        </section>
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
      {history.isError ? (
        <p role="alert">Backup history is unavailable.</p>
      ) : null}
      <ol
        aria-label="Compose volume backup history"
        className="min-w-0 space-y-2 break-all"
      >
        {history.data?.items.map((b) => (
          <li key={b.id}>
            <p>
              {b.volume || "Volume unavailable"} · {b.state} · {b.created_at}
            </p>
            <Button
              className={button}
              variant="outline"
              disabled={busy || !!recovery}
              onClick={() => {
                setSelected(b.id);
                setRecovered("");
                setRecovery(undefined);
                setResumeAck(false);
              }}
            >
              View volume backup {b.id}
            </Button>
          </li>
        ))}
      </ol>
      <div className="flex flex-wrap gap-2">
        <Button
          className={button}
          variant="outline"
          disabled={!offset || history.isFetching}
          onClick={() => setOffset(Math.max(0, offset - 20))}
        >
          Previous volume backups
        </Button>
        <Button
          className={button}
          variant="outline"
          disabled={
            !history.data ||
            offset + history.data.count >= history.data.total ||
            history.isFetching
          }
          onClick={() => setOffset(offset + 20)}
        >
          Next volume backups
        </Button>
      </div>
      {selected ? (
        <p role="status" className="break-all">
          Volume backup {selected}. Acceptance alone does not prove an archive
          or writer resumption.
        </p>
      ) : null}
      {backup.isError || detail.isError ? (
        <p role="alert">
          Selected backup evidence is unavailable. Refresh before recovery.
        </p>
      ) : null}
      {backup.data && !backup.isError ? (
        <div className="break-all text-sm">
          <p>
            Backup state: {backup.data.state}. Volume: {backup.data.volume}.
            Source deployment: {backup.data.source_deployed_operation_id}
          </p>
          {backup.data.blob ? (
            <p>
              Local artifact: {backup.data.blob.size_bytes} bytes ·{" "}
              {backup.data.blob.sha256} · {backup.data.blob.format}
            </p>
          ) : (
            <p>No local archive artifact is confirmed.</p>
          )}
        </div>
      ) : null}
      {detail.data && !detail.isError ? (
        <OperationDetails operation={detail.data} />
      ) : null}
      {owner &&
      detail.data &&
      !detail.isError &&
      (detail.data.mutationHold ||
        detail.data.ownershipHold ||
        detail.data.result?.uncertain ||
        detail.data.state === "reconciliation_required") &&
      detail.data.mode !== "recover" ? (
        <div className="space-y-2 break-all">
          <p>
            Recovery verifies the held plan and resumes its original writers. It
            does not repeat archive creation. Plan {detail.data.planHash}
          </p>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={resumeAck}
              disabled={disabled || !!recovery}
              onChange={(e) => setResumeAck(e.target.checked)}
            />
            <span>
              I reviewed the held operation and request writer resumption.
            </span>
          </label>
          <Button
            className={button}
            disabled={disabled || !resumeAck}
            onClick={() => void resume()}
          >
            {recovery
              ? "Retry identical Compose resume recovery"
              : "Request Compose writer resume recovery"}
          </Button>
        </div>
      ) : null}
    </section>
  );
}
