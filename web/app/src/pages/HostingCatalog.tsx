import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { getCatalog, installCatalog, type CatalogInstallInput, type CatalogInstallation } from "@/api/hosting-catalog";
import { listDestinations } from "@/api/destinations";
import { databaseRequestID } from "@/api/hosting-databases";
import { revealManagedSecret } from "@/api/managed-secrets";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useDatabaseAccess } from "./ResourceDatabase";
export function HostingCatalog({ projectID, environmentID, blocked }: {
    projectID: string;
    environmentID: string;
    blocked: boolean;
}) {
    const access = useDatabaseAccess(projectID, environmentID);
    if (!access.ready)
        return <p role="status">Checking catalog access...</p>;
    if (!access.owner)
        return <p>Project owner access is required to install catalog applications.</p>;
    return <CatalogForm key={access.epoch} projectID={projectID} environmentID={environmentID} actor={access.scope.actor} blocked={blocked || access.refreshing}/>;
}
function CatalogForm({ projectID, environmentID, actor, blocked }: {
    projectID: string;
    environmentID: string;
    actor: string;
    blocked: boolean;
}) {
    const id = useId();
    const client = useQueryClient();
    const [name, setName] = useState("Linkding");
    const [destination, setDestination] = useState("");
    const [template, setTemplate] = useState("");
    const [attempt, setAttempt] = useState<CatalogInstallInput>();
    const [created, setCreated] = useState<CatalogInstallation>();
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState("");
    const [values, setValues] = useState<Record<string, string>>({});
    const active = useRef(true);
    const controller = useRef<AbortController | undefined>(undefined);
    useEffect(() => {
        active.current = true;
        return () => {
            active.current = false;
            controller.current?.abort();
        };
    }, []);
    const catalog = useQuery({ queryKey: ["hostingCatalog", actor, projectID], queryFn: ({ signal }) => getCatalog(projectID, signal), retry: false, gcTime: 0 });
    const [offset, setOffset] = useState(0);
    const destinations = useQuery({ queryKey: ["catalogDestinations", actor, projectID, offset], queryFn: ({ signal }) => listDestinations({ project_id: projectID, limit: 20, offset }, { signal }), retry: false, gcTime: 0 });
    const selected = catalog.data?.find(t => t.id === template);
    const unavailable = blocked || busy || catalog.isError || catalog.isPending || destinations.isError || destinations.isPending;
    async function submit(event: FormEvent) {
        event.preventDefault();
        if (unavailable || (!attempt && (!selected || !destination)))
            return;
        const body = attempt ?? { request_id: databaseRequestID(), project_id: projectID, project_environment_id: environmentID, destination_id: destination, name, template_id: selected!.id as "linkding", immutable_version: selected!.immutable_version as CatalogInstallInput["immutable_version"] };
        setAttempt(body);
        setBusy(true);
        setError("");
        controller.current = new AbortController();
        try {
            const result = await installCatalog(body, controller.current.signal);
            if (!active.current)
                return;
            if (result.request.request_id !== body.request_id || result.request.project_id !== projectID || result.request.project_environment_id !== environmentID || result.binding.destination_id !== body.destination_id)
                throw new Error("Catalog response scope mismatch");
            setCreated(result);
            await client.invalidateQueries({ queryKey: ["hostingResources", projectID] });
        }
        catch {
            if (active.current)
                setError("Installation unconfirmed. Retry keeps the same request and credentials. Refresh access if permission changed.");
        }
        finally {
            if (active.current)
                setBusy(false);
        }
    }
    async function reveal() {
        if (!created || busy)
            return;
        setBusy(true);
        setError("");
        controller.current = new AbortController();
        try {
            const next: Record<string, string> = {};
            for (const entry of created.credentials) {
                if (entry.kind !== "managed_secret" || !entry.secret)
                    throw new Error("Invalid credential metadata");
                const result = await revealManagedSecret(entry.secret.secret_id, entry.secret.version, { signal: controller.current.signal });
                next[entry.name] = result.value;
            }
            if (active.current)
                setValues(next);
        }
        catch {
            if (active.current) {
                setValues({});
                setError("Credential reveal unavailable. Check current project access.");
            }
        }
        finally {
            if (active.current)
                setBusy(false);
        }
    }
    return <section aria-label="Hosting catalog" className="min-w-0 space-y-3 rounded-xl border p-3">
    <h4 className="font-medium">Install from catalog</h4>
    <p>Choose a pinned application. Installation saves its credentials and persistent storage. Deploy it from the application deployment panel.</p>
    <p className="text-sm">Arbitrary Compose import is incomplete.</p>
 {created ? <>
        <p role="status">Installed {created.resource.name}. Open its application, review deployment settings, and deploy.</p>
        <p>Login and password are retained managed secrets. Linkding creates the initial account only when its username is absent. Changing startup credentials does not rotate an existing account password.</p>
        <Button type="button" disabled={busy || blocked} onClick={() => void reveal()}>Reveal login credentials</Button>
        <Button type="button" variant="outline" onClick={() => setValues({})}>Hide credentials</Button>{Object.entries(values).map(([key, value]) => <p className="break-all" key={key}>{key}: <output>{value}</output>
            </p>)}</> : <form onSubmit={event => void submit(event)} className="space-y-3" aria-label="Install catalog application">
        <label htmlFor={`${id}-template`}>Catalog application</label>
        <select id={`${id}-template`} className="h-10 w-full min-w-0 rounded-xl border bg-card px-2" disabled={unavailable || !!attempt} value={template} onChange={e => setTemplate(e.target.value)}>
        <option value="">Select an application</option>{catalog.data?.map(t => <option key={t.id} value={t.id}>{t.name} · {t.immutable_version}</option>)}</select>
        <label htmlFor={`${id}-name`}>Installation name</label>
        <Input id={`${id}-name`} required value={name} disabled={unavailable || !!attempt} onChange={e => setName(e.target.value)}/>
        <label htmlFor={`${id}-destination`}>Catalog destination</label>
        <select id={`${id}-destination`} className="h-10 w-full min-w-0 rounded-xl border bg-card px-2" required value={destination} disabled={unavailable || !!attempt} onChange={e => setDestination(e.target.value)}>
        <option value="">Select a destination</option>{destinations.data?.items.map(d => <option key={d.id} value={d.id}>{d.name}</option>)}</select>
        <div className="flex gap-2">
        <Button type="button" variant="outline" disabled={unavailable || !!attempt || offset === 0} onClick={() => setOffset(Math.max(0, offset - 20))}>Previous destinations</Button>
        <Button type="button" variant="outline" disabled={unavailable || !!attempt || !destinations.data || offset + 20 >= destinations.data.total} onClick={() => setOffset(offset + 20)}>Next destinations</Button>
        </div>{selected ? <p className="break-all text-sm">{selected.definition.image_reference}. Persistent data: {selected.definition.mount_path}.</p> : null}<Button disabled={unavailable || (!attempt && (!selected || !destination))} type="submit">{busy ? "Installing..." : attempt ? "Retry same installation" : "Install application"}</Button>{attempt ? <p className="break-all text-sm">Retained request: {attempt.request_id}</p> : null}</form>}
 {error ? <p role="alert">{error}</p> : null}{catalog.isError || destinations.isError ? <p role="alert">Catalog or destination access is unavailable.</p> : null}
 </section>;
}
