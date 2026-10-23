import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError } from "@/api";
import type { Resource } from "@/api/hosting";
import { listServiceAccounts } from "@/api/service-accounts";
import { listManagedSecrets, listManagedSecretVersions } from "@/api/managed-secrets";
import { eligibleSourceGrant, listHostingSourceGrants } from "@/api/hosting-source-grants";
import { withGitHubPushDeadline, executeGitHubPushDecision, getGitHubPushPolicy, githubPushDenied, githubPushError, listGitHubPushDeliveries, pushReason, verificationCurrent, type GitHubPushConfiguration, type GitHubPushDelivery, type GitHubPushPolicy, type PushDecision } from "@/api/hosting-github-push";
import { Button } from "@/components/ui/button";
import { PageNavigation } from "@/components/common/PageNavigation";
const button = "h-auto max-w-full whitespace-normal break-all";
const select = "min-h-10 w-full min-w-0 rounded border bg-background p-2 text-sm";
const limit = 20;
const nextOffset = (offset: number) => Math.min(100000, offset + limit);
function metadataError(cause: unknown): never { throw cause instanceof ApiError ? new ApiError(cause.status, "Reference metadata is unavailable", cause.requestID) : new Error("Reference metadata is unavailable"); }
const policyKey = (actor: string, resource: Resource) => ["githubPushPolicy", actor, resource.project_id, resource.id] as const;
export function ResourceGithubPushBuilds({ actor, resource, blocked }: { actor: string; resource: Resource; blocked: boolean }) {
  if (resource.source.kind !== "git") return null;
  return <PushPolicy key={JSON.stringify([actor, resource.project_id, resource.id, resource.generation, resource.source.git.generation])} actor={actor} resource={resource} blocked={blocked} />;
}
function PushPolicy({ actor, resource, blocked }: { actor: string; resource: Resource; blocked: boolean }) {
  const client = useQueryClient();
  const [cursor, setCursor] = useState(""), [denied, setDenied] = useState(false);
  const policy = useQuery({ queryKey: policyKey(actor, resource), enabled: !blocked && !denied, retry: false, gcTime: 0, refetchOnWindowFocus: false, queryFn: async ({ signal }) => { const p = await withGitHubPushDeadline(signal, 5000, (signal) => getGitHubPushPolicy(resource.id, { signal })); if (p.project_id !== resource.project_id) throw new Error("Push policy project mismatch"); return p; } });
  const history = useQuery({ queryKey: ["githubPushDeliveries", actor, resource.project_id, resource.id, cursor], enabled: !blocked && !denied && Boolean(policy.data), retry: false, gcTime: 0, refetchOnWindowFocus: false, queryFn: async ({ signal }) => { const page = await withGitHubPushDeadline(signal, 5000, (signal) => listGitHubPushDeliveries(resource.id, cursor, { signal })); if (policy.data?.id && page.items.some((d) => d.policy_id !== policy.data.id)) throw new Error("Push delivery policy mismatch"); return page; } });
  useEffect(() => { if (githubPushDenied(policy.error) || githubPushDenied(history.error)) setDenied(true); }, [policy.error, history.error]);
  useEffect(() => {
    if (!blocked && !denied) return;
    for (const name of ["githubPushPolicy", "githubPushDeliveries", "githubPushAccounts", "githubPushGrants", "githubPushSecrets", "githubPushVersions"]) void client.cancelQueries({ queryKey: [name, actor, resource.project_id, resource.id] });
  }, [blocked, denied, actor, resource.project_id, resource.id, client]);
  const p = policy.data;
  const paused = blocked || denied || policy.isPending || policy.isError || policy.isFetching || history.isError;
  useEffect(() => {
    if (!paused) return;
    for (const name of ["githubPushAccounts", "githubPushGrants", "githubPushSecrets", "githubPushVersions"]) void client.cancelQueries({ queryKey: [name, actor, resource.project_id, resource.id] });
  }, [paused, actor, resource.project_id, resource.id, client]);
  const refresh = async () => {
    if (blocked || denied) return;
    const result = await policy.refetch();
    if (result.isError || !result.data || !result.data.can_manage) { await history.refetch(); return; }
    await Promise.all([history.refetch(), ...["githubPushAccounts", "githubPushGrants", "githubPushSecrets", "githubPushVersions"].map((name) => client.invalidateQueries({ queryKey: [name, actor, resource.project_id, resource.id] }))]);
  };
  const changed = (result: GitHubPushPolicy | GitHubPushDelivery) => {
    if ("can_manage" in result) { if (result.project_id !== resource.project_id) throw new Error("Push policy project mismatch"); client.setQueryData(policyKey(actor, resource), result); }
    void history.refetch();
  };
  return <section className="min-w-0 space-y-3 border-t pt-3" aria-label="Build on push"><h5 className="font-medium">Build on push</h5>
    <p className="text-sm">Signed GitHub.com pushes can build the configured branch. Deployments remain manual. A queued or successful build does not deploy the application.</p>
    <Button className={button} type="button" variant="outline" disabled={blocked || denied || policy.isFetching || history.isFetching} onClick={() => void refresh()}>Refresh build-on-push metadata</Button>
    {policy.isPending && !blocked ? <p role="status">Loading build-on-push policy...</p> : null}
    {denied ? <p role="alert">Build-on-push access is unavailable. Reload to verify current access.</p> : policy.isError || history.isError ? <p role="alert">Unable to refresh build-on-push metadata. Retained information may be stale; actions are paused.</p> : null}
    {p && !denied ? <><dl className="break-all text-sm"><dt>Policy state</dt><dd>{p.status} · {p.enabled ? "enabled" : "disabled"}</dd><dt>Policy generation</dt><dd>{p.generation}</dd><dt>Repository and branch</dt><dd>{p.repository || "No supported repository"} · {p.branch || "No branch"}</dd><dt>Saved source generations</dt><dd>Resource {p.resource_generation} / source {p.source_generation}</dd><dt>Policy outcome</dt><dd>{pushReason[p.reason] ?? "Review current policy metadata."}</dd>{p.desired_commit ? <><dt>Desired commit</dt><dd>{p.desired_commit}</dd></> : null}{p.last_build_id ? <><dt>Last build</dt><dd>{p.last_build_id}. Select it in build history below to inspect the artifact and deploy manually.</dd></> : null}</dl>
      {p.can_manage ? <PushManager actor={actor} resource={resource} policy={p} paused={paused} onDenied={() => setDenied(true)} changed={changed} refresh={refresh} /> : <p className="text-xs">A current project owner or administrator configures build on push. Maintainer build access does not grant policy management.</p>}
      {p.id && p.callback_url ? <section className="min-w-0 space-y-2 rounded border p-3" aria-label="GitHub webhook setup"><h6 className="font-medium">Manual GitHub webhook setup</h6><label className="block text-sm">Server callback URL<input className={`${select} break-all`} readOnly value={p.callback_url} /></label><p className="text-sm">In the repository’s Settings → Webhooks, add this exact payload URL. Choose application/json, select only push events, and keep SSL verification enabled. Set GitHub’s secret to the exact value of the selected webhook secret version using your approved secret-management process.</p><p className="text-xs">This panel selects existing secret references. It never reveals values or creates provider credentials.</p></section> : null}
    </> : null}
    <p className="text-xs">Ancestry proof is bounded to 100 commits. Automatic retries stop after 32 attempts or 24 hours and use their existing bounded deadline. An authorized owner can explicitly retry to start one new bounded recovery window. Replaying the identical command returns the same window without extending it. Retry does not enable the policy, reset its branch baseline, or reset deduplication.</p>
    <ol className="min-w-0 space-y-2" aria-label="GitHub push deliveries">{!denied && history.data?.items.map((d) => <li key={d.id} className="min-w-0 space-y-1 break-all rounded border p-2 text-sm"><p>Delivery {d.id} · {d.state} · policy generation {d.policy_generation}</p><p>{pushReason[d.reason] ?? "Review delivery metadata."}</p><p>Received {d.received_at} · attempts {d.attempts}/32 · expires {d.expires_at}</p>{d.next_retry_at ? <p>Next retry {d.next_retry_at}</p> : null}<p>Push commit {d.after_commit || "unavailable"} · verified commit {d.verified_commit || "unavailable"}</p>{d.build_id ? <p>Build {d.build_id}. Deployment remains manual.</p> : null}{p?.can_manage ? <DeliveryRetry actor={actor} resource={resource} policy={p} delivery={d} paused={paused} changed={changed} onDenied={() => setDenied(true)} refresh={refresh} /> : null}</li>)}</ol>
    {!history.isPending && !denied && history.data?.items.length === 0 ? <p>No push deliveries recorded.</p> : null}
    <div className="flex flex-wrap gap-2"><Button className={button} type="button" variant="outline" disabled={blocked || denied || !cursor || history.isFetching} onClick={() => setCursor("")}>First push deliveries</Button><Button className={button} type="button" variant="outline" disabled={blocked || denied || history.isFetching || history.isError || !history.data?.has_more} onClick={() => { if (history.data?.has_more) setCursor(history.data.next_cursor); }}>Next push deliveries</Button></div>
  </section>;
}

