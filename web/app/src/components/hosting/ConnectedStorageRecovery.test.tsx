import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { ConnectedStorageRecovery } from "./ConnectedStorageRecovery";
import type { ConnectedProgress, StorageRecoveryAction } from "@/api/hosting-database-connections";
const scope = { actor: "actor", project: "project", resource: "app" };
const hash = `sha256:${"a".repeat(64)}`, stageHash = `sha256:${"b".repeat(64)}`, candidateHash = `sha256:${"c".repeat(64)}`;
const storage = { strategy: "replacement_with_interruption" as const, storage_id: "storage", generation: 1 as const, hash };
const connection = { id: "connection", project_id: "project", application_resource_id: "app", database_resource_id: "db", binding_id: "binding", binding_hash: hash, state: "held", reason_code: "failure", capture_id: "capture", upgrade_operation_id: "upgrade", application_operation_id: "operation", hold_operation_id: "operation", membership_revision: 7, created_at: "", updated_at: "" };
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
let client: QueryClient;
function setup(actions: StorageRecoveryAction[] = ["resume_predecessor"], handler?: (url: string, init: RequestInit) => Response | Promise<Response> | undefined, recoveryState: NonNullable<ConnectedProgress["storageState"]>["recovery_state"] = "none") {
  const recoveryAction: "" | StorageRecoveryAction = recoveryState === "none" ? "" : "resume_predecessor";
  const progress: ConnectedProgress = { id: "operation", state: "failed", mutation_hold: true, restore_ownership_held: false, yielded: false, storage, storagePhase: "replacement_failed", storageState: { phase: "replacement_failed", state_hash: hash, stage_hash: stageHash, candidate_hash: candidateHash, recovery_actions: actions, recovery_action: recoveryAction, recovery_state: recoveryState } };
  const raw = { id: "operation", project_id: "project", resource_id: "app", state: "failed", mutation_hold: true, restore_ownership_held: false, application_database_storage: { version: "application-database.storage.v1", strategy: storage.strategy, storage_ref: { storage_id: "storage", generation: 1, hash }, hash }, application_database_storage_state: { version: "application-database.storage.v1", phase: "replacement_failed", receipt_id: "receipt", state_hash: hash, previous_state_hash: hash, candidate_hash: candidateHash, stage_hash: stageHash, recovery_actions: actions, recovery_action: recoveryAction, recovery_state: recoveryState } };
  const fetch = vi.fn(async (url: string, init: RequestInit) => {
    const custom = handler?.(url, init); if (custom) return custom;
    if (url.includes("/hosting-operations/")) return json(raw);
    if (url.endsWith("/database-connection")) return json(connection);
    return json({ ...raw, snapshot: { resource_id: "app" } }, 202);
  });
  vi.stubGlobal("fetch", fetch);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const authorize = vi.fn().mockResolvedValue(true), onDenied = vi.fn(), onSubmitting = vi.fn(), onUpdated = vi.fn().mockResolvedValue(undefined);
  render(<QueryClientProvider client={client}><ConnectedStorageRecovery scope={scope} progress={progress} paused={false} authorize={authorize} onDenied={onDenied} onSubmitting={onSubmitting} onUpdated={onUpdated} /></QueryClientProvider>);
  return { fetch, raw, authorize, onDenied, onSubmitting, onUpdated };
}
afterEach(() => { cleanup(); client?.clear(); vi.unstubAllGlobals(); });
async function review(label = "Resume previous application") {
  fireEvent.click(screen.getByRole("button", { name: `Review: ${label}` }));
  await screen.findByText(`Reviewed action: ${label}. Storage generation 1 for operation operation.`);
  fireEvent.click(screen.getByLabelText("I acknowledge application interruption and retained data changes for this recovery."));
  return screen.getByRole("button", { name: label });
}
test("phase alone offers no recovery action", () => {
  setup([]);
  expect(screen.queryByRole("button")).toBeNull();
  expect(screen.getByText(/No recovery action is offered/)).toBeTruthy();
});
test.each(["resume_predecessor", "complete_replacement"] as const)("explicit offered %s freezes correlation and interruption acknowledgement", async (action) => {
  const { fetch, onSubmitting, onUpdated } = setup([action]);
  const label = action === "resume_predecessor" ? "Resume previous application" : "Complete interrupted replacement";
  const submit = await review(label);
  expect(onSubmitting).not.toHaveBeenCalled();
  fireEvent.click(submit);
  await screen.findByText(/Recovery request accepted/);
  expect(onUpdated).toHaveBeenCalledOnce();
  const mutation = fetch.mock.calls.find(([, init]) => init.method === "POST")!;
  expect(onSubmitting).toHaveBeenCalledExactlyOnceWith("operation");
  const mutationIndex = fetch.mock.calls.indexOf(mutation);
  expect(onSubmitting.mock.invocationCallOrder[0]).toBeLessThan(fetch.mock.invocationCallOrder[mutationIndex]!);
  expect(mutation[0]).toBe("/api/v1/resources/app/database-connection/recovery");
  expect(JSON.parse(String(mutation[1].body))).toEqual({ action, operation_id: "operation", stage_hash: stageHash, candidate_hash: candidateHash, expected_membership_revision: 7, idempotency_key: expect.any(String), storage_state_hash: hash, application_database_storage: { expected_storage_generation: 1, expected_storage_hash: hash, acknowledge_interruption: true } });
});
test("fresh recovery membership conflict prevents mutation and requires separate review", async () => {
  let changed = false;
  const { fetch, onSubmitting } = setup(undefined, (url) => changed && url.endsWith("/database-connection") ? json({ ...connection, membership_revision: 8 }) : undefined);
  const submit = await review(); changed = true; fireEvent.click(submit);
  await screen.findByText(/Recovery evidence changed. Nothing was resubmitted/);
  expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(0);
  expect(onSubmitting).not.toHaveBeenCalled();
  expect((screen.getByRole("button", { name: "Resume previous application" }) as HTMLButtonElement).disabled).toBe(true);
});
test.each(["before evidence", "after evidence"])("authority loss %s cannot select recovery or send a request", async (stage) => {
  const { fetch, authorize, onSubmitting } = setup();
  const submit = await review();
  if (stage === "after evidence") authorize.mockResolvedValueOnce(true);
  authorize.mockResolvedValue(false); fireEvent.click(submit);
  await waitFor(() => expect((submit as HTMLButtonElement).disabled).toBe(false));
  expect(onSubmitting).not.toHaveBeenCalled();
  expect(fetch.mock.calls.some(([, init]) => init.method === "POST")).toBe(false);
});
test("unconfirmed recovery retries the exact request even when fresh evidence is unavailable", async () => {
  let posted = false;
  const { fetch } = setup(undefined, (url, init) => {
    if (init.method === "POST") { posted = true; return Promise.reject(new TypeError("lost")); }
    if (posted && url.includes("/hosting-operations/")) return json({}, 503);
  });
  fireEvent.click(await review()); await screen.findByText(/No confirmed response/);
  fireEvent.click(screen.getByRole("button", { name: "Retry identical storage recovery request" }));
  await waitFor(() => expect(fetch.mock.calls.filter(([, init]) => init.method === "POST")).toHaveLength(2));
  const bodies = fetch.mock.calls.filter(([, init]) => init.method === "POST").map(([, init]) => init.body);
  expect(bodies[0]).toBe(bodies[1]);
});
test("denied recovery retires action without displaying a successful response", async () => {
  const { onDenied } = setup(undefined, (_url, init) => init.method === "POST" ? json({}, 403) : undefined);
  fireEvent.click(await review()); await waitFor(() => expect(onDenied).toHaveBeenCalledOnce());
  expect(screen.queryByText(/Recovery request accepted/)).toBeNull();
});

