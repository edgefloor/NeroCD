import { ApiError } from "@/api";
import {
  bindResourceDestination,
  destinationKeys,
  destinationsQuery,
  resourceBindingQuery,
} from "@/api/destinations";
import type { Resource } from "@/api/hosting";
import { PageNavigation } from "@/components/common/PageNavigation";
import { Button } from "@/components/ui/button";
import { maximumPageOffset } from "@/router/search";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useId, useState } from "react";

export function ResourceDestination(props: {
  resource: Resource;
  writable: boolean;
  blocked: boolean;
}) {
  return (
    <Placement
      key={`${props.resource.project_id}:${props.resource.id}`}
      {...props}
    />
  );
}
function Placement(props: {
  resource: Resource;
  writable: boolean;
  blocked: boolean;
}) {
  const viewModel = usePlacement(props);
  const { resource, error, notice, binding, unbound, destinations, failed } =
    viewModel;
  return (
    <section
      className="min-w-0 space-y-3 border-t pt-3"
      aria-label={`Desired destination for ${resource.name}`}
    >
      <h5 className="font-medium">Desired destination</h5>
      <p className="text-xs text-muted-foreground">
        Saved placement records where this application should run. It does not
        deploy it or report runtime health.
      </p>
      {binding.isPending ? (
        <p role="status">Loading saved placement...</p>
      ) : null}
      {failed ? (
        <div className="space-y-2">
          <p role="alert">
            Unable to load placement information. Your selection is preserved;
            changes are paused.
          </p>
          <Button
            type="button"
            variant="outline"
            disabled={binding.isFetching || destinations.isFetching}
            onClick={() => {
              void binding.refetch();
              if (unbound) void destinations.refetch();
            }}
          >
            Retry placement
          </Button>
        </div>
      ) : null}
      {binding.data ? (
        <dl className="space-y-1 break-all text-sm">
          <dt>Saved placement</dt>
          <dd>{binding.data.destination_id}</dd>
          <dt>Runtime namespace</dt>
          <dd>{binding.data.runtime_namespace}</dd>
          <dt>Binding ID</dt>
          <dd>{binding.data.id}</dd>
        </dl>
      ) : null}
      <UnboundPlacement {...viewModel} />
      {error ? <p role="alert">{error}</p> : null}
      {notice ? <p role="status">{notice}</p> : null}
    </section>
  );
}

function usePlacement({
  resource,
  writable,
  blocked,
}: {
  resource: Resource;
  writable: boolean;
  blocked: boolean;
}) {
  const id = useId();
  const client = useQueryClient();
  const [offset, setOffset] = useState(0);
  const [selected, setSelected] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const binding = useQuery(
    resourceBindingQuery(resource.project_id, resource.id),
  );
  const unbound = binding.data === null;
  const destinations = useQuery({
    ...destinationsQuery({
      project_id: resource.project_id,
      limit: 20,
      offset,
    }),
    enabled: unbound,
  });
  const failed = binding.isError || (unbound && destinations.isError);
  const choices = destinations.data?.items ?? [];
  async function save(event: FormEvent) {
    event.preventDefault();
    if (
      !writable ||
      blocked ||
      failed ||
      busy ||
      !unbound ||
      !choices.some((item) => item.id === selected)
    )
      return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await bindResourceDestination(resource.id, selected);
      await client.invalidateQueries({
        queryKey: destinationKeys.binding(resource.project_id, resource.id),
      });
      setNotice(
        "Desired destination saved. This does not deploy the application.",
      );
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 409) {
        const current = await binding.refetch();
        await destinations.refetch();
        setError(
          current.isSuccess && current.data
            ? "A saved placement already exists. Showing the saved binding; no placement was overwritten."
            : "Placement was not saved. Review the refreshed binding and available destinations before retrying explicitly.",
        );
      } else
        setError(
          "Unable to save desired destination. Your selection is preserved. Check project access and retry.",
        );
    } finally {
      setBusy(false);
    }
  }
  return {
    resource,
    writable,
    blocked,
    id,
    offset,
    setOffset,
    selected,
    setSelected,
    busy,
    error,
    notice,
    binding,
    unbound,
    destinations,
    failed,
    choices,
    save,
  };
}

type UnboundPlacementModel = Pick<
  ReturnType<typeof usePlacement>,
  | "unbound"
  | "destinations"
  | "resource"
  | "save"
  | "id"
  | "selected"
  | "writable"
  | "blocked"
  | "busy"
  | "setSelected"
  | "choices"
  | "failed"
  | "setOffset"
  | "offset"
>;
function UnboundPlacement(viewModel: UnboundPlacementModel) {
  const {
    unbound,
    destinations,
    resource,
    save,
    id,
    selected,
    writable,
    blocked,
    busy,
    choices,
    failed,
    setOffset,
    offset,
  } = viewModel;

  if (!unbound) return null;
  return (
    <>
      <p className="text-sm">No saved placement.</p>
      {destinations.isPending ? (
        <p role="status">Loading available destinations...</p>
      ) : null}
      <form
        aria-label={`Save destination for ${resource.name}`}
        onSubmit={(event) => void save(event)}
        className="space-y-2"
      >
        <label htmlFor={`${id}-destination`}>Available destination</label>
        <DestinationSelection {...viewModel} />
        {!destinations.isPending &&
        !destinations.isError &&
        choices.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            No destinations granted on this page. A runner administrator can
            grant this project access.
          </p>
        ) : null}
        {writable ? (
          <Button
            type="submit"
            disabled={
              blocked ||
              failed ||
              busy ||
              destinations.isPending ||
              !choices.some((item) => item.id === selected)
            }
          >
            {busy ? "Saving placement..." : "Save desired destination"}
          </Button>
        ) : (
          <p className="text-xs text-muted-foreground">
            Project maintainer access is required to save placement.
          </p>
        )}
      </form>
      <PageNavigation
        label={`destinations for ${resource.name}`}
        page={destinations.data}
        loading={destinations.isFetching || busy}
        onFirst={() => setOffset(0)}
        onPrevious={() => setOffset(Math.max(0, offset - 20))}
        onNext={() => setOffset(Math.min(maximumPageOffset, offset + 20))}
      />
    </>
  );
}

type DestinationSelectionModel = Pick<
  ReturnType<typeof usePlacement>,
  | "id"
  | "selected"
  | "writable"
  | "blocked"
  | "busy"
  | "setSelected"
  | "choices"
>;
function DestinationSelection(viewModel: DestinationSelectionModel) {
  const { id, selected, writable, blocked, busy, setSelected, choices } =
    viewModel;

  return (
    <select
      id={`${id}-destination`}
      className="h-10 w-full min-w-0 rounded-xl border bg-card px-2 text-sm"
      value={selected}
      disabled={!writable || blocked || busy}
      onChange={(event) => setSelected(event.target.value)}
    >
      <option value="">Select a destination</option>
      {selected && !choices.some((item) => item.id === selected) ? (
        <option value={selected} disabled>
          Previous selection unavailable: {selected}
        </option>
      ) : null}
      {choices.map((item) => (
        <option key={item.id} value={item.id}>
          {item.name} · {item.server_id} · {item.runtime_kind}
        </option>
      ))}
    </select>
  );
}
