import { ApiError } from "@/api/errors";
import {
  getStackLifecycle,
  requestStackStop,
  stackLifecycleQuery,
  type StackStopInput,
} from "@/api/hosting-stack-lifecycle";
import type { StorageDeploymentScope } from "@/api/hosting-storage-deployment";
import { Button } from "@/components/ui/button";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

export function StackLifecycle(input: {
  scope: StorageDeploymentScope;
  generation: number;
  writable: boolean;
  blocked: boolean;
  mutationHeld: boolean;
  onDenied: () => void;
  onSelected: (id: string) => void;
  onRefresh: (signal?: AbortSignal) => Promise<void>;
  gitBuilds?: boolean;
}) {
  const viewModel = useStackLifecycle(input);
  const {
    gitBuilds,
    lifecycle,
    setReview,
    sent,
    busy,
    error,
    accepted,
    current,
  } = viewModel;
  return (
    <section
      className="min-w-0 space-y-3 rounded-lg border p-3"
      aria-label="Compose lifecycle"
    >
      <h6 className="font-medium">Compose lifecycle</h6>
      <p>
        Last recorded action:{" "}
        {lifecycle.isError || !current ? "unavailable" : current.state}. Check runtime status for current health.
      </p>
      {current?.state === "held" ? (
        <p role="status">
          Another action is blocked until settlement or recovery completes.
        </p>
      ) : null}
      {current?.state === "stopped" ? (
        <p>
          Recorded stop operation {current.stop_operation_id}.{" "}
          {gitBuilds
            ? "Select the deployed successful Git build and review Start in its deployment controls."
            : "Start through the saved Compose review below."}{" "}
          Start may recreate containers with different IDs and retains named
          volumes.
        </p>
      ) : null}
      <Button
        type="button"
        variant="outline"
        disabled={busy || lifecycle.isFetching}
        onClick={() => {
          if (!sent) setReview(undefined);
          void lifecycle.refetch();
        }}
      >
        Refresh Compose lifecycle
      </Button>
      <ComposeStopAction {...viewModel} />
      <ComposeStopReview {...viewModel} />
      {accepted ? (
        <p role="status">
          Stop request accepted: {accepted}. Inspect the operation for recorded
          completion; acceptance does not prove containers stopped.
        </p>
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
    </section>
  );
}

function useStackLifecycle({
  scope,
  generation,
  writable,
  blocked,
  mutationHeld,
  onDenied,
  onSelected,
  onRefresh,
  gitBuilds = false,
}: {
  scope: StorageDeploymentScope;
  generation: number;
  writable: boolean;
  blocked: boolean;
  mutationHeld: boolean;
  onDenied: () => void;
  onSelected: (id: string) => void;
  onRefresh: (signal?: AbortSignal) => Promise<void>;
  gitBuilds?: boolean;
}) {
  const lifecycle = useQuery(stackLifecycleQuery(scope));
  const [review, setReview] = useState<StackStopInput>();
  const [sent, setSent] = useState(false),
    [busy, setBusy] = useState(false),
    [conflict, setConflict] = useState(false);
  const [error, setError] = useState(""),
    [accepted, setAccepted] = useState("");
  const active = useRef(true),
    request = useRef<AbortController | null>(null);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      request.current?.abort();
    };
  }, []);
  useEffect(() => {
    if (
      lifecycle.error instanceof ApiError &&
      [401, 403, 404].includes(lifecycle.error.status)
    )
      onDenied();
  }, [lifecycle.error, onDenied]);
  const unavailable = blocked || !writable;
  async function prepare() {
    if (request.current || unavailable || mutationHeld) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    setReview(undefined);
    setSent(false);
    setConflict(false);
    setAccepted("");
    try {
      const value = await getStackLifecycle(scope, controller.signal);
      if (!active.current || controller.signal.aborted) return;
      if (
        value.state !== "running" ||
        value.resource_generation !== generation ||
        !value.deployed_operation_id
      )
        throw new Error(
          "The saved generation or recorded stack changed. Refresh lifecycle and configuration before reviewing Stop.",
        );
      setReview(
        Object.freeze({
          idempotency_key: crypto.randomUUID(),
          expected_deployed_operation_id: value.deployed_operation_id,
          expected_generation: value.resource_generation,
        }),
      );
    } catch (cause) {
      failed(cause);
    } finally {
      if (request.current === controller) request.current = null;
      if (active.current) setBusy(false);
    }
  }
  function failed(cause: unknown) {
    if (!active.current || request.current?.signal.aborted) return;
    if (cause instanceof ApiError && [401, 403, 404].includes(cause.status)) {
      setReview(undefined);
      onDenied();
    } else {
      if (cause instanceof ApiError && [400, 409].includes(cause.status))
        setConflict(true);
      setError(
        cause instanceof ApiError && [400, 409].includes(cause.status)
          ? "Stop conflicts with current stack state. Refresh and review before a separate request."
          : "Stop is unconfirmed or unavailable. If submitted, retry the identical reviewed request to recover its operation.",
      );
    }
  }
  async function stop() {
    if (
      !review ||
      request.current ||
      unavailable ||
      conflict ||
      (mutationHeld && !sent)
    )
      return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setSent(true);
    setError("");
    try {
      const operation = await requestStackStop(
        scope,
        review,
        controller.signal,
      );
      if (!active.current || controller.signal.aborted) return;
      setAccepted(operation.id);
      setReview(undefined);
      setSent(false);
      onSelected(operation.id);
      try {
        await onRefresh(controller.signal);
        if (active.current && !controller.signal.aborted)
          await lifecycle.refetch();
      } catch (cause) {
        if (!active.current || controller.signal.aborted) return;
        if (cause instanceof ApiError && [401, 403, 404].includes(cause.status))
          onDenied();
        else
          setError(
            `Stop ${operation.id} was accepted, but lifecycle refresh failed. Refresh the operation to inspect completion.`,
          );
      }
    } catch (cause) {
      failed(cause);
    } finally {
      if (request.current === controller) request.current = null;
      if (active.current) setBusy(false);
    }
  }
  const current = lifecycle.data;
  return {
    generation,
    writable,
    mutationHeld,
    gitBuilds,
    lifecycle,
    review,
    setReview,
    sent,
    busy,
    conflict,
    error,
    accepted,
    request,
    unavailable,
    prepare,
    stop,
    current,
  };
}

