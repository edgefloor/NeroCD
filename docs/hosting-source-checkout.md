# Hosting source checkout reference

`runnerruntime.WithSourceCheckout` acquires one immutable Git checkout and lends it to a callback. It supports public HTTPS, HTTPS basic credentials, and SSH private keys with operator-configured host fingerprints. The adapter is available on Unix hosts. The [build executor](hosting-build-executor.md) supplies authorization and credential callbacks, commits its source receipt, and passes the checkout to the [local build engine](hosting-build-engine.md). SSH support in this adapter does not enable SSH sources in resource configuration.

## Request and caller contract

`SourceCheckoutRequest` includes `BuildID`, `SourceID`, `InputID`, `RepositoryURL`, `RequestedRef`, optional `FrozenCommit`, `source.RepositoryPolicy`, `CheckoutPath`, and `SourceCheckoutLimits`. IDs are opaque caller assertions. The caller authorizes the policy, credential callback, receipt callback, and use callback. A policy does not establish repository ownership.

`CheckoutPath` is a stable absolute path selected by the trusted caller. Its existing parent must be owned by the current effective user and have no group or other permissions. Every ancestor must be a real directory. The adapter creates the checkout exclusively and rejects an existing path. It never deletes an existing checkout to make room for another request. On macOS, use a canonical `/private/var/...` path rather than a path through `/var`.

Use the same stable path when replaying an accepted build engine request. The build engine includes the checkout path in request identity. The adapter removes the checkout after the use callback returns. It does not retain source or implement replay. The receipt owner must reject a changed commit or tree for an already accepted immutable input identity. Supply the recorded `FrozenCommit` when retrying. A missing frozen commit fails acquisition; it never falls back to a moving branch.

The context must have an overall deadline. Every callback must honor that context. The adapter cannot forcibly stop a trusted Go callback that ignores cancellation. The caller must not expose the checkout concurrently, mutate it while validation runs, or retain its path after the callback returns. The source must contain no production secrets, including credentials committed under ordinary filenames.

## Acquisition and receipt order

The adapter performs these steps:

1. Validate the URL, ref, policy, stable path, callbacks, deadline, and explicit limits.
2. Resolve DNS and validate every answer against the policy. Select one permitted address.
3. Create private acquisition directories and check workspace usage and free-space reserve.
4. Ask the credential callback for a credential, if configured.
5. Fetch the requested ref, or the exact frozen commit, and verify a full immutable commit ID.
6. Reject unsupported tree entries, remove Git metadata, and calculate the tree digest.
7. Call `accept(ctx, receipt)`. A nil return means the receipt owner has durably acknowledged that exact receipt.
8. Remove transport files and clear the delivered credential byte slice. Confirm removal before continuing.
9. Call `use(ctx, checkoutPath, receipt)`.
10. Cancel and join the monitor, then remove the owned checkout and transport workspace.

`SourceReceipt` binds the three IDs, full commit, policy hash, and canonical tree digest. It has no local path, credential, or server diagnostic. The policy hash is SHA-256 of the typed policy's JSON representation, including configured list order. The tree digest hashes sorted, length-delimited relative paths, directory type, executable bit, file length, and file bytes. Host timestamps and directory sizes do not affect it. The build engine separately calculates its sanitized context digest after applying its credential filename exclusions.

A failed acknowledgment prevents use. A use callback failure still triggers cleanup. Cleanup failures return `cleanup_unconfirmed`; the receipt owner must reconcile that outcome before scheduling another attempt. A receipt can already be durable when later transport cleanup or use fails. Successful acceptance alone does not mean a build completed.

## Credentials and controlled transport

`SourceCredential` contains `Kind`, `Username`, `Bytes`, and `UseUntil`. Supported kinds are `https_basic` and `ssh_private_key`. Credentials need a future `UseUntil`. The fetch context expires at that time. The adapter enforces the credential window through tree validation, receipt acceptance, transport-file removal and clearing the delivered bytes. Once that cleanup is confirmed, the use callback and post-use check remain governed by the overall operation context and deadline. A valid long build may finish after its cleared Git credential expires; an expired acquisition or receipt window still prevents use. A short delivery lease cannot recall a PAT already delivered to the process. The callback transfers ownership of `Bytes`; the adapter clears that slice on all callback return paths. Go or child processes can make additional copies, so this is not a promise of cryptographic memory erasure.

HTTPS uses an owner-only askpass script and separate owner-only username and password files outside the checkout. Credential values never enter the repository URL, argv, or environment. Git retains TLS hostname validation while `http.curloptResolve` pins the chosen address. The pin uses the exact fetch hostname, including a terminal DNS dot. Policy host equivalence never changes curl's transport lookup key. A Git installation without this capability fails before fetch. Redirects are disabled even when the policy lists redirect hosts. Plain HTTP is unsupported by this hosting adapter.

