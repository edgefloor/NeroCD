import { ApiError, listProjectMembers, principalQuery } from "@/api";
import { type Resource } from "@/api/hosting";
import {
  getHostingRuntimeConfiguration,
  putHostingRuntimeConfiguration,
  type HostingRuntimeConfiguration,
  type HostingRuntimeConfigurationInput,
} from "@/api/hosting-runtime-config";
import {
  listManagedSecrets,
  listManagedSecretVersions,
} from "@/api/managed-secrets";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { canOwnProject } from "@/lib/permissions";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { draftRowIdentity } from "./draftRowIdentity";

type Entry = HostingRuntimeConfigurationInput["entries"][number];
const button = "h-auto max-w-full whitespace-normal";
const select =
  "min-h-10 w-full min-w-0 rounded-xl border bg-background p-2 text-sm";
const denied = (cause: unknown) =>
  cause instanceof ApiError && [401, 403, 404].includes(cause.status);
const scoped = (value: HostingRuntimeConfiguration, resource: Resource) => {
  if (
    value.resource_id !== resource.id ||
    value.project_id !== resource.project_id ||
    !Number.isSafeInteger(value.runtime_generation) ||
    value.runtime_generation < 0
  )
    throw new Error("Runtime configuration scope mismatch");
  return value;
};
function validation(entries: Entry[]): string {
  if (entries.length > 64) return "At most 64 runtime variables are allowed.";
  const names = new Set<string>(),
    references = new Set<string>();
  for (const entry of entries) {
    if (
      !/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(entry.name) ||
      entry.name.startsWith("NEROCD_")
    )
      return "Use a unique ASCII variable name of at most 128 characters. Names starting with NEROCD_ are reserved.";
    if (names.has(entry.name)) return "Runtime variable names must be unique.";
    names.add(entry.name);
    if (entry.kind === "literal") {
      if (
        new TextEncoder().encode(entry.value).length > 4096 ||
        /[\0\r\n]/.test(entry.value)
      )
        return "Nonsecret values allow at most 4096 UTF-8 bytes and no NUL or line breaks.";
    } else {
      if (
        !entry.secret.secret_id ||
        !Number.isSafeInteger(entry.secret.version) ||
        entry.secret.version < 1
      )
        return "Choose an existing managed secret and an exact version for every secret reference.";
      references.add(JSON.stringify(entry.secret));
    }
  }
  return references.size > 16
    ? "At most 16 distinct managed secret versions are allowed."
    : "";
}

