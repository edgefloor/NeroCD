import { ApiError } from "@/api/errors";
import {
  databaseDeploymentKey,
  databaseDeploymentsQuery,
  databaseOperationHeld,
  databaseOperationKey,
  databaseOperationQuery,
  deployDatabase,
  type DatabaseDeploymentInput,
  type DatabaseOperation,
} from "@/api/hosting-database-deployments";
import {
  databaseDenied,
  databaseError,
  databaseRequestID,
  type DatabaseScope,
  type HostingDatabase,
} from "@/api/hosting-databases";
import { PageNavigation } from "@/components/common/PageNavigation";
import { Button } from "@/components/ui/button";
import { maximumPageOffset } from "@/router/search";
import {
  useQuery,
  useQueryClient,
  type QueryFunctionContext,
} from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState } from "react";

export function DatabaseDeployments(
  props: Parameters<typeof useDatabaseDeployments>[0],
) {
  const {
    blocked,
    denial,
    database,
    maintainer,
    unavailable,
    conflict,
    submit,
    busy,
    attempt,
    latest,
    accepted,
    error,
    knownHold,
    review,
    history,
    operation,
    offset,
    selected,
    observe,
    setSelected,
    setOffset,
    current,
  } = useDatabaseDeployments(props);

  if (blocked || denial)
    return (
      <p role="status">Database deployment access is pending or unavailable.</p>
    );
  return (
    <DatabaseDeploymentActions
      database={database}
      maintainer={maintainer}
      unavailable={unavailable}
      conflict={conflict}
      submit={submit}
      busy={busy}
      attempt={attempt}
      latest={latest}
      accepted={accepted}
      error={error}
      knownHold={knownHold}
      review={review}
      history={history}
      operation={operation}
      offset={offset}
      selected={selected}
      observe={observe}
      setSelected={setSelected}
      setOffset={setOffset}
      current={current}
    />
  );
}
function useDatabaseDeployments({
  database,
  scope,
  maintainer,
  blocked,
  onDenied,
  onRefresh,
}: {
  database: HostingDatabase;
  scope: DatabaseScope;
  maintainer: boolean;
  blocked: boolean;
  onDenied: () => void;
  onRefresh: () => Promise<boolean>;
}) {
  const client = useQueryClient();
  const active = useRef(true);
  const pending = useRef<AbortController | null>(null);
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState("");
  const [attempt, setAttempt] = useState<{
    body: DatabaseDeploymentInput;
    database: HostingDatabase;
  }>();
  const [accepted, setAccepted] = useState<DatabaseOperation>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const { latest, history, operation, observe, denial, current, knownHold } =
    useDatabaseDeploymentHistory({
      database,
      scope,
      offset,
      selected,
      accepted,
      active,
      pending,
      onDenied,
    });
  const unavailable =
    blocked ||
    Boolean(denial) ||
    busy ||
    latest.isPending ||
    latest.isError ||
    latest.isFetching ||
    history.isError ||
    history.isFetching ||
    operation.isError ||
    operation.isFetching ||
    offset !== 0 ||
    // A hold blocks new intent. Retrying the retained identity asks the server
    // for that original admission receipt without creating another operation.
    (knownHold && !attempt);
  async function submit() {
    if (
      unavailable ||
      conflict ||
      !maintainer ||
      pending.current ||
      !active.current
    )
      return;
    const intent = attempt ?? {
      database,
      body: Object.freeze({
        expected_generation: database.resource.generation,
        idempotency_key: databaseRequestID(),
      }),
    };
    setAttempt(intent);
    setBusy(true);
    setError("");
    const controller = new AbortController();
    pending.current = controller;
    try {
      const receipt = await deployDatabase(
        intent.database,
        scope,
        intent.body,
        controller.signal,
      );
      if (!active.current || controller.signal.aborted) return;
      setAccepted(receipt);
      setSelected(receipt.id);
      observe([receipt]);
      setAttempt(undefined);
      client.setQueryData(
        databaseOperationKey(scope, database.resource.id, receipt.id),
        receipt,
      );
      void latest.refetch();
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      setError(databaseError(cause));
      if (databaseDenied(cause)) {
        active.current = false;
        onDenied();
      } else if (cause instanceof ApiError && cause.status === 409) {
        setConflict(true);
        void latest.refetch();
      }
    } finally {
      if (pending.current === controller) pending.current = null;
      setBusy(false);
    }
  }
  async function review() {
    if (busy || blocked || !active.current) return;
    setBusy(true);
    try {
      const [current, page] = await Promise.all([
        onRefresh(),
        latest.refetch(),
      ]);
      if (active.current && current && !page.isError) {
        setConflict(false);
        setAttempt(undefined);
        setError("");
      }
    } catch (cause) {
      if (active.current) setError(databaseError(cause));
    } finally {
      setBusy(false);
    }
  }
  return {
    blocked,
    denial,
    database,
    maintainer,
    unavailable,
    conflict,
    submit,
    busy,
    attempt,
    latest,
    accepted,
    error,
    knownHold,
    review,
    history,
    operation,
    offset,
    selected,
    observe,
    setSelected,
    setOffset,
    current,
  };
}

