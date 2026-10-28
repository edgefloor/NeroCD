import { ApiError } from "@/api/errors";
import type { Resource } from "@/api/hosting";
import { getComposeBackupVolumes } from "@/api/hosting-compose-storage-backups";
import type { RemoteBackupTarget } from "@/api/hosting-database-backup-targets";
import { storageRequestID } from "@/api/hosting-storage";
import type { ArchiveScope } from "@/api/hosting-storage-archives";
import type { StorageBackupOccurrence } from "@/api/hosting-storage-backup-schedules";
import {
  listStorageBackupOccurrences,
  listStorageBackupSchedules,
  saveStorageBackupSchedule,
  storageBackupSchedulesKey,
  validStorageBackupScheduleFields,
  type StorageBackupSchedule,
  type StorageBackupScheduleInput,
} from "@/api/hosting-storage-backup-schedules";
import { readBackupMetadata } from "@/components/hosting/backup-access";
import { BackupAccessBoundary } from "@/components/hosting/BackupAccessBoundary";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  validSelectedBackupDestination,
  type useDatabaseBackupDestinations,
} from "@/pages/database-backup-destinations";
import { BackupDestinationSelect } from "@/pages/DatabaseBackupDestinations";
import type { UseQueryResult } from "@tanstack/react-query";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";

