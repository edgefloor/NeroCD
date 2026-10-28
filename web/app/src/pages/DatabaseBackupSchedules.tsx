import {
  BackupDestinationSelect,
  useDatabaseBackupDestinations,
  validSelectedBackupDestination,
} from "./DatabaseBackupDestinations";
import type { RemoteBackupTarget } from "../api/hosting-database-backup-targets";
import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "../components/ui/button";
import { ApiError } from "../api/errors";
import {
  databaseDenied,
  databaseError,
  databaseRequestID,
  type DatabaseScope,
  type HostingDatabase,
} from "../api/hosting-databases";
import { databaseBackupsKey } from "../api/hosting-database-backups";
import {
  databaseBackupSchedulesKey,
  listDatabaseBackupOccurrences,
  listDatabaseBackupSchedules,
  saveDatabaseBackupSchedule,
  validDatabaseBackupScheduleFields,
  type DatabaseBackupSchedule,
  type DatabaseBackupScheduleInput,
} from "../api/hosting-database-backup-schedules";

type Attempt = { schedule: string; input: DatabaseBackupScheduleInput };
type Props = {
  database: HostingDatabase;
  scope: DatabaseScope;
  owner: boolean;
  blocked: boolean;
  onDenied: () => void;
};
export function DatabaseBackupSchedules(props: Props) {
  const identity = JSON.stringify([
    props.scope.actor,
    props.scope.project,
    props.scope.environment,
    props.database.resource.id,
  ]);
  return <DatabaseBackupSchedulePanel key={identity} {...props} />;
}
function DatabaseBackupSchedulePanel({
  database,
  scope,
  owner,
  blocked,
  onDenied,
}: Props) {
  const client = useQueryClient();
  const [accessRevoked, setAccessRevoked] = useState(false);
  const destinations = useDatabaseBackupDestinations({
    database,
    scope,
    owner,
    blocked,
    onDenied,
  });
  const [remoteTarget, setRemoteTarget] = useState<RemoteBackupTarget>();
  const [editing, setEditing] = useState<{ id: string; revision: number }>({
    id: "",
    revision: 0,
  });
  const [frequency, setFrequency] = useState("daily");
  const [timezone, setTimezone] = useState("UTC");
  const [enabled, setEnabled] = useState(true);
  const [selected, setSelected] = useState("");
  const [attempt, setAttempt] = useState<Attempt>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  const [accepted, setAccepted] = useState("");
  const lifetime = useRef<AbortController | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    return () => {
      controller.abort();
      if (lifetime.current === controller) lifetime.current = null;
    };
  }, []);
  const key = databaseBackupSchedulesKey(scope, database.resource.id);
  const schedules = useQuery({
    queryKey: key,
    queryFn: ({ signal }) =>
      listDatabaseBackupSchedules(scope, database, signal),
    enabled: !blocked && !accessRevoked && ["postgresql", "mysql"].includes(database.engine),
    retry: false,
    refetchInterval: 10000,
  });
  const occurrences = useQuery({
    queryKey: [...key, "occurrences", selected],
    queryFn: ({ signal }) =>
      listDatabaseBackupOccurrences(scope, database, selected, signal),
    enabled:
      !blocked &&
      !accessRevoked &&
      Boolean(selected) &&
      schedules.data?.some((s) => s.id === selected) === true,
    retry: false,
    refetchInterval: 3000,
  });
  const denied =
    accessRevoked ||
    destinations.denied ||
    databaseDenied(schedules.error) ||
    databaseDenied(occurrences.error) ||
    databaseDenied(error);
  useEffect(() => {
    if (denied) {
      setAccessRevoked(true);
      lifetime.current?.abort();
      client.removeQueries({ queryKey: key });
      onDenied();
    }
  }, [denied]);
  if (!["postgresql", "mysql"].includes(database.engine)) return null;
  if (denied) return <p role="alert">Backup schedule access is unavailable.</p>;
  function review(schedule?: DatabaseBackupSchedule) {
    if (attempt || pending) return;
    setEditing({ id: schedule?.id ?? "", revision: schedule?.revision ?? 0 });
    setFrequency(schedule?.frequency ?? "daily");
    setTimezone(schedule?.timezone ?? "UTC");
    setEnabled(schedule?.enabled ?? true);
    setRemoteTarget(schedule?.remote_target);
    setError(undefined);
    setAccepted("");
  }
  async function submit(next: Attempt) {
    const controller = lifetime.current;
    if (
      !owner ||
      blocked ||
      pending ||
      !controller ||
      controller.signal.aborted
    )
      return;
    setAttempt(next);
    setPending(true);
    setError(undefined);
    try {
      const out = await saveDatabaseBackupSchedule(
        scope,
        database,
        next.schedule,
        next.input,
        controller.signal,
      );
      if (controller.signal.aborted) return;
      setAttempt(undefined);
      setAccepted(`${out.id} revision ${out.revision}`);
      setSelected(out.id);
      if (editing.id === out.id || !next.schedule) {
        setEditing({ id: out.id, revision: out.revision });
        setFrequency(out.frequency);
        setTimezone(out.timezone);
        setEnabled(out.enabled);
        setRemoteTarget(out.remote_target);
      }
      await schedules.refetch();
    } catch (cause) {
      if (!controller.signal.aborted) {
        setError(cause);
        if (cause instanceof ApiError && [400, 404, 409].includes(cause.status))
          setAttempt(undefined);
      }
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }
  const unavailable =
    blocked ||
    pending ||
    Boolean(attempt) ||
    schedules.isPending ||
    schedules.isError ||
    schedules.isFetching;
  return (
    <section
      aria-label="Database backup schedules"
      className="min-w-0 space-y-3 rounded-xl border p-3"
    >
      <h4 className="font-medium">{database.engine === "mysql" ? "MySQL" : "PostgreSQL"} backup schedules</h4>
      <p>
        Save a five-field cron expression or every_minute, hourly, daily,
        weekly, monthly or yearly. The IANA timezone is explicit. The controller
        selects only the newest due minute within ten minutes, including the
        current minute. It does not backfill an outage backlog.
      </p>
      <p>
        Disable stops future discovery. Accepted work keeps its original
        identity and authority checks. Overlapping backups of the same source
        are skipped. Missed, denied and failed occurrences remain in history.
        External notifications are not available. {database.engine === "mysql" ? "MySQL schedules create local logical backups only. Remote copies and retention controls are not available for MySQL." : "Retention is configured in the backup retention section. Remote uploads have separate transfer status in backup history."}
      </p>
      {!owner ? (
        <p>
          Project owners can create, edit and disable schedules. You can inspect
          schedule and occurrence history.
        </p>
      ) : (
        <div className="space-y-2">
          <p>
            {editing.id
              ? `Editing ${editing.id}, reviewed revision ${editing.revision}`
              : "New schedule"}
          </p>
          <label className="block">
            Backup frequency
            <input
              aria-label="Backup frequency"
              className="block w-full min-w-0 rounded border p-2"
              maxLength={128}
              value={frequency}
              disabled={unavailable}
              onChange={(e) => setFrequency(e.target.value)}
            />
          </label>
          <label className="block">
            Backup timezone
            <input
              aria-label="Backup timezone"
              className="block w-full min-w-0 rounded border p-2"
              maxLength={128}
              value={timezone}
              disabled={unavailable}
              onChange={(e) => setTimezone(e.target.value)}
            />
          </label>
          {database.engine === "postgresql" ? <BackupDestinationSelect
            label="Scheduled backup destination"
            value={remoteTarget}
            onChange={setRemoteTarget}
            destinations={destinations}
            disabled={unavailable}
          /> : null}
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              aria-label="Schedule enabled"
              checked={enabled}
              disabled={unavailable}
              onChange={(e) => setEnabled(e.target.checked)}
            />
            Schedule enabled
          </label>
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              className="h-auto max-w-full whitespace-normal"
              disabled={
                unavailable ||
                !validDatabaseBackupScheduleFields(frequency, timezone) ||
                !validSelectedBackupDestination(remoteTarget, destinations)
              }
              onClick={() =>
                void submit({
                  schedule: editing.id,
                  input: {
                    request_id: databaseRequestID(),
                    expected_resource_generation: database.resource.generation,
                    expected_version: editing.revision,
                    frequency,
                    timezone,
                    enabled,
                    ...(database.engine === "postgresql" && remoteTarget ? { remote_target: remoteTarget } : {}),
                  },
                })
              }
            >
              {editing.id ? "Save schedule changes" : "Create backup schedule"}
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={unavailable}
              onClick={() => review()}
            >
              New schedule
            </Button>
          </div>
        </div>
      )}
      {pending ? (
        <p role="status">Submitting the saved schedule request…</p>
      ) : null}
      {accepted ? (
        <p role="status" className="break-all">
          Saved schedule {accepted}. Execution is shown only by server
          occurrence and backup history.
        </p>
      ) : null}
      {error ? (
        <p role="alert">
          {databaseError(error)}{" "}
          {attempt
            ? `Retained request ${attempt.input.request_id}.`
            : "Refresh schedules and review the current revision before correcting a rejected edit."}
        </p>
      ) : null}
      {attempt && error && !pending && owner ? (
        <Button
          type="button"
          disabled={blocked}
          onClick={() => void submit(attempt)}
        >
          Retry the same schedule request
        </Button>
      ) : null}
      {schedules.isPending ? (
        <p role="status">Loading backup schedules…</p>
      ) : null}
      {schedules.isError ? (
        <p role="alert">
          Backup schedules are unavailable. {databaseError(schedules.error)}
        </p>
      ) : null}
      {schedules.data?.length === 0 ? <p>No backup schedules yet.</p> : null}
      <ul className="space-y-3">
        {schedules.data?.map((s) => (
          <li key={s.id} className="min-w-0 space-y-1 border-t pt-2 break-all">
            <p>
              {s.id} · revision {s.revision} ·{" "}
              {s.enabled ? "Enabled" : "Disabled"}
            </p>
            <p>
              {s.frequency} · {s.timezone}
            </p>
            <p>
              {s.remote_target
                ? `Remote destination ${s.remote_target.target_id}, revision ${s.remote_target.revision}`
                : "Local archive only"}
            </p>
            {s.last_scheduled_at ? (
              <p>Last considered UTC minute: {s.last_scheduled_at}</p>
            ) : (
              <p>No occurrence considered yet.</p>
            )}
            <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={blocked || pending}
                onClick={() => setSelected(s.id)}
              >
                View occurrences
              </Button>
              {owner ? (
                <>
                  <Button
                    type="button"
                    variant="outline"
                    disabled={unavailable}
                    onClick={() => review(s)}
                  >
                    Edit schedule
                  </Button>
                  <Button
                    type="button"
                    variant="outline"
                    disabled={
                      unavailable ||
                      (!s.enabled &&
                        !validSelectedBackupDestination(
                          s.remote_target,
                          destinations,
                        ))
                    }
                    onClick={() =>
                      void submit({
                        schedule: s.id,
                        input: {
                          request_id: databaseRequestID(),
                          expected_resource_generation:
                            database.resource.generation,
                          expected_version: s.revision,
                          frequency: s.frequency,
                          timezone: s.timezone,
                          enabled: !s.enabled,
                          ...(database.engine === "postgresql" && s.remote_target
                            ? { remote_target: s.remote_target }
                            : {}),
                        },
                      })
                    }
                  >
                    {s.enabled ? "Disable schedule" : "Enable schedule"}
                  </Button>
                </>
              ) : null}
            </div>
          </li>
        ))}
      </ul>
      {selected ? (
        <section
          aria-label="Backup schedule occurrences"
          className="min-w-0 space-y-2"
        >
          <h5 className="break-all">Occurrences for {selected}</h5>
          {occurrences.isPending ? (
            <p role="status">Loading occurrences…</p>
          ) : null}
          {occurrences.isError ? (
            <p role="alert">
              Occurrence history is unavailable.{" "}
              {databaseError(occurrences.error)}
            </p>
          ) : null}
          {occurrences.data?.length === 0 ? <p>No occurrences yet.</p> : null}
          <ul className="space-y-2">
            {occurrences.data?.map((o) => (
              <li key={o.id} className="break-all">
                <p>
                  {o.scheduled_at} · {o.state} · schedule revision{" "}
                  {o.schedule_revision}
                </p>
                <p>
                  {o.frequency} · {o.timezone}
                </p>
                {o.operation_id ? (
                  <p>Backup operation: {o.operation_id}</p>
                ) : null}
                {o.reason ? <p>Recorded reason: {o.reason}</p> : null}
                {o.ownership_held ? (
                  <p>
                    Source ownership remains held. Uncertain execution is not
                    automatically repeated.
                  </p>
                ) : null}
              </li>
            ))}
          </ul>
        </section>
      ) : null}
      <Button
        type="button"
        variant="outline"
        disabled={blocked || schedules.isFetching || occurrences.isFetching}
        onClick={() => {
          void schedules.refetch();
          if (selected) void occurrences.refetch();
          void client.invalidateQueries({
            queryKey: databaseBackupsKey(scope, database.resource.id),
          });
        }}
      >
        Refresh schedules
      </Button>
    </section>
  );
}
