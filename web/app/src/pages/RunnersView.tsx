import type { CreatedRunnerEnrollment, Runner, RunnerDetail } from "@/api";
import { HelpDetails } from "@/components/common/HelpDetails";
import { EmptyState } from "@/components/common/EmptyState";
import { StatusBadge } from "@/components/common/StatusBadge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { formatDate, matchesQuery } from "@/lib/format";
import {
  configuredImageHostingCapabilities,
  imageHostingCapabilities,
  readinessImageHostingCapabilities,
  runnerEnrollmentCommand,
} from "@/lib/runner-enrollment-command";
import { parseRunnerTags } from "@/lib/runnerTags";
import { Link } from "@tanstack/react-router";
import { Copy, Download, KeyRound, ShieldOff } from "lucide-react";
import {
  type FormEvent,
  type ReactNode,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";

type CopyStatus = "idle" | "copying" | "copied" | "failed";

function splitValues(value: string): string[] {
  return value
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
}
function runnerState(runner: Runner): string {
  if (runner.status === "revoked")
    return "Revoked. This runner can no longer authenticate.";
  const age = Date.now() - new Date(runner.last_heartbeat_at).getTime();
  if (!Number.isFinite(age))
    return "Heartbeat unavailable. Connectivity is unknown.";
  if (age > 2 * 60_000)
    return "Heartbeat overdue. Check the runner connection.";
  return "Recent heartbeat.";
}

export function RunnersView(props: {
  runners: Runner[];
  q?: string;
  showEnrollment?: boolean;
  canAdmin: boolean;
  controlsDisabled?: boolean;
  onEnroll: (input: {
    runner_id: string;
    runner_name: string;
    tags: string[];
    capabilities: string[];
    ttl_seconds: number;
  }) => Promise<CreatedRunnerEnrollment>;
}) {
  return <RunnerInventory key={String(props.canAdmin)} {...props} />;
}

function RunnerInventory({
  runners,
  q,
  canAdmin,
  controlsDisabled = false,
  showEnrollment = true,
  onEnroll,
}: {
  runners: Runner[];
  q?: string;
  showEnrollment?: boolean;
  canAdmin: boolean;
  controlsDisabled?: boolean;
  onEnroll: (input: {
    runner_id: string;
    runner_name: string;
    tags: string[];
    capabilities: string[];
    ttl_seconds: number;
  }) => Promise<CreatedRunnerEnrollment>;
}): ReactNode {
  const [enrollment, setEnrollment] = useState<CreatedRunnerEnrollment>();
  const [copyStatus, setCopyStatus] = useState<CopyStatus>("idle");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const enrolling = useRef(false);
  const secretRef = useRef<string | undefined>(undefined);
  const active = useRef(true);
  const request = useRef(0);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      request.current++;
      secretRef.current = undefined;
    };
  }, []);
  useEffect(() => {
    if (!enrollment) return;
    const remaining = Date.parse(enrollment.enrollment.expires_at) - Date.now();
    if (!Number.isFinite(remaining) || remaining <= 0) {
      expireEnrollment();
      return;
    }
    const timer = window.setTimeout(expireEnrollment, remaining);
    return () => window.clearTimeout(timer);
  }, [enrollment]);
  const visible = runners.filter((runner) =>
    matchesQuery(
      q,
      runner.id,
      runner.name,
      runner.status,
      ...(runner.tags ?? []),
      ...(runner.capabilities ?? []),
    ),
  );
  async function enroll(input: {
    runner_id: string;
    runner_name: string;
    tags: string[];
    capabilities: string[];
    ttl_seconds: number;
  }) {
    if (enrolling.current || !canAdmin || controlsDisabled) return;
    enrolling.current = true;
    const current = ++request.current;
    setBusy(true);
    setError("");
    setCopyStatus("idle");
    try {
      const created = await onEnroll(input);
      if (!active.current || request.current !== current || !canAdmin) return;
      if (
        !Number.isFinite(Date.parse(created.enrollment.expires_at)) ||
        Date.parse(created.enrollment.expires_at) <= Date.now()
      ) {
        expireEnrollment();
        return;
      }
      secretRef.current = created.token;
      setEnrollment(created);
    } catch {
      if (active.current && request.current === current)
        setError(
          "Enrollment could not be created. Verify the identity and try again.",
        );
    } finally {
      enrolling.current = false;
      if (active.current) setBusy(false);
    }
  }
  function clearEnrollment(open: boolean) {
    if (!open) {
      request.current++;
      secretRef.current = undefined;
      setEnrollment(undefined);
      setCopyStatus("idle");
    }
  }
  function expireEnrollment() {
    clearEnrollment(false);
    setError("Enrollment expired. Create a new enrollment before continuing.");
  }
  function currentSecret() {
    if (
      !active.current ||
      !canAdmin ||
      !enrollment ||
      Date.parse(enrollment.enrollment.expires_at) <= Date.now()
    ) {
      expireEnrollment();
      return undefined;
    }
    return secretRef.current;
  }
  async function copySecret() {
    const secret = currentSecret();
    if (!secret || copyStatus === "copying") return;
    setCopyStatus("copying");
    try {
      await navigator.clipboard.writeText(secret);
      if (secretRef.current === secret) setCopyStatus("copied");
    } catch {
      if (secretRef.current === secret) setCopyStatus("failed");
    }
  }
  function downloadSecret() {
    const secret = currentSecret();
    if (!secret) return;
    const blob = new Blob([secret + "\n"], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = "nerocd-runner-enrollment.token";
    anchor.click();
    URL.revokeObjectURL(url);
  }
  return (
    <section
      className={`grid min-w-0 grid-cols-1 gap-4 ${canAdmin && showEnrollment ? "xl:grid-cols-[minmax(0,1fr)_360px]" : ""}`}
    >
      <Card className="min-w-0">
        <CardHeader>
          <CardTitle>Runner inventory</CardTitle>
        </CardHeader>
        <CardContent className="grid min-w-0 grid-cols-1 gap-3">
          {!visible.length ? (
            <EmptyState
              title={
                runners.length
                  ? "No runners match this filter"
                  : "No runners on this page"
              }
              description={
                runners.length
                  ? "Change or clear the page filter."
                  : canAdmin
                    ? "Choose a setup method to connect a host."
                    : "Ask a runner administrator to connect a host."
              }
              icon={KeyRound}
            />
          ) : (
            visible.map((runner) => (
              <Link
                key={runner.id}
                to="/runners/$runnerId"
                params={{ runnerId: runner.id }}
                search={(previous) => previous}
                className="min-w-0 rounded border p-3 hover:bg-muted"
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="min-w-0 break-words font-medium">
                    {runner.name}
                  </span>
                  <StatusBadge className="shrink-0" status={runner.status} />
                </div>
                <p className="mt-1 break-all font-mono text-xs text-muted-foreground">
                  {runner.id}
                </p>
                <p className="mt-2 text-sm text-muted-foreground">
                  {runnerState(runner)}
                </p>
                <p className="mt-2 break-words text-xs text-muted-foreground">
                  Heartbeat {formatDate(runner.last_heartbeat_at)} ·{" "}
                  {(runner.tags ?? []).join(", ") || "no tags"}
                </p>
              </Link>
            ))
          )}
        </CardContent>
      </Card>
      {canAdmin ? (
        <div hidden={!showEnrollment}>
          <EnrollmentForm
            busy={busy}
            disabled={controlsDisabled}
            error={error}
            onSubmit={enroll}
          />
        </div>
      ) : (
        <Card>
          <CardContent className="p-6" role="status">
            Runner operations require runner administrator permissions.
          </CardContent>
        </Card>
      )}
      <EnrollmentSecret
        copyStatus={copyStatus}
        enrollment={canAdmin ? enrollment : undefined}
        token={canAdmin ? secretRef.current : undefined}
        onOpenChange={clearEnrollment}
        onCopy={copySecret}
        onDownload={downloadSecret}
      />
    </section>
  );
}