type ManagerProps = { actor: string; resource: Resource; policy: GitHubPushPolicy; paused: boolean; changed: (p: GitHubPushPolicy | GitHubPushDelivery) => void; onDenied: () => void; refresh: () => Promise<void> };
function useDecision(props: ManagerProps) {
  const client = useQueryClient();
  const [attempt, setAttempt] = useState<PushDecision>(), [busy, setBusy] = useState(false), [conflict, setConflict] = useState(false), [error, setError] = useState(""), [retryAt, setRetryAt] = useState(0);
  const request = useRef<AbortController | null>(null), active = useRef(true);
  function retire(controller: AbortController, uncertain: boolean) {
    if (request.current !== controller) return false;
    request.current = null;
    if (active.current) { setBusy(false); if (uncertain) setError("The decision outcome is unconfirmed. Retry the identical retained request after current access is available."); }
    return true;
  }
  useEffect(() => { active.current = true; return () => { active.current = false; const controller = request.current; if (controller) { retire(controller, false); controller.abort(); } }; }, []);
  useEffect(() => { const controller = request.current; if (props.paused && controller) { retire(controller, true); controller.abort(); } }, [props.paused]);
  useEffect(() => { if (!retryAt) return; const timer = setTimeout(() => setRetryAt(0), Math.max(0, retryAt - Date.now())); return () => clearTimeout(timer); }, [retryAt]);
  async function send(decision: PushDecision) {
    if (props.paused || busy || conflict || retryAt > Date.now() || request.current) return undefined;
    const frozen = attempt ?? decision, controller = new AbortController(); request.current = controller; setAttempt(frozen); setBusy(true); setError("");
    const owns = () => active.current && request.current === controller && !controller.signal.aborted;
    try {
      const result = await withGitHubPushDeadline(controller.signal, frozen.action === "verify" ? 25_000 : 10_000, (signal) => executeGitHubPushDecision(props.resource.id, frozen, { signal }), () => retire(controller, true));
      if (!owns()) return;
      if (!client.getQueryData<GitHubPushPolicy>(policyKey(props.actor, props.resource))?.can_manage) { props.onDenied(); return; }
      props.changed(result); setAttempt(undefined); return result;
    } catch (cause) {
      if (!owns()) return;
      setError(githubPushError(cause));
      if (cause instanceof ApiError && cause.status === 503) setRetryAt(Date.now() + 10_000);
      if (githubPushDenied(cause)) props.onDenied();
      if (cause instanceof ApiError && cause.status === 409) { setConflict(true); await props.refresh(); }
    } finally { retire(controller, false); }
  }
  return { attempt, busy, conflict, error, retryBlocked: retryAt > 0, send, reset: () => { setAttempt(undefined); setConflict(false); setError(""); } };
}
function PushManager(props: ManagerProps) {
  const { actor, resource, policy, paused, onDenied } = props;
  const [base, setBase] = useState(policy), [account, setAccount] = useState(""), [webhook, setWebhook] = useState({ id: "", version: 0 }), [provider, setProvider] = useState({ id: "", version: 0 }), [grantID, setGrantID] = useState(""), [confirmed, setConfirmed] = useState(false), [offset, setOffset] = useState(0), [grantOffset, setGrantOffset] = useState(0);
  const decision = useDecision(props);
  const [now, setNow] = useState(Date.now());
  useEffect(() => { const timer = setInterval(() => setNow(Date.now()), 1000); return () => clearInterval(timer); }, []);
  const locked = paused || decision.busy || Boolean(decision.attempt), stale = base.generation !== policy.generation || base.resource_generation !== policy.resource_generation || base.source_generation !== policy.source_generation;
  const privateSource = resource.source.kind === "git" && resource.source.git.credential_kind === "https_basic";
  const accounts = useQuery({ queryKey: ["githubPushAccounts", actor, resource.project_id, resource.id, offset], enabled: !paused, retry: false, gcTime: 0, refetchOnWindowFocus: false, queryFn: async ({ signal }) => { const page = await withGitHubPushDeadline(signal, 5000, (signal) => listServiceAccounts(resource.project_id, limit, offset, { signal })).catch(metadataError); if (page.items.some((a) => a.project_id !== resource.project_id)) throw new Error("Account scope mismatch"); return { count: page.count, total: page.total, limit: page.limit, offset: page.offset, items: page.items.map(({ id, project_id, name, status, role }) => ({ id, project_id, name, status, role })) }; } });
  const grants = useQuery({ queryKey: ["githubPushGrants", actor, resource.project_id, resource.id, grantOffset], enabled: privateSource && !paused, retry: false, gcTime: 0, refetchOnWindowFocus: false, queryFn: ({ signal }) => withGitHubPushDeadline(signal, 5000, (signal) => listHostingSourceGrants(resource.id, grantOffset, { signal })) });
  useEffect(() => { if (githubPushDenied(accounts.error) || githubPushDenied(grants.error)) onDenied(); }, [accounts.error, grants.error]);
  const selectedAccount = accounts.data?.items.find((a) => a.id === account && a.status === "active" && ["maintainer", "owner"].includes(a.role));
  const selectedGrant = grants.data?.items.find((g) => g.id === grantID && eligibleSourceGrant(g, resource));
  const ready = Boolean(policy.repository && policy.branch && selectedAccount && webhook.id && webhook.version > 0 && (!privateSource || provider.id && provider.version > 0 && selectedGrant) && (!provider.id || provider.version > 0 && provider.id !== webhook.id) && resource.generation === policy.resource_generation && resource.source.kind === "git" && resource.source.git.generation === policy.source_generation && !accounts.isError && !accounts.isPending && (!privateSource || !grants.isError && !grants.isPending));
  async function send(command: PushDecision) { const result = await decision.send(command); if (result && "can_manage" in result) { setBase(result); setConfirmed(false); } }
  const command = (action: "verify" | "enabled", enabled?: boolean): PushDecision => action === "verify" ? { action, body: { expected_policy_generation: base.generation, request_id: crypto.randomUUID() } } : { action, body: { expected_policy_generation: base.generation, request_id: crypto.randomUUID(), enabled: Boolean(enabled) } };
  function configure() {
    if (!confirmed || !ready || locked || stale || decision.conflict) return;
    const configuration: GitHubPushConfiguration = { automation_actor_id: account, webhook_secret: webhook, ...(provider.id ? { provider_read_secret: provider } : {}), ...(privateSource ? { source_grant_id: grantID } : {}) };
    void send({ action: "configure", body: { expected_policy_generation: base.generation, expected_resource_generation: base.resource_generation, expected_source_generation: base.source_generation, request_id: crypto.randomUUID(), configuration, reset_branch_baseline: true } });
  }
  return <section className="min-w-0 space-y-3 rounded border p-3" aria-label="Manage build on push"><p className="text-sm">Only a current project owner or administrator can manage this policy. The automation actor must be an active service account in this project with maintainer or owner access.</p><p className="text-sm"><a className="underline" href="/settings">Manage service accounts</a> · <a className="underline" href="/projects">Manage project secrets and source grants</a></p>
    {base.owner_configuration ? <p className="break-all text-xs">Recorded configuration: actor {base.owner_configuration.automation_actor_id} · webhook {base.owner_configuration.webhook_secret.id} version {base.owner_configuration.webhook_secret.version}{base.owner_configuration.provider_read_secret ? ` · provider ${base.owner_configuration.provider_read_secret.id} version ${base.owner_configuration.provider_read_secret.version}` : " · unauthenticated provider reads"}{base.owner_configuration.source_grant_id ? ` · source grant ${base.owner_configuration.source_grant_id}` : ""}</p> : null}
    <form aria-label="Configure GitHub build on push" className="min-w-0 space-y-3" onSubmit={(event) => { event.preventDefault(); configure(); }}>
      <label className="block">Automation service account<select className={select} value={account} disabled={locked} onChange={(event) => { setAccount(event.target.value); setConfirmed(false); }}><option value="">Choose an active project maintainer or owner</option>{accounts.data?.items.filter((a) => a.status === "active" && ["maintainer", "owner"].includes(a.role)).map((a) => <option key={a.id} value={a.id}>{a.name} · {a.role} · {a.id}</option>)}</select></label>
      <PageNavigation label="push automation accounts" page={accounts.data} loading={locked || accounts.isFetching} onFirst={() => { setOffset(0); setAccount(""); }} onPrevious={() => { setOffset(Math.max(0, offset - limit)); setAccount(""); }} onNext={() => { setOffset(nextOffset(offset)); setAccount(""); }} />
      <SecretReference label="Webhook signing" actor={actor} resource={resource} locked={locked} onDenied={onDenied} value={webhook} onChange={(value) => { setWebhook(value); setConfirmed(false); }} />
      <SecretReference label="GitHub provider read" actor={actor} resource={resource} locked={locked} onDenied={onDenied} value={provider} onChange={(value) => { setProvider(value); setConfirmed(false); }} />
      <p className="text-xs">Choose distinct existing managed secrets and explicit immutable versions for webhook signing and provider reads. Private Git requires a provider read secret and a current source grant. Public repositories may omit provider credentials and use GitHub’s unauthenticated rate limit.</p>
      {privateSource ? <><label className="block">Push private source grant<select className={select} value={grantID} disabled={locked} onChange={(event) => { setGrantID(event.target.value); setConfirmed(false); }}><option value="">Choose a current HTTPS source grant</option>{grants.data?.items.filter((g) => eligibleSourceGrant(g, resource)).map((g) => <option key={g.id} value={g.id}>{g.id} · source {g.source_generation}</option>)}</select></label><PageNavigation label="push source grants" page={grants.data} loading={locked || grants.isFetching} onFirst={() => { setGrantOffset(0); setGrantID(""); }} onPrevious={() => { setGrantOffset(Math.max(0, grantOffset - limit)); setGrantID(""); }} onNext={() => { setGrantOffset(nextOffset(grantOffset)); setGrantID(""); }} /></> : null}
      <label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={confirmed} disabled={locked} onChange={(event) => setConfirmed(event.target.checked)} /><span>I reviewed these exact references and generations. Saving disables the policy, resets the branch baseline, and requires new verification before enabling.</span></label>
      <Button className={button} type="submit" disabled={locked || stale || decision.conflict || !confirmed || !ready}>Save disabled push configuration</Button>
    </form>
    {accounts.isError || grants.isError ? <p role="alert">Unable to verify current account or source grant metadata. Configuration is paused.</p> : null}
    <div className="flex flex-wrap gap-2"><Button className={button} type="button" disabled={locked || stale || decision.conflict || !base.id} onClick={() => void send(command("verify"))}>Verify GitHub repository and branch</Button><Button className={button} type="button" disabled={locked || stale || decision.conflict || !base.id || base.enabled || !verificationCurrent(base, now)} onClick={() => void send(command("enabled", true))}>Enable build on push</Button><Button className={button} type="button" disabled={locked || stale || decision.conflict || !base.id || !base.enabled} onClick={() => void send(command("enabled", false))}>Disable build on push</Button></div>
    <p className="text-xs">Enabling requires verification of the current configuration within 60 seconds. Provider failures can return a blocked policy even when the command succeeds; review the recorded outcome above.</p>
    {decision.attempt ? <><p className="break-all text-xs">Retained {decision.attempt.action} request {decision.attempt.body.request_id} · expected policy generation {decision.attempt.body.expected_policy_generation}</p><Button className={button} type="button" disabled={paused || decision.busy || decision.conflict || decision.retryBlocked} onClick={() => { if (decision.attempt) void send(decision.attempt); }}>Retry identical push decision</Button></> : null}
    {decision.error ? <p role="alert">{decision.error}</p> : null}
    {decision.conflict || stale || decision.attempt ? <section className="space-y-2" aria-label="Review refreshed push policy"><p className="break-all text-sm">Current policy {policy.generation} · resource {policy.resource_generation} / source {policy.source_generation}. Check delivery and build history before replacing an unconfirmed decision.</p><Button className={button} type="button" disabled={paused || decision.busy} onClick={() => { setBase(policy); decision.reset(); setConfirmed(false); setAccount(""); setWebhook({ id: "", version: 0 }); setProvider({ id: "", version: 0 }); setGrantID(""); }}>Use reviewed policy for a separate decision</Button></section> : null}
  </section>;
}
function SecretReference({ label, actor, resource, locked, value, onChange, onDenied }: { label: string; actor: string; resource: Resource; locked: boolean; value: { id: string; version: number }; onChange: (value: { id: string; version: number }) => void; onDenied: () => void }) {
  const [offset, setOffset] = useState(0), [versionOffset, setVersionOffset] = useState(0);
  const secrets = useQuery({ queryKey: ["githubPushSecrets", actor, resource.project_id, resource.id, label, offset], enabled: !locked, retry: false, gcTime: 0, refetchOnWindowFocus: false, queryFn: async ({ signal }) => { const page = await withGitHubPushDeadline(signal, 5000, (signal) => listManagedSecrets({ project_id: resource.project_id, limit, offset }, { signal })).catch(metadataError); if (page.items.some((s) => s.project_id !== resource.project_id)) throw new Error("Secret project mismatch"); return { count: page.count, total: page.total, limit: page.limit, offset: page.offset, items: page.items.map(({ id, project_id, name, status }) => ({ id, project_id, name, status })) }; } });
  const versions = useQuery({ queryKey: ["githubPushVersions", actor, resource.project_id, resource.id, label, value.id, versionOffset], enabled: !locked && Boolean(value.id), retry: false, gcTime: 0, refetchOnWindowFocus: false, queryFn: async ({ signal }) => { const page = await withGitHubPushDeadline(signal, 5000, (signal) => listManagedSecretVersions(value.id, { limit, offset: versionOffset }, { signal })).catch(metadataError); if (page.items.some((s) => s.secret_id !== value.id)) throw new Error("Secret version mismatch"); return { count: page.count, total: page.total, limit: page.limit, offset: page.offset, items: page.items.map(({ secret_id, version }) => ({ secret_id, version })) }; } });
  useEffect(() => { if (githubPushDenied(secrets.error) || githubPushDenied(versions.error)) onDenied(); }, [secrets.error, versions.error]);
  useEffect(() => { if (!locked && value.id && (secrets.isError || secrets.data && !secrets.data.items.some((s) => s.id === value.id && s.status === "active"))) onChange({ id: "", version: 0 }); else if (!locked && value.version && (versions.isError || versions.data && !versions.data.items.some((v) => v.version === value.version))) onChange({ id: value.id, version: 0 }); }, [locked, value.id, value.version, secrets.data, secrets.isError, versions.data, versions.isError]);
  return <fieldset className="min-w-0 space-y-2" disabled={locked}><legend className="text-sm font-medium">{label} secret reference</legend><label className="block">{label} managed secret<select className={select} value={value.id} onChange={(event) => { setVersionOffset(0); onChange({ id: event.target.value, version: 0 }); }}><option value="">{label === "GitHub provider read" ? "No provider secret selected" : "Choose an existing signing secret"}</option>{secrets.data?.items.filter((s) => s.status === "active").map((s) => <option value={s.id} key={s.id}>{s.name} · {s.id}</option>)}</select></label>
    <PageNavigation label={`${label} secret metadata`} page={secrets.data} loading={locked || secrets.isFetching} onFirst={() => { setOffset(0); onChange({ id: "", version: 0 }); }} onPrevious={() => { setOffset(Math.max(0, offset - limit)); onChange({ id: "", version: 0 }); }} onNext={() => { setOffset(nextOffset(offset)); onChange({ id: "", version: 0 }); }} />
    <label className="block">{label} immutable version<select className={select} value={value.version || ""} disabled={locked || !value.id || versions.isPending || versions.isError} onChange={(event) => onChange({ id: value.id, version: Number(event.target.value) })}><option value="">Choose an existing version</option>{versions.data?.items.map((v) => <option value={v.version} key={v.version}>Version {v.version}</option>)}</select></label>
    {value.id ? <PageNavigation label={`${label} versions`} page={versions.data} loading={locked || versions.isFetching} onFirst={() => { setVersionOffset(0); onChange({ id: value.id, version: 0 }); }} onPrevious={() => { setVersionOffset(Math.max(0, versionOffset - limit)); onChange({ id: value.id, version: 0 }); }} onNext={() => { setVersionOffset(nextOffset(versionOffset)); onChange({ id: value.id, version: 0 }); }} /> : null}
    {secrets.isError || versions.isError ? <p role="alert">Unable to verify {label.toLowerCase()} metadata. No secret value was requested.</p> : null}
  </fieldset>;
}
function DeliveryRetry(props: ManagerProps & { delivery: GitHubPushDelivery }) {
  const decision = useDecision(props), d = props.delivery;
  const eligible = ["retry_wait", "blocked_exhausted"].includes(d.state) && d.policy_id === props.policy.id && d.policy_generation === props.policy.generation && props.policy.enabled && props.policy.can_manage;
  if (!eligible && !decision.attempt) return null;
  return <div className="space-y-2"><Button className={button} type="button" disabled={props.paused || decision.busy || decision.conflict || decision.retryBlocked || !eligible} onClick={() => { if (eligible) void decision.send({ action: "retry", body: { expected_policy_generation: props.policy.generation, request_id: crypto.randomUUID(), delivery_id: d.id } }); }}>{decision.attempt ? "Retry identical delivery decision" : `Retry retained delivery ${d.id}`}</Button>{decision.attempt ? <p className="break-all text-xs">Retained retry request {decision.attempt.body.request_id}. If accepted, this owner decision starts one new bounded recovery window. An identical retry returns the same window without extending it.</p> : null}{decision.error ? <p role="alert">{decision.error}</p> : null}{decision.conflict ? <p>Review refreshed policy and delivery history before another retry decision.</p> : null}</div>;
}
