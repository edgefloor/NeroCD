# Git application configuration

Project hosting saves public Git applications alongside image applications. Choose a
shared environment, choose **Public Git repository**, then enter the HTTPS URL,
branch/tag/full commit, and build settings. Editing preserves your draft when the
saved generation changes. Review the current values before applying that draft to
a newer generation. A viewer can read configuration; a maintainer or owner can
save it. Image applications retain their existing JSON and deployment controls.

Saving Git configuration does not fetch Git, run a build, create an image, or
request deployment. The UI shows build and deployment as unavailable until a
reviewed controller is connected. Placement, deployment history, and routes remain
visible. Git applications cannot call the image deployment endpoint. Source kind
conversion between image and Git is unsupported.

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
Build execution, Git-to-image handoff, deployment, and Git-to-HTTPS acceptance are
outside this configuration checkpoint.
