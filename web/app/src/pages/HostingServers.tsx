import { type FormEvent, useId, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { runnersQuery, type Runner } from "@/api";
import { createHostingServer, destinationGrantsQuery, destinationKeys, grantDestinationProject, hostingServerQuery, hostingServersQuery, revokeDestinationProject } from "@/api/destinations";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { PageNavigation } from "@/components/common/PageNavigation";
import { ServerReadiness } from "./ServerReadiness";
import { HostingProxy } from "./HostingProxy";
import { maximumPageOffset } from "@/router/search";

export function eligibleHostingRunner(runner: Runner): boolean {
  const capabilities = runner.capabilities ?? [];
  return runner.status === "active" && !capabilities.includes("shell") && (capabilities.includes("compose-deploy") || capabilities.includes("compose_deploy"));
}
function ServerGrants({ destinationID, blocked }: { destinationID: string; blocked: boolean }) {
  const client = useQueryClient();
  const id = useId();
  const [offset, setOffset] = useState(0);
  const [projectID, setProjectID] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const grants = useQuery(destinationGrantsQuery(destinationID, offset));
  async function change(project: string, revoke: boolean) {
    if (busy || blocked || grants.isError || grants.isPending) return;
    setBusy(true); setError("");
    try {
      if (revoke) await revokeDestinationProject(destinationID, project);
      else await grantDestinationProject(destinationID, project);
      if (!revoke) setProjectID("");
      await Promise.all([client.invalidateQueries({ queryKey: destinationKeys.grants(destinationID) }), client.invalidateQueries({ queryKey: destinationKeys.destinations })]);
    } catch { setError("Unable to change the project grant. Your project ID is preserved. Verify the project and retry explicitly."); }
    finally { setBusy(false); }
  }
  return <section className="min-w-0 space-y-3" aria-label="Destination project grants">
    <h4 className="font-medium">Project grants</h4><p className="text-xs text-muted-foreground">Grant access for new application placements. Revoking a grant removes future choices; existing saved bindings and workloads are retained.</p>
    {grants.isError ? <div className="space-y-2"><p role="alert">Unable to load project grants. Changes are paused.</p><Button type="button" variant="outline" onClick={() => void grants.refetch()}>Retry project grants</Button></div> : null}
    {grants.isPending ? <p role="status">Loading project grants...</p> : null}
    {(grants.data?.items ?? []).map((grant) => <div key={grant.project_id} className="min-w-0 space-y-2 rounded-lg border p-3"><p className="break-all text-sm">{grant.project_id}</p><p className="text-xs">{grant.revoked_at ? "Grant revoked" : "Grant active"}</p><Button type="button" variant="outline" className="h-auto max-w-full whitespace-normal" disabled={busy || blocked || grants.isError} onClick={() => void change(grant.project_id, !grant.revoked_at)}><span className="min-w-0 break-all">{grant.revoked_at ? "Restore grant for" : "Revoke grant for"} {grant.project_id}</span></Button></div>)}
    <PageNavigation label="project grants" page={grants.data} loading={grants.isFetching || busy} onFirst={() => setOffset(0)} onPrevious={() => setOffset(Math.max(0, offset - 20))} onNext={() => setOffset(Math.min(maximumPageOffset, offset + 20))} />
    <form className="space-y-2" aria-label="Grant destination to project" onSubmit={(event) => { event.preventDefault(); void change(projectID, false); }}>
      <label htmlFor={`${id}-project`}>Project ID to grant</label><Input id={`${id}-project`} value={projectID} onChange={(event) => setProjectID(event.target.value)} disabled={busy || blocked} required />
      <p className="text-xs text-muted-foreground">Use the ID shown under Project hosting on Projects. A runner administrator may enter an authorized project ID even without project membership.</p>
      <Button type="submit" disabled={busy || blocked || grants.isError || grants.isPending}>{busy ? "Saving grant..." : "Grant project access"}</Button>
    </form>{error ? <p role="alert">{error}</p> : null}
  </section>;
}
function ServerDetail({ serverID, blocked }: { serverID: string; blocked: boolean }) {
  const detail = useQuery(hostingServerQuery(serverID));
  return <section className="min-w-0 space-y-3" aria-label="Hosting server detail">
    {detail.isPending ? <p role="status">Loading server destination...</p> : null}
    {detail.isError ? <div className="space-y-2"><p role="alert">Unable to load server destination.</p><Button type="button" variant="outline" onClick={() => void detail.refetch()}>Retry server destination</Button></div> : null}
    {detail.data ? <><h3 className="break-words font-semibold [overflow-wrap:anywhere]">{detail.data.server.name}</h3><dl className="space-y-1 break-all text-sm"><dt>Server ID</dt><dd>{detail.data.server.id}</dd><dt>Enrolled runner</dt><dd>{detail.data.server.runner_id}</dd><dt>Default destination</dt><dd>{detail.data.destination.id}</dd><dt>Runtime kind</dt><dd>{detail.data.destination.runtime_kind}</dd></dl><ServerGrants key={detail.data.destination.id} destinationID={detail.data.destination.id} blocked={blocked || detail.isError} /><ServerReadiness serverID={serverID} blocked={blocked || detail.isError || detail.isFetching} /><HostingProxy serverID={serverID} blocked={blocked || detail.isError} /></> : null}
  </section>;
}
export function HostingServers({ canAdmin, blocked }: { canAdmin: boolean; blocked: boolean }) {
  return canAdmin ? <ServerAdministration blocked={blocked} /> : null;
}
function ServerAdministration({ blocked }: { blocked: boolean }) {
  const client = useQueryClient();
  const id = useId();
  const [offset, setOffset] = useState(0);
  const [runnerOffset, setRunnerOffset] = useState(0);
  const [name, setName] = useState("");
  const [runnerID, setRunnerID] = useState("");
  const [selected, setSelected] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const servers = useQuery(hostingServersQuery(offset));
  const runners = useQuery({ ...runnersQuery({ limit: 20, offset: runnerOffset }), retry: false });
  const choices = (runners.data?.items ?? []).filter(eligibleHostingRunner);
  const failed = servers.isError || runners.isError;
  async function create(event: FormEvent) {
    event.preventDefault();
    if (blocked || busy || failed || servers.isPending || runners.isPending || !choices.some((item) => item.id === runnerID)) return;
    setBusy(true); setError("");
    try {
      const result = await createHostingServer({ name, runner_id: runnerID });
      setName(""); setRunnerID(""); setSelected(result.server.id); setOffset(0);
      await client.invalidateQueries({ queryKey: destinationKeys.servers });
    } catch { setError("Server registration failed. Your draft is preserved. Verify the enrollment was consumed, the runner is eligible, and no server already uses it."); }
    finally { setBusy(false); }
  }
  function runnerPage(next: number) { setRunnerOffset(next); setRunnerID(""); }
  return <Card className="min-w-0" role="region" aria-label="Hosting servers"><CardHeader><CardTitle>Hosting servers</CardTitle><p className="text-sm text-muted-foreground">Register a consumed Compose runner, then grant projects access to its default destination. Saved records do not test Docker, configure SSH or HTTPS, or deploy an application.</p></CardHeader><CardContent className="grid min-w-0 items-start gap-5 lg:grid-cols-2">
    <section className="min-w-0 space-y-3">
      {failed ? <div className="space-y-2"><p role="alert">Unable to load hosting server information. Drafts are preserved; changes are paused.</p><Button type="button" variant="outline" onClick={() => { if (servers.isError) void servers.refetch(); if (runners.isError) void runners.refetch(); }}>Retry hosting servers</Button></div> : null}
      {servers.isPending ? <p role="status">Loading hosting servers...</p> : null}
      {(servers.data?.items ?? []).map((server) => <article aria-label={`Hosting server ${server.name}`} key={server.id} className="min-w-0 space-y-2 rounded-lg border p-3"><h3 className="break-words font-medium [overflow-wrap:anywhere]">{server.name}</h3><p className="break-all text-xs">Runner {server.runner_id}</p><Button type="button" variant="outline" onClick={() => setSelected(server.id)}>View server destination</Button></article>)}
      <PageNavigation label="hosting servers" page={servers.data} loading={servers.isFetching || busy} onFirst={() => setOffset(0)} onPrevious={() => setOffset(Math.max(0, offset - 20))} onNext={() => setOffset(Math.min(maximumPageOffset, offset + 20))} />
      <form aria-label="Register hosting server" className="space-y-3 border-t pt-3" onSubmit={(event) => void create(event)}>
        <label htmlFor={`${id}-name`}>Hosting server name</label><Input id={`${id}-name`} required value={name} disabled={busy || blocked} onChange={(event) => setName(event.target.value)} />
        <label htmlFor={`${id}-runner`}>Enrolled Compose runner</label><select id={`${id}-runner`} className="h-10 w-full min-w-0 rounded-xl border bg-card px-2 text-sm" value={runnerID} disabled={busy || blocked} onChange={(event) => setRunnerID(event.target.value)}><option value="">Select an active Compose runner</option>{choices.map((runner) => <option key={runner.id} value={runner.id}>{runner.name} · {runner.id}</option>)}</select>
        <p className="text-xs text-muted-foreground">Only active Compose-capable runners without shell capability are offered from this page. The API also verifies consumed enrollment and dedicated credentials.</p>
        <Button type="button" variant="outline" disabled={runners.isFetching} onClick={() => void runners.refetch()}>Refresh eligible runners</Button>
        <PageNavigation label="enrolled runners" page={runners.data} loading={runners.isFetching || busy} onFirst={() => runnerPage(0)} onPrevious={() => runnerPage(Math.max(0, runnerOffset - 20))} onNext={() => runnerPage(Math.min(maximumPageOffset, runnerOffset + 20))} />
        <Button type="submit" disabled={blocked || busy || failed || servers.isPending || runners.isPending || !choices.some((item) => item.id === runnerID)}>{busy ? "Registering server..." : "Register hosting server"}</Button>{error ? <p role="alert">{error}</p> : null}
      </form>
    </section>
    {selected ? <ServerDetail key={selected} serverID={selected} blocked={blocked || failed} /> : <p className="text-sm text-muted-foreground">Select or register a server to view its destination and project grants.</p>}
  </CardContent></Card>;
}
