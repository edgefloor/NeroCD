# Git application configuration

Project hosting saves public Git applications alongside image applications. Choose a
shared environment, choose **Public Git repository**, then enter the HTTPS URL,
branch/tag/full commit, and build settings. Editing preserves your draft when the
saved generation changes. Review the current values before applying that draft to
a newer generation. A viewer can read configuration; a maintainer or owner can
save it. Image applications retain their existing JSON and deployment controls.

Saving Git configuration does not fetch Git, run a build, create an image, or
request deployment. **Public Git builds** provides separate build requests and
retained history when the server operator enables build admission. Placement,
deployment history, and routes remain visible. Artifact deployment and build
output logs are not offered by these build controls. Git applications cannot call the image deployment endpoint. Source kind
conversion between image and Git is unsupported.

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
   deployment. No build-output or runtime-log stream is available here.

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

## Operator repository policy

The server defaults to public HTTPS repositories on `github.com`. Set
`NEROCD_HOSTING_GIT_ALLOWED_HOSTS=github.com,gitlab.com` before starting
`nerocd server` to replace the allowlist. This follows the server's existing
environment configuration convention. Up to 32 exact DNS hostnames are accepted.
Schemes, wildcards, IP literals, ports, paths, empty entries, and suffix patterns
are rejected as host configuration. The list never enables internal network
access. The accepted source transport still validates every resolved address and
redirect at execution time. Configuration writes do not perform network IO.

Only `credential_kind: public` is admitted by this public configuration API.
Private HTTPS credentials and SSH are explicitly unsupported here. Private source
support remains required for the complete Git journey. Existing trusted internal
credential delivery and its permission checks are preserved.

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

Real PostgreSQL tests cover atomic rollback, current authorization, concurrent
CAS, generation rules, immutable history, direct sidecar denial, mismatched raw
references, schema guards, and dump/restore. The actual backend browser test creates
and edits both source types through the UI at 320, 390, 430, and 1440 pixels. Role
fixtures use the real membership API, without SQL or API-created Git applications.
The build UI fixture exercises real request admission, exact replay, source
generation conflicts, cancellation revision conflicts, policy-disabled errors,
reload persistence, and viewer denial at all four widths. It consumes real
enrollment before work exists, requires the exact local build-runtime preflight
refusal, verifies dedicated identity and persisted build capability, and never
executes that fixture runner after admission. These checks are UI/admission evidence, not successful image-building
evidence; no artifact rows or successful receipts are seeded. Existing image
deployment browser coverage runs separately in the same isolated test setup.
Artifact deployment and Git-to-HTTPS acceptance remain separate work.
