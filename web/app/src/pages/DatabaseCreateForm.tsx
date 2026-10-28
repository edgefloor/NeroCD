import { type FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { destinationsQuery } from "@/api/destinations";
import {
  boundedDatabaseRequest,
  createHostingDatabase,
  databaseDenied,
  databaseError,
  databaseRecipes,
  databaseRequestID,
  type HostingDatabase,
  type HostingDatabaseCreateInput,
  type DatabaseScope,
} from "@/api/hosting-databases";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { PageNavigation } from "@/components/common/PageNavigation";
import { maximumPageOffset } from "@/router/search";
import { DatabaseMetadata, useDatabaseAccess } from "./ResourceDatabase";

export function DatabaseCreateForm({
  projectID,
  environmentID,
  blocked = false,
  accessError,
}: {
  projectID: string;
  environmentID: string;
  blocked?: boolean;
  accessError?: unknown;
}) {
  const access = useDatabaseAccess(projectID, environmentID);
  const currentError = [access.error, accessError].find(databaseDenied)
    ?? access.error ?? accessError;
  const [denied, setDenied] = useState<{
    epoch: string;
    identity: string;
    error: string;
  }>();
  if (denied?.epoch === access.epoch)
    return (
      <p role="alert" className="break-all">
        Database creation access is unavailable. Retained request ID:{" "}
        {denied.identity || "none"}. {denied.error}
      </p>
    );
  if (!access.ready)
    return <p role={access.error ? "alert" : "status"}>Database creation access is pending or unavailable.</p>;
  if (!access.owner)
    return (
      <p className="text-sm">
        Project owner access is required to create database configuration.
      </p>
    );
  return (
    <Create
      key={access.epoch}
      scope={access.scope}
      blocked={blocked}
      refreshing={access.refreshing}
      accessError={currentError}
      onDenied={(identity, error) =>
        setDenied({ epoch: access.epoch, identity, error })
      }
    />
  );
}
function Create({
  scope,
  blocked,
  refreshing,
  accessError,
  onDenied,
}: {
  scope: DatabaseScope;
  blocked: boolean;
  refreshing: boolean;
  accessError?: unknown;
  onDenied: (identity: string, error: string) => void;
}) {
  const client = useQueryClient();
  const active = useRef(true);
  const request = useRef<AbortController | null>(null);
  const refresh = useRef<AbortController | null>(null);
  const [offset, setOffset] = useState(0);
  const [name, setName] = useState("");
  const [engine, setEngine] =
    useState<keyof typeof databaseRecipes>("postgresql");
  const [destination, setDestination] = useState("");
  const [attempt, setAttempt] = useState<HostingDatabaseCreateInput>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [created, setCreated] = useState<HostingDatabase>();
  const [acceptedID, setAcceptedID] = useState("");
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      request.current?.abort();
      refresh.current?.abort();
    };
  }, []);
  const definition = destinationsQuery({
    project_id: scope.project,
    limit: 20,
    offset,
  });
  const destinationKey = [
    "databaseDestinations",
    scope.actor,
    scope.project,
    scope.environment,
    offset,
  ];
  const destinations = useQuery({
    queryKey: destinationKey,
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    queryFn: (context) =>
      boundedDatabaseRequest((signal) => {
        const read = definition.queryFn;
        if (typeof read !== "function")
          throw new Error("Destination read unavailable");
        return Promise.resolve(
          read({ ...context, queryKey: definition.queryKey, signal }),
        );
      }, context.signal),
  });
  const deny = useCallback((cause: unknown, identity = attempt?.request_id ?? acceptedID) => {
    active.current = false;
    request.current?.abort();
    refresh.current?.abort();
    setAttempt(undefined);
    setCreated(undefined);
    client.removeQueries({
      queryKey: [
        "hostingDatabase",
        scope.actor,
        scope.project,
        scope.environment,
      ],
    });
    client.removeQueries({
      queryKey: [
        "databaseDestinations",
        scope.actor,
        scope.project,
        scope.environment,
      ],
    });
    onDenied(identity, databaseError(cause));
  }, [client, scope.actor, scope.project, scope.environment, onDenied, attempt?.request_id, acceptedID]);
  useEffect(() => {
    if (destinations.isError && databaseDenied(destinations.error))
      deny(destinations.error);
  }, [destinations.isError, destinations.error, deny]);
  useEffect(() => {
    if (databaseDenied(accessError)) deny(accessError);
  }, [accessError, deny]);
  const choices = destinations.data?.items ?? [];
  const selectable = choices.some((choice) => choice.id === destination);
  const paused =
    blocked ||
    refreshing ||
    busy ||
    (!attempt &&
      (destinations.isPending || destinations.isError || !selectable));
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (paused || request.current || !name.trim()) return;
    const recipe = engine === "postgresql"
      ? { engine: "postgresql" as const, recipe_version: databaseRecipes.postgresql }
      : engine === "mysql" ? { engine: "mysql" as const, recipe_version: databaseRecipes.mysql }
      : { engine: "redis" as const, recipe_version: databaseRecipes.redis };
    const body: HostingDatabaseCreateInput = attempt ?? {
      request_id: databaseRequestID(),
      project_id: scope.project,
      project_environment_id: scope.environment,
      name: name.trim(),
      destination_id: destination,
      ...recipe,
    };
    const controller = new AbortController();
    request.current = controller;
    setAttempt(body);
    setBusy(true);
    setError("");
    setNotice("");
    try {
      const database = await createHostingDatabase(body, {
        signal: controller.signal,
      });
      if (!active.current || controller.signal.aborted) return;
      setCreated(database);
      setAcceptedID(body.request_id);
      setAttempt(undefined);
      setName("");
      setNotice("Database metadata saved. Engine startup is not qualified.");
      const refreshController = new AbortController();
      refresh.current?.abort();
      refresh.current = refreshController;
      void boundedDatabaseRequest(async (signal) => {
        const key = ["hostingResources", scope.project];
        const cancel = () => {
          void client.cancelQueries({ queryKey: key });
        };
        signal.addEventListener("abort", cancel, { once: true });
        try {
          await client.invalidateQueries(
            { queryKey: key },
            { throwOnError: true },
          );
        } finally {
          signal.removeEventListener("abort", cancel);
        }
      }, refreshController.signal).catch((cause) => {
        if (!active.current || refreshController.signal.aborted) return;
        if (databaseDenied(cause)) deny(cause, body.request_id);
        else
          setNotice(
            "Database metadata saved. The resource list could not be refreshed; the returned metadata is shown below.",
          );
      });
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      if (databaseDenied(cause)) deny(cause, body.request_id);
      else setError(databaseError(cause));
    } finally {
      if (request.current === controller) request.current = null;
      if (active.current) setBusy(false);
    }
  }
  if (databaseDenied(accessError))
    return <p role="alert" className="break-all">Database access is unavailable. Retained request ID: {(attempt?.request_id ?? acceptedID) || "none"}. {databaseError(accessError)}</p>;
  return (
    <section
      className="min-w-0 space-y-3 border-t pt-4"
      aria-label="Database creation"
    >
      <form
        aria-label="Create database configuration"
        className="space-y-3"
        onSubmit={(event) => void submit(event)}
      >
        <h4 className="font-medium">Add database configuration</h4>
        <p className="text-sm">
          Save PostgreSQL, MySQL, or Redis metadata with an owned storage reference and
          encrypted credential references. Saving does not start an engine.
        </p>
        <label className="block space-y-1">
          Database name
          <Input
            required
            maxLength={255}
            value={name}
            disabled={refreshing || busy || Boolean(attempt)}
            onChange={(event) => setName(event.target.value)}
          />
        </label>
        <label className="block space-y-1">
          Database engine
          <select
            className="h-10 w-full min-w-0 rounded-xl border bg-card px-2 text-sm"
            value={engine}
            disabled={refreshing || busy || Boolean(attempt)}
            onChange={(event) => {
              if (Object.hasOwn(databaseRecipes, event.target.value))
                setEngine(event.target.value as keyof typeof databaseRecipes);
            }}
          >
            <option value="postgresql">PostgreSQL</option>
            <option value="redis">Redis</option>
            <option value="mysql">MySQL</option>
          </select>
        </label>
        <p className="break-all text-sm">Recipe: {databaseRecipes[engine]}</p>
        {engine === "mysql" ? <p className="text-sm">The destination runner requires hosting_mysql_v1 for deployment and hosting_mysql_backup_v1 for logical backup and restore. The server verifies current placement and capabilities.</p> : null}
        <label className="block space-y-1">
          Database destination
          <select
            className="h-10 w-full min-w-0 rounded-xl border bg-card px-2 text-sm"
            value={destination}
            disabled={refreshing || busy || Boolean(attempt)}
            onChange={(event) => setDestination(event.target.value)}
          >
            <option value="">Select a granted destination</option>
            {destination && !selectable ? (
              <option value={destination}>
                Retained selection: {destination}
              </option>
            ) : null}
            {choices.map((choice) => (
              <option key={choice.id} value={choice.id}>
                {choice.name} · {choice.server_id}
              </option>
            ))}
          </select>
        </label>
        {destinations.isError ? (
          <p role="alert">
            Destinations are unavailable. {databaseError(destinations.error)}
          </p>
        ) : null}
        {!destinations.isPending && !destinations.isError && !choices.length ? (
          <p>No granted destinations on this page.</p>
        ) : null}
        <PageNavigation
          label="database destinations"
          page={destinations.data}
          loading={refreshing || destinations.isFetching || busy || Boolean(attempt)}
          onFirst={() => setOffset(0)}
          onPrevious={() => setOffset(Math.max(0, offset - 20))}
          onNext={() => setOffset(Math.min(maximumPageOffset, offset + 20))}
        />
        <Button
          type="button"
          variant="outline"
          className="h-auto max-w-full whitespace-normal"
          disabled={refreshing || busy || destinations.isFetching}
          onClick={() => void destinations.refetch()}
        >
          Refresh database destinations
        </Button>
        <Button
          type="submit"
          className="h-auto max-w-full whitespace-normal"
          disabled={paused || !name.trim()}
        >
          {busy
            ? "Saving database metadata..."
            : attempt
              ? "Retry identical database request"
              : "Save database configuration"}
        </Button>
        {attempt ? (
          <>
            <p className="break-all text-sm">
              Retained request ID: {attempt.request_id}. The submitted body is
              frozen for an identical retry.
            </p>
            <Button
              type="button"
              variant="outline"
              className="h-auto max-w-full whitespace-normal"
              disabled={refreshing || busy}
              onClick={() => {
                setAttempt(undefined);
                setError("");
                setNotice(
                  "Retained intent abandoned. The earlier request may already exist. Changing this draft and saving creates a separate request.",
                );
              }}
            >
              Change draft and abandon retained request
            </Button>
          </>
        ) : null}
        {error ? <p role="alert">{error}</p> : null}
      </form>
      {acceptedID ? (
        <p className="break-all text-sm">Saved request ID: {acceptedID}</p>
      ) : null}
      {notice ? <p role="status">{notice}</p> : null}
      {refreshing ? <p role="status">Refreshing database authority; protected metadata is hidden.</p> : null}
      {refreshing && accessError ? <p role="alert">Database authority is unavailable. {databaseError(accessError)}</p> : null}
      {created && !refreshing ? (
        <DatabaseMetadata
          database={created}
          label={`Saved database metadata for ${created.resource.name}`}
        />
      ) : null}
    </section>
  );
}