test("fresh server choices can remove an offered action without authorizing mutation", async () => {
  const { raw, fetch } = setup();
  raw.application_database_storage_state.recovery_actions = [];
  fireEvent.click(screen.getByRole("button", { name: "Review: Resume previous application" }));
  await screen.findByText(/Recovery evidence changed. Nothing was resubmitted/);
  expect(fetch.mock.calls.some(([, init]) => init.method === "POST")).toBe(false);
});
test("late accepted response cannot publish after project authority is lost", async () => {
  let resolve!: (value: Response) => void;
  const { authorize } = setup(undefined, (_url, init) => init.method === "POST" ? new Promise<Response>((done) => { resolve = done; }) : undefined);
  fireEvent.click(await review()); await waitFor(() => expect(resolve).toBeTruthy());
  authorize.mockResolvedValue(false);
  resolve(json({ id: "operation", project_id: "project", resource_id: "app", snapshot: { resource_id: "app" } }, 202));
  await waitFor(() => expect(authorize).toHaveBeenCalledTimes(5));
  expect(screen.queryByText(/Recovery request accepted/)).toBeNull();
});

test("accepted recovery remains known when its following progress refresh fails", async () => {
  const { onUpdated } = setup(); onUpdated.mockRejectedValue(new TypeError("refresh failed"));
  fireEvent.click(await review());
  await screen.findByText(/Recovery was accepted, but current progress could not be refreshed/);
  expect(screen.getByText(/Recovery request accepted/)).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Retry identical storage recovery request" })).toBeNull();
});

test.each(["queued", "running", "succeeded", "uncertain"] as const)("fresh page renders explicit recovery %s independently of failed original operation", (state) => {
  setup([], undefined, state);
  expect(screen.getByText(new RegExp(`Recovery status: ${state}`))).toBeTruthy();
  if (state === "queued" || state === "running") expect(screen.getByText(/Recovery remains pending, including when the original deployment is failed/)).toBeTruthy();
  else if (state === "succeeded") expect(screen.getByText(/server recorded verified recovery settlement/)).toBeTruthy();
  else expect(screen.getByText(/Recovery outcome is uncertain. Keep ownership held/)).toBeTruthy();
  expect(screen.queryByRole("button")).toBeNull();
});
