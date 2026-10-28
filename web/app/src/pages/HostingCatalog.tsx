import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { getCatalog, installCatalog, type CatalogInstallInput, type CatalogInstallation } from "@/api/hosting-catalog";
import { getCatalogDestinationOptions, destinationRequirementMessage } from "@/api/hosting-catalog-destinations";
import { CatalogRequirements, catalogFeatureName } from "./CatalogRequirements";
import { databaseRequestID } from "@/api/hosting-databases";
import { ApiError } from "@/api/errors";
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
    return <CatalogForm key={JSON.stringify([access.epoch, projectID, environmentID, access.scope.actor])} projectID={projectID} environmentID={environmentID} actor={access.scope.actor} blocked={blocked || access.refreshing}/>;
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
    const [nameEdited, setNameEdited] = useState(false);
    const [destination, setDestination] = useState("");
    const [publicURL, setPublicURL] = useState("");
    const [template, setTemplate] = useState("");
    const [search, setSearch] = useState("");
    const [category, setCategory] = useState("");
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
    const categories = Array.from(new Map((catalog.data ?? []).flatMap(t =>
        (t.category ?? "").split(",").map(value => value.trim()).filter(Boolean)
    ).map(value => [value.toLowerCase(), value])).entries()).sort((a, b) => a[1].localeCompare(b[1], undefined, { numeric: true, sensitivity: "base" }));
    const terms = search.trim().toLowerCase().split(/\s+/).filter(Boolean);
    const matches = catalog.data?.filter(t => {
        const text = [t.name, t.id, t.immutable_version, t.definition.image_reference,
            t.definition.mount_path, t.definition.compose?.original, t.definition.compose?.public_component, ...t.definition.credential_names].join(" ").toLowerCase();
        return terms.every(term => text.includes(term)) && (!category || (t.category ?? "").split(",").some(value => value.trim().toLowerCase() === category));
    }) ?? [];
    const selected = matches.find(t => t.id === template);
    const destinations = useQuery({ queryKey: ["catalogDestinationOptions", actor, projectID, selected?.id, selected?.immutable_version, offset], enabled: !!selected && !blocked && !attempt && !created, queryFn: ({ signal }) => getCatalogDestinationOptions(projectID, selected!, offset, signal), retry: false, gcTime: 0, refetchOnWindowFocus: false, refetchOnReconnect: false });
    const unavailable = blocked || busy || catalog.isError || catalog.isPending;
    const currentOptions = selected && destinations.isSuccess && !destinations.isFetching && destinations.data.template_id === selected.id && destinations.data.immutable_version === selected.immutable_version && destinations.data.offset === offset ? destinations.data : undefined;
    const selectedDestination = currentOptions?.items.find(option => option.destination.id === destination);
    function resetDestination() { setDestination(""); setOffset(0); }

    async function submit(event: FormEvent) {
        event.preventDefault();
        if (unavailable || (!attempt && (!selected || !selectedDestination?.available)))
            return;
        if (!attempt && (!name || name.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, "") !== name || new TextEncoder().encode(name).length > 128 || /\p{Cc}/u.test(name))) {
            setError("Enter a name with 1 to 128 UTF-8 bytes, without surrounding whitespace or control characters.");
            return;
        }
        if (!attempt && selected?.definition.compose) {
            try {
                const url = new URL(publicURL);
                if (publicURL !== publicURL.trim() || publicURL.length > 512 || /[\\\p{Cc}]/u.test(publicURL) || !["http:", "https:"].includes(url.protocol) || !url.hostname || url.username || url.password || url.search || url.hash || !["", "/"].includes(url.pathname))
                    throw new Error("Invalid public URL");
            } catch {
                setError("Enter a public HTTP or HTTPS base URL without credentials, a path, a query or a fragment.");
                return;
            }
        }
        const body = attempt ?? { request_id: databaseRequestID(), project_id: projectID, project_environment_id: environmentID, destination_id: destination, name, template_id: selected!.id as CatalogInstallInput["template_id"], immutable_version: selected!.immutable_version as CatalogInstallInput["immutable_version"], ...(selected!.definition.compose ? { public_url: publicURL } : {}) };
        setAttempt(body);
        setBusy(true);
        setError("");
        controller.current = new AbortController();
        try {
            const result = await installCatalog(body, controller.current.signal);
            if (!active.current)
                return;
            if (result.request.request_id !== body.request_id || result.request.project_id !== projectID || result.request.project_environment_id !== environmentID || result.binding.destination_id !== body.destination_id || result.request.template_id !== body.template_id || result.request.immutable_version !== body.immutable_version || result.request.public_url !== body.public_url)
                throw new Error("Catalog response scope mismatch");
            const pinned = catalog.data?.find(entry => entry.id === body.template_id && entry.immutable_version === body.immutable_version);
            if (!pinned || result.template.id !== pinned.id || result.template.immutable_version !== pinned.immutable_version || result.template.original_hash !== pinned.original_hash || result.template.definition_hash !== pinned.definition_hash)
                throw new Error("Catalog response template mismatch");
            if (pinned.definition.compose && (result.resource.source.kind !== "compose" || result.resource.source.compose.original !== pinned.definition.compose.original || result.resource.source.compose.public_component !== pinned.definition.compose.public_component || result.resource.source.compose.container_port !== pinned.definition.compose.container_port || result.storage_ref.storage_id !== "" || result.storage_ref.generation !== 0 || result.storage_ref.hash !== ""))
                throw new Error("Catalog response Compose mismatch");
            setCreated(result);
            await client.invalidateQueries({ queryKey: ["hostingResources", projectID] });
        }
        catch (cause) {
            if (active.current) {
                if (cause instanceof ApiError && cause.status === 400) {
                    setAttempt(undefined);
                    setError("Installation settings were rejected. Correct the draft and submit a new request.");
                } else {
                    setError("Installation unconfirmed. Retry keeps the same request and credentials. Refresh access if permission changed.");
                }
            }
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
    <p>Choose a pinned application. Installation saves persistent storage and any template-generated credentials. Deploy it from the application deployment panel.</p>
    <p className="text-sm">Arbitrary Compose import is incomplete.</p>
 {created ? <>
        <p role="status">Installed {created.resource.name}. Open its application, review deployment settings, and deploy.</p>
        {created.resource.source.kind === "compose" ? <><p>Generated runtime secrets are saved as managed-secret references. Review the saved runtime configuration and Compose deployment settings before deploying. Open the application after deployment to complete its initial setup. The public URL does not create a route or HTTPS certificate.</p><p className="break-all">Deploy public component <code>{created.resource.source.compose.public_component}</code> on container port {created.resource.source.compose.container_port}. Configure a matching HTTPS route for {created.request.public_url} separately.</p></> : created.credentials.length > 0 ? <>
        <p>Login and password are retained managed secrets. Linkding creates the initial account only when its username is absent. Changing startup credentials does not rotate an existing account password.</p>
        <Button type="button" disabled={busy || blocked} onClick={() => void reveal()}>Reveal login credentials</Button>
        <Button type="button" variant="outline" onClick={() => setValues({})}>Hide credentials</Button>{Object.entries(values).map(([key, value]) => <p className="break-all" key={key}>{key}: <output>{value}</output>
            </p>)}</> : <p>No login credentials were generated. After deployment, open the application to complete its initial setup.</p>}</> : <form onSubmit={event => void submit(event)} className="space-y-3" aria-label="Install catalog application">
        <label className="block" htmlFor={`${id}-category`}>Catalog category</label>
        <select id={`${id}-category`} className="h-10 w-full min-w-0 rounded-xl border bg-card px-2" value={category} disabled={unavailable || !!attempt} onChange={e => { setCategory(e.target.value); setTemplate(""); resetDestination(); }}>
            <option value="">All categories</option>{categories.map(([value, label]) => <option key={value} value={value}>{label}</option>)}
        </select>
        <label className="block" htmlFor={`${id}-search`}>Search catalog</label>
        <Input id={`${id}-search`} type="search" value={search} disabled={unavailable || !!attempt}
            aria-describedby={`${id}-search-help`} onChange={e => { setSearch(e.target.value); setTemplate(""); resetDestination(); }}/>
        <p id={`${id}-search-help`} className="text-sm text-muted-foreground">Search by application, version, image, data mount or generated variable name.</p>
        {catalog.isSuccess ? <p role="status">Catalog matches: {matches.length} of {catalog.data.length}.</p> : null}
        {catalog.isSuccess && matches.length === 0 ? <p>{catalog.data.length === 0 ? "No catalog applications are available." : "No applications match your search and category. Clear the filters to see available applications."}</p> : null}
        {search ? <Button type="button" variant="outline" disabled={unavailable || !!attempt} onClick={() => { setSearch(""); setTemplate(""); resetDestination(); }}>Clear search</Button> : null}
        {category ? <Button type="button" variant="outline" disabled={unavailable || !!attempt} onClick={() => { setCategory(""); setSearch(""); setTemplate(""); resetDestination(); resetDestination(); }}>Clear filters</Button> : null}
        <label className="block" htmlFor={`${id}-template`}>Catalog application</label>
        <select id={`${id}-template`} className="h-10 w-full min-w-0 rounded-xl border bg-card px-2" disabled={unavailable || !!attempt} value={template} onChange={e => { setTemplate(e.target.value); resetDestination(); if (!nameEdited) setName(matches.find(t => t.id === e.target.value)?.name ?? ""); }}>
        <option value="">Select an application</option>{matches.map(t => <option key={t.id} value={t.id}>{t.name} · {t.immutable_version}</option>)}</select>
        {selected ? <section aria-label="Catalog installation preview" className="min-w-0 space-y-2 rounded-lg border p-3 text-sm">
            <h5 className="font-medium">{selected.name} installation preview</h5>
            <dl className="space-y-1 break-all">
                <dt>Category</dt><dd>{selected.category || "Not declared"}</dd>
                <dt>Upstream architecture restriction</dt><dd>{selected.arm_only && selected.amd_only ? "ARM only and AMD only are both declared. Review the upstream template before choosing a destination." : selected.arm_only ? "ARM only" : selected.amd_only ? "AMD only" : "Not declared"}</dd>
                <dt>Template version</dt><dd>{selected.immutable_version}</dd>
                {selected.definition.compose ? <>
                    <dt>Public component</dt><dd>{selected.definition.compose.public_component}</dd>
                    <dt>Compose source</dt><dd><pre className="max-h-96 max-w-full overflow-auto whitespace-pre-wrap break-all rounded-lg border p-2">{selected.definition.compose.original}</pre></dd>
                </> : <>
                    <dt>Image</dt><dd>{selected.definition.image_reference}</dd>
                    <dt>Persistent data</dt><dd>{selected.definition.mount_path}</dd>
                </>}
                <dt>Generated variables</dt><dd>{selected.definition.credential_names.join(", ") || "None"}</dd>
                {Object.keys(selected.definition.runtime_environment ?? {}).length > 0 ? <>
                    <dt>Runtime environment defaults</dt><dd>{Object.entries(selected.definition.runtime_environment ?? {}).map(([key, value]) => <p key={key}>{key}={value}</p>)}</dd>
                </> : null}
                <dt>Container port</dt><dd>{selected.definition.container_port}</dd>
                <dt>Readiness check</dt><dd>{selected.definition.readiness.path}, timeout {selected.definition.readiness.timeout_seconds} seconds</dd>
                <dt>Template source</dt><dd>{selected.upstream_repository}, {selected.upstream_path} at {selected.upstream_commit}</dd>
            </dl>
            <p>{selected.definition.compose ? "Runtime secrets are generated once during installation and saved as managed-secret references. Complete initial setup in the application after deployment." : selected.definition.credential_names.length > 0
                ? "Credentials are generated once during installation and stored as managed secrets."
                : "No login credentials are generated. After deployment, open the application to complete its initial setup."} Configure a domain and HTTPS route after deployment.</p>
            {currentOptions ? <CatalogRequirements requirements={currentOptions.requirements} /> : <p>Choose a destination after its requirements have loaded. Hardware requirements and compatibility have not been verified.</p>}
        </section> : null}
        {selected?.definition.compose ? <>
            <label className="block" htmlFor={`${id}-public-url`}>Public URL</label>
            <Input id={`${id}-public-url`} type="url" required maxLength={512} value={publicURL} disabled={unavailable || !!attempt} onChange={e => setPublicURL(e.target.value)} aria-describedby={`${id}-public-url-help`} placeholder="https://app.example.com" />
            <p id={`${id}-public-url-help`} className="text-sm text-muted-foreground">Saved as the application's public URL. Configure a matching domain and HTTPS route separately. Installation does not publish the application.</p>
        </> : null}
        <label className="block" htmlFor={`${id}-name`}>Installation name</label>
        <Input id={`${id}-name`} required value={name} disabled={unavailable || !!attempt} onChange={e => { setName(e.target.value); setNameEdited(true); }}/>
        <label className="block" htmlFor={`${id}-destination`}>Catalog destination</label>
        <select id={`${id}-destination`} className="h-10 w-full min-w-0 rounded-xl border bg-card px-2" required value={destination} disabled={unavailable || !currentOptions || !!attempt} onChange={e => setDestination(e.target.value)}>
        <option value="">Select a destination</option>{currentOptions?.items.map(option => <option key={option.destination.id} value={option.destination.id} disabled={!option.available}>{option.destination.name}{option.available ? "" : ` · ${destinationRequirementMessage(option.reason_code)}`}</option>)}</select>
        {selected && destinations.isFetching ? <p role="status">Checking application requirements and destinations...</p> : null}
        {currentOptions && currentOptions.items.length === 0 ? <p>No destinations are granted to this project on this page. Ask an administrator to grant a Docker destination.</p> : null}
        {currentOptions && currentOptions.items.length > 0 && currentOptions.items.every(option => !option.available) ? <p>No destination on this page can install this application. Review the reasons below or try another page.</p> : null}
        {currentOptions ? <ul aria-label="Catalog destination requirements" className="space-y-2 break-words text-sm">{currentOptions.items.map(option => <li key={option.destination.id}>
            {option.destination.name} on {option.server_name}: {option.available ? "Ready to install. Runtime and required features are available; hardware compatibility is unverified." : destinationRequirementMessage(option.reason_code)}
            {!option.available && option.missing_capabilities.length ? <p>Missing features: {option.missing_capabilities.map(catalogFeatureName).join(", ")}</p> : null}
        </li>)}</ul> : null}
        {selected ? <Button type="button" variant="outline" disabled={unavailable || destinations.isFetching || !!attempt} onClick={() => { setDestination(""); void destinations.refetch(); }}>Refresh destination compatibility</Button> : null}
        <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" disabled={unavailable || !currentOptions || !!attempt || offset === 0} onClick={() => { setDestination(""); setOffset(Math.max(0, offset - 20)); }}>Previous destinations</Button>
        <Button type="button" variant="outline" disabled={unavailable || !currentOptions || !!attempt || offset + 20 >= (currentOptions?.total ?? 0)} onClick={() => { setDestination(""); setOffset(offset + 20); }}>Next destinations</Button>
        </div><Button disabled={unavailable || (!attempt && (!selected || !selectedDestination?.available))} type="submit">{busy ? "Installing..." : attempt ? "Retry same installation" : "Install application"}</Button>{attempt ? <p className="break-all text-sm">Retained request: {attempt.request_id}</p> : null}</form>}
 {error ? <p role="alert">{error}</p> : null}{catalog.isError || destinations.isError ? <>
    <p role="alert">Catalog or destination access is unavailable.</p>
    <Button type="button" variant="outline" disabled={blocked || busy || catalog.isFetching || destinations.isFetching} onClick={() => {
        if (catalog.isError) void catalog.refetch();
        if (selected && destinations.isError) void destinations.refetch();
    }}>Retry catalog loading</Button>
 </> : null}
 </section>;
}