export function ResourceRuntimeConfiguration({
  resource,
  blocked,
}: {
  resource: Resource;
  blocked: boolean;
}) {
  const identity = useQuery(principalQuery());
  if (
    blocked ||
    identity.isPending ||
    identity.isFetching ||
    identity.isError ||
    !identity.data
  )
    return (
      <p role={identity.isError ? "alert" : "status"}>
        Runtime configuration access is pending or unavailable.
      </p>
    );
  const actor = JSON.stringify([identity.data.provider, identity.data.id]);
  return (
    <RuntimeAccess
      key={JSON.stringify([actor, resource.project_id, resource.id])}
      actor={actor}
      resource={resource}
    />
  );
}
function RuntimeAccess({
  actor,
  resource,
}: {
  actor: string;
  resource: Resource;
}) {
  const principal = useQuery(principalQuery());
  const members = useQuery({
    queryKey: ["runtimeConfigurationMembers", actor, resource.project_id],
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    queryFn: async ({ signal }) => {
      const rows = [];
      let offset = 0;
      for (;;) {
        const page = await listProjectMembers(
          { project_id: resource.project_id, limit: 100, offset },
          { signal },
        );
        if (page.items.some((row) => row.project_id !== resource.project_id))
          throw new Error("Membership scope mismatch");
        rows.push(...page.items);
        if (!page.count || page.offset + page.count >= page.total) return rows;
        const next = page.offset + page.count;
        if (next <= offset || next > 100000)
          throw new Error("Membership pagination unavailable");
        offset = next;
      }
    },
  });
  const current =
    principal.data &&
    JSON.stringify([principal.data.provider, principal.data.id]) === actor;
  const ready =
    current &&
    !principal.isFetching &&
    !principal.isError &&
    !members.isPending &&
    !members.isFetching &&
    !members.isError;
  const readable =
    ready &&
    (principal.data.roles.includes("system_admin") ||
      members.data?.some((row) => row.user_id === principal.data.id));
  const owner = Boolean(
    ready &&
      members.data &&
      canOwnProject(principal.data, members.data, resource.project_id),
  );
  return (
    <section
      className="min-w-0 space-y-3 border-t pt-3"
      aria-label={`Runtime configuration for ${resource.name}`}
    >
      <h5 className="font-medium">Runtime variables</h5>
      <p className="text-sm">
        Save desired variables for a new deployment. Saving does not restart or
        rebuild a workload. Nonsecret values are readable by project viewers.
        Managed secrets use an exact reference and version; their values stay
        hidden.
      </p>
      <Button
        className={button}
        type="button"
        variant="outline"
        disabled={members.isFetching}
        onClick={() => void members.refetch()}
      >
        Refresh runtime configuration access
      </Button>
      {!ready ? (
        <p role={members.isError ? "alert" : "status"}>
          Unable to verify current runtime configuration access. Drafts and
          pending requests are cleared.
        </p>
      ) : !readable ? (
        <p role="alert">
          Project access is required to read runtime configuration.
        </p>
      ) : (
        <RuntimeValues
          key={JSON.stringify([actor, resource.id, owner])}
          resource={resource}
          actor={actor}
          owner={owner}
        />
      )}
    </section>
  );
}
function RuntimeValues({
  resource,
  actor,
  owner,
}: {
  resource: Resource;
  actor: string;
  owner: boolean;
}) {
  const client = useQueryClient();
  const key = [
    "runtimeConfiguration",
    actor,
    resource.project_id,
    resource.id,
  ] as const;
  const current = useQuery({
    queryKey: key,
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    queryFn: async ({ signal }) =>
      scoped(
        (await getHostingRuntimeConfiguration(resource.id, { signal }))
          .runtime_configuration,
        resource,
      ),
  });
  const [deniedAccess, setDeniedAccess] = useState(false);
  useEffect(
    () => () => {
      client.removeQueries({
        queryKey: [
          "runtimeConfiguration",
          actor,
          resource.project_id,
          resource.id,
        ],
      });
    },
    [client, actor, resource.project_id, resource.id],
  );
  if (deniedAccess || denied(current.error))
    return (
      <p role="alert">
        Runtime configuration access is unavailable. Refresh access before
        continuing.
      </p>
    );
  return (
    <>
      {current.isPending ? (
        <p role="status">Loading saved runtime configuration...</p>
      ) : null}
      {current.isError ? (
        <p role="alert">
          Unable to load runtime configuration. Values are hidden and saves are
          paused.
        </p>
      ) : null}
      <Button
        className={button}
        type="button"
        variant="outline"
        disabled={current.isFetching}
        onClick={() => void current.refetch()}
      >
        Refresh saved runtime configuration
      </Button>
      {!current.isError && current.data ? (
        <>
          <p>
            Saved runtime generation {current.data.runtime_generation} ·
            resource generation {current.data.resource_generation}
          </p>
          <p role="status">
            Configured values are not evidence of workload application. Runtime
            delivery and runner prerequisites must be verified for a new
            deployment.
          </p>
          <ul
            className="space-y-1 break-all"
            aria-label="Saved runtime variables"
          >
            {current.data.entries.map((entry) => (
              <li key={entry.name}>
                {entry.name}:{" "}
                {entry.kind === "literal"
                  ? `Nonsecret value ${JSON.stringify(entry.value)}`
                  : `Managed secret ${entry.secret.secret_id} · version ${entry.secret.version} · value hidden`}
              </li>
            ))}
          </ul>
          {current.data.entries.length === 0 ? (
            <p>No saved runtime variables.</p>
          ) : null}
        </>
      ) : null}
      {current.data && owner ? (
        <RuntimeEditor
          resource={resource}
          actor={actor}
          initial={current.data}
          paused={current.isFetching || current.isError}
          onDenied={() => {
            setDeniedAccess(true);
            client.removeQueries({ queryKey: key });
          }}
          onSaved={async () => {
            await current.refetch();
          }}
        />
      ) : current.data ? (
        <p>Project owner access is required to save runtime variables.</p>
      ) : null}
    </>
  );
}
function SecretChoice({
  entry,
  onChange,
  disabled,
  resource,
  actor,
  onDenied,
}: {
  entry: Extract<Entry, { kind: "managed_secret" }>;
  onChange: (entry: Entry) => void;
  disabled: boolean;
  resource: Resource;
  actor: string;
  onDenied: () => void;
}) {
  const key = [
    "runtimeSecretMetadata",
    actor,
    resource.project_id,
    resource.id,
  ] as const;
  const secrets = useQuery({
    queryKey: key,
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    queryFn: async ({ signal }) => {
      const rows = [];
      let offset = 0;
      for (;;) {
        const page = await listManagedSecrets(
          { project_id: resource.project_id, limit: 100, offset },
          { signal },
        );
        if (page.items.some((row) => row.project_id !== resource.project_id))
          throw new Error("Secret scope mismatch");
        rows.push(
          ...page.items.map(({ id, name, status }) => ({ id, name, status })),
        );
        if (!page.count || page.offset + page.count >= page.total) return rows;
        const next = page.offset + page.count;
        if (next <= offset || next > 100000)
          throw new Error("Secret pagination unavailable");
        offset = next;
      }
    },
  });
  const versions = useQuery({
    queryKey: [...key, entry.secret.secret_id],
    enabled: Boolean(entry.secret.secret_id),
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    queryFn: async ({ signal }) => {
      const rows: number[] = [];
      let offset = 0;
      for (;;) {
        const page = await listManagedSecretVersions(
          entry.secret.secret_id,
          { limit: 100, offset },
          { signal },
        );
        if (page.items.some((row) => row.secret_id !== entry.secret.secret_id))
          throw new Error("Secret version scope mismatch");
        rows.push(...page.items.map((row) => row.version));
        if (!page.count || page.offset + page.count >= page.total) return rows;
        const next = page.offset + page.count;
        if (next <= offset || next > 100000)
          throw new Error("Version pagination unavailable");
        offset = next;
      }
    },
  });
  useEffect(() => {
    if (denied(secrets.error) || denied(versions.error)) onDenied();
  }, [secrets.error, versions.error, onDenied]);
  return (
    <>
      <label className="block">
        Existing managed secret for {entry.name || "variable"}
        <select
          className={select}
          value={entry.secret.secret_id}
          disabled={disabled || secrets.isPending || secrets.isError}
          onChange={(event) =>
            onChange({
              ...entry,
              secret: { secret_id: event.target.value, version: 0 },
            })
          }
        >
          <option value="">Choose secret metadata</option>
          {secrets.data
            ?.filter((secret) => secret.status === "active")
            .map((secret) => (
              <option key={secret.id} value={secret.id}>
                {secret.name} · {secret.id}
              </option>
            ))}
        </select>
      </label>
      <label className="block">
        Exact secret version for {entry.name || "variable"}
        <select
          className={select}
          value={entry.secret.version || ""}
          disabled={
            disabled ||
            !entry.secret.secret_id ||
            versions.isPending ||
            versions.isError
          }
          onChange={(event) =>
            onChange({
              ...entry,
              secret: { ...entry.secret, version: Number(event.target.value) },
            })
          }
        >
          <option value="">Choose exact version</option>
          {versions.data?.map((version) => (
            <option key={version} value={version}>
              {version}
            </option>
          ))}
        </select>
      </label>
      {entry.secret.secret_id &&
      secrets.isSuccess &&
      !secrets.data.some(
        (secret) =>
          secret.id === entry.secret.secret_id && secret.status === "active",
      ) ? (
        <p role="alert">
          The selected secret is unavailable or revoked. Review an active secret
          reference and exact version.
        </p>
      ) : null}
      {secrets.isError || (entry.secret.secret_id && versions.isError) ? (
        <>
          <p role="alert">
            Secret reference metadata is unavailable. No secret value was
            requested.
          </p>
          <Button
            type="button"
            className={button}
            onClick={() => {
              void secrets.refetch();
              if (entry.secret.secret_id) void versions.refetch();
            }}
          >
            Refresh runtime secret metadata
          </Button>
        </>
      ) : null}
    </>
  );
}
function RuntimeEditor({
  resource,
  actor,
  initial,
  paused,
  onDenied,
  onSaved,
}: {
  resource: Resource;
  actor: string;
  initial: HostingRuntimeConfiguration;
  paused: boolean;
  onDenied: () => void;
  onSaved: () => Promise<void>;
}) {
  const [rowIdentity] = useState(draftRowIdentity);
  const [base, setBase] = useState(initial),
    [entries, setEntries] = useState<Entry[]>(initial.entries);
  const [attempt, setAttempt] = useState<HostingRuntimeConfigurationInput>(),
    [latest, setLatest] = useState<HostingRuntimeConfiguration>();
  const [busy, setBusy] = useState(false),
    [conflict, setConflict] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState("");
  const client = useQueryClient();
  const active = useRef(true),
    request = useRef<AbortController | null>(null);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      request.current?.abort();
      client.removeQueries({
        queryKey: [
          "runtimeSecretMetadata",
          actor,
          resource.project_id,
          resource.id,
        ],
      });
    };
  }, [client, actor, resource.project_id, resource.id]);
  const update = (index: number, entry: Entry) => {
    setEntries((rows) =>
      rows.map((row, i) =>
        i === index ? rowIdentity.replace(row, entry) : row,
      ),
    );
    setError("");
  };
  async function save(event: FormEvent) {
    event.preventDefault();
    if (request.current || conflict || paused) return;
    const problem = validation(entries);
    if (problem) {
      setError(problem);
      return;
    }
    const bytes = crypto.getRandomValues(new Uint8Array(16));
    const body = attempt ?? {
      expected_resource_generation: base.resource_generation,
      expected_runtime_generation: base.runtime_generation,
      request_id: Array.from(bytes, (byte) =>
        byte.toString(16).padStart(2, "0"),
      ).join(""),
      entries,
    };
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setAttempt(body);
    setError("");
    setNotice("");
    try {
      const saved = scoped(
        (
          await putHostingRuntimeConfiguration(resource.id, body, {
            signal: controller.signal,
          })
        ).runtime_configuration,
        resource,
      );
      if (!active.current || controller.signal.aborted) return;
      const matches =
        saved.entries.length === body.entries.length &&
        saved.entries.every((entry) => {
          const expected = body.entries.find((row) => row.name === entry.name);
          return (
            expected?.kind === entry.kind &&
            (entry.kind === "literal"
              ? expected.kind === "literal" && expected.value === entry.value
              : expected.kind === "managed_secret" &&
                expected.secret.secret_id === entry.secret.secret_id &&
                expected.secret.version === entry.secret.version)
          );
        });
      if (
        saved.runtime_generation !== body.expected_runtime_generation + 1 ||
        !matches
      )
        throw new Error("Saved configuration does not match request");
      setBase(saved);
      setAttempt(undefined);
      setNotice(
        "Runtime configuration saved. Workload application requires a new deployment.",
      );
      await onSaved();
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      if (denied(cause)) {
        onDenied();
        return;
      }
      if (cause instanceof ApiError && cause.status === 409) {
        setConflict(true);
        setError(
          "Runtime or resource generation changed. Your draft and retained request are preserved. Refresh and review before a separate save.",
        );
      } else if (cause instanceof ApiError && cause.status === 400) {
        setAttempt(undefined);
        setError(
          "The server rejected this configuration. Review the names, nonsecret values, and exact secret references. Your draft is preserved.",
        );
      } else
        setError(
          "Save delivery is unconfirmed. Retry the identical retained request to recover its result.",
        );
    } finally {
      if (request.current === controller) request.current = null;
      if (active.current) setBusy(false);
    }
  }
  async function refresh() {
    if (request.current) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setLatest(undefined);
    try {
      const value = scoped(
        (
          await getHostingRuntimeConfiguration(resource.id, {
            signal: controller.signal,
          })
        ).runtime_configuration,
        resource,
      );
      if (active.current && !controller.signal.aborted) setLatest(value);
    } catch (cause) {
      if (active.current && !controller.signal.aborted) {
        if (denied(cause)) onDenied();
        else
          setError(
            "Unable to refresh saved runtime configuration. The retained request is unchanged.",
          );
      }
    } finally {
      if (request.current === controller) request.current = null;
      if (active.current) setBusy(false);
    }
  }
  return (
    <form
      aria-label="Save runtime variables"
      className="min-w-0 space-y-3 rounded-lg border p-3"
      onSubmit={(event) => void save(event)}
    >
      <p>
        Reviewed runtime generation {base.runtime_generation} · resource
        generation {base.resource_generation}
      </p>
      {entries.map((entry, index) => (
        <RuntimeVariableRow
          key={rowIdentity.key(entry)}
          entry={entry}
          index={index}
          disabled={busy || Boolean(attempt)}
          onChange={(value) => update(index, value)}
          onRemove={() =>
            setEntries((rows) => rows.filter((_, i) => i !== index))
          }
          resource={resource}
          actor={actor}
          onDenied={onDenied}
        />
      ))}
      <Button
        className={button}
        type="button"
        variant="outline"
        disabled={busy || Boolean(attempt) || entries.length >= 64}
        onClick={() =>
          setEntries((rows) => [
            ...rows,
            { name: "", kind: "literal", value: "" },
          ])
        }
      >
        Add runtime variable
      </Button>
      <Button
        className={button}
        type="submit"
        disabled={busy || conflict || paused}
      >
        {busy
          ? "Saving runtime configuration..."
          : attempt
            ? "Retry identical runtime save"
            : "Save runtime configuration"}
      </Button>
      {attempt ? (
        <p className="break-all text-xs">
          Retained save request {attempt.request_id} · resource/runtime
          generations {attempt.expected_resource_generation}/
          {attempt.expected_runtime_generation}. Entries remain frozen.
        </p>
      ) : null}
      {attempt ? (
        <section
          className="space-y-2"
          aria-label="Review separate runtime save"
        >
          <Button
            className={button}
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() => void refresh()}
          >
            Refresh runtime configuration for review
          </Button>
          {latest ? (
            <>
              <p>
                Current runtime generation {latest.runtime_generation} ·
                resource generation {latest.resource_generation}. Review saved
                entries before discarding the earlier request.
              </p>
              <ul className="break-all">
                {latest.entries.map((entry) => (
                  <li key={entry.name}>
                    {entry.name}:{" "}
                    {entry.kind === "literal"
                      ? JSON.stringify(entry.value)
                      : `${entry.secret.secret_id} · version ${entry.secret.version}`}
                  </li>
                ))}
              </ul>
              <Button
                className={button}
                type="button"
                disabled={busy}
                onClick={() => {
                  setBase(latest);
                  setAttempt(undefined);
                  setConflict(false);
                  setLatest(undefined);
                  setError("");
                }}
              >
                Keep draft with reviewed generations for a separate save
              </Button>
            </>
          ) : null}
        </section>
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
      {notice ? <p role="status">{notice}</p> : null}
    </form>
  );
}

