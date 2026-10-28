# Git application configuration

Project hosting saves public and private HTTPS Git applications alongside image
applications. Choose a shared environment and **Git repository**. In **Git source
access**, choose **Public HTTPS** or **Private HTTPS with a source grant**, then
enter the URL, branch/tag/full commit and build settings. A maintainer or owner
can save configuration. A viewer can read it. Review current settings when the
saved generation changes; editing retains your draft for an explicit decision.

Saving configuration does not fetch Git, build an image or deploy an application.
**Public Git builds** and **Private HTTPS Git builds** provide separate requests
and retained history when the operator enables admission. Build detail offers
opt-in [typed build output](hosting-build-logs.md#resource-build-output-viewer)
and [deployment of a selected local artifact](hosting-build-deployments.md#deploy-through-the-resource-ui).
Build success is distinct from deployment, readiness and route selection.
Git applications cannot call the image deployment endpoint. Source-kind
conversion between image and Git remains unsupported.

## Configure Dockerfile build variables

In the Git source form, use **Dockerfile build variables** and **Add build variable**
to save explicit public values. `settings.build_variables` accepts up to 64 unique
names, with literal UTF-8 values of at most 4096 bytes each. Empty and multiline
values are supported; NUL is rejected. These values are not secrets and do not
implicitly import host or runtime environment variables.

**Inject build arguments into Dockerfile** is enabled by default. With configured
variables, the runner adds their `ARG` declarations after each `FROM` in its
prepared Dockerfile copy. It does not edit the repository. To declare `ARG`
yourself, clear the checkbox. The API setting
`settings.inject_build_args_to_dockerfile` accepts `false` to disable injection;
absent, `null`, or `true` enables it when variables exist. Both modes pass the
configured values as build arguments.

Editing variables advances the saved source generation. Request a new build
and, for private HTTPS sources, create a grant for that generation. Existing
builds and grants remain tied to their original source. Nonempty Dockerfile
variables require the runner capability `hosting_dockerfile_args_v1` in addition
to the existing build and source-access capabilities.

## Select a private build-base registry

For Dockerfile or Git Compose builds, create a project-managed registry secret
using the [registry credential payload](hosting-image-deployments.md#pull-a-private-image). In the Git source form,
set **Build registry credential** to **Select build registry credential**, then select
**Build registry credential secret** and **Build registry credential version**.
These controls read metadata, not secret values. Save the exact reference before
requesting a build. The API field is
`settings.build_registry_credential: {"secret_id": "secret_build", "version": 1}`.

The build credential is independent of the Git source grant and deployment
**Image access** selection. It authorizes one exact registry host for private
`FROM` images. Other registries remain anonymous. Rotating a secret does not
retarget saved settings or existing builds. Select the new explicit version,
save the source, and request a new build. Private HTTPS Git also needs a fresh
source grant for the changed generation. Railpack does not accept this setting.
The runner requires `hosting_build_registry_v1`; private CA configuration is
[operator controlled](hosting-build-runtime.md#trust-a-private-build-registry-ca).

### Private build-registry qualification

At `0644c49d` on 2026-10-28, the stateful Git Compose workflow built from a TLS
private base with version 1, rotated the fixture's accepted password, then rebuilt
and redeployed with distinct version 2. Denied base authentication preserved the
prior HTTPS application, PostgreSQL records and uploads. Route recovery and
uncached Stop/Start persistence also passed. Ordinary Dockerfile support has an
actual production-engine private-FROM probe and focused controller, CLI and UI
coverage; its full browser workflow was not rerun for this slice.

The [evidence index](/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-build-registry-20261028/0644c49dad62b7494a6bf6f917f1af53a9873301/evidence-index.json) records both failed fixture attempts, deterministic repairs, final cleanup,
scoped checks and review attribution. Server registry inventory, login/logout,
push, multiple selected build registries, build secrets and cross-daemon artifact
transfer remain open.

## Request and review a public Git build

1. Save a desired destination with an active project grant. Its active enrolled
   runner needs `hosting_build_v1` and must not have shell capability. The server
   also requires PostgreSQL/River and an explicit trusted build policy; see
   [hosting build control](hosting-build-control.md). The UI cannot enable policy.
2. In the Git application's **Public Git builds** section, review the exact
   repository, requested ref, resource generation, and source generation.
3. Acknowledge that source and select **Request public Git build**. Maintainers
   and owners can request and cancel; viewers can read history and detail.
4. Review the operation's state and recorded source receipt/artifact metadata.
   `queued`, `sourcing`, and `building` describe build work. `cleanup_required`
   does not prove cleanup completed. A succeeded artifact is not a running
   deployment. Select **Show build output** to read persisted typed build output.
   For running applications, use the separate [runtime log viewer](hosting-runtime-log-viewer.md).

An unconfirmed request preserves the same generations and idempotency key for an
explicit identical retry. Changed source or placement can make that replay
conflict; it must not silently build the new source. Refresh and explicitly
select **Use reviewed source for a separate build**, then acknowledge again.
Check history first because an earlier unconfirmed request may already exist.

Build detail shows the exact request's source, placement, revision, timestamps,
resolved commit/tree digest, and artifact identities only when returned by the
public API. Internal repository policy, toolchain policy, credentials, and grants
are excluded. Polling stops for terminal states and when the view is unmounted.
Failed refreshes mark retained metadata as potentially stale and pause actions.

To cancel, confirm the exact build at its displayed revision and select
**Request build cancellation**. Confirmation retains that revision even if
background polling advances metadata. A conflict refreshes detail and requires
explicit review and reconfirmation before using a new revision; it never submits
a new cancellation automatically. A reported `cancel_requested` failure code
records intent, while the state describes cancellation or pending cleanup.
Identity, resource/project, and write-permission changes discard action intent
and ignore stale mutation responses.

## Request a private HTTPS Git build

The operator must first enable [private execution and protected controller
transport](hosting-build-runtime.md#enable-private-https-git). The bound runner
needs both `hosting_build_v1` and `hosting_git_https_basic_v1`, no shell capability,
and current destination/project grants. The UI cannot enable those prerequisites.

1. In **Project secrets**, create or rotate an existing project-managed secret
   with exactly two JSON string fields, `username` and `password`. A PAT belongs
   in `password`; use the provider's required username. See the
   [payload and disclosure limits](secrets.md#private-https-git-source-credentials).
2. Save the application's **Private HTTPS with a source grant** configuration.
   Under **Private HTTPS source grants**, a project owner selects **Existing
   managed secret** and **Explicit secret version**. These controls read metadata
   and version references, never the reveal endpoint or plaintext value.
3. Review the exact source/resource generations and secret version. Acknowledge
   the source and select **Create source grant**. Maintainers can list and select
   eligible grants; owners create and revoke them. Viewers have no grant controls.
4. In **Private HTTPS Git builds**, select an eligible **Private source grant**,
   review the exact source and acknowledge it, then select **Request private Git
   build**. The request carries that grant ID and the reviewed generations.
5. Wait for a succeeded build with its source receipt and local artifact. Read
   opt-in typed output, then follow the separate
   [artifact deployment and route workflow](hosting-build-deployments.md#deploy-through-the-resource-ui).

After an uncertain response, use **Retry identical source grant request** or
**Retry identical build request**. The UI retains each exact request identity,
generations, selected version/grant and body. It does not mint a new grant or
select another secret automatically. A conflict requires refresh, review and an
explicit separate decision. Check recorded history before discarding uncertainty.
Permission or actor/resource changes pause actions and discard stale intent.

An owner can select **Revoke source grant <grant ID>** after reviewing that exact
grant. Revocation denies future authorized use; it cannot recall a PAT already
delivered to Git transport. Secret rotation does not retarget an existing grant.
Create a new grant for the intended explicit version. Revocation does not delete
a successful artifact or an already serving application.

## Operator repository policy

The server defaults to HTTPS repositories on `github.com`, with public access unless
private access and a source grant are selected. Set
`NEROCD_HOSTING_GIT_ALLOWED_HOSTS=github.com,gitlab.com` before starting
`nerocd server` to replace the allowlist. This follows the server's existing
environment configuration convention. Up to 32 exact DNS hostnames are accepted.
Schemes, wildcards, IP literals, ports, paths, empty entries, and suffix patterns
are rejected as host configuration. The list never enables internal network
access. The accepted source transport still validates every resolved address and
redirect at execution time. Configuration writes do not perform network IO.

The configuration API accepts `credential_kind: public` or `https_basic`. Private
credentials are never part of resource configuration. An owner-created source
grant selects a managed-secret version separately. SSH, provider installation integrations, and internal-LAN repository
configuration are unsupported here. Configure [GitHub push webhooks](hosting-github-push.md)
separately. The operator allowlist applies to both public and private sources.

## API and persistence

Create with `POST /api/v1/resources`. An example source input is:

```json
{
  "kind": "git",
  "git": {
    "repository_url": "https://github.com/team/application.git",
    "requested_ref": "main",
    "credential_kind": "public",
    "settings": { "strategy": "dockerfile", "dockerfile_path": "Dockerfile" }
  }
}
```

The enclosing resource input also requires `project_id`, `project_environment_id`,
`name`, and `kind: application`. PATCH accepts `name`, `source`, and required
`expected_generation`. It returns 409 on stale generation. The API rejects mixed,
missing, null, or unknown source variants and rejects caller source IDs,
generations, policy, credential IDs, and grant IDs. Image source input/output
remains `{kind: image, image: {reference: ...}}`.

Git output includes `source_id` and source `generation` alongside the nonsecret
URL, ref, credential kind, and settings. It never exposes repository policy,
internal CIDRs, SSH fingerprints, or credential values. List and detail use the
exact referenced immutable version, rather than whichever version is current
when a second read happens.

Migration 0049 changes `resources.source` to contain only
`{kind: git, git: {source_id, generation}}` for Git resources. Actual configuration
and trusted policy exist only in immutable `hosting_build_source_versions`.
Creation and update atomically commit resource CAS, source identity/current
pointer, immutable version, and canonical audit. Name-only updates advance only
resource generation. Submitting identical canonical source settings does not
append another source version. A changed configuration or trusted policy advances
source generation once as well as resource generation. Source ID stays stable.
Deferred, schema-bound constraints verify resource/project/source/current-version
consistency at commit. Startup and readiness require the build and Git resource
schema predicates. Historical migrations and checksums remain unchanged.

The independently mutable sidecar writer is retired. Historical image sidecars
remain readable, but image resources cannot request builds or resolve a current
build artifact. Memory-backed development returns unavailable before writing a
Git resource or its audit. PostgreSQL is required for this configuration workflow.

## Verification boundary

[Configuration tests](../web/app/tests/browser/hosting-git-resources.spec.ts) and
[build-admission tests](../web/app/tests/browser/hosting-builds.spec.ts) check saved
configuration and admission. PostgreSQL tests cover CAS, immutable versions,
rollback, authorization, schema guards, and dump/restore.

The [private browser workflow](../web/app/tests/browser/private-git-build-deployments.spec.ts)
creates an owner grant, retries an identical lost response, checks the grant ID
used by the build, reads typed output, and deploys the exact artifact through
fixture-CA HTTPS. It checks keyboard controls, 320/390/430/1440-pixel layouts,
viewer denial with a real 403, and absence of secret reveal or legacy log reads.

The [local build fixture](../acceptance/runtime-hosting-local-build/README.md)
covers Dockerfile and Railpack in CLI and browser modes, including a compiled
Vite SPA variant. These tests use isolated DNS and a trusted test CA. They do not
qualify arbitrary Railpack language providers, production ACME, or workspace execution.

### Dockerfile argument qualification

The private HTTPS Dockerfile workflow passed at `3b6e0b39` on 2026-10-28.
It proved default injection across stages without source `ARG` declarations,
variable-edit rebuilds with new image identity and served content, explicit
injection disablement with declared `ARG`, and exact multiline and empty values.
A genuine failed build retained the prior HTTPS application. The existing grant,
source provenance, typed output, responsive layout and owned cleanup checks passed.
The grant retry deliberately drops the first committed response in Playwright.

The [evidence index](/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-dockerfile-build-arguments-20261028/3b6e0b39f47f3bc5561955ea528640cd6e8dea53/evidence-index.json)
records source hashes, scoped checks, reviews, cleanup and remaining test limits.
The initial aggregate Go test failed stale CLI expectations; the complete affected
package passed after repair. The contract check still exposes an unchanged
undocumented 401 on storage-backup-volumes. Build secrets and remote artifact publication remain outside this qualification.
[Selected build-base authentication](#private-build-registry-qualification) has separate evidence.