type Attempt = { schedule: string; input: StorageBackupScheduleInput };
type StorageBackupSchedulesProps = {
  scope: ArchiveScope;
  resource: Pick<Resource, "id" | "generation">;
  owner: boolean;
  blocked: boolean;
  remoteTarget?: RemoteBackupTarget;
  onRemoteTarget: (v?: RemoteBackupTarget) => void;
  onSelectBackup: (v: string) => void;
  onDenied: () => void;
  composeVolume?: string;
  remoteDestinations?: ReturnType<typeof useDatabaseBackupDestinations>;
};
export function StorageBackupSchedules(props: StorageBackupSchedulesProps) {
  return (
    <BackupAccessBoundary
      key={JSON.stringify([props.scope, props.resource.id])}
      onDenied={props.onDenied}
      message="Backup schedule access is unavailable."
    >
      {(onDenied) => (
        <StorageBackupSchedulesSession {...props} onDenied={onDenied} />
      )}
    </BackupAccessBoundary>
  );
}
function StorageBackupSchedulesSession(
  props: Parameters<typeof useStorageSchedule>[0],
) {
  const {
    title,
    compose,
    busy,
    schedules,
    selected,
    history,
    draftLocked,
    select,
    owner,
    attempt,
    review,
    prepare,
    frequency,
    setFrequency,
    timezone,
    setTimezone,
    enabled,
    setEnabled,
    remoteDestinations,
    remoteTarget,
    onRemoteTarget,
    composeVolume,
    submit,
    resource,
    ack,
    locked,
    setAck,
    setReview,
    setAttempt,
    setError,
    onSelectBackup,
    error,
  } = useStorageSchedule(props);

  return (
    <section
      aria-label={title}
      className="min-w-0 space-y-2 border-t pt-3 [&_button]:h-auto [&_button]:max-w-full [&_button]:whitespace-normal [&_button]:break-all"
    >
      <h5 className="font-medium">{title}</h5>
      <p>
        {compose
          ? "Each due backup archives the selected named volume and interrupts its running writers. Other volumes are excluded. The volume is fixed for this schedule; create a separate schedule to select another volume."
          : "Each due backup preserves a local archive. The selected remote target is pinned to its revision."}{" "}
        Disabling stops future discovery and leaves admitted backups running.
      </p>
      <Button
        type="button"
        variant="outline"
        disabled={busy}
        onClick={() => {
          void schedules.refetch();
          if (selected) void history.refetch();
        }}
      >
        Refresh backup schedules and occurrences
      </Button>
      {schedules.isError || history.isError ? (
        <p role="alert">
          Schedule metadata is unavailable. Refresh current access and status.
        </p>
      ) : null}
      <StoredStorageSchedules
        schedules={schedules}
        draftLocked={draftLocked}
        select={select}
      />
      {owner ? (
        <StorageScheduleForm
          compose={compose}
          attempt={attempt}
          review={review}
          prepare={prepare}
          frequency={frequency}
          draftLocked={draftLocked}
          setFrequency={setFrequency}
          timezone={timezone}
          setTimezone={setTimezone}
          enabled={enabled}
          setEnabled={setEnabled}
          remoteDestinations={remoteDestinations}
          remoteTarget={remoteTarget}
          onRemoteTarget={onRemoteTarget}
          selected={selected}
          composeVolume={composeVolume}
          select={select}
          submit={submit}
          resource={resource}
        />
      ) : null}
      {review && !attempt ? (
        <StorageScheduleReview
          review={review}
          ack={ack}
          locked={locked}
          setAck={setAck}
          submit={submit}
          busy={busy}
          setReview={setReview}
        />
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
              setReview(undefined);
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
      <StorageScheduleOccurrences
        history={history}
        onSelectBackup={onSelectBackup}
      />
      {error ? <p role="alert">{error}</p> : null}
    </section>
  );
}
function useStorageSchedule({
  scope,
  resource,
  owner,
  blocked,
  remoteTarget,
  onRemoteTarget,
  onSelectBackup,
  onDenied,
  composeVolume,
  remoteDestinations,
}: StorageBackupSchedulesProps) {
  const compose = composeVolume !== undefined;
  const title = compose
    ? "Recurring Compose volume backups"
    : "Recurring upload backups";
  const client = useQueryClient();
  const [selected, setSelected] = useState<StorageBackupSchedule>();
  const [frequency, setFrequency] = useState("daily");
  const [timezone, setTimezone] = useState("UTC");
  const [enabled, setEnabled] = useState(true);
  const [attempt, setAttempt] = useState<Attempt>();
  const [review, setReview] = useState<Attempt>();
  const [ack, setAck] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const active = useRef<AbortController | null>(null);
  const mounted = useRef(true);
  const key = useMemo(
    () =>
      storageBackupSchedulesKey(
        {
          actor: scope.actor,
          project: scope.project,
          resource: scope.resource,
          destination: scope.destination,
          binding: scope.binding,
        },
        resource.id,
      ),
    [
      scope.actor,
      scope.project,
      scope.resource,
      scope.destination,
      scope.binding,
      resource.id,
    ],
  );
  const schedules = useQuery({
    queryKey: key,
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () =>
          listStorageBackupSchedules(scope, resource, signal).then((items) => {
            if (
              items.some(
                (v) =>
                  (v.version ===
                    "managed-compose.local-volume-archive.schedule.v1") !==
                  compose,
              )
            )
              throw new Error("Schedule family mismatch");
            return items;
          }),
        retireScheduleAccess,
        [401, 403, 404],
      ),
    enabled: !blocked,
    retry: false,
  });
  const history = useQuery({
    queryKey: [...key, selected?.id, "occurrences"],
    queryFn: ({ signal }) =>
      readBackupMetadata(
        signal,
        () =>
          listStorageBackupOccurrences(
            scope,
            resource,
            selected!.id,
            signal,
          ).then((items) => {
            if (items.some((v) => v.volume !== selected!.volume))
              throw new Error("Occurrence volume mismatch");
            return items;
          }),
        retireScheduleAccess,
        [401, 403, 404],
      ),
    enabled: !!selected && !blocked,
    retry: false,
    refetchInterval: 5000,
  });
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      active.current?.abort();
      void client.cancelQueries({ queryKey: key });
      client.removeQueries({ queryKey: key });
    };
  }, [client, key]);
  useEffect(() => {
    if (blocked || !owner) active.current?.abort();
  }, [blocked, owner]);
  function retireScheduleAccess() {
    active.current?.abort();
    client.removeQueries({ queryKey: key });
    onDenied();
  }

  function select(v?: StorageBackupSchedule) {
    setSelected(v);
    setFrequency(v?.frequency ?? "daily");
    setTimezone(v?.timezone ?? "UTC");
    setEnabled(v?.enabled ?? true);
    onRemoteTarget(v?.remote_target);
    setError("");
  }
  async function submit(saved: Attempt) {
    if (!owner || active.current || blocked) return;
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
      if (!mounted.current || control.signal.aborted) return;
      setReview(undefined);
      setAttempt(undefined);
      select(out);
      await client.invalidateQueries({ queryKey: key });
    } catch (e) {
      if (!mounted.current || control.signal.aborted) return;
      if (e instanceof ApiError && [401, 403, 404].includes(e.status)) {
        client.removeQueries({ queryKey: key });
        onDenied();
      }
      setError(
        e instanceof ApiError && e.status === 409
          ? "The schedule or resource changed. Review current status before a separate submission. Your saved request is preserved."
          : "Schedule delivery or status is unknown. Retry the identical saved request to recover its result.",
      );
    } finally {
      if (active.current === control) active.current = null;
      if (mounted.current) setBusy(false);
    }
  }
  async function prepare() {
    const input: StorageBackupScheduleInput = {
      request_id: storageRequestID(),
      expected_resource_generation: resource.generation,
      expected_version: selected?.revision ?? 0,
      frequency,
      timezone,
      enabled,
      ...(compose ? { volume: selected?.volume ?? composeVolume } : {}),
      ...(remoteTarget
        ? { remote_target: Object.freeze({ ...remoteTarget }) }
        : {}),
    };
    const next = { schedule: selected?.id ?? "", input };
    if (!compose || (selected && !enabled)) {
      await submit(next);
      return;
    }
    if (!owner || active.current || blocked) return;
    if (
      remoteDestinations &&
      !validSelectedBackupDestination(remoteTarget, remoteDestinations)
    ) {
      setError(
        "Refresh the remote target validation and select its current revision before review.",
      );
      return;
    }
    const control = new AbortController();
    active.current = control;
    setBusy(true);
    setError("");
    try {
      const evidence = await getComposeBackupVolumes(scope, control.signal);
      if (!mounted.current || control.signal.aborted) return;
      if (
        !evidence.available ||
        evidence.resource_generation !== resource.generation ||
        !evidence.operation_id ||
        !evidence.volumes.some((v) => v.volume === input.volume && v.eligible)
      )
        throw new ApiError(409, "Compose volume unavailable", null);
      setAck(false);
      setReview({
        schedule: next.schedule,
        input: Object.freeze({
          ...input,
          expected_deployed_operation_id: evidence.operation_id,
          acknowledge_interruption: true,
        }),
      });
    } catch (e) {
      if (!mounted.current || control.signal.aborted) return;
      if (e instanceof ApiError && [401, 403, 404].includes(e.status)) {
        client.removeQueries({ queryKey: key });
        onDenied();
      }
      setError(
        "Current Compose volume evidence is unavailable or changed. Refresh and review before scheduling.",
      );
    } finally {
      if (active.current === control) active.current = null;
      if (mounted.current) setBusy(false);
    }
  }
  const locked = blocked || busy;
  const draftLocked = locked || !!attempt || !!review;
  return {
    title,
    compose,
    busy,
    schedules,
    selected,
    history,
    draftLocked,
    select,
    owner,
    attempt,
    review,
    prepare,
    frequency,
    setFrequency,
    timezone,
    setTimezone,
    enabled,
    setEnabled,
    remoteDestinations,
    remoteTarget,
    onRemoteTarget,
    composeVolume,
    submit,
    resource,
    ack,
    locked,
    setAck,
    setReview,
    setAttempt,
    setError,
    onSelectBackup,
    error,
  };
}