function EnrollmentForm({
  busy,
  disabled,
  error,
  onSubmit,
}: {
  busy: boolean;
  disabled: boolean;
  error: string;
  onSubmit: (input: {
    runner_id: string;
    runner_name: string;
    tags: string[];
    capabilities: string[];
    ttl_seconds: number;
  }) => void;
}) {
  const [id, setID] = useState("");
  const [name, setName] = useState("");
  const [tags, setTags] = useState("compose-runtime");
  const [tagsError, setTagsError] = useState("");
  const tagsID = useId();
  const tagsRef = useRef<HTMLInputElement>(null);
  const [capabilities, setCapabilities] = useState("compose-deploy");
  const [ttl, setTTL] = useState("600");
  const locked = busy || disabled;
  const [profile, setProfile] = useState("compose");
  function submit(event: FormEvent) {
    event.preventDefault();
    if (locked) return;
    let parsedTags: string[];
    try {
      parsedTags = parseRunnerTags(tags);
      setTagsError("");
    } catch (cause) {
      setTagsError((cause as Error).message);
      tagsRef.current?.focus();
      return;
    }
    onSubmit({
      runner_id: id,
      runner_name: name,
      tags: parsedTags,
      capabilities: splitValues(capabilities),
      ttl_seconds: Number(ttl),
    });
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle>Manual runner setup</CardTitle>
      </CardHeader>
      <CardContent>
        <form
          className="grid gap-3"
          aria-label="Create runner enrollment"
          aria-busy={busy}
          onSubmit={submit}
        >
          <label className="grid gap-1 text-sm">
            Runner ID
            <Input
              disabled={locked}
              aria-label="Runner ID"
              value={id}
              onChange={(event) => setID(event.target.value)}
              placeholder="runner_edge_01"
              required
            />
          </label>
          <label className="grid gap-1 text-sm">
            Runner name
            <Input
              disabled={locked}
              aria-label="Runner name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              required
            />
          </label>
          <div className="grid gap-1">
            <label className="grid gap-1 text-sm">
              Tags
              <Input
                ref={tagsRef}
                disabled={locked}
                aria-label="Runner tags"
                value={tags}
                aria-invalid={Boolean(tagsError)}
                aria-describedby={`${tagsID}-help${tagsError ? ` ${tagsID}-error` : ""}`}
                onChange={(event) => {
                  setTags(event.target.value);
                  setTagsError("");
                }}
              />
            </label>
            <p id={`${tagsID}-help`} className="text-xs text-muted-foreground">
              Separate tags with commas, or use a JSON array for tags containing
              commas.
            </p>
            {tagsError ? (
              <p
                id={`${tagsID}-error`}
                role="alert"
                className="text-sm text-destructive"
              >
                {tagsError}
              </p>
            ) : null}
          </div>
          <label className="grid gap-1 text-sm">
            Runner profile
            <Select
              aria-label="Runner profile"
              value={profile}
              disabled={locked}
              onChange={(event) => {
                setProfile(event.target.value);
                if (event.target.value !== "custom")
                  setCapabilities(
                    (
                      {
                        compose: ["compose-deploy"],
                        image: imageHostingCapabilities,
                        configured: configuredImageHostingCapabilities,
                        readiness: readinessImageHostingCapabilities,
                        custom: [],
                      }[event.target.value] ?? []
                    ).join(","),
                  );
              }}
            >
              <option value="compose">Compose tasks</option>
              <option value="image">Image hosting</option>
              <option value="configured">
                Image hosting with variables and logs
              </option>
              <option value="readiness">
                Image hosting with readiness checks
              </option>
              <option value="custom">Custom capabilities</option>
            </Select>
          </label>
          <p className="text-xs text-muted-foreground">
            {profile === "readiness"
              ? "Requires HTTPS, Linux, matching native Docker architecture, Docker API 1.54, and 1 GiB free in each work and journal root. Readiness checks report prerequisites, not deployment success."
              : profile === "configured"
                ? "Requires HTTPS and a dedicated Linux runner. Includes runtime variables, observations and requested logs."
                : profile === "image"
                  ? "Deploy container images with a dedicated Compose runner."
                  : profile === "compose"
                    ? "Run Compose tasks on this host. Choose an image-hosting profile for application deployment."
                    : "Enter the capabilities your runner is configured to accept."}
          </p>
          {profile === "custom" && (
            <label className="grid gap-1 text-sm">
              Capabilities
              <Input
                disabled={locked}
                aria-label="Runner capabilities"
                value={capabilities}
                onChange={(event) => setCapabilities(event.target.value)}
                required
              />
            </label>
          )}
          <HelpDetails title="Capability details">
            <p className="break-all">
              Selected capabilities: {capabilities || "None"}.
            </p>
            <p>
              Profiles replace this enrollment draft only. Existing grants are
              unchanged. Image-hosting profiles cannot include shell execution.
            </p>
          </HelpDetails>
          <label className="grid gap-1 text-sm">
            Enrollment lifetime
            <Select
              disabled={locked}
              aria-label="Enrollment lifetime"
              value={ttl}
              onChange={(event) => setTTL(event.target.value)}
            >
              <option value="300">5 minutes</option>
              <option value="600">10 minutes</option>
              <option value="1800">30 minutes</option>
              <option value="3600">1 hour</option>
            </Select>
          </label>
          {error ? (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          ) : null}
          <Button type="submit" disabled={locked}>
            {busy ? "Creating…" : "Create enrollment"}
          </Button>
          <p className="text-xs text-muted-foreground">
            Save the one-time secret in the next step. It cannot be retrieved
            later.
          </p>
        </form>
      </CardContent>
    </Card>
  );
}

