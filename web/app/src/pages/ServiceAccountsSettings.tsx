import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, KeyRound, RotateCw, ShieldOff } from "lucide-react";
import type { Project } from "@/api";
import {
  createServiceAccount,
  listServiceAccounts,
  revokeServiceAccount,
  rotateServiceAccountCredential,
  updateServiceAccount,
  type ServiceAccount,
  type ServiceAccountCreateInput,
  type ServiceAccountListResponse,
  type ServiceAccountUpdateInput,
} from "@/api/service-accounts";
import { PageNavigation } from "@/components/common/PageNavigation";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";

export const serviceAccountsPageSize = 10;

type OneTimeSecret = { accountName: string; token: string; expiresAt?: string };

export function ServiceAccountsSettingsPanel({ projects, readOnly = false }: { projects: Project[]; readOnly?: boolean }): ReactNode {
  const client = useQueryClient();
  const [projectID, setProjectID] = useState(projects[0]?.id ?? "");
  const [offset, setOffset] = useState(0);
  const [expiryError, setExpiryError] = useState("");
  const expiryHintID = useId();
  const expiryErrorID = useId();
  const [secret, setSecret] = useState<OneTimeSecret>();
  const [revoking, setRevoking] = useState<ServiceAccount>();
  const [revokeError, setRevokeError] = useState("");
  const revokingRef = useRef(false);

  useEffect(() => {
    if (!projects.some((project) => project.id === projectID)) setProjectID(projects[0]?.id ?? "");
  }, [projectID, projects]);
  useEffect(() => {
    setOffset(0);
    setSecret(undefined);
  }, [projectID]);

  const page = useQuery({
    queryKey: ["service-accounts", projectID, offset],
    queryFn: ({ signal }) => listServiceAccounts(projectID, serviceAccountsPageSize, offset, { signal }),
    enabled: Boolean(projectID),
  });
  const invalidate = () => client.invalidateQueries({ queryKey: ["service-accounts", projectID] });
  const create = useMutation({
    mutationFn: (input: ServiceAccountCreateInput) => createServiceAccount(input),
    onSuccess: async (result) => {
      setSecret({ accountName: result.service_account.name, token: result.token, expiresAt: result.credential.expires_at });
      await invalidate();
    },
  });
  async function retainConfirmedAccount(account: ServiceAccount) {
    const queryKey = ["service-accounts", account.project_id];
    client.setQueriesData<ServiceAccountListResponse>({ queryKey }, (cached) => cached ? {
      ...cached,
      items: cached.items.map((item) => item.id === account.id ? account : item),
    } : cached);
    await client.invalidateQueries({ queryKey });
  }
  const update = useMutation({ mutationFn: (input: ServiceAccountUpdateInput) => updateServiceAccount(input), onSuccess: retainConfirmedAccount });
  const rotate = useMutation({
    mutationFn: ({ account }: { account: ServiceAccount }) => rotateServiceAccountCredential({ service_account_id: account.id }),
    onSuccess: async (result, variables) => {
      setSecret({ accountName: variables.account.name, token: result.token, expiresAt: result.credential.expires_at });
      await invalidate();
    },
  });
  const revoke = useMutation({
    mutationFn: (serviceAccountID: string) => revokeServiceAccount(serviceAccountID),
    onSuccess: async (account) => {
      setRevoking(undefined);
      await retainConfirmedAccount(account);
    },
    onError: () => setRevokeError("Unable to revoke this account. Try again."),
    onSettled: () => { revokingRef.current = false; },
  });
  const busy = create.isPending || update.isPending || rotate.isPending || revoke.isPending;
  const controlsDisabled = readOnly || busy || page.isError;
  const result = page.data;
  useEffect(() => {
    if (!revoking || !result || revokingRef.current) return;
    const current = result.items.find((account) => account.id === revoking.id);
    if (!current || current.status !== "active") {
      setRevoking(undefined);
      setRevokeError("");
    } else if (current !== revoking) {
      setRevoking(current);
    }
  }, [result, revoking, revoke.isPending]);

  function submitCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (controlsDisabled || !projectID) return;
    const element = event.currentTarget;
    const form = new FormData(element);
    const expiry = String(form.get("expires_at") ?? "");
    const expiresAt = expiry ? new Date(expiry) : undefined;
    const now = Date.now();
    if (expiresAt && (!Number.isFinite(expiresAt.getTime()) || expiresAt.getTime() <= now || expiresAt.getTime() > now + 365 * 24 * 60 * 60 * 1000)) {
      setExpiryError("Choose a future date within the next 365 days.");
      element.querySelector<HTMLInputElement>('[name="expires_at"]')?.focus();
      return;
    }
    setExpiryError("");
    create.mutate({
      project_id: projectID,
      name: String(form.get("name") ?? ""),
      role: String(form.get("role") ?? "viewer") as "owner" | "maintainer" | "viewer",
      ...(expiresAt ? { expires_at: expiresAt.toISOString() } : {}),
    }, { onSuccess: () => element.reset() });
  }

  return (
    <Card className="min-w-0 lg:col-span-2">
      <CardHeader><CardTitle><Bot className="mr-2 inline h-4 w-4" />Service accounts</CardTitle></CardHeader>
      <CardContent className="grid min-w-0 gap-4">
        <label className="grid gap-1 text-sm">Project<Select aria-label="Service account project" disabled={busy} value={projectID} onChange={(event) => setProjectID(event.target.value)}>{projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</Select></label>
        <form className="grid min-w-0 items-start gap-2 md:grid-cols-4" aria-label="Create service account" aria-busy={create.isPending} onSubmit={submitCreate}>
          <label className="grid min-w-0 gap-1 text-sm">Name<Input name="name" aria-label="Service account name" disabled={busy} placeholder="Automation account" maxLength={128} required /></label>
          <label className="grid min-w-0 gap-1 text-sm">Role<Select name="role" aria-label="Service account role" disabled={busy} defaultValue="maintainer"><option value="viewer">viewer</option><option value="maintainer">maintainer</option><option value="owner">owner</option></Select></label>
          <div className="grid min-w-0 gap-1">
            <label className="grid min-w-0 gap-1 text-sm">Credential expiry<Input name="expires_at" disabled={busy} type="datetime-local" aria-invalid={Boolean(expiryError)} aria-describedby={`${expiryHintID}${expiryError ? ` ${expiryErrorID}` : ""}`} onChange={() => setExpiryError("")} /></label>
            <p id={expiryHintID} className="text-xs text-muted-foreground">Optional, in your local time. Leave blank for 90 days, or choose a date within 365 days.</p>
            {expiryError ? <p id={expiryErrorID} role="alert" className="text-sm text-destructive">{expiryError}</p> : null}
          </div>
          <Button className="self-start md:mt-6" type="submit" disabled={controlsDisabled || !projectID}><KeyRound className="h-4 w-4" />{create.isPending ? "Creating…" : "Create account"}</Button>
          {create.error && !create.isPending && create.variables?.project_id === projectID ? <p role="alert" className="break-words text-sm text-destructive [overflow-wrap:anywhere] md:col-span-4">Unable to create account: {create.error.message}</p> : null}
        </form>
        {page.isError ? <section className="space-y-3">
          <p role="alert">{result ? "Unable to refresh service accounts. Showing previously loaded accounts." : "Unable to load service accounts."}</p>
          <Button variant="outline" disabled={page.isFetching} onClick={() => void page.refetch()}>Retry service accounts</Button>
        </section> : null}
        {page.isPending ? <p role="status">Loading service accounts…</p> : result?.items.length === 0 ? <p className="text-sm text-muted-foreground">No service accounts on this page.</p> : result?.items.map((account) => (
          <ServiceAccountEditor
            key={account.id}
            account={account}
            busy={busy}
            disabled={controlsDisabled}
            saving={update.isPending && update.variables?.service_account_id === account.id}
            rotating={rotate.isPending && rotate.variables?.account.id === account.id}
            saveError={!update.isPending && update.variables?.service_account_id === account.id ? update.error?.message : undefined}
            rotateError={!rotate.isPending && rotate.variables?.account.id === account.id ? rotate.error?.message : undefined}
            onSave={(input) => update.mutateAsync(input)}
            onRotate={() => rotate.mutate({ account })}
            onRevoke={() => { setRevokeError(""); setRevoking(account); }}
          />
        ))}
        <PageNavigation page={result ?? { count: 0, total: 0, limit: serviceAccountsPageSize, offset }} label="accounts" loading={busy || page.isFetching}
          onFirst={() => setOffset(0)} onPrevious={() => setOffset((value) => Math.max(0, value - serviceAccountsPageSize))}
          onNext={() => setOffset((value) => value + serviceAccountsPageSize)} />
        {secret ? <ServiceAccountCredential secret={secret} onClose={() => setSecret(undefined)} /> : null}
        <Dialog open={Boolean(revoking)} onOpenChange={(open) => { if (!open && !revokingRef.current) setRevoking(undefined); }}>
          <DialogContent showCloseButton={!revoke.isPending} className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
            <DialogHeader className="min-w-0 pr-6">
              <DialogTitle className="break-all">Revoke {revoking?.name}?</DialogTitle>
              <DialogDescription>This immediately disables the stable account and every credential issued for it.</DialogDescription>
            </DialogHeader>
            {revokeError ? <p role="alert" className="text-sm text-destructive">{revokeError}</p> : null}
            <DialogFooter>
              <Button variant="outline" disabled={revoke.isPending} onClick={() => { if (!revokingRef.current) setRevoking(undefined); }}>Keep account active</Button>
              <Button variant="destructive" disabled={controlsDisabled} onClick={() => {
                if (!revoking || controlsDisabled || revokingRef.current) return;
                revokingRef.current = true;
                setRevokeError("");
                revoke.mutate(revoking.id);
              }}>{revoke.isPending ? "Revoking..." : "Revoke account"}</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </CardContent>
    </Card>
  );
}