function DatabaseOperationEvidence({
  operation,
}: {
  operation: DatabaseOperation;
}) {
  return (
    <section
      className="min-w-0 space-y-2 rounded-lg border p-3"
      aria-label="Recorded database operation"
    >
      <h5 className="font-medium">Recorded database operation</h5>
      <dl className="space-y-1 text-sm">
        <dt>Operation ID</dt>
        <dd>{operation.id}</dd>
        <dt>Recorded state</dt>
        <dd>{operation.state}</dd>
        <dt>Recorded configuration generation</dt>
        <dd>{operation.snapshot.resource_generation}</dd>
        <dt>Recorded at</dt>
        <dd>{operation.updated_at}</dd>
        <dt>Mutation hold</dt>
        <dd>
          {operation.mutation_hold || operation.restore_ownership_held
            ? "Held"
            : "No recorded hold"}
        </dd>
        <dt>Release</dt>
        <dd>{operation.release?.canonical_image ?? "Not prepared"}</dd>
        <dt>Plan hash</dt>
        <dd>{operation.plan_hash ?? "Not prepared"}</dd>
        <dt>Recorded engine readiness</dt>
        <dd>
          {operation.result
            ? operation.result.ready
              ? "Ready at recorded completion"
              : "Not ready at recorded completion"
            : "No completion evidence"}
        </dd>
      </dl>
      {operation.result?.uncertain ? (
        <p role="alert">
          Engine effects are uncertain. A new deployment remains blocked.
        </p>
      ) : null}
    </section>
  );
}

function DatabaseDeploymentHistory({
  latest,
  history,
}: {
  latest: ReturnType<typeof useDatabaseDeployments>["latest"];
  history: ReturnType<typeof useDatabaseDeployments>["history"];
}) {
  return (
    <>
      {latest.isPending || history.isPending ? (
        <p role="status">Loading database deployment history...</p>
      ) : null}
    </>
  );
}

