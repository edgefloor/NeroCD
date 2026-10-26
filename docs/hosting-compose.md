# Managed Compose sources

A hosting application can save a `compose` source without a Git repository.
The source contains the exact original Compose text, the public component name,
and its container port. Each resource generation retains the original bytes,
their SHA-256 hash, and the normalized definition in
`hosting_compose_source_versions`.

Creation requires a 32-character lowercase hexadecimal `request_id`. The create
receipt commits with the resource and normalization. Retrying an identical
request returns the existing resource without overwriting later edits. Reusing
the identity with a different request returns a conflict. Updates use the
ordinary resource `expected_generation` compare-and-swap.

## Managed profile

The `managed-compose.v1` normalizer accepts up to 16 services with image tags or
repository digests, exec-form commands, environment mappings or lists, named
volume mounts, container healthchecks, and dependency conditions. Images without
a tag normalize to `latest`; immutable image resolution belongs to deployment
preparation. Dependency conditions include `service_started`, `service_healthy`,
and `service_completed_successfully`.

The original source preserves variable references. Runtime configuration stores
nonsecret literals or versioned managed-secret references separately. Supported
interpolation includes `$NAME`, `${NAME}`, unset and empty defaults, required
values, alternate values, and escaped `$$`. Missing required values produce an
error containing the variable name, without the source error message or value.
Nested interpolation is unsupported in this profile.

Unknown fields, duplicate YAML keys or environment names, aliases, bind mounts,
external volumes, custom volume drivers, dependency cycles, and missing health
contracts fail preflight. Build directives, Git Compose execution, and arbitrary
raw Compose remain outside this profile. The legacy Git Compose volume policy
is unchanged.

## Execution status

This source-storage checkpoint saves desired configuration only. It does not
admit Compose deployment or claim stack runtime support. Whole-stack execution,
per-component receipts, owned-volume materialization, replacement recovery,
selected-component HTTPS routing, and physical persistence qualification remain
required before this feature can be accepted.

## Upstream reference

The reference is Coolify commit
`b12d1386f3d458618b4c579babcd1e339c6c40b3`. The vendored
[Vikunja source](../internal/domain/testdata/vikunja-with-postgresql.yaml) has
SHA-256 `4ae479af1c9ee76836315b952db8d177fb1b1426316ead2f0f5c09bfa4acdb54`.
Its upstream path is `templates/compose/vikunja-with-postgresql.yaml`, licensed
under Apache-2.0 by the Coolify project.

The inspected paths are `DockerCompose.php::submit`,
`ServicesController.php::create_service`, `Service::parse`,
`Service::saveComposeConfigs`, `serviceParser`, `StartService`, and `StopService`.
Coolify saves raw source and retains component and generated variable identities
with `firstOrCreate`. It resolves remote secrets before stopping a service,
keeps a stable Compose project identity, and preserves dependency conditions.
NeroCD must retain those behaviors through its existing Go runner authority,
protected material delivery, durable receipts, and owned-volume provenance.
SSH scripts, persistent `.env` files, and remote shell cleanup are not the
execution mechanism for this managed profile.