function StorageScheduleReview({
  review,
  ack,
  locked,
  setAck,
  submit,
  busy,
  setReview,
}: {
  review: Attempt;
  ack: boolean;
  locked: boolean;
  setAck: React.Dispatch<React.SetStateAction<boolean>>;
  submit: (saved: Attempt) => Promise<void>;
  busy: boolean;
  setReview: React.Dispatch<React.SetStateAction<Attempt | undefined>>;
}) {
  return (
    <section
      aria-label="Reviewed Compose volume schedule"
      className="space-y-2 break-all"
    >
      <p>
        Volume {review.input.volume} · generation{" "}
        {review.input.expected_resource_generation} · deployed operation{" "}
        {review.input.expected_deployed_operation_id} · {review.input.frequency}{" "}
        · {review.input.timezone} ·{" "}
        {review.input.enabled ? "enabled" : "disabled"}
      </p>
      <p>
        Remote target for this schedule:{" "}
        {review.input.remote_target
          ? `${review.input.remote_target.target_id} revision ${review.input.remote_target.revision}`
          : "local only"}
        . The target revision is pinned for due backups.
      </p>
      <label className="flex items-start gap-2">
        <input
          type="checkbox"
          checked={ack}
          disabled={locked}
          onChange={(e) => setAck(e.target.checked)}
        />
        <span>
          I acknowledge interruption of this volume's writers for each scheduled
          local archive.
        </span>
      </label>
      <Button
        type="button"
        disabled={locked || !ack}
        onClick={() => void submit(review)}
      >
        Confirm Compose volume schedule
      </Button>
      <Button
        type="button"
        variant="outline"
        disabled={busy}
        onClick={() => {
          setReview(undefined);
          setAck(false);
        }}
      >
        Discard schedule review
      </Button>
    </section>
  );
}

