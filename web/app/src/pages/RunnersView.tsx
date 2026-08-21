import { type FormEvent, type ReactNode, useEffect, useRef, useState } from "react";
import { Link } from "@tanstack/react-router";
import { Copy, Download, KeyRound, ShieldOff } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { EmptyState } from "@/components/common/EmptyState";
import { StatusBadge } from "@/components/common/StatusBadge";
import type { CreatedRunnerEnrollment, Runner, RunnerDetail } from "@/api";
import { formatDate, matchesQuery } from "@/lib/format";

function splitValues(value: string): string[] { return value.split(",").map((item) => item.trim()).filter(Boolean); }
function runnerState(runner: Runner): string {
  if (runner.status === "revoked") return "Credential revoked; the next authenticated runner operation is denied.";
  const age = Date.now() - new Date(runner.last_heartbeat_at).getTime();
  if (Number.isFinite(age) && age > 2 * 60_000) return "Heartbeat is stale; wait for recovery or inspect runner connectivity.";
  return "Active and recently observed.";
}

export function RunnersView({ runners, q, canAdmin, onEnroll, onRevokeRunner }: { runners: Runner[]; q?: string; canAdmin: boolean; onEnroll: (input: { runner_id: string; runner_name: string; tags: string[]; capabilities: string[]; ttl_seconds: number }) => Promise<CreatedRunnerEnrollment>; onRevokeRunner: (id: string) => Promise<void> }): ReactNode {
  const [enrollment, setEnrollment] = useState<CreatedRunnerEnrollment>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const secretRef = useRef<string | undefined>(undefined);
  useEffect(() => () => { secretRef.current = undefined; }, []);
  const visible = runners.filter((runner) => matchesQuery(q, runner.id, runner.name, runner.status, ...runner.tags, ...runner.capabilities));
  async function enroll(input: { runner_id: string; runner_name: string; tags: string[]; capabilities: string[]; ttl_seconds: number }) {
    setBusy(true); setError("");
    try { const created = await onEnroll(input); secretRef.current = created.token; setEnrollment(created); }
    catch { setError("Enrollment could not be created. Verify the identity and try again."); }
    finally { setBusy(false); }
  }
  function clearEnrollment(open: boolean) { if (!open) { secretRef.current = undefined; setEnrollment(undefined); } }
  async function copySecret() { if (secretRef.current) await navigator.clipboard.writeText(secretRef.current); }
  function downloadSecret() {
    if (!secretRef.current) return;
    const blob = new Blob([secretRef.current + "\n"], { type: "text/plain" });
    const url = URL.createObjectURL(blob); const anchor = document.createElement("a");
    anchor.href = url; anchor.download = "nerocd-runner-enrollment.token"; anchor.click();
    URL.revokeObjectURL(url);
  }
  return <section className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_360px]">
    <Card><CardHeader><CardTitle>Runner inventory</CardTitle></CardHeader><CardContent className="grid gap-3">{!visible.length ? <EmptyState title="No runners match this view" icon={KeyRound} /> : visible.map((runner) => <Link key={runner.id} to="/runners/$runnerId" params={{ runnerId: runner.id }} search={(previous) => previous} className="rounded border p-3 hover:bg-muted"><div className="flex items-center justify-between gap-2"><span className="font-medium">{runner.name}</span><StatusBadge status={runner.status} /></div><p className="mt-1 font-mono text-xs text-muted-foreground">{runner.id}</p><p className="mt-2 text-sm text-muted-foreground">{runnerState(runner)}</p><p className="mt-2 text-xs text-muted-foreground">Heartbeat {formatDate(runner.last_heartbeat_at)} · {runner.tags.join(", ") || "no tags"}</p></Link>)}</CardContent></Card>
    {canAdmin ? <EnrollmentForm busy={busy} error={error} onSubmit={enroll} /> : <Card><CardContent className="p-6" role="status">Runner operations are restricted to global administrators.</CardContent></Card>}
    <EnrollmentSecret enrollment={enrollment} token={secretRef.current} onOpenChange={clearEnrollment} onCopy={copySecret} onDownload={downloadSecret} />
  </section>;
}