function DatabaseDeploymentActions({
  database,
  maintainer,
  unavailable,
  conflict,
  submit,
  busy,
  attempt,
  latest,
  accepted,
  error,
  knownHold,
  review,
  history,
  operation,
  offset,
  selected,
  observe,
  setSelected,
  setOffset,
  current,
}: {
  database: HostingDatabase;
  maintainer: boolean;
  unavailable: boolean;
  conflict: boolean;
  submit: () => Promise<void>;
  busy: boolean;
  attempt: ReturnType<typeof useDatabaseDeployments>["attempt"];
  latest: ReturnType<typeof useDatabaseDeployments>["latest"];
  accepted: DatabaseOperation | undefined;
  error: string;
  knownHold: boolean;
  review: () => Promise<void>;
  history: ReturnType<typeof useDatabaseDeployments>["history"];
  operation: ReturnType<typeof useDatabaseDeployments>["operation"];
  offset: number;
  selected: string;
  observe: ReturnType<typeof useDatabaseDeployments>["observe"];
  setSelected: React.Dispatch<React.SetStateAction<string>>;
  setOffset: React.Dispatch<React.SetStateAction<number>>;
  current: DatabaseOperation | undefined;
}) {
  return (
    <section
      className="min-w-0 space-y-3 rounded-xl border p-3 [overflow-wrap:anywhere]"
      aria-label={`Database deployments for ${database.resource.name}`}
    >
      <h4 className="font-medium">Database deployments</h4>
      <p>
        Deploy the saved {database.engine} configuration, generation{" "}
        {database.resource.generation}. The server checks current runner
        capability, placement, storage, and admission before execution.
      </p>
      <p>
        Private database network. No host port is published and no public
        database URL is provided.
      </p>
      <p className="text-sm text-muted-foreground">
        Readiness below is recorded deployment evidence, not continuous health
        or a TLS or database encryption guarantee. Review{" "}
        <a className="underline" href="/runners">
          Servers
        </a>{" "}
        for runner readiness.
      </p>
      <DatabaseDeploymentRequest
        maintainer={maintainer}
        unavailable={unavailable}
        conflict={conflict}
        submit={submit}
        busy={busy}
        attempt={attempt}
        latest={latest}
        accepted={accepted}
      />
      {attempt ? (
        <p className="text-sm">
          Retained deployment request ID: {attempt.body.idempotency_key}.
          Generation {attempt.body.expected_generation}.
        </p>
      ) : null}
      {error ? (
        <p role="alert">Database deployment was not confirmed. {error}</p>
      ) : null}
      {knownHold ? (
        <p role="status">
          A known operation is active or holds mutations. Review its recorded
          status before deploying again.
        </p>
      ) : null}
      {conflict ? (
        <DatabaseDeploymentConflict busy={busy} review={review} />
      ) : null}
      <Button
        type="button"
        variant="outline"
        className="h-auto max-w-full whitespace-normal"
        disabled={
          busy ||
          latest.isFetching ||
          history.isFetching ||
          operation.isFetching
        }
        onClick={() => {
          void latest.refetch();
          if (offset !== 0) void history.refetch();
          if (selected) void operation.refetch();
        }}
      >
        Refresh database deployment history
      </Button>
      <DatabaseDeploymentHistory latest={latest} history={history} />
      {latest.isError || history.isError ? (
        <p role="alert">
          Database deployment history is unavailable.{" "}
          {databaseError(latest.error ?? history.error)}
        </p>
      ) : null}
      <RecordedDatabaseDeployments
        history={history}
        observe={observe}
        setSelected={setSelected}
        setOffset={setOffset}
        offset={offset}
      />
      {operation.isError ? (
        <p role="alert">
          Database operation is unavailable. {databaseError(operation.error)}
        </p>
      ) : null}
      {current && !operation.isError ? (
        <DatabaseOperationEvidence operation={current} />
      ) : null}
    </section>
  );
}

function useDatabaseDeploymentHistory({
  database,
  scope,
  offset,
  selected,
  accepted,
  active,
  pending,
  onDenied,
}: {
  database: HostingDatabase;
  scope: DatabaseScope;
  offset: number;
  selected: string;
  accepted: DatabaseOperation | undefined;
  active: React.RefObject<boolean>;
  pending: React.RefObject<AbortController | null>;
  onDenied: () => void;
}) {
  const client = useQueryClient();
  // Keep observed holds across page changes. A receipt for the same operation
  // can release its hold; a different page cannot erase it.
  const [holds, setHolds] = useState<
    Record<string, { held: boolean; updatedAt: string }>
  >({});
  const observe = useCallback((operations: DatabaseOperation[]) => {
    setHolds((previous) => {
      const next = { ...previous };
      for (const operation of operations) {
        const prior = next[operation.id];
        if (
          !prior ||
          Date.parse(operation.updated_at) >= Date.parse(prior.updatedAt)
        )
          next[operation.id] = {
            held: databaseOperationHeld(operation),
            updatedAt: operation.updated_at,
          };
      }
      return next;
    });
  }, []);
  function observedPage(offset: number) {
    const definition = databaseDeploymentsQuery(database, scope, offset);
    return {
      ...definition,
      queryFn: async (context: QueryFunctionContext<(string | number)[]>) => {
        const read = definition.queryFn;
        if (typeof read !== "function")
          throw new Error("Database history unavailable");
        const page = await read(context);
        if (active.current && !context.signal.aborted) observe(page.items);
        return page;
      },
    };
  }
  const latest = useQuery(observedPage(0));
  const history = useQuery(observedPage(offset));
  const operationDefinition = databaseOperationQuery(database, scope, selected);
  const operation = useQuery({
    ...operationDefinition,
    queryFn: async (context) => {
      const read = operationDefinition.queryFn;
      if (typeof read !== "function")
        throw new Error("Database operation unavailable");
      const receipt = await read(context);
      if (active.current && !context.signal.aborted) observe([receipt]);
      return receipt;
    },
    enabled: Boolean(selected),
    refetchInterval: (query) =>
      query.state.status !== "error" &&
      query.state.data &&
      databaseOperationHeld(query.state.data)
        ? 5000
        : false,
  });
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      pending.current?.abort();
    };
  }, [active, pending]);
  const denial = [latest.error, history.error, operation.error].find(
    databaseDenied,
  );
  useEffect(() => {
    if (!denial) return;
    active.current = false;
    pending.current?.abort();
    client.removeQueries({
      queryKey: databaseDeploymentKey(scope, database.resource.id),
    });
    client.removeQueries({
      queryKey: databaseOperationKey(scope, database.resource.id, "").slice(
        0,
        -1,
      ),
    });
    onDenied();
  }, [denial, client, scope, database.resource.id, onDenied, active, pending]);
  const current =
    operation.data && !operation.isError ? operation.data : accepted;
  const held = (item: DatabaseOperation) =>
    databaseOperationHeld(
      current?.id === item.id &&
        Date.parse(current.updated_at) >= Date.parse(item.updated_at)
        ? current
        : item,
    );
  const knownHold =
    Object.entries(holds).some(([id, value]) =>
      current?.id === id &&
      Date.parse(current.updated_at) >= Date.parse(value.updatedAt)
        ? databaseOperationHeld(current)
        : value.held,
    ) ||
    Boolean(accepted && held(accepted)) ||
    Boolean(current && databaseOperationHeld(current)) ||
    Boolean(history.data?.items.some(held));
  return { latest, history, operation, observe, denial, current, knownHold };
}

