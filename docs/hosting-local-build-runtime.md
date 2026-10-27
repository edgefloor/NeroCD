# Same-host deployment of build artifacts

The Go agent can deploy a successful server-selected local build artifact through the fenced hosting operation lifecycle. Enable this separately from build execution with `nerocd runner --local-build-deploy`. It is disabled by default. A runner that builds and deploys needs both this flag and its validated `--build-config-file`; managed proxy execution also requires its existing explicit proxy settings.

The flag advertises `local_build_deploy_v1` when registering a runner. An enrolled runner's persisted capability must exactly match the flag before heartbeat, claim or local journal replay. The client preserves its older identity methods as wrappers that require local deployment to be disabled. Shell execution cannot share a configured local deployment runner. Build, local deployment and proxy capabilities can coexist with their respective explicit configurations; opaque unrelated capabilities retain their existing meaning.

The operator's image Docker endpoint must identify the same local daemon recorded in the admitted artifact. The agent does not infer another daemon, architecture or artifact from a tag. The build engine's socket and the image execution endpoint remain separately configured; a mismatch fails closed. Local deployment does not transfer an image between hosts.

For deploy, observation and retained cleanup, canonical internal task construction is validated before generic process handling. Local tasks cannot use generic completion flags or execute a supplied process, checkout, workflow, secret or artifact plan. The same run/lease/attempt/fence authority supervises requests, renewal, cancellation and external effects. The hosting controller settles its task and lease through the existing hosting completion endpoint; the agent sends no second generic completion.

Preparation derives the release from the exact admitted artifact and checks its daemon, ImageID and platform. The preparation acknowledgment must preserve the source, artifact, placement and release and contain a valid recompiled plan. Only the existing server-side resolution of an initially unallocated port is allowed. A fixed port cannot change. The agent inspects and creates the workload using the exact ImageID, with `--pull=never`. Missing or mismatched images fail before workload mutation. There is no pull, rebuild, import, tag lookup or fabricated RepoDigest fallback. Registry image deployments retain their existing digest acquisition behavior.

Local artifact evidence establishes journal format floor **6**. The floor applies to preparation, apply intent, result-only markers, cleanup targets, previous healthy releases and retained releases, including local evidence nested beneath a registry release. Journal formats below 6 cannot contain local evidence. Other record families can require a later format. The result-only marker identifies the required format/capability; it grants no authority. Replay still uses the full attempt tuple, exact operation, retained body and server receipt. Existing versions 1–5 and registry JSON/hash bytes remain compatible when local fields are absent. Mixed build/proxy/image appends never lower the floor.

Unresolved effects, lease loss, cancellation and restored ownership holds retain their journal evidence. Startup reconciliation precedes normal runner traffic. Missing local configuration blocks local replay. A retained local workload can be cleaned up after its image cache disappears, using the exact container ID, daemon, labels, image identity and port proof plus current server route guards. Cleanup never deletes images. Route-protected releases cannot be removed, and a healthy new release does not silently switch an existing route.

`acceptance/runtime-hosting-local-build/gate.sh` uses an owned disposable Linux Docker daemon, real PostgreSQL/River and production HTTP APIs. Its default mode compiles the normal CLI for public HTTPS Git builds and local deployments, verifies ordinary HTTP, then uses the production proxy runtime with the existing test-only Pebble trust factory for managed Traefik HTTPS. The fixture does not establish public-CA issuance or test the entire proxy CLI. It uses the existing TCP/database readiness and verified PostgreSQL cache helpers, publishes no outer host ports in its default mode, and verifies owned cleanup. Its opt-in browser mode publishes only the disposable authenticated API and UI on an ephemeral host loopback port. See [the resource UI and browser check](hosting-build-deployments.md#deploy-through-the-resource-ui).

The resource UI can deploy a successful build artifact and select its settled
healthy release for a managed proxy route. See the [UI workflow](hosting-build-deployments.md#deploy-through-the-resource-ui).
Private HTTPS Basic/PAT checkout requires [explicit configuration](hosting-build-runtime.md#enable-private-https-git).
[Automatic deployment](hosting-auto-deploy.md) requires a reviewed baseline.
[Runtime logs](hosting-runtime-logs.md) and typed build output use separate viewers.
Images remain on the build daemon; cross-host transfer is unsupported. Rootful
BuildKit, delegated cgroups, and monitored disk limits require a trusted host.

## Private runtime and browser qualification

Run `NEROCD_PRIVATE_GIT_RUNTIME=1 bash acceptance/runtime-hosting-local-build/gate.sh`
from the repository root to exercise the public regression plus private TLS Git
Dockerfile and Railpack through the compiled CLI, exact local artifact deployment
and managed HTTPS with the fixture CA. The private server rejects anonymous and
wrong authentication. The fixture checks credential-response-loss retry, fresh
registration capability composition, secret absence and transport cleanup.

After building the frontend, use
`NEROCD_PRIVATE_GIT_BROWSER=1 bash acceptance/runtime-hosting-local-build/gate.sh`
for the dedicated [private browser test](../web/app/tests/browser/private-git-build-deployments.spec.ts).
Its fixture seeds a secret reference and resource, not a source grant or successful
build. The UI creates the grant and drives the Dockerfile journey. Metadata
contains references only. This opt-in mode publishes the disposable authenticated
UI/API on host loopback; production must use the protected controller transport
above. Public browser mode remains `NEROCD_LOCAL_BUILD_BROWSER=1`.

For the Railpack browser variant, also set `NEROCD_PRIVATE_GIT_STRATEGY=railpack`.
The [fixture guide](../acceptance/runtime-hosting-local-build/README.md) describes
prepared-static and compiled Vite cases. HTTPS probes use a local test CA and
isolated DNS. Production ACME is outside this fixture.