function EnrollmentSecret({
  enrollment,
  token,
  copyStatus,
  onOpenChange,
  onCopy,
  onDownload,
}: {
  copyStatus: CopyStatus;
  enrollment?: CreatedRunnerEnrollment;
  token?: string;
  onOpenChange: (open: boolean) => void;
  onCopy: () => void;
  onDownload: () => void;
}) {
  const id = useId();
  const [serverURL, setServerURL] = useState(() =>
    typeof window === "undefined" ? "" : window.location.origin,
  );
  const { command, issues } = runnerEnrollmentCommand(
    serverURL,
    enrollment?.enrollment.capabilities,
  );
  return (
    <Dialog open={Boolean(enrollment && token)} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] min-w-0 grid-cols-1 overflow-y-auto">
        <DialogHeader className="min-w-0 pr-6">
          <DialogTitle>One-time runner enrollment secret</DialogTitle>
          <DialogDescription>
            Save it now: it will disappear when this dialog closes or expires
            and cannot be retrieved again.
          </DialogDescription>
        </DialogHeader>
        <h3 className="font-medium">1. Save the enrollment secret</h3>
        <code
          className="break-all rounded border p-3 text-xs"
          aria-label="Enrollment secret"
        >
          {token}
        </code>
        <p className="min-w-0 break-words text-xs text-muted-foreground">
          Runner ID: {enrollment?.enrollment.runner_id}. Expires{" "}
          {enrollment ? formatDate(enrollment.enrollment.expires_at) : ""}.
        </p>
        {copyStatus === "failed" ? (
          <p role="alert" className="text-sm text-destructive">
            Unable to copy. Download the secret instead, or try again.
          </p>
        ) : copyStatus === "copied" ? (
          <p role="status" className="text-sm text-muted-foreground">
            Copied to clipboard.
          </p>
        ) : null}
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            disabled={copyStatus === "copying"}
            onClick={onCopy}
          >
            <Copy className="mr-1 h-4 w-4" />
            {copyStatus === "copying" ? "Copying..." : "Copy"}
          </Button>
          <Button onClick={onDownload}>
            <Download className="mr-1 h-4 w-4" />
            Download once
          </Button>
        </div>
        <h3 className="font-medium">2. Prepare the host</h3>
        <p className="text-sm">
          Install NeroCD, Docker Engine and Docker Compose on the host.
        </p>
        <label htmlFor={`${id}-origin`}>Runner-reachable server URL</label>
        <Input
          id={`${id}-origin`}
          value={serverURL}
          onChange={(event) => setServerURL(event.target.value)}
        />
        <p className="text-xs text-muted-foreground">
          Enter the URL this host can reach. Use HTTPS in production; HTTP
          commands enable development mode.
        </p>
        <HelpDetails title="Enrolled capabilities">
          <p className="break-all">
            {(enrollment?.enrollment.capabilities ?? []).join(", ") || "None"}.
            These saved grants determine the command.
          </p>
        </HelpDetails>
        <h3 className="font-medium">3. Run the enrollment command</h3>
        {command ? (
          <pre
            aria-label="Manual runner command"
            className="min-w-0 overflow-auto whitespace-pre-wrap break-all rounded border p-3 text-xs"
          >
            {command}
          </pre>
        ) : (
          <div role="alert" className="min-w-0 break-words text-sm">
            <p>
              Manual command unavailable. Resolve these requirements before
              starting the runner.
            </p>
            <ul className="list-disc space-y-2 pl-5">
              {issues.map((issue) => (
                <li key={issue}>{issue}</li>
              ))}
            </ul>
            <p>
              See the repository guides for{" "}
              <a
                className="underline"
                href="https://github.com/edgefloor/NeroCD/blob/main/docs/hosting-build-runtime.md"
              >
                build configuration
              </a>
              ,{" "}
              <a
                className="underline"
                href="https://github.com/edgefloor/NeroCD/blob/main/docs/hosting-proxy-control.md"
              >
                managed proxy configuration
              </a>
              , and{" "}
              <a
                className="underline"
                href="https://github.com/edgefloor/NeroCD/blob/main/docs/hosting-local-build-runtime.md"
              >
                local build deployment
              </a>
              .
            </p>
          </div>
        )}
        <p className="text-xs text-muted-foreground">
          Save the token at the command's enrollment-file path with mode 0600.
          Keep the permanent credential private and persist the dedicated work
          and journal directories with mode 0700. After enrollment, register the
          runner as a hosting server.
        </p>
      </DialogContent>
    </Dialog>
  );
}