type ServiceAccountEditorProps = {
  account: ServiceAccount;
  busy: boolean;
  disabled: boolean;
  saving: boolean;
  rotating: boolean;
  saveError?: string;
  rotateError?: string;
  onSave: (input: ServiceAccountUpdateInput) => Promise<ServiceAccount>;
  onRotate: () => void;
  onRevoke: () => void;
};

function ServiceAccountEditor({ account, busy, disabled, saving, rotating, saveError, rotateError, onSave, onRotate, onRevoke }: ServiceAccountEditorProps): ReactNode {
  const [draft, setDraft] = useState<Partial<Pick<ServiceAccount, "name" | "role">>>({});
  const name = draft.name ?? account.name;
  const role = draft.role ?? account.role;
  const changed = name !== account.name || role !== account.role;

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (disabled || account.status !== "active") return;
    try {
      await onSave({ service_account_id: account.id, name, role });
      setDraft({});
    } catch {
      // The parent mutation supplies the error; retain the draft for retry.
    }
  }

  return <form aria-label={`Edit service account ${account.name}`} aria-busy={saving || rotating} className="grid min-w-0 gap-2 rounded border p-3 md:grid-cols-[minmax(0,1fr)_10rem_auto_auto]" onSubmit={(event) => void submit(event)} onReset={(event) => {
    event.preventDefault();
    if (!busy) setDraft({});
  }}>
    <div className="min-w-0">
      <Input name="name" disabled={busy} aria-label={`Name for ${account.name}`} value={name} onChange={(event) => {
        const value = event.target.value;
        setDraft((current) => ({ ...current, name: value === account.name ? undefined : value }));
      }} maxLength={128} required />
      <p className="mt-1 break-all font-mono text-xs text-muted-foreground">{account.id}</p>
    </div>
    <div className="grid gap-1">
      <Select name="role" disabled={busy} aria-label={`Role for ${account.name}`} value={role} onChange={(event) => {
        const value = event.target.value as ServiceAccount["role"];
        setDraft((current) => ({ ...current, role: value === account.role ? undefined : value }));
      }}>
        <option value="viewer">viewer</option><option value="maintainer">maintainer</option><option value="owner">owner</option>
      </Select>
      <Badge variant={account.status === "active" ? "success" : "secondary"}>{account.status}</Badge>
    </div>
    <div className="flex flex-wrap items-start gap-2">
      <Button type="submit" variant="outline" size="sm" disabled={disabled || account.status !== "active"}>{saving ? "Saving…" : "Save"}</Button>
      <Button type="reset" variant="outline" size="sm" disabled={busy || !changed}>Reset</Button>
      <Button type="button" variant="outline" size="sm" disabled={disabled || account.status !== "active"} onClick={onRotate}><RotateCw className="h-4 w-4" />{rotating ? "Rotating…" : "Rotate"}</Button>
    </div>
    <Button type="button" variant="destructive" size="sm" disabled={disabled || account.status !== "active"} onClick={onRevoke}><ShieldOff className="h-4 w-4" />Revoke</Button>
    {saveError ? <p role="alert" className="break-words text-sm text-destructive [overflow-wrap:anywhere] md:col-span-4">Unable to save account: {saveError}</p> : null}
    {rotateError ? <p role="alert" className="break-words text-sm text-destructive [overflow-wrap:anywhere] md:col-span-4">Unable to rotate credential: {rotateError}</p> : null}
  </form>;
}