function EnrollmentForm({ busy, error, onSubmit }: { busy: boolean; error: string; onSubmit: (input: { runner_id: string; runner_name: string; tags: string[]; capabilities: string[]; ttl_seconds: number }) => void }) {
  const [id, setID] = useState(""); const [name, setName] = useState(""); const [tags, setTags] = useState("compose-runtime"); const [capabilities, setCapabilities] = useState("compose-deploy"); const [ttl, setTTL] = useState("600");
  return <Card><CardHeader><CardTitle>Create one-time enrollment</CardTitle></CardHeader><CardContent><form className="grid gap-3" aria-label="Create runner enrollment" onSubmit={(event: FormEvent) => { event.preventDefault(); onSubmit({ runner_id: id, runner_name: name, tags: splitValues(tags), capabilities: splitValues(capabilities), ttl_seconds: Number(ttl) }); }}><label className="grid gap-1 text-sm">Runner ID<Input aria-label="Runner ID" value={id} onChange={(event) => setID(event.target.value)} placeholder="runner_edge_01" required /></label><label className="grid gap-1 text-sm">Runner name<Input aria-label="Runner name" value={name} onChange={(event) => setName(event.target.value)} required /></label><label className="grid gap-1 text-sm">Tags<Input aria-label="Runner tags" value={tags} onChange={(event) => setTags(event.target.value)} /></label><label className="grid gap-1 text-sm">Capabilities<Input aria-label="Runner capabilities" value={capabilities} onChange={(event) => setCapabilities(event.target.value)} required /></label><label className="grid gap-1 text-sm">Enrollment lifetime<Select aria-label="Enrollment lifetime" value={ttl} onChange={(event) => setTTL(event.target.value)}><option value="300">5 minutes</option><option value="600">10 minutes</option><option value="1800">30 minutes</option><option value="3600">1 hour</option></Select></label>{error ? <p role="alert" className="text-sm text-destructive">{error}</p> : null}<Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create enrollment"}</Button><p className="text-xs text-muted-foreground">The secret is shown once, never placed in links, search, storage, notifications, or logs.</p></form></CardContent></Card>;
}

function EnrollmentSecret({ enrollment, token, onOpenChange, onCopy, onDownload }: { enrollment?: CreatedRunnerEnrollment; token?: string; onOpenChange: (open: boolean) => void; onCopy: () => void; onDownload: () => void }) {
  return <Dialog open={Boolean(enrollment && token)} onOpenChange={onOpenChange}><DialogContent><DialogHeader><DialogTitle>One-time runner enrollment secret</DialogTitle><DialogDescription>Save it now: it will disappear when this dialog closes and cannot be retrieved again.</DialogDescription></DialogHeader><code className="break-all rounded border p-3 text-xs" aria-label="Enrollment secret">{token}</code><p className="text-xs text-muted-foreground">Runner: {enrollment?.enrollment.runner_id}. Use the downloaded file with the runner enrollment command.</p><DialogFooter><Button variant="outline" onClick={onCopy}><Copy className="mr-1 h-4 w-4" />Copy</Button><Button onClick={onDownload}><Download className="mr-1 h-4 w-4" />Download once</Button></DialogFooter></DialogContent></Dialog>;
}

export function RunnerDetailView({ runner, canAdmin, onRevoke }: { runner?: RunnerDetail; canAdmin: boolean; onRevoke: () => Promise<void> }): ReactNode {
  const [confirming, setConfirming] = useState(false); const [busy, setBusy] = useState(false);
  if (!runner) return <Card><CardContent className="p-6" role="alert">Runner is unavailable.</CardContent></Card>;
  return <section className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_320px]"><Card><CardHeader><CardTitle>{runner.name}</CardTitle></CardHeader><CardContent className="grid gap-3"><StatusBadge status={runner.status} /><p className="font-mono text-xs">{runner.id}</p><p>{runnerState(runner)}</p><dl className="grid gap-2 text-sm"><div><dt className="text-muted-foreground">Registered</dt><dd>{formatDate(runner.registered_at)}</dd></div><div><dt className="text-muted-foreground">Last heartbeat</dt><dd>{formatDate(runner.last_heartbeat_at)}</dd></div><div><dt className="text-muted-foreground">Tags</dt><dd>{runner.tags.join(", ") || "None"}</dd></div><div><dt className="text-muted-foreground">Capabilities</dt><dd>{runner.capabilities.join(", ")}</dd></div><div><dt className="text-muted-foreground">Telemetry</dt><dd>{runner.telemetry ? <>Observed <time dateTime={runner.telemetry.observed_at}>{formatDate(runner.telemetry.observed_at)}</time> · journal {runner.telemetry.journal_depth} · retries {runner.telemetry.retry_count} · renewal failures {runner.telemetry.renew_failures}</> : "No authenticated telemetry has been received yet."}</dd></div></dl></CardContent></Card><Card><CardHeader><CardTitle>Credential</CardTitle></CardHeader><CardContent>{canAdmin && runner.status !== "revoked" ? <Button variant="destructive" onClick={() => setConfirming(true)}><ShieldOff className="mr-1 h-4 w-4" />Revoke credential</Button> : <p className="text-sm text-muted-foreground">{runner.status === "revoked" ? "Credential is revoked." : "Administrative role required."}</p>}</CardContent></Card><Dialog open={confirming} onOpenChange={setConfirming}><DialogContent><DialogHeader><DialogTitle>Revoke runner credential?</DialogTitle><DialogDescription>The runner will fail its next authenticated operation and must be re-enrolled.</DialogDescription></DialogHeader><DialogFooter><Button variant="outline" onClick={() => setConfirming(false)}>Keep credential</Button><Button variant="destructive" disabled={busy} onClick={async () => { setBusy(true); try { await onRevoke(); setConfirming(false); } finally { setBusy(false); } }}>Revoke credential</Button></DialogFooter></DialogContent></Dialog></section>;
}