export function RunnerDetailView({
  runner,
  canAdmin,
  controlsDisabled = false,
  onRevoke,
}: {
  runner?: RunnerDetail;
  canAdmin: boolean;
  controlsDisabled?: boolean;
  onRevoke: () => Promise<void>;
}): ReactNode {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [revokeError, setRevokeError] = useState("");
  const revoking = useRef(false);
  const revoked = runner?.status === "revoked";
  const [observedRevoked, setObservedRevoked] = useState(revoked);
  if (observedRevoked !== revoked) {
    setObservedRevoked(revoked);
    setConfirming(false);
    setRevokeError("");
  }
  async function revoke() {
    if (
      revoking.current ||
      !canAdmin ||
      controlsDisabled ||
      !runner ||
      runner.status === "revoked"
    )
      return;
    revoking.current = true;
    setBusy(true);
    setRevokeError("");
    try {
      await onRevoke();
      setConfirming(false);
    } catch {
      setRevokeError("Unable to revoke credential. Try again.");
    } finally {
      revoking.current = false;
      setBusy(false);
    }
  }
  if (!runner)
    return (
      <Card>
        <CardContent className="p-6" role="alert">
          Runner is unavailable.
        </CardContent>
      </Card>
    );
  return (
    <section className="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_320px]">
      <Card className="min-w-0">
        <CardHeader className="min-w-0 grid-cols-1">
          <CardTitle className="min-w-0 break-words">{runner.name}</CardTitle>
        </CardHeader>
        <CardContent className="grid min-w-0 grid-cols-1 gap-3">
          <StatusBadge status={runner.status} />
          <p className="break-all font-mono text-xs">{runner.id}</p>
          <p>{runnerState(runner)}</p>
          <dl className="grid min-w-0 grid-cols-1 gap-2 break-words text-sm">
            <div>
              <dt className="text-muted-foreground">Registered</dt>
              <dd>{formatDate(runner.registered_at)}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Last heartbeat</dt>
              <dd>{formatDate(runner.last_heartbeat_at)}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Tags</dt>
              <dd>{(runner.tags ?? []).join(", ") || "None"}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Capabilities</dt>
              <dd>{(runner.capabilities ?? []).join(", ") || "None"}</dd>
            </div>
            <div>
              <dt className="text-muted-foreground">Telemetry</dt>
              <dd>
                {runner.telemetry ? (
                  <>
                    Observed{" "}
                    <time dateTime={runner.telemetry.observed_at}>
                      {formatDate(runner.telemetry.observed_at)}
                    </time>{" "}
                    · journal {runner.telemetry.journal_depth} · retries{" "}
                    {runner.telemetry.retry_count} · renewal failures{" "}
                    {runner.telemetry.renew_failures}
                  </>
                ) : (
                  "No authenticated telemetry has been received yet."
                )}
              </dd>
            </div>
          </dl>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>Credential</CardTitle>
        </CardHeader>
        <CardContent>
          {canAdmin && runner.status !== "revoked" ? (
            <Button
              variant="destructive"
              disabled={controlsDisabled}
              onClick={() => {
                setRevokeError("");
                setConfirming(true);
              }}
            >
              <ShieldOff className="mr-1 h-4 w-4" />
              Revoke credential
            </Button>
          ) : (
            <p className="text-sm text-muted-foreground">
              {runner.status === "revoked"
                ? "Credential is revoked."
                : "Administrative role required."}
            </p>
          )}
        </CardContent>
      </Card>
      <Dialog
        open={confirming}
        onOpenChange={(open) => {
          if (!revoking.current) setConfirming(open);
        }}
      >
        <DialogContent
          showCloseButton={!busy}
          aria-busy={busy}
          className="max-h-[calc(100dvh-2rem)] overflow-y-auto"
        >
          <DialogHeader>
            <DialogTitle>Revoke runner credential?</DialogTitle>
            <DialogDescription>
              The runner will fail its next authenticated operation and must be
              re-enrolled.
            </DialogDescription>
          </DialogHeader>
          {revokeError ? (
            <p role="alert" className="text-sm text-destructive">
              {revokeError}
            </p>
          ) : null}
          <DialogFooter>
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setConfirming(false)}
            >
              Keep credential
            </Button>
            <Button
              variant="destructive"
              disabled={busy || !canAdmin || controlsDisabled}
              onClick={() => void revoke()}
            >
              {busy ? "Revoking…" : "Revoke credential"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}