SSH writes a mode `0600` key outside the checkout. `ssh-keygen -y -P ''` rejects unusable or passphrased keys before network authentication. A bounded `ssh-keyscan` result must match an operator fingerprint. Strict known-host checking, the pinned address, an explicit username, no agent, and an empty SSH configuration control the fetch. Host-key changes fail before Git fetch.

Compose and hosting call the same `controlledGitCheckout` implementation. They also share DNS-policy helpers, fingerprint validation, process-group cancellation, and SSH command construction. The Compose wrapper keeps its existing runner-file credential authorization, HTTP support under an explicit internal policy, workspace preparation, Compose rendering, and receipt behavior. Hosting's stricter tree admission and HTTPS credentials do not alter those contracts.

The host, Git/SSH binaries selected through the operator-controlled `PATH`, CA trust, and private parent directory are trusted. The adapter supplies an isolated environment without ambient Git configuration, credential helpers, proxy variables, or SSH agents. Hooks and file/ext protocols are disabled. This process boundary is not hostile-fork isolation. Private dependency credentials, OAuth, passphrased keys, submodule expansion, and Git LFS expansion are unsupported.

## Bounds and failure codes

The caller supplies positive `MaxTreeBytes`, `MaxWorkspaceBytes`, `MinFreeBytes`, `MaxEntries`, `MaxDepth`, and `PollInterval`. Workspace bytes must be at least tree bytes. Entry count is at most one million, depth is at most 256, and the polling interval is between 10 milliseconds and one second. The shared command runner caps combined output at 5 MiB per command. Credentials are at most 1 MiB. The overall context deadline also bounds Git, SSH, and key inspection commands. Cancellation terminates the owned process group and waits for it.

The monitor checks the acquisition checkout, Git metadata, and transport workspace with cancellation-aware batches of 64 directory entries. Scans enforce byte, entry, and depth limits. The exported tree separately enforces `MaxTreeBytes`. All links and special files are rejected. Gitlinks, `.gitmodules`, `.lfsconfig`, and LFS pointer files are rejected. No submodule or LFS expansion command runs.

Disk limits are monitored, not filesystem quotas. A fetch can exceed the byte limit or consume free space between scans. Overshoot depends on write rate, poll interval, scan time, and process-group shutdown latency. A free-space reserve must account for other host users. The adapter does not set host quotas, reserve blocks, or configure filesystems. Directory system calls can still wait on the underlying filesystem. Use a healthy local filesystem and a suitably bounded overall deadline.

Errors are `*SourceCheckoutError` with a fixed `Code`. Validation and admission codes include `invalid_input`, `invalid_path`, `deadline_required`, `checkout_unavailable`, `url_policy`, `dns_policy`, and `transport_unavailable`. Credential codes include `credential_required`, `credential_denied`, `credential_expired`, `invalid_credential`, and `host_key`. Acquisition and callback codes include `fetch_failed`, `commit_mismatch`, `unsupported_tree`, `tree_limit`, `workspace_limit`, `workspace`, `canceled`, `receipt_rejected`, `use_failed`, and `cleanup_unconfirmed`. Raw Git/SSH output and callback error text are not exposed through these errors. Shared diagnostics contain fixed stage/status vocabulary.

## Verification

Ordinary package tests run real local HTTPS Git servers with isolated test CAs. They include a CA-verified `localhost.` regression with a selected address of `127.0.0.2` and a server listening only on `127.0.0.1`. Neither a request nor a credential may reach that unselected server. Dotted and ordinary hostnames both succeed when the selected address is the listener. They also cover public and authenticated fetches, frozen commits after branch movement, absent frozen commits, DNS pinning with hostname verification, redirect refusal, ambient configuration and hook isolation, callback failures, cancellation after a started-fetch handshake, bounded scans, and credential cleanup before use. Private HTTPS regressions also verify successful use beyond the cleared credential window, expiry during receipt acceptance preventing use, and parent cancellation/deadline during use preventing success. The fixture-only Git wrapper adds its isolated CA; production has no request option to disable TLS verification.

The SSH transport test requires an explicitly selected disposable fixture image. Build the existing `acceptance/runtime-provenance/GitDockerfile` and set `NEROCD_SOURCE_SSH_IMAGE` when running `TestSourceCheckoutSSHTransport`. It starts a labelled container with a temporary SSH server, binds an ephemeral port on loopback, mounts only its disposable fixture directory read-only, verifies exact fetched content, rejects a changed fingerprint, and removes the exact container. It mounts no Docker socket or production credential. The fixture image uses Alpine 3.22.2 at `sha256:4b7ce07002c69e8f3d704a9c5d6fd3053be500b7f1c69fc0d80990c2ad8dd412`; package versions come from that existing fixture's APK installation.

These package tests cover acquisition. The [combined runtime fixture](../acceptance/runtime-hosting-local-build/README.md) separately covers managed credentials, builds, deployment, and fixture-CA HTTPS.
