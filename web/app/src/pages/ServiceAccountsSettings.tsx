import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, KeyRound, RotateCw, ShieldOff } from "lucide-react";
import { toast } from "sonner";
import type { Project } from "@/api";
import {
  createServiceAccount,
  listServiceAccounts,
  revokeServiceAccount,
  rotateServiceAccountCredential,
  updateServiceAccount,
  type ServiceAccount,
  type ServiceAccountCreateInput,
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

export function ServiceAccountsSettingsPanel({ projects }: { projects: Project[] }): ReactNode {
  const client = useQueryClient();
  const [projectID, setProjectID] = useState(projects[0]?.id ?? "");
  const [offset, setOffset] = useState(0);
  const [secret, setSecret] = useState<OneTimeSecret>();
  const [revoking, setRevoking] = useState<ServiceAccount>();

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
    onError: (error) => toast.error(error.message),
  });
  const update = useMutation({ mutationFn: (input: ServiceAccountUpdateInput) => updateServiceAccount(input), onSuccess: invalidate, onError: (error) => toast.error(error.message) });
  const rotate = useMutation({
    mutationFn: ({ account }: { account: ServiceAccount }) => rotateServiceAccountCredential({ service_account_id: account.id }),
    onSuccess: async (result, variables) => {
      setSecret({ accountName: variables.account.name, token: result.token, expiresAt: result.credential.expires_at });
      await invalidate();
    },
    onError: (error) => toast.error(error.message),
  });
  const revoke = useMutation({
    mutationFn: (serviceAccountID: string) => revokeServiceAccount(serviceAccountID),
    onSuccess: async () => {
      setRevoking(undefined);
      await invalidate();
    },
    onError: (error) => toast.error(error.message),
  });
  const busy = create.isPending || update.isPending || rotate.isPending || revoke.isPending;
  const controlsDisabled = busy || page.isError;
  const result = page.data;

  function submitCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (controlsDisabled || !projectID) return;
    const element = event.currentTarget;
    const form = new FormData(element);
    const expiry = String(form.get("expires_at") ?? "");
    create.mutate({
      project_id: projectID,
      name: String(form.get("name") ?? ""),
      role: String(form.get("role") ?? "viewer") as "owner" | "maintainer" | "viewer",
      ...(expiry ? { expires_at: new Date(expiry).toISOString() } : {}),
    }, { onSuccess: () => element.reset() });
  }

  return (
    <Card className="min-w-0 lg:col-span-2">
      <CardHeader><CardTitle><Bot className="mr-2 inline h-4 w-4" />Service accounts</CardTitle></CardHeader>
      <CardContent className="grid min-w-0 gap-4">
        <label className="grid gap-1 text-sm">Project<Select aria-label="Service account project" disabled={busy} value={projectID} onChange={(event) => setProjectID(event.target.value)}>{projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</Select></label>
        <form className="grid min-w-0 gap-2 md:grid-cols-4" onSubmit={submitCreate}>
          <Input name="name" aria-label="Service account name" disabled={busy} placeholder="Automation account" maxLength={128} required />
          <Select name="role" aria-label="Service account role" disabled={busy} defaultValue="maintainer"><option value="viewer">viewer</option><option value="maintainer">maintainer</option><option value="owner">owner</option></Select>
          <Input name="expires_at" aria-label="Credential expiry" disabled={busy} type="datetime-local" />
          <Button type="submit" disabled={controlsDisabled || !projectID}><KeyRound className="h-4 w-4" />Create account</Button>
        </form>
        {page.isError ? <section className="space-y-3">
          <p role="alert">{result ? "Unable to refresh service accounts. Showing previously loaded accounts." : "Unable to load service accounts."}</p>
          <Button variant="outline" disabled={page.isFetching} onClick={() => void page.refetch()}>Retry service accounts</Button>
        </section> : null}
        {page.isPending ? <p role="status">Loading service accounts…</p> : result?.items.length === 0 ? <p className="text-sm text-muted-foreground">No service accounts on this page.</p> : result?.items.map((account) => (
          <form key={account.id} className="grid min-w-0 gap-2 rounded border p-3 md:grid-cols-[minmax(0,1fr)_10rem_auto_auto]" onSubmit={(event) => {
            event.preventDefault();
            if (controlsDisabled) return;
            const form = new FormData(event.currentTarget);
            update.mutate({ service_account_id: account.id, name: String(form.get("name")), role: String(form.get("role")) as "owner" | "maintainer" | "viewer" });
          }}>
            <div className="min-w-0"><Input name="name" aria-label={`Name for ${account.name}`} defaultValue={account.name} maxLength={128} required /><p className="mt-1 break-all font-mono text-xs text-muted-foreground">{account.id}</p></div>
            <div className="grid gap-1"><Select name="role" aria-label={`Role for ${account.name}`} defaultValue={account.role}><option value="viewer">viewer</option><option value="maintainer">maintainer</option><option value="owner">owner</option></Select><Badge variant={account.status === "active" ? "success" : "secondary"}>{account.status}</Badge></div>
            <div className="flex flex-wrap items-start gap-2"><Button type="submit" variant="outline" size="sm" disabled={controlsDisabled || account.status !== "active"}>Save</Button><Button type="button" variant="outline" size="sm" disabled={controlsDisabled || account.status !== "active"} onClick={() => rotate.mutate({ account })}><RotateCw className="h-4 w-4" />Rotate</Button></div>
            <Button type="button" variant="destructive" size="sm" disabled={controlsDisabled || account.status !== "active"} onClick={() => setRevoking(account)}><ShieldOff className="h-4 w-4" />Revoke</Button>
          </form>
        ))}
        <PageNavigation page={result ?? { count: 0, total: 0, limit: serviceAccountsPageSize, offset }} label="accounts" loading={busy || page.isFetching}
          onFirst={() => setOffset(0)} onPrevious={() => setOffset((value) => Math.max(0, value - serviceAccountsPageSize))}
          onNext={() => setOffset((value) => value + serviceAccountsPageSize)} />
        {secret ? <ServiceAccountCredential secret={secret} onClose={() => setSecret(undefined)} /> : null}
        <Dialog open={Boolean(revoking)} onOpenChange={(open) => !open && setRevoking(undefined)}><DialogContent><DialogHeader><DialogTitle>Revoke {revoking?.name}?</DialogTitle><DialogDescription>This immediately disables the stable account and every credential issued for it.</DialogDescription></DialogHeader><DialogFooter><Button variant="outline" onClick={() => setRevoking(undefined)}>Keep account active</Button><Button variant="destructive" disabled={controlsDisabled} onClick={() => revoking && revoke.mutate(revoking.id)}>Revoke account</Button></DialogFooter></DialogContent></Dialog>
      </CardContent>
    </Card>
  );
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
