import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError } from "@/api/errors";
import type { Resource } from "@/api/hosting";
import type { ArchiveScope } from "@/api/hosting-storage-archives";
import { storageRequestID } from "@/api/hosting-storage";
import type { RemoteBackupTarget } from "@/api/hosting-database-backup-targets";
import {
  listStorageBackupSchedules,
  listStorageBackupOccurrences,
  saveStorageBackupSchedule,
  storageBackupSchedulesKey,
  validStorageBackupScheduleFields,
  type StorageBackupSchedule,
  type StorageBackupScheduleInput,
} from "@/api/hosting-storage-backup-schedules";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

type Attempt = { schedule: string; input: StorageBackupScheduleInput };
export function StorageBackupSchedules({
  scope,
  resource,
  owner,
  blocked,
  remoteTarget,
  onRemoteTarget,
  onSelectBackup,
  onDenied,
}: {
  scope: ArchiveScope;
  resource: Resource;
  owner: boolean;
  blocked: boolean;
  remoteTarget?: RemoteBackupTarget;
  onRemoteTarget: (v?: RemoteBackupTarget) => void;
  onSelectBackup: (v: string) => void;
  onDenied: () => void;
}) {
  const client = useQueryClient();
  const [selected, setSelected] = useState<StorageBackupSchedule>();
  const [frequency, setFrequency] = useState("daily");
  const [timezone, setTimezone] = useState("UTC");
  const [enabled, setEnabled] = useState(true);
  const [attempt, setAttempt] = useState<Attempt>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [revoked, setRevoked] = useState(false);
  const active = useRef<AbortController | null>(null);
  const key = storageBackupSchedulesKey(scope, resource.id);
  const schedules = useQuery({
    queryKey: key,
    queryFn: ({ signal }) =>
      listStorageBackupSchedules(scope, resource, signal),
    enabled: !blocked && !revoked,
    retry: false,
  });
  const history = useQuery({
    queryKey: [...key, selected?.id, "occurrences"],
    queryFn: ({ signal }) =>
      listStorageBackupOccurrences(scope, resource, selected!.id, signal),
    enabled: !!selected && !blocked && !revoked,
    retry: false,
    refetchInterval: 5000,
  });
  useEffect(() => () => active.current?.abort(), []);
  useEffect(() => {
    if (
      [schedules.error, history.error].some(
        (e) => e instanceof ApiError && [401, 403].includes(e.status),
      )
    ) {
      setRevoked(true);
      client.removeQueries({ queryKey: key });
      onDenied();
    }
  }, [schedules.error, history.error]);
  function select(v?: StorageBackupSchedule) {
    setSelected(v);
    setFrequency(v?.frequency ?? "daily");
    setTimezone(v?.timezone ?? "UTC");
    setEnabled(v?.enabled ?? true);
    onRemoteTarget(v?.remote_target);
    setError("");
  }
  async function submit(saved: Attempt) {
    if (busy || blocked || revoked) return;
    setAttempt(saved);
    setBusy(true);
    setError("");
    const control = new AbortController();
    active.current = control;
    try {
      const out = await saveStorageBackupSchedule(
        scope,
        resource,
        saved.schedule,
        saved.input,
        control.signal,
      );
      setAttempt(undefined);
      select(out);
      await client.invalidateQueries({ queryKey: key });
    } catch (e) {
      if (e instanceof ApiError && [401, 403].includes(e.status)) {
        setRevoked(true);
        client.removeQueries({ queryKey: key });
        onDenied();
      }
      setError(
        e instanceof ApiError && e.status === 409
          ? "The schedule or resource changed. Review current status before a separate submission. Your saved request is preserved."
          : "Schedule delivery or status is unknown. Retry the identical saved request to recover its result.",
      );
    } finally {
      active.current = null;
      setBusy(false);
    }
  }
  const locked = blocked || revoked || busy;
  return (
    <section
      aria-label="Recurring upload backups"
      className="min-w-0 space-y-2 border-t pt-3"
    >
      <h5 className="font-medium">Recurring upload backups</h5>
      <p>
        Each due backup preserves a local archive. The selected remote target is
        pinned to its revision. Disabling stops future discovery and leaves
        admitted backups running.
      </p>
      {schedules.isError || history.isError ? (
        <p role="alert">
          Schedule metadata is unavailable. Refresh current access and status.
        </p>
      ) : null}
      {!revoked && !schedules.isError
        ? (schedules.data ?? []).map((v) => (
            <p key={v.id} className="break-all">
              {v.id} · revision {v.revision} · {v.frequency} · {v.timezone} ·{" "}
              {v.enabled ? "enabled" : "disabled"}{" "}
              <Button
                type="button"
                disabled={locked || !!attempt}
                onClick={() => select(v)}
              >
                View schedule {v.id}
              </Button>
            </p>
          ))
        : null}
      {owner && !revoked ? (
        <form
          aria-label="Save recurring upload backup"
          onSubmit={(e) => {
            e.preventDefault();
            if (!attempt)
              void submit({
                schedule: selected?.id ?? "",
                input: {
                  request_id: storageRequestID(),
                  expected_resource_generation: resource.generation,
                  expected_version: selected?.revision ?? 0,
                  frequency,
                  timezone,
                  enabled,
                  ...(remoteTarget ? { remote_target: remoteTarget } : {}),
                },
              });
          }}
          className="space-y-2"
        >
          <label className="block">
            Backup frequency
            <Input
              aria-label="Backup frequency"
              value={frequency}
              maxLength={128}
              disabled={locked || !!attempt}
              onChange={(e) => setFrequency(e.target.value)}
            />
          </label>
          <label className="block">
            Backup timezone
            <Input
              aria-label="Backup timezone"
              value={timezone}
              maxLength={128}
              disabled={locked || !!attempt}
              onChange={(e) => setTimezone(e.target.value)}
            />
          </label>
          <label className="flex gap-2">
            <input
              type="checkbox"
              checked={enabled}
              disabled={locked || !!attempt}
              onChange={(e) => setEnabled(e.target.checked)}
            />
            Enabled
          </label>
          <p>
            Remote target for this submission:{" "}
            {remoteTarget
              ? `${remoteTarget.target_id} revision ${remoteTarget.revision}`
              : "local only"}
            . Review the remote target selector above.
          </p>
          <Button
            type="submit"
            disabled={
              locked ||
              !!attempt ||
              !validStorageBackupScheduleFields(frequency, timezone)
            }
          >
            {selected ? "Save schedule changes" : "Create backup schedule"}
          </Button>{" "}
          <Button
            type="button"
            variant="outline"
            disabled={locked || !!attempt}
            onClick={() => select()}
          >
            New schedule
          </Button>
          {selected?.enabled ? (
            <Button
              type="button"
              disabled={locked || !!attempt}
              onClick={() =>
                void submit({
                  schedule: selected.id,
                  input: {
                    request_id: storageRequestID(),
                    expected_resource_generation: resource.generation,
                    expected_version: selected.revision,
                    frequency: selected.frequency,
                    timezone: selected.timezone,
                    enabled: false,
                    ...(selected.remote_target
                      ? { remote_target: selected.remote_target }
                      : {}),
                  },
                })
              }
            >
              Disable schedule
            </Button>
          ) : null}
        </form>
      ) : null}
      {attempt ? (
        <div>
          <p className="break-all">
            Saved schedule request {attempt.input.request_id}. Inputs remain
            frozen for retry.
          </p>
          <Button
            type="button"
            disabled={locked}
            onClick={() => void submit(attempt)}
          >
            Retry identical schedule request
          </Button>
          <Button
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() => {
              setAttempt(undefined);
              setError("");
            }}
          >
            Discard saved schedule request
          </Button>
          <p>
            Discarding does not cancel a request accepted by the server. Review
            history before another submission.
          </p>
        </div>
      ) : null}
      {!revoked && !history.isError
        ? (history.data ?? []).map((v) => (
            <p key={v.id} className="break-all">
              {v.scheduled_at} · revision {v.schedule_revision} · {v.state}
              {v.reason ? ` · ${v.reason}` : ""} · ownership{" "}
              {v.ownership_held ? "held" : "not held"}
              {v.operation_id ? ` · operation ${v.operation_id}` : ""}
              {v.backup_id ? (
                <Button
                  type="button"
                  onClick={() => onSelectBackup(v.backup_id!)}
                >
                  View scheduled archive {v.backup_id}
                </Button>
              ) : null}
            </p>
          ))
        : null}
      {error ? <p role="alert">{error}</p> : null}
    </section>
  );
}