function DatabaseDeploymentConflict({
  busy,
  review,
}: {
  busy: boolean;
  review: () => Promise<void>;
}) {
  return (
    <section aria-label="Database deployment conflict" className="space-y-2">
      <p>
        Admission conflicted with current configuration or an operation hold.
        Refresh and review before starting another deliberate deployment.
      </p>
      <Button
        type="button"
        variant="outline"
        className="h-auto max-w-full whitespace-normal"
        disabled={busy}
        onClick={() => void review()}
      >
        Refresh and review database configuration
      </Button>
    </section>
  );
}

function DatabaseDeploymentRequest({
  maintainer,
  unavailable,
  conflict,
  submit,
  busy,
  attempt,
  latest,
  accepted,
}: {
  maintainer: boolean;
  unavailable: boolean;
  conflict: boolean;
  submit: () => Promise<void>;
  busy: boolean;
  attempt: ReturnType<typeof useDatabaseDeployments>["attempt"];
  latest: ReturnType<typeof useDatabaseDeployments>["latest"];
  accepted: DatabaseOperation | undefined;
}) {
  return (
    <>
      {maintainer ? (
        <Button
          type="button"
          className="h-auto max-w-full whitespace-normal"
          disabled={unavailable || conflict}
          onClick={() => void submit()}
        >
          {busy
            ? "Requesting database deployment..."
            : attempt && !conflict
              ? "Retry identical deployment request"
              : latest.data?.total || accepted
                ? "Deploy again"
                : "Deploy database"}
        </Button>
      ) : (
        <p>Project maintainer access is required to deploy this database.</p>
      )}
    </>
  );
}

function RecordedDatabaseDeployments({
  history,
  observe,
  setSelected,
  setOffset,
  offset,
}: {
  history: ReturnType<typeof useDatabaseDeployments>["history"];
  observe: ReturnType<typeof useDatabaseDeployments>["observe"];
  setSelected: React.Dispatch<React.SetStateAction<string>>;
  setOffset: React.Dispatch<React.SetStateAction<number>>;
  offset: number;
}) {
  return (
    <>
      {!history.isError && history.data ? (
        <>
          <ul className="space-y-2" aria-label="Database deployment history">
            {history.data.items.map((item) => (
              <li key={item.id}>
                <Button
                  type="button"
                  variant="outline"
                  className="h-auto max-w-full whitespace-normal text-left"
                  onClick={() => {
                    observe([item]);
                    setSelected(item.id);
                  }}
                >
                  {item.id} · generation {item.snapshot.resource_generation} ·{" "}
                  {item.state}
                </Button>
              </li>
            ))}
          </ul>
          {history.data.items.length === 0 ? (
            <p>No recorded database deployments.</p>
          ) : null}
          <PageNavigation
            page={history.data}
            label="database deployments"
            loading={history.isFetching}
            onFirst={() => setOffset(0)}
            onPrevious={() => setOffset(Math.max(0, offset - 20))}
            onNext={() => setOffset(Math.min(maximumPageOffset, offset + 20))}
          />
        </>
      ) : null}
    </>
  );
}