function RuntimeVariableRow({
  entry,
  index,
  disabled,
  onChange,
  onRemove,
  resource,
  actor,
  onDenied,
}: {
  entry: Entry;
  index: number;
  disabled: boolean;
  onChange: (entry: Entry) => void;
  onRemove: () => void;
  resource: Resource;
  actor: string;
  onDenied: () => void;
}) {
  return (
    <fieldset
      className="min-w-0 space-y-2 rounded border p-2"
      disabled={disabled}
    >
      <legend>Runtime variable {index + 1}</legend>
      <label className="block">
        Variable name {index + 1}
        <Input
          value={entry.name}
          onChange={(event) => onChange({ ...entry, name: event.target.value })}
          maxLength={128}
          required
          spellCheck={false}
        />
      </label>
      <label className="block">
        Value type {index + 1}
        <select
          className={select}
          value={entry.kind}
          onChange={(event) =>
            onChange(
              event.target.value === "literal"
                ? { name: entry.name, kind: "literal", value: "" }
                : {
                    name: entry.name,
                    kind: "managed_secret",
                    secret: { secret_id: "", version: 0 },
                  },
            )
          }
        >
          <option value="literal">
            Nonsecret value, readable by project viewers
          </option>
          <option value="managed_secret">
            Existing managed secret reference
          </option>
        </select>
      </label>
      {entry.kind === "literal" ? (
        <label className="block">
          Nonsecret value {index + 1}
          <Input
            value={entry.value}
            onChange={(event) =>
              onChange({ ...entry, value: event.target.value })
            }
            spellCheck={false}
            autoComplete="off"
          />
        </label>
      ) : (
        <SecretChoice
          entry={entry}
          onChange={onChange}
          disabled={disabled}
          resource={resource}
          actor={actor}
          onDenied={onDenied}
        />
      )}
      <Button
        className={button}
        variant="outline"
        type="button"
        onClick={onRemove}
      >
        Remove variable {index + 1}
      </Button>
    </fieldset>
  );
}