function StorageScheduleOccurrences({
  history,
  onSelectBackup,
}: {
  history: UseQueryResult<StorageBackupOccurrence[], Error>;
  onSelectBackup: (v: string) => void;
}) {
  return (
    <>
      {!history.isError
        ? (history.data ?? []).map((v) => (
            <p key={v.id} className="break-all">
              {v.scheduled_at}
              {v.volume ? ` · volume ${v.volume}` : ""} · revision{" "}
              {v.schedule_revision} · {v.state}
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
    </>
  );
}

function StorageScheduleForm({
  compose,
  attempt,
  review,
  prepare,
  frequency,
  draftLocked,
  setFrequency,
  timezone,
  setTimezone,
  enabled,
  setEnabled,
  remoteDestinations,
  remoteTarget,
  onRemoteTarget,
  selected,
  composeVolume,
  select,
  submit,
  resource,
}: {
  compose: boolean;
  attempt: Attempt | undefined;
  review: Attempt | undefined;
  prepare: () => Promise<void>;
  frequency: string;
  draftLocked: boolean;
  setFrequency: React.Dispatch<React.SetStateAction<string>>;
  timezone: string;
  setTimezone: React.Dispatch<React.SetStateAction<string>>;
  enabled: boolean;
  setEnabled: React.Dispatch<React.SetStateAction<boolean>>;
  remoteDestinations:
    | ReturnType<typeof useDatabaseBackupDestinations>
    | undefined;
  remoteTarget: RemoteBackupTarget | undefined;
  onRemoteTarget: (v?: RemoteBackupTarget | undefined) => void;
  selected: StorageBackupSchedule | undefined;
  composeVolume: string | undefined;
  select: (v?: StorageBackupSchedule | undefined) => void;
  submit: (saved: Attempt) => Promise<void>;
  resource: Pick<Resource, "id" | "generation">;
}) {
  return (
    <form
      aria-label={
        compose
          ? "Save recurring Compose volume backup"
          : "Save recurring upload backup"
      }
      onSubmit={(e) => {
        e.preventDefault();
        if (!attempt && !review) void prepare();
      }}
      className="space-y-2"
    >
      <label className="block">
        Backup frequency
        <Input
          aria-label="Backup frequency"
          value={frequency}
          maxLength={128}
          disabled={draftLocked}
          onChange={(e) => setFrequency(e.target.value)}
        />
      </label>
      <label className="block">
        Backup timezone
        <Input
          aria-label="Backup timezone"
          value={timezone}
          maxLength={128}
          disabled={draftLocked}
          onChange={(e) => setTimezone(e.target.value)}
        />
      </label>
      <label className="flex gap-2">
        <input
          type="checkbox"
          checked={enabled}
          disabled={draftLocked}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        Enabled
      </label>
      {compose && remoteDestinations ? (
        <>
          <BackupDestinationSelect
            label="Remote scheduled Compose backup target"
            value={remoteTarget}
            onChange={onRemoteTarget}
            destinations={remoteDestinations}
            disabled={draftLocked}
          />
          <Button
            type="button"
            variant="outline"
            disabled={draftLocked}
            onClick={() => void remoteDestinations.refresh()}
          >
            Refresh scheduled Compose backup targets
          </Button>
        </>
      ) : null}
      <ScheduledVolumeSummary
        compose={compose}
        selected={selected}
        composeVolume={composeVolume}
        remoteTarget={remoteTarget}
      />
      <Button
        type="submit"
        disabled={
          draftLocked ||
          (compose && !(selected?.volume || composeVolume)) ||
          !validStorageBackupScheduleFields(frequency, timezone)
        }
      >
        {compose && (!selected || enabled)
          ? "Review Compose volume schedule"
          : selected
            ? "Save schedule changes"
            : "Create backup schedule"}
      </Button>{" "}
      <Button
        type="button"
        variant="outline"
        disabled={draftLocked}
        onClick={() => select()}
      >
        New schedule
      </Button>
      {selected?.enabled ? (
        <Button
          type="button"
          disabled={draftLocked}
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
                ...(compose ? { volume: selected.volume } : {}),
                ...(selected.remote_target
                  ? {
                      remote_target: Object.freeze({
                        ...selected.remote_target,
                      }),
                    }
                  : {}),
              },
            })
          }
        >
          Disable schedule
        </Button>
      ) : null}
    </form>
  );
}

