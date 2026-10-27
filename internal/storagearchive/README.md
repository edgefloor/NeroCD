# Upload archive helper v1

This package creates, validates, and restores uncompressed upload archives. The caller
must stop source writers, provide a read-only source and an exclusively owned empty
target, and retain exclusive ownership of the archive and roots. Source metadata checks
detect some concurrent changes; they do not provide a transactional snapshot.

Backup authorization, writer recovery, artifact publication, and durable operation
receipts belong to the calling controller.

## Package contract

`Write(ctx, source *os.Root, writer io.Writer, limits Limits)` returns a `Summary` and
error. The caller owns the writer and its cancellation. Context checks cannot interrupt
arbitrary blocked IO. `Validate(ctx, archive *os.File, limits)` accepts an exclusively
owned pinned regular archive file. `Extract(ctx, archive *os.File, target *os.Root, limits)` fully validates and hashes before target mutation, then checks and hashes the
archive again during extraction. Archive mutation between passes fails the restore. The
caller must retain exclusive ownership of archive and roots throughout each operation.

Filesystem creation/extraction supports Linux only. Other platforms fail with
`ErrUnsupported`; local Darwin parser/CLI-validation tests do not qualify the Linux
filesystem implementation. Numeric UID/GID and ordinary permission bits are preserved.
Ownership-setting, metadata inspection or fsync failures fail the operation rather than
silently dropping metadata. Root and final directory modes are applied after
descendants. Timestamps are deliberately outside v1's supported metadata and are not
preserved.

## Archive format and limits

V1 is strict USTAR with exactly two terminal zero blocks and no trailing bytes. Its
first record is a `.` directory carrying root UID/GID/mode and the fixed Uname `nerocd-storage-v1`. All later records are regular files or directories, with explicit parents
before children. UID/GID must fit USTAR's 21-bit unsigned fields. Paths are canonical
graphical ASCII, at most 255 bytes, without absolute paths, dot components, empty
components, backslashes, colons, whitespace or trailing dots. `.nerocd-restore-incomplete` and its descendants are reserved. PAX/GNU extensions, links, special/sparse
files, xattrs, ACLs and privileged permission bits are rejected.

Source hard links, cross-device entries and sparse files are rejected too. A filesystem
without required metadata or hole inspection fails closed.

Limits bound physical archive size, total payload, each file, member count including
root, depth and path bytes. Defaults are 256 MiB archive, 128 MiB payload, 64 MiB per
file, 100000 members, depth 32 and 255 path bytes. The summary contains only format,
SHA-256 digest and bounded counts. The digest establishes integrity of these passes, not
authenticity or caller authority; artifactfs publication and durable receipts remain
outside this helper.

## Command contract

The internal-use `nerocd-storage-archive` command has three fixed modes:

```
nerocd-storage-archive create   # /source read-only mount to stdout tar
nerocd-storage-archive validate # stdin to private bounded local spool; JSON summary
nerocd-storage-archive restore  # stdin to private spool, then empty /target; JSON summary
```

The command accepts only numeric `--max-archive-bytes`, `--max-data-bytes`, `--max-file-bytes`, `--max-members`, `--max-depth` and `--max-path-bytes` options. It accepts no
mount/path arguments, runs no shell or Docker commands, and performs no network
operations. On Linux/Darwin, used stdin/stdout streams must be process-owned FIFOs or
`SOCK_STREAM` sockets. Datagram and sequence-packet sockets are rejected before
descriptor flags change. Regular-file redirection, TTYs and devices are unsupported, so
callers must pipe archive bytes. Nonblocking descriptor reads/writes use polling of at
most 50 ms between context checks, without a background IO goroutine. Original
descriptor flags are restored and streams closed synchronously after use.

Package regular-file validation/extraction remains unchanged; no bound is claimed for
stalled regular-filesystem operations. Private 0600 spool files are removed on return.
Failure stderr is fixed text; upload names, bytes and parser/OS diagnostics are not
printed. Diagnostics use one nonblocking write to a FIFO or `SOCK_STREAM` sink and may
be dropped or partial when it is full, closed or interrupted. Unsupported stderr sinks,
including regular files and TTYs, receive no diagnostic. The command never waits or
starts a background writer for diagnostics, including on failures without cancellation.

