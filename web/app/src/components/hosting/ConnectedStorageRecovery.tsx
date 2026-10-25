import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ApiError } from "@/api";
import { boundedDatabaseRequest, databaseDenied, databaseRequestID } from "@/api/hosting-databases";
import { connectedProgressQuery, connectionError, getDatabaseConnection, recoverConnectionStorage, storageRecoveryInput, type ConnectedProgress, type ConnectionStorageRecoveryInput, type DatabaseConnectionScope, type StorageRecoveryAction } from "@/api/hosting-database-connections";
import { Button } from "@/components/ui/button";

const labels: Record<StorageRecoveryAction, string> = { resume_predecessor: "Resume previous application", complete_replacement: "Complete interrupted replacement" };
const button = "h-auto min-h-9 max-w-full min-w-0 whitespace-normal break-words";
export function ConnectedStorageRecovery({ scope, progress, paused, authorize, onDenied, onUpdated }: { scope: DatabaseConnectionScope; progress: ConnectedProgress; paused: boolean; authorize: () => Promise<boolean>; onDenied: () => void; onUpdated: () => Promise<void> }) {
  const client = useQueryClient();
  const active = useRef(true), sending = useRef(false), pending = useRef<AbortController | null>(null);
  const [intent, setIntent] = useState<ConnectionStorageRecoveryInput>();
  const [attempted, setAttempted] = useState(false), [confirmed, setConfirmed] = useState(false), [busy, setBusy] = useState(false), [conflict, setConflict] = useState(false), [error, setError] = useState(""), [notice, setNotice] = useState("");
  useEffect(() => { active.current = true; return () => { active.current = false; pending.current?.abort(); }; }, []);
  async function fresh(action: StorageRecoveryAction, key: string, signal: AbortSignal) {
    const recorded = await boundedDatabaseRequest(() => client.fetchQuery({ ...connectedProgressQuery(scope, progress.id), staleTime: 0 }), signal);
    const connection = await getDatabaseConnection(scope, signal);
    if (!connection) throw new ApiError(409, "Connection unavailable", null);
    return storageRecoveryInput(recorded, connection, action, key);
  }
  async function run(action: StorageRecoveryAction, submit: boolean) {
    if (paused || busy || sending.current || conflict || submit && (!intent || !confirmed)) return;
    const controller = new AbortController(); pending.current = controller; sending.current = true; setBusy(true); setError(""); setNotice("");
    let sent = false;
    try {
      if (!await authorize() || controller.signal.aborted) return;
      if (!submit) {
        const reviewed = await fresh(action, databaseRequestID(), controller.signal);
        if (active.current && !controller.signal.aborted && await authorize()) { setIntent(reviewed); setConfirmed(false); setAttempted(false); }
        return;
      }
      const body = intent!;
      if (!attempted) {
        const current = await fresh(body.action, body.idempotency_key, controller.signal);
        if (JSON.stringify(current) !== JSON.stringify(body)) throw new ApiError(409, "Recovery evidence changed", null);
      }
      if (!active.current || controller.signal.aborted || !await authorize()) return;
      setAttempted(true);
      sent = true;
      await recoverConnectionStorage(scope, body, controller.signal);
      if (!active.current || controller.signal.aborted || !await authorize()) return;
      setNotice(`Recovery request accepted for ${body.operation_id}. Refresh the database connection to follow recorded progress; acceptance does not confirm runtime health or release a hold.`);
      setIntent(undefined); setConfirmed(false); setAttempted(false);
      try { await onUpdated(); }
      catch (cause) {
        if (!active.current || controller.signal.aborted) return;
        if (databaseDenied(cause)) onDenied();
        else setError("Recovery was accepted, but current progress could not be refreshed. Refresh the database connection before relying on recorded state.");
      }
    } catch (cause) {
      if (!active.current || controller.signal.aborted) return;
      if (databaseDenied(cause)) { onDenied(); return; }
      if (!await authorize()) return;
      setError(!sent && !attempted && !(cause instanceof ApiError) ? "Unable to verify fresh recovery evidence. No recovery request was sent. Refresh before reviewing again." : connectionError(cause));
      if (cause instanceof ApiError && cause.status === 409) { setConflict(true); setConfirmed(false); }
      if (cause instanceof ApiError && cause.status === 400) { setIntent(undefined); setConfirmed(false); setAttempted(false); }
    } finally { sending.current = false; if (pending.current === controller) pending.current = null; if (active.current) setBusy(false); }
  }
  if (!progress.storage || !progress.storageState) return null;
  return <section aria-label="Connected storage recovery" className="min-w-0 space-y-2 break-words rounded-lg border p-3 text-sm">
    <p>Recovery for operation {progress.id}. Recorded storage phase: {progress.storageState.phase}.</p>
    <p role="status">Recovery status: {progress.storageState.recovery_state}. {progress.storageState.recovery_action ? `Recorded recovery action: ${labels[progress.storageState.recovery_action]}.` : "No recovery request is recorded."}</p>
    {["queued", "running"].includes(progress.storageState.recovery_state) ? <p>Recovery remains pending, including when the original deployment is failed. Status checks are bounded; refresh manually if they stop or become unavailable. Acceptance is not completion.</p> : progress.storageState.recovery_state === "succeeded" ? <p>The server recorded verified recovery settlement. This does not confirm continuous application, HTTPS or database health.</p> : progress.storageState.recovery_state === "uncertain" ? <p role="alert">Recovery outcome is uncertain. Keep ownership held. No completion or data rollback is confirmed.</p> : null}
    <p>The server offers only actions supported by current evidence. Recovery retains existing file data and does not undo application or database writes.</p>
    {!intent ? progress.storageState.recovery_actions.length ? <div className="flex flex-wrap gap-2">{progress.storageState.recovery_actions.map((action) => <Button key={action} type="button" variant="outline" className={button} disabled={paused || busy || conflict} onClick={() => void run(action, false)}>Review: {labels[action]}</Button>)}</div> : <p>No recovery action is offered. Keep ownership held and request server review of uncertain or missing evidence. Refreshing does not authorize an unlock.</p> : <>
      <p>Reviewed action: {labels[intent.action]}. Storage generation {intent.application_database_storage.expected_storage_generation} for operation {intent.operation_id}.</p>
      <p>{intent.action === "resume_predecessor" ? "Restart the previous application version only after the server verifies the candidate is stopped. Database writes and uploaded-file changes remain; process recovery does not guarantee data compatibility." : "Continue deployment of the same candidate through a fresh route switch and finalization. This does not restore older file or database data."}</p>
      <label className="flex items-start gap-2"><input type="checkbox" checked={confirmed} disabled={paused || busy || conflict} onChange={(event) => setConfirmed(event.target.checked)} /><span>I acknowledge application interruption and retained data changes for this recovery.</span></label>
      <Button type="button" className={button} disabled={paused || busy || conflict || !confirmed} onClick={() => void run(intent.action, true)}>{attempted ? "Retry identical storage recovery request" : labels[intent.action]}</Button>
      {attempted ? <p className="break-all">Retained recovery request: {intent.idempotency_key}. Only an identical retry can recover an unconfirmed response.</p> : null}
    </>}
    {conflict ? <><p role="status">Recovery evidence changed. Nothing was resubmitted. Discard this review, then request fresh server choices.</p><Button type="button" variant="outline" className={button} disabled={paused || busy} onClick={() => { setIntent(undefined); setConfirmed(false); setAttempted(false); setConflict(false); setError(""); }}>Discard conflicted recovery review</Button></> : null}
    {error ? <p role="alert">{error}</p> : null}{notice ? <p role="status">{notice}</p> : null}
  </section>;
}
