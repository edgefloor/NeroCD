import { useEffect, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { Principal, Project, ProjectMember } from "@/api";
import { ApiError } from "@/api/errors";
import { databaseDenied, databaseRequestID } from "@/api/hosting-databases";
import {
  getNotificationDelivery,
  listNotificationChannels,
  listNotificationDeliveries,
  notificationFailure,
  saveNotificationChannel,
  testNotificationChannel,
  type BackupEvent,
  type NotificationChannel,
  type NotificationChannelInput,
  type NotificationConfiguration,
} from "@/api/hosting-backup-notifications";
import { canOwnProject } from "@/lib/permissions";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

const eventNames = {
  backup_failed: "Backup failed",
  upload_failed: "Upload failed, local backup available",
  test: "Test delivery",
};
const selectClass = "w-full min-w-0 rounded-xl border bg-card p-2";
function failure(cause: unknown) {
  if (databaseDenied(cause))
    return "Notification access is unavailable. Refresh project access.";
  if (cause instanceof ApiError && cause.status === 409)
    return "The channel changed or this request conflicts. Refresh current channels and explicitly review the generation before saving again.";
  if (cause instanceof ApiError && cause.status === 400)
    return "Settings were rejected. Review the fields and re-enter credentials.";
  return "Response unconfirmed. Refresh history before retrying the same request. Credentials were cleared.";
}
async function fingerprint(
  configuration: NotificationConfiguration | undefined,
) {
  const bytes = new TextEncoder().encode(JSON.stringify(configuration ?? null));
  const hash = await crypto.subtle.digest("SHA-256", bytes);
  return Array.from(new Uint8Array(hash), (b) =>
    b.toString(16).padStart(2, "0"),
  ).join("");
}
type RetainedSave = {
  input: Omit<NotificationChannelInput, "configuration">;
  digest: string;
  replaces: boolean;
};
function ChannelForm({
  initial,
  project,
  members,
  blocked,
  onSaved,
  onClose,
  onDenied,
  signal,
}: {
  initial?: NotificationChannel;
  project: string;
  members: ProjectMember[];
  blocked: boolean;
  onSaved: () => Promise<void>;
  onClose: () => void;
  onDenied: () => void;
  signal: AbortSignal;
}) {
  const [name, setName] = useState(initial?.name ?? "");
  const [type, setType] = useState<"email" | "webhook">(
    initial?.type ?? "email",
  );
  const [generation, setGeneration] = useState(initial?.generation ?? 0);
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [events, setEvents] = useState<BackupEvent[]>(
    initial?.events ?? ["backup_failed", "upload_failed"],
  );
  const [replace, setReplace] = useState(!initial);
  const [host, setHost] = useState("");
  const [port, setPort] = useState("587");
  const [security, setSecurity] = useState<"starttls" | "tls">("starttls");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [webhook, setWebhook] = useState("");
  const [from, setFrom] = useState("");
  const [recipients, setRecipients] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [retained, setRetained] = useState<RetainedSave>();
  const [conflict, setConflict] = useState(false);
  const [current, setCurrent] = useState<NotificationChannel>();
  const clear = () => {
    setUsername("");
    setPassword("");
    setWebhook("");
  };
  useEffect(() => {
    if (blocked) clear();
  }, [blocked]);
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (busy || blocked || conflict || signal.aborted) return;
    setBusy(true);
    setError("");
    const configuration: NotificationConfiguration | undefined = replace
      ? type === "webhook"
        ? { webhook_url: webhook }
        : {
            smtp_host: host,
            smtp_port: Number(port),
            smtp_security: security,
            smtp_username: username,
            smtp_password: password,
            from_address: from,
            recipient_user_ids: recipients.slice().sort(),
          }
      : undefined;
    clear();
    try {
      const digest = await fingerprint(configuration);
      if (signal.aborted) return;
      const input = retained?.input ?? {
        request_id: databaseRequestID(),
        expected_generation: generation,
        name,
        type,
        enabled,
        events,
      };
      if (
        retained &&
        (digest !== retained.digest || replace !== retained.replaces)
      ) {
        setError(
          "Re-enter the exact original credentials to retry this request, or discard the retained request after checking history.",
        );
        return;
      }
      setRetained({ input, digest, replaces: replace });
      await saveNotificationChannel(
        project,
        { ...input, ...(configuration ? { configuration } : {}) },
        initial?.id,
        signal,
      );
      if (signal.aborted) return;
      setRetained(undefined);
      await onSaved();
      onClose();
    } catch (cause) {
      if (signal.aborted) return;
      setError(failure(cause));
      if (databaseDenied(cause)) onDenied();
      if (
        cause instanceof ApiError &&
        [400, 401, 403, 404, 409].includes(cause.status)
      )
        setRetained(undefined);
      if (initial && cause instanceof ApiError && cause.status === 409)
        setConflict(true);
    } finally {
      if (!signal.aborted) {
        clear();
        setBusy(false);
      }
    }
  }
  const frozen = busy || blocked || Boolean(retained);
  async function refreshCurrent() {
    setBusy(true);
    setError("");
    try {
      const channels = await listNotificationChannels(project, signal);
      if (!signal.aborted)
        setCurrent(channels.find((c) => c.id === initial?.id));
    } catch (cause) {
      if (!signal.aborted) {
        setError(failure(cause));
        if (databaseDenied(cause)) onDenied();
      }
    } finally {
      if (!signal.aborted) setBusy(false);
    }
  }
  return (
    <form
      aria-label={
        initial ? `Edit ${initial.name}` : "Create notification channel"
      }
      onSubmit={(event) => void submit(event)}
      autoComplete="off"
      className="min-w-0 space-y-3 rounded-xl border p-3"
    >
      <h3 className="font-medium">
        {initial ? "Edit notification channel" : "Create notification channel"}
      </h3>
      <label className="block">
        Channel name
        <Input
          value={name}
          required
          maxLength={255}
          disabled={frozen}
          onChange={(e) => setName(e.target.value)}
        />
      </label>
      <label className="block">
        Channel type
        <select
          className={selectClass}
          value={type}
          disabled={frozen || Boolean(initial)}
          onChange={(e) => {
            clear();
            setType(e.target.value as typeof type);
          }}
        >
          <option value="email">Email</option>
          <option value="webhook">Webhook</option>
        </select>
      </label>
      <label className="flex gap-2">
        <input
          type="checkbox"
          checked={enabled}
          disabled={frozen}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        Channel enabled
      </label>
      <fieldset className="space-y-2" disabled={frozen}>
        <legend>Subscribed backup events</legend>
        {(["backup_failed", "upload_failed"] as const).map((event) => (
          <label key={event} className="flex items-start gap-2">
            <input
              type="checkbox"
              checked={events.includes(event)}
              onChange={(e) =>
                setEvents((values) =>
                  e.target.checked
                    ? [...values, event]
                    : values.filter((v) => v !== event),
                )
              }
            />
            {eventNames[event]}
          </label>
        ))}
      </fieldset>
      <p className="text-sm">
        Selecting both events matches Coolify's backup failure subscription.
        Upload failure preserves the local backup. A test is a separate explicit
        action.
      </p>
      {initial ? (
        <label className="flex gap-2">
          <input
            type="checkbox"
            checked={replace}
            disabled={frozen}
            onChange={(e) => {
              clear();
              setReplace(e.target.checked);
            }}
          />
          Replace entire delivery configuration
        </label>
      ) : null}
      <p className="text-sm">
        Saved delivery configuration is write-only.{" "}
        {replace
          ? "Enter every configuration field to replace it. Credentials clear when submitted."
          : "Saving retains the existing delivery configuration."}
      </p>
      {replace ? (
        <fieldset className="min-w-0 space-y-3" disabled={busy || blocked}>
          <legend>Delivery configuration</legend>
          {type === "webhook" ? (
            <label className="block">
              Webhook URL
              <Input
                type="password"
                autoComplete="new-password"
                value={blocked ? "" : webhook}
                required
                onChange={(e) => setWebhook(e.target.value)}
              />
            </label>
          ) : (
            <>
              <label className="block">
                SMTP host
                <Input
                  value={host}
                  required
                  disabled={frozen}
                  onChange={(e) => setHost(e.target.value)}
                />
              </label>
              <label className="block">
                SMTP port
                <Input
                  type="number"
                  min={1}
                  max={65535}
                  value={port}
                  required
                  disabled={frozen}
                  onChange={(e) => setPort(e.target.value)}
                />
              </label>
              <label className="block">
                SMTP security
                <select
                  className={selectClass}
                  value={security}
                  disabled={frozen}
                  onChange={(e) =>
                    setSecurity(e.target.value as typeof security)
                  }
                >
                  <option value="starttls">STARTTLS</option>
                  <option value="tls">TLS</option>
                </select>
              </label>
              <label className="block">
                SMTP username
                <Input
                  value={blocked ? "" : username}
                  autoComplete="off"
                  onChange={(e) => setUsername(e.target.value)}
                />
              </label>
              <label className="block">
                SMTP password
                <Input
                  type="password"
                  autoComplete="new-password"
                  value={blocked ? "" : password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </label>
              <label className="block">
                From address
                <Input
                  type="email"
                  value={from}
                  required
                  disabled={frozen}
                  onChange={(e) => setFrom(e.target.value)}
                />
              </label>
              <fieldset disabled={frozen} className="space-y-2">
                <legend>Current project recipients</legend>
                {members.map((m) => (
                  <label
                    key={m.user_id}
                    className="flex items-start gap-2 break-all"
                  >
                    <input
                      type="checkbox"
                      checked={recipients.includes(m.user_id)}
                      onChange={(e) =>
                        setRecipients((values) =>
                          e.target.checked
                            ? [...values, m.user_id]
                            : values.filter((v) => v !== m.user_id),
                        )
                      }
                    />
                    {m.name || m.email} · {m.email}
                  </label>
                ))}
                {!members.length ? (
                  <p role="alert">
                    No current project members are available. Refresh project
                    membership before configuring email.
                  </p>
                ) : null}
              </fieldset>
            </>
          )}
        </fieldset>
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
      {retained ? (
        <p role="status" className="break-all">
          Retained request {retained.input.request_id}. Nonsecret fields are
          preserved. Re-enter identical credentials for an exact retry.
        </p>
      ) : null}
      {conflict ? (
        <section aria-label="Channel generation conflict" className="space-y-2">
          <p>
            Your draft is preserved. Refresh and review current metadata before
            retrying.
          </p>
          <Button
            type="button"
            disabled={busy || blocked}
            onClick={() => void refreshCurrent()}
          >
            Refresh current channel
          </Button>
          {current ? (
            <>
              <p>
                Current name {current.name} · generation {current.generation} ·{" "}
                {current.enabled ? "enabled" : "disabled"}
              </p>
              <Button
                type="button"
                disabled={busy || blocked}
                onClick={() => {
                  setGeneration(current.generation);
                  setConflict(false);
                  clear();
                }}
              >
                Use current generation {current.generation}
              </Button>
            </>
          ) : null}
        </section>
      ) : null}
      <div className="flex flex-wrap gap-2">
        <Button
          type="submit"
          className="h-auto whitespace-normal"
          disabled={
            busy ||
            blocked ||
            conflict ||
            (replace && type === "email" && !recipients.length)
          }
        >
          {busy
            ? "Saving channel..."
            : retained
              ? "Retry exact channel request"
              : "Save notification channel"}
        </Button>
        {retained ? (
          <Button
            type="button"
            variant="outline"
            className="h-auto whitespace-normal"
            disabled={busy}
            onClick={() => {
              setRetained(undefined);
              clear();
              setError("");
            }}
          >
            Discard retained request
          </Button>
        ) : null}
        <Button
          type="button"
          variant="outline"
          disabled={busy}
          onClick={() => {
            clear();
            onClose();
          }}
        >
          Close channel editor
        </Button>
      </div>
    </form>
  );
}
function Notifications({
  project,
  actor,
  owner,
  members,
  blocked,
}: {
  project: string;
  actor: string;
  owner: boolean;
  members: ProjectMember[];
  blocked: boolean;
}) {
  const client = useQueryClient();
  const [controller, setController] = useState<AbortController>();
  const key = ["backup-notifications", actor, project];
  useEffect(() => {
    const active = new AbortController();
    setController(active);
    return () => {
      active.abort();
      client.removeQueries({ queryKey: key });
    };
  }, []);
  const [revoked, setRevoked] = useState(false);
  const channels = useQuery({
    queryKey: [...key, "channels"],
    queryFn: ({ signal }) => listNotificationChannels(project, signal),
    enabled: !blocked && !revoked,
    retry: false,
    gcTime: 0,
  });
  const deliveries = useQuery({
    queryKey: [...key, "deliveries"],
    queryFn: ({ signal }) => listNotificationDeliveries(project, signal),
    enabled: !blocked && !revoked,
    retry: false,
    gcTime: 0,
    refetchInterval: (q) =>
      q.state.data?.some((d) => ["pending", "sending"].includes(d.state))
        ? 3000
        : false,
  });
  const [selected, setSelected] = useState("");
  const detail = useQuery({
    queryKey: [...key, "detail", selected],
    queryFn: ({ signal }) => getNotificationDelivery(project, selected, signal),
    enabled: Boolean(selected) && !blocked && !revoked,
    retry: false,
    gcTime: 0,
    refetchInterval: (q) =>
      q.state.data && ["pending", "sending"].includes(q.state.data.state)
        ? 3000
        : false,
  });
  const [editing, setEditing] = useState<NotificationChannel | "new">();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [testAttempt, setTestAttempt] = useState<{
    channel: string;
    input: { request_id: string; expected_generation: number };
  }>();
  const denied = [channels.error, deliveries.error, detail.error].some(
    databaseDenied,
  );
  useEffect(() => {
    if (denied) {
      setRevoked(true);
      controller?.abort();
      setEditing(undefined);
      setTestAttempt(undefined);
      client.removeQueries({ queryKey: key });
    }
  }, [denied]);
  const refresh = async () => {
    await Promise.all([
      channels.refetch(),
      deliveries.refetch(),
      ...(selected ? [detail.refetch()] : []),
    ]);
  };
  async function testChannel(channel: NotificationChannel, retry = false) {
    if (
      !controller ||
      controller.signal.aborted ||
      !owner ||
      blocked ||
      busy ||
      revoked ||
      channels.isError ||
      channels.isFetching
    )
      return;
    const attempt =
      retry && testAttempt
        ? testAttempt
        : {
            channel: channel.id,
            input: {
              request_id: databaseRequestID(),
              expected_generation: channel.generation,
            },
          };
    setTestAttempt(attempt);
    setBusy(true);
    setError("");
    setNotice("");
    try {
      const out = await testNotificationChannel(
        project,
        attempt.channel,
        attempt.input,
        controller?.signal,
      );
      if (controller?.signal.aborted) return;
      setTestAttempt(undefined);
      setSelected(out.id);
      setNotice(
        `Test accepted. Recorded outcome: ${out.state}. A test does not verify backup event production.`,
      );
      await refresh();
    } catch (cause) {
      if (controller?.signal.aborted) return;
      setError(failure(cause));
      if (databaseDenied(cause)) {
        setRevoked(true);
        controller?.abort();
        client.removeQueries({ queryKey: key });
        return;
      }
      if (
        cause instanceof ApiError &&
        [400, 401, 403, 404, 409].includes(cause.status)
      ) {
        setTestAttempt(undefined);
        await channels.refetch();
      }
    } finally {
      if (!controller?.signal.aborted) setBusy(false);
    }
  }
  if (revoked || denied)
    return (
      <p role="alert">
        Notification access is unavailable. Refresh project access.
      </p>
    );
  return (
    <div className="min-w-0 space-y-4">
      {!owner ? (
        <p>
          You have read access to masked notification metadata and delivery
          history. Project owners configure channels and send tests.
        </p>
      ) : null}
      {channels.isPending ? (
        <p role="status">Loading notification channels...</p>
      ) : null}
      {channels.isError || deliveries.isError || detail.isError ? (
        <p role="alert">
          Notification metadata is unavailable.{" "}
          {failure(channels.error ?? deliveries.error ?? detail.error)}
        </p>
      ) : null}
      <Button
        type="button"
        variant="outline"
        disabled={
          blocked || busy || channels.isFetching || deliveries.isFetching
        }
        onClick={() => void refresh()}
      >
        Refresh notification history
      </Button>
      {channels.data?.length === 0 ? (
        <p>No notification channels configured.</p>
      ) : null}
      {channels.data?.map((channel) => (
        <article
          key={channel.id}
          className="min-w-0 space-y-2 rounded-xl border p-3 break-words [overflow-wrap:anywhere]"
          aria-label={`Channel ${channel.name}`}
        >
          <h3 className="font-medium">{channel.name}</h3>
          <p>
            {channel.type} · {channel.enabled ? "Enabled" : "Disabled"} ·
            generation {channel.generation} ·{" "}
            {channel.configured
              ? "Configuration saved, values hidden"
              : "Configuration required"}
          </p>
          <p>
            Events:{" "}
            {channel.events.map((e) => eventNames[e]).join("; ") || "None"}
          </p>
          <p>Consecutive failures: {channel.consecutive_failures}</p>
          {owner ? (
            <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={
                  blocked ||
                  busy ||
                  channels.isError ||
                  channels.isFetching ||
                  Boolean(editing) ||
                  Boolean(testAttempt)
                }
                className="h-auto max-w-full whitespace-normal break-all"
                onClick={() => setEditing(channel)}
              >
                Edit {channel.name}
              </Button>
              <Button
                type="button"
                variant="outline"
                disabled={
                  blocked ||
                  busy ||
                  channels.isError ||
                  channels.isFetching ||
                  !channel.enabled ||
                  !channel.configured ||
                  Boolean(editing) ||
                  Boolean(testAttempt)
                }
                className="h-auto max-w-full whitespace-normal break-all"
                onClick={() => void testChannel(channel)}
              >
                Send test for {channel.name}
              </Button>
            </div>
          ) : null}
        </article>
      ))}
      {owner && !editing ? (
        <Button
          type="button"
          disabled={
            blocked ||
            busy ||
            channels.isError ||
            channels.isPending ||
            Boolean(testAttempt)
          }
          onClick={() => setEditing("new")}
        >
          Add notification channel
        </Button>
      ) : null}
      {owner && editing && controller ? (
        <ChannelForm
          key={editing === "new" ? "new" : editing.id}
          initial={editing === "new" ? undefined : editing}
          project={project}
          members={members}
          signal={controller.signal}
          blocked={blocked || channels.isError || channels.isFetching}
          onSaved={refresh}
          onDenied={() => {
            setRevoked(true);
            controller.abort();
            client.removeQueries({ queryKey: key });
          }}
          onClose={() => setEditing(undefined)}
        />
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
      {notice ? <p role="status">{notice}</p> : null}
      {owner && testAttempt ? (
        <section className="space-y-2">
          <p className="break-all">
            Retained test request {testAttempt.input.request_id}. Check history
            before retrying.
          </p>
          <Button
            type="button"
            disabled={busy || blocked}
            onClick={() => {
              const c = channels.data?.find(
                (c) => c.id === testAttempt.channel,
              );
              if (c) void testChannel(c, true);
            }}
          >
            Retry exact test request
          </Button>
          <Button
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() => setTestAttempt(undefined)}
          >
            Discard test request
          </Button>
        </section>
      ) : null}
      <section
        aria-label="Notification delivery history"
        className="min-w-0 space-y-3"
      >
        <h3 className="font-medium">Latest 100 deliveries</h3>
        {deliveries.data?.length === 0 ? (
          <p>No recorded notification deliveries.</p>
        ) : null}
        {deliveries.data?.map((d) => (
          <article
            key={d.id}
            className="min-w-0 space-y-2 rounded-xl border p-3 break-words [overflow-wrap:anywhere]"
          >
            <p>
              {d.source_state === "uncertain"
                ? "Backup outcome uncertain, needs attention"
                : eventNames[d.event]}{" "}
              · {d.state} · {d.id}
            </p>
            <p>
              Channel generation {d.channel_generation} · attempts{" "}
              {d.attempt_count}/{d.max_attempts}
            </p>
            <p>{notificationFailure(d.failure_code)}</p>
            {d.event === "upload_failed" ? (
              <p>
                {d.local_copy_available
                  ? d.source_state === "uncertain"
                    ? "Local backup remains available; remote upload needs attention."
                    : "Local backup remains available; remote upload failed."
                  : "Local backup availability is not confirmed."}
              </p>
            ) : null}
            {d.recovery_url ? (
              <a className="block underline" href={d.recovery_url}>
                {d.event === "test"
                  ? "Open project"
                  : "Open authenticated backup recovery"}
              </a>
            ) : null}
            <Button
              type="button"
              variant="outline"
              className="h-auto max-w-full whitespace-normal break-all"
              onClick={() => setSelected(d.id)}
            >
              Inspect delivery {d.id}
            </Button>
          </article>
        ))}
      </section>
      {selected && detail.data && !detail.isError && !blocked ? (
        <section
          aria-label="Delivery attempts"
          className="space-y-2 break-words [overflow-wrap:anywhere]"
        >
          <h3>
            Delivery {detail.data.id} · {detail.data.state}
          </h3>
          {detail.data.attempts.map((a) => (
            <p key={a.number}>
              Attempt {a.number} · {a.state} ·{" "}
              {notificationFailure(a.failure_code)} · {a.started_at}
            </p>
          ))}
          <Button
            type="button"
            variant="outline"
            onClick={() => setSelected("")}
          >
            Close delivery
          </Button>
        </section>
      ) : null}
    </div>
  );
}
export function ProjectNotifications({
  projects,
  principal,
  members,
  unavailable = false,
}: {
  projects: Project[];
  principal?: Principal;
  members: ProjectMember[];
  unavailable?: boolean;
}) {
  const [project, setProject] = useState("");
  const exists = projects.some((p) => p.id === project);
  const viewer = Boolean(
    principal &&
    (principal.roles.includes("system_admin") ||
      members.some(
        (m) => m.project_id === project && m.user_id === principal.id,
      )),
  );
  const actor = JSON.stringify([principal?.provider, principal?.id]);
  return (
    <Card
      className="min-w-0"
      role="region"
      aria-label="Project backup notifications"
    >
      <CardHeader>
        <CardTitle>Project backup notifications</CardTitle>
        <p className="text-sm">
          Configure project email and webhook delivery for backup failures and
          failed remote uploads. Inspect recorded delivery outcomes separately
          from database backup history.
        </p>
      </CardHeader>
      <CardContent className="min-w-0 space-y-4">
        <label className="block max-w-xl">
          Notifications project
          <select
            className={selectClass}
            value={exists ? project : ""}
            onChange={(e) => setProject(e.target.value)}
          >
            <option value="">Select a project</option>
            {projects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </label>
        {exists && viewer ? (
          <Notifications
            key={JSON.stringify([
              actor,
              project,
              canOwnProject(principal, members, project),
            ])}
            actor={actor}
            project={project}
            owner={canOwnProject(principal, members, project)}
            members={members.filter((m) => m.project_id === project)}
            blocked={unavailable}
          />
        ) : exists ? (
          <p role="alert">Notification access is unavailable.</p>
        ) : (
          <p>Select a project to view notification settings.</p>
        )}
      </CardContent>
    </Card>
  );
}