function StoredStorageSchedules({
  schedules,
  draftLocked,
  select,
}: {
  schedules: UseQueryResult<StorageBackupSchedule[], Error>;
  draftLocked: boolean;
  select: (v?: StorageBackupSchedule | undefined) => void;
}) {
  return (
    <>
      {!schedules.isError
        ? (schedules.data ?? []).map((v) => (
            <p key={v.id} className="break-all">
              {v.id}
              {v.volume ? ` · volume ${v.volume}` : ""} · revision {v.revision}{" "}
              · {v.frequency} · {v.timezone} ·{" "}
              {v.enabled ? "enabled" : "disabled"}{" "}
              <Button
                type="button"
                disabled={draftLocked}
                onClick={() => select(v)}
              >
                View schedule {v.id}
              </Button>
            </p>
          ))
        : null}
    </>
  );
}

function ScheduledVolumeSummary({
  compose,
  selected,
  composeVolume,
  remoteTarget,
}: {
  compose: boolean;
  selected: StorageBackupSchedule | undefined;
  composeVolume: string | undefined;
  remoteTarget: RemoteBackupTarget | undefined;
}) {
  return (
    <>
      {compose ? (
        <p className="break-all">
          Scheduled named volume:{" "}
          {selected?.volume || composeVolume || "Select a named volume above"}.
        </p>
      ) : null}
      <p>
        Remote target for this submission:{" "}
        {remoteTarget
          ? `${remoteTarget.target_id} revision ${remoteTarget.revision}`
          : "local only"}
        .{" "}
        {compose
          ? "Writers resume before upload. Upload failure leaves the local archive successful; retry upload from that archive's transfer controls."
          : "Review the remote target selector above."}
      </p>
    </>
  );
}