function ServiceAccountCredential({ secret, onClose }: { secret: OneTimeSecret; onClose: () => void }): ReactNode {
  const [copyStatus, setCopyStatus] = useState<"idle" | "copying" | "copied" | "failed">("idle");
  const active = useRef(true);
  useEffect(() => { active.current = true; return () => { active.current = false; }; }, []);

  async function copyCredential() {
    if (copyStatus === "copying") return;
    setCopyStatus("copying");
    try {
      await navigator.clipboard.writeText(secret.token);
      if (active.current) setCopyStatus("copied");
    } catch {
      if (active.current) setCopyStatus("failed");
    }
  }

  return <Dialog open onOpenChange={(open) => !open && onClose()}>
    <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto">
      <DialogHeader className="min-w-0 pr-6">
        <DialogTitle>Save this credential now</DialogTitle>
        <DialogDescription className="break-words">The credential for {secret.accountName} is shown once and expires{secret.expiresAt ? ` ${new Date(secret.expiresAt).toLocaleString()}` : " at its configured time"}. Closing this dialog clears it from the page.</DialogDescription>
      </DialogHeader>
      <Input aria-label="One-time service account credential" value={secret.token} readOnly autoComplete="off" />
      {copyStatus === "copied" ? <p role="status" className="text-sm">Credential copied.</p> : null}
      {copyStatus === "failed" ? <p role="alert" className="text-sm text-destructive">Copy failed. Select and copy the credential manually, or try again.</p> : null}
      <DialogFooter>
        <Button variant="outline" disabled={copyStatus === "copying"} onClick={() => void copyCredential()}>{copyStatus === "copying" ? "Copying..." : "Copy credential"}</Button>
        <Button onClick={onClose}>I saved it</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>;
}