type ComposeStopReviewModel = Pick<
  ReturnType<typeof useStackLifecycle>,
  | "review"
  | "unavailable"
  | "busy"
  | "conflict"
  | "mutationHeld"
  | "sent"
  | "stop"
  | "prepare"
  | "setReview"
>;
function ComposeStopReview(viewModel: ComposeStopReviewModel) {
  const {
    review,
    unavailable,
    busy,
    conflict,
    mutationHeld,
    sent,
    stop,
    prepare,
    setReview,
  } = viewModel;

  if (!review) return null;
  return (
    <section
      className="min-w-0 space-y-2 break-all"
      aria-label="Reviewed Compose stop"
    >
      <p>
        Stop deployed operation {review.expected_deployed_operation_id} at saved
        generation {review.expected_generation}. This interrupts every component
        and removes its containers. Named volumes and application data are
        retained.
      </p>
      <Button
        type="button"
        disabled={unavailable || busy || conflict || (mutationHeld && !sent)}
        onClick={() => void stop()}
      >
        {sent ? "Retry identical Compose Stop request" : "Confirm Stop Compose"}
      </Button>
      {conflict ? (
        <Button
          type="button"
          variant="outline"
          disabled={unavailable || busy || mutationHeld}
          onClick={() => void prepare()}
        >
          Review current Compose Stop
        </Button>
      ) : null}
      {sent ? (
        <p className="text-xs">
          Request key {review.idempotency_key}. The reviewed body remains frozen
          for an identical retry.
        </p>
      ) : (
        <Button
          type="button"
          variant="outline"
          disabled={busy}
          onClick={() => setReview(undefined)}
        >
          Keep Compose running
        </Button>
      )}
    </section>
  );
}

type ComposeStopActionModel = Pick<
  ReturnType<typeof useStackLifecycle>,
  | "writable"
  | "unavailable"
  | "busy"
  | "mutationHeld"
  | "lifecycle"
  | "current"
  | "review"
  | "prepare"
>;
function ComposeStopAction(viewModel: ComposeStopActionModel) {
  const {
    writable,
    unavailable,
    busy,
    mutationHeld,
    lifecycle,
    current,
    review,
    prepare,
  } = viewModel;

  if (!writable)
    return (
      <p>Project maintainer access is required to Stop or Start Compose.</p>
    );
  return (
    <Button
      type="button"
      variant="outline"
      disabled={
        unavailable ||
        busy ||
        mutationHeld ||
        lifecycle.isFetching ||
        lifecycle.isError ||
        current?.state !== "running" ||
        Boolean(review)
      }
      onClick={() => void prepare()}
    >
      Review Stop Compose
    </Button>
  );
}
