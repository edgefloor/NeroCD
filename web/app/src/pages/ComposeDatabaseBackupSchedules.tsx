import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { Resource } from "../api/hosting";
import { ApiError } from "../api/errors";
import {
  databaseDenied,
  databaseError,
  databaseRequestID,
  type DatabaseScope,
} from "../api/hosting-databases";
import {
  databaseBackupsKey,
  getDatabaseBackupComponents,
} from "../api/hosting-database-backups";
import {
  databaseBackupSchedulesKey,
  listDatabaseBackupOccurrences,
  listDatabaseBackupSchedules,
  saveDatabaseBackupSchedule,
  validDatabaseBackupScheduleFields,
  type DatabaseBackupSchedule,
  type DatabaseBackupScheduleInput,
} from "../api/hosting-database-backup-schedules";
import { Button } from "../components/ui/button";

type Props = {
  resource: Resource;
  scope: DatabaseScope;
  owner: boolean;
  onDenied: () => void;
};
type Attempt = { schedule: string; input: DatabaseBackupScheduleInput };
export function ComposeDatabaseBackupSchedules(props: Props) {
  return (
    <SchedulePanel
      key={JSON.stringify([
        props.scope,
        props.resource.id,
        props.resource.generation,
      ])}
      {...props}
    />
  );
}
function SchedulePanel({ resource, scope, owner, onDenied }: Props) {
  const client = useQueryClient();
  const context = { resource };
  const key = databaseBackupSchedulesKey(scope, resource.id);
  const componentKey = ["database-backup-components", scope, resource.id];
  const [revoked, setRevoked] = useState(false);
  const [editing, setEditing] = useState<DatabaseBackupSchedule>();
  const [component, setComponent] = useState("");
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
    if (!owner) {
      setAttempt(undefined);
      setPending(false);
    }
    return () => {
      controller.abort();
      if (lifetime.current === controller) lifetime.current = null;
    };
  }, [owner]);
  useEffect(
    () => () => {
      void client.cancelQueries({ queryKey: key });
      client.removeQueries({ queryKey: key });
    },
    [client],
  );
  const schedules = useQuery({
    queryKey: key,
    queryFn: ({ signal }) =>
      listDatabaseBackupSchedules(scope, context, signal),
    enabled: !revoked,
    retry: false,
    refetchInterval: 10000,
  });
  const components = useQuery({
    queryKey: componentKey,
    queryFn: ({ signal }) =>
      getDatabaseBackupComponents(scope, resource.id, signal),
    enabled: !revoked && owner,
    retry: false,
  });
  const occurrences = useQuery({
    queryKey: [...key, "occurrences", selected],
    queryFn: ({ signal }) =>
      listDatabaseBackupOccurrences(scope, context, selected, signal),
    enabled:
      !revoked &&
      !!selected &&
      schedules.data?.some((s) => s.id === selected) === true,
    retry: false,
    refetchInterval: 3000,
  });
  const denied =
    revoked ||
    databaseDenied(schedules.error) ||
    databaseDenied(components.error) ||
    databaseDenied(occurrences.error) ||
    databaseDenied(error);
  useEffect(() => {
    if (!denied) return;
    setRevoked(true);
    lifetime.current?.abort();
    void client.cancelQueries({ queryKey: key });
    client.removeQueries({ queryKey: key });
    void client.cancelQueries({ queryKey: componentKey });
    client.removeQueries({ queryKey: componentKey });
    onDenied();
  }, [denied]);
  if (denied) return <p role="alert">Backup schedule access is unavailable.</p>;
  const evidence =
    components.isSuccess &&
    !components.isFetching &&
    components.data.available &&
    components.data.resource_generation === resource.generation
      ? components.data
      : undefined;
  const eligible = (name: string) =>
    !!evidence?.operation_id &&
    evidence.components.some((c) => c.component === name && c.eligible);
  const locked = pending || !!attempt;
  const unavailable =
    locked || schedules.isPending || schedules.isError || schedules.isFetching;
  function review(schedule?: DatabaseBackupSchedule) {
    if (unavailable) return;
    setEditing(schedule);
    setComponent(schedule?.component ?? "");
    setFrequency(schedule?.frequency ?? "daily");
    setTimezone(schedule?.timezone ?? "UTC");
    setEnabled(schedule?.enabled ?? true);
    setAccepted("");
    setError(undefined);
  }
  async function submit(next: Attempt) {
    const controller = lifetime.current;
    if (
      !owner ||
      pending ||
      (attempt && next !== attempt) ||
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
        context,
        next.schedule,
        next.input,
        controller.signal,
      );
      if (controller.signal.aborted) return;
      setAttempt(undefined);
      setAccepted(`${out.id} revision ${out.revision}`);
      setSelected(out.id);
      if (!next.schedule || editing?.id === out.id) {
        setEditing(out);
        setComponent(out.component ?? "");
        setFrequency(out.frequency);
        setTimezone(out.timezone);
        setEnabled(out.enabled);
      }
      await schedules.refetch();
      void client.invalidateQueries({
        queryKey: databaseBackupsKey(scope, resource.id),
      });
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
  function input(
    name: string,
    revision: number,
    nextEnabled: boolean,
    nextFrequency: string,
    nextTimezone: string,
  ): DatabaseBackupScheduleInput {
    return {
      request_id: databaseRequestID(),
      expected_resource_generation: resource.generation,
      expected_version: revision,
      component: name,
      frequency: nextFrequency,
      timezone: nextTimezone,
      enabled: nextEnabled,
      ...((nextEnabled || revision === 0) && evidence?.operation_id
        ? { expected_deployed_operation_id: evidence.operation_id }
        : {}),
    };
  }
  return (
    <section
      aria-label="Component database backup schedules"
      className="min-w-0 space-y-3 rounded-xl border p-3"
    >
      <h5 className="font-medium">PostgreSQL component backup schedules</h5>
      <p>
        Save a five-field cron expression or every_minute, hourly, daily,
        weekly, monthly or yearly with an explicit IANA timezone. These
        schedules create local logical archives of PostgreSQL16 database
        records. Uploaded files are excluded.
      </p>
      <p>
        Each schedule keeps its database component. Create another schedule to
        select a different component. Disable stops future occurrences; accepted
        backups continue. Missed, skipped and failed occurrences remain in
        history.
      </p>
      {owner ? (
        <div className="space-y-2">
          <p>
            {editing
              ? `Editing ${editing.id}, reviewed revision ${editing.revision}`
              : "New schedule"}
          </p>
          <label className="block">
            Scheduled database component
            <select
              className="block w-full min-w-0 rounded border p-2"
              aria-label="Scheduled database component"
              value={component}
              disabled={unavailable || !!editing}
              onChange={(e) => setComponent(e.target.value)}
            >
              <option value="">Select a database component</option>
              {editing?.component ? (
                <option value={editing.component}>{editing.component}</option>
              ) : (
                evidence?.components
                  .filter((c) => c.eligible)
                  .map((c) => (
                    <option key={c.component} value={c.component}>
                      {c.component}
                    </option>
                  ))
              )}
            </select>
          </label>
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
          <label className="flex items-center gap-2">
            <input
              aria-label="Schedule enabled"
              type="checkbox"
              checked={enabled}
              disabled={unavailable}
              onChange={(e) => setEnabled(e.target.checked)}
            />
            Schedule enabled
          </label>
          <div className="flex flex-wrap gap-2">
            <Button
              className="h-auto max-w-full whitespace-normal"
              disabled={
                unavailable ||
                !component ||
                !validDatabaseBackupScheduleFields(frequency, timezone) ||
                ((!editing || enabled) && !eligible(component))
              }
              onClick={() =>
                void submit({
                  schedule: editing?.id ?? "",
                  input: input(
                    component,
                    editing?.revision ?? 0,
                    enabled,
                    frequency,
                    timezone,
                  ),
                })
              }
            >
              {editing ? "Save schedule changes" : "Create backup schedule"}
            </Button>
            <Button
              variant="outline"
              disabled={unavailable}
              onClick={() => review()}
            >
              New schedule
            </Button>
            <Button
              variant="outline"
              className="h-auto max-w-full whitespace-normal"
              disabled={locked || components.isFetching}
              onClick={() => void components.refetch()}
            >
              Refresh schedule components
            </Button>
          </div>
          {!evidence ? (
            <p role="status">
              A settled supported database component is unavailable. Refresh
              components after deployment settles. Existing schedules can still
              be inspected and disabled.
            </p>
          ) : null}
        </div>
      ) : (
        <p>
          Project owners can create, edit and disable schedules. You can inspect
          schedules and occurrence history.
        </p>
      )}
      {pending ? (
        <p role="status">Submitting the saved schedule request…</p>
      ) : null}
      {accepted ? (
        <p role="status" className="break-all">
          Saved schedule {accepted}.
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
        <Button onClick={() => void submit(attempt)}>
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
            <p>Component: {s.component}</p>
            <p>
              {s.frequency} · {s.timezone} · Local archive only
            </p>
            <p>
              {s.last_scheduled_at
                ? `Last considered UTC minute: ${s.last_scheduled_at}`
                : "No occurrence considered yet."}
            </p>
            <div className="flex flex-wrap gap-2">
              <Button
                variant="outline"
                disabled={locked}
                onClick={() => setSelected(s.id)}
              >
                View occurrences
              </Button>
              {owner ? (
                <>
                  <Button
                    variant="outline"
                    disabled={unavailable}
                    onClick={() => review(s)}
                  >
                    Edit schedule
                  </Button>
                  <Button
                    variant="outline"
                    disabled={
                      unavailable ||
                      !s.component ||
                      (!s.enabled && !eligible(s.component))
                    }
                    onClick={() =>
                      void submit({
                        schedule: s.id,
                        input: input(
                          s.component!,
                          s.revision,
                          !s.enabled,
                          s.frequency,
                          s.timezone,
                        ),
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
          <h6 className="break-all">Occurrences for {selected}</h6>
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
                  Component: {o.component} · {o.frequency} · {o.timezone}
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
        variant="outline"
        disabled={schedules.isFetching || occurrences.isFetching}
        onClick={() => {
          void schedules.refetch();
          if (selected) void occurrences.refetch();
        }}
      >
        Refresh schedules
      </Button>
    </section>
  );
}