## Partial restore handling

A restore must begin with an empty target owned by the helper's effective UID, with
owner read/write/search permission and no group/other write permission. The helper
restricts its root to 0700 while restoring descendants. Once it creates its exclusive
incomplete marker, every failure wraps `ErrPartialRestore`, including failures to
persist or remove that marker. The target must not be reused after this error. No
automatic erase, rollback or retry occurs. On ordinary failures the incomplete marker
remains; a failing filesystem can prevent marker persistence, so the error and later
coordinator state are authoritative. Successful extraction fsyncs files/directories and
removes the marker.

The caller must publish the artifact and hand off the target only after its own
operation completes.

## Prepare the local helper image

Use a clean committed checkout and the exact Go toolchain named in `go.mod`. The
selected executable is a trusted operator input; its path and reported version do not
establish its trustworthiness. Dependencies must already exist in the default
`$HOME/go/pkg/mod` cache. Preparation disables proxy access, toolchain downloads,
workspace files and inherited build overrides. It does not invoke Docker or pull an
image.

Create a private parent directory, then select an unused absolute canonical output path
beneath it. Symlink aliases, traversal, existing output directories and output inside
the source checkout are rejected. The parent must be owned by the effective user and not
writable by group or others.

```sh
python3 scripts/build-storage-archive-helper.py \
  --output /absolute/private-parent/prepared \
  --go /absolute/go1.26.10/bin/go
```

Preparation exports only tracked helper sources at `HEAD` into a temporary build tree,
ignoring untracked and ignored files. It builds a static Linux arm64 executable with
`CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, read-only modules and an empty build
ID. Each subprocess has a 60-second deadline and bounded output; timeout or excess
output terminates its owned process group and joins the subprocess. Missing dependencies
or a different toolchain fail preparation. A failed publication may leave a partial
output directory; inspect it and choose a new unused path rather than overwriting it.

SIGTERM and SIGINT record a stop request. The subprocess watchdog observes it within its
100 ms polling interval, terminates the exact owned process group and waits up to five
seconds to join the child. Further signals cannot interrupt this cleanup. The command
then emits a fixed interruption diagnostic and exits with status 143 for SIGTERM or 130
for SIGINT. These bounds cover subprocess control, not stalled filesystem calls.
Interruption does not delete an output directory or authorize reuse of a partial output.

The output contains `context/Dockerfile`, `context/nerocd-storage-archive` and
`provenance.json`. The context contains no source tree, credentials or other host files.
Provenance records the source commit and timestamp, toolchain version, executable
SHA-256, recipe SHA-256 and rendered Dockerfile SHA-256. Its `prepared_only` status and
null `image_config_id` mean no image has been built or qualified. The rendered scratch
recipe has exact source/executable labels, a fixed `/nerocd-storage-archive` entrypoint
and empty `/source`, `/target` and `/tmp` mount points. It contains no shell and
declares no capabilities, ports or volumes.

After separate authorization for the target Docker daemon, build only this prepared
context:

```sh
docker build --platform linux/arm64 --pull=false --network=none \
  --iidfile /absolute/private-parent/image-id \
  /absolute/private-parent/prepared/context
```

Inspect the returned image config ID and compare all three `io.nerocd.storage-archive.*`
labels with the recipe/source/executable values in `provenance.json`. Keep that exact
`sha256:...` config ID with the preparation metadata and actual image qualification
evidence. It is a local image identity, not a registry manifest digest. Trusted operator
configuration must pin that exact qualified image ID; this recipe grants no permission
to choose arbitrary images or commands.

Runtime qualification must use a read-only root, no network, no Docker socket, separate
authorized source/target mounts and a bounded private writable tmpfs at `/tmp`. Pipe
archive bytes and summaries through supported streams. Validation needs no capabilities.
Creation needs read/search access to the authorized source. Restore starts with an empty
target owned by the helper UID; restoring different numeric owners requires explicitly
authorized `CHOWN` and `FOWNER` capabilities. Drop all other capabilities and use `no-new-privileges`; do not make the container privileged. These are runtime requirements,
not Dockerfile privileges. Validate the actual image and run create/restore roundtrips
before using it for backups.

Building this context does not prove the runtime workflow.
