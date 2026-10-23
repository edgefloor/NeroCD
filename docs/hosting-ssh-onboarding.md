# Durable SSH onboarding foundation

This internal domain/store foundation reserves one reviewed installation identity and keeps its commands, leases, private enrollment credential, and receipts durable. It performs no SSH or remote process execution. The controller/API, queue wiring, operator UI, and exact remote receipt integration are subsequent work. This foundation alone does not complete A01 or prove a healthy deployment destination.

## Review and authority

Creation requires the existing system administrator or runner administrator permission together with project owner authority. A system administrator retains the existing implicit project-owner semantics; managed service-account credentials do not gain runner-administrator permission. Every command, claim, renewal, delivery, completion, and replay rechecks current authority. Legacy API-token wall-clock expiry is checked after blocking writes immediately before commit.

The creation key binds the actor, project, and complete request hash. Stable operation, installation, remote-install operation, runner, enrollment, credential-reference, server, and destination IDs are allocated once. Exact retries retain them. Changing the body conflicts. A normalized literal IP and SSH port identify the target across key and host-fingerprint changes. Another unfinished, uncertain, or completed owned installation cannot be created for that endpoint. Only a safely canceled or refused operation without remote effects releases the target.

The review contains the exact transport manifest bytes and their SHA256, the pinned target hash, typed prerequisite facts, observation time, and a maximum thirty-second expiry. The store validates a closed manifest identity against reserved IDs and reviewed artifact/controller facts. It stores manifest bytes as base64 inside JSONB so PostgreSQL cannot rewrite their serialization. It does not produce a transport manifest or import the SSH adapter. The future application must call the transport's canonical-manifest helper and map its observations into this immutable review, then revalidate the host before install delivery.

The initial `prepared_debian12_v1` profile requires Debian 12, matching native architecture, actual systemd running, Docker API exactly 1.54, Compose available with normalized version `5.1.1`, and at least 1 GiB root free space. Buildx is unnecessary for deployment-only installation. The available profile was observed on the separate pinned Debian 12/arm64 fixture with a fixture-provisioned Compose binary; the product never installs Docker or Compose. The domain can represent amd64 and arm64, but a later API must offer only separately qualified artifact/profile combinations. This is not general Linux or Compose-version compatibility, enrolled identity, current TLS, or workload health evidence.

## Durable commands

Commands have actor/key/body identity, an immutable original deadline no more than one hour after creation, at most eight lease attempts, and a maximum of 32 ordinary commands, eight reserved inspections, and one reserved safe cancellation per operation. Request keys are at most 128 bytes; creation/manifest/evidence documents are bounded at 16 KiB and command/completion envelopes at 32 KiB. Read and exact replay never consume these limits. Safe cancellation retires only known read-only probe or inspection stages without any effect intent, advances their fences, and revokes unused enrollment and the grant. Late old completion and delivery fail. Probe and ownership evidence use closed typed fields; raw remote output and credential material are excluded.

| Action | Allowed state | Result |
| --- | --- | --- |
| `probe` | `draft` | Prerequisite evidence; no enrollment or remote mutation intent |
| `review` | `draft`, `reviewed`, without a remote hold | Immutable exact review; state `reviewed` |
| `install` | `reviewed`, without a remote hold | Delivery atomically records enrollment and remote effect intent; state `running` with retained remote ownership |
| `inspect_owned` | `reviewed`, `running`, `installed_waiting_enrollment`, `registered`, `completed`, `uncertain` | Read-only evidence; never releases uncertainty |
| `register_server` | `installed_waiting_enrollment` | Exact consumed enrollment and current enrolled runner checked independently; creates preallocated server/destination |
| `grant_destination` | `registered` | Separate explicit project grant; state `completed` |
| `cancel` | `draft`, `reviewed`, before remote intent | Safe cancellation and bootstrap-grant revocation |
| `restart_owned`, `cleanup_owned` | unavailable | Conflict until exact remote receipt/quiescence integration is implemented |

Leases last at most thirty seconds and renewal never extends the original command deadline. An expired lease before effect intent may use another finite attempt. Once delivery records intent, expiry never authorizes another mutation attempt. The retained intent and remote hold survive response loss and restart; an expired unfinished intent is presented as `uncertain`. It does not imply that the remote process stopped. Read-only inspection, file presence, a claimed boolean, or elapsed time cannot clear that hold. These are bootstrap ownership holds, separate from hosting resource mutation holds.

Install completion binds the reserved remote operation and reviewed manifest hash. A successful install receipt moves to `installed_waiting_enrollment` while retaining ownership. It does not prove enrollment consumption. Registration independently checks the exact durable used enrollment, consume request, credential hash, runner identity, and enrolled-runner admission constraints. It creates no implicit project grant. `completed` means registration plus the explicit project grant; it does not mean healthy. Completion receipts retain their original result on exact replay, even after later commands; current read authorization still applies.

The read projection includes the exact accepted review, active command ID and stage, and all bounded command metadata ordered by creation time then ID. An unfinished mutation intent takes precedence over inspections. The 41-row history bound includes the active or uncertain stage. These lease and fence fields are observations until a fresh claim or delivery check grants authority. Future cleanup and reconciliation need separate reserved admission when implemented; they are unavailable here.

## Credential boundaries

The purpose-specific SSH grant pins operation, target hash, principal, project-managed secret ID/version, and a lifetime no longer than one hour. It is separate from ordinary reveal grants. Delivery checks the current grant, managed-secret active status, exact version, current stage, review freshness, and current actor. Revocation and wrong-key decryption fail before credentials or remote intent are published. Another current administrator/owner can read the records, but cannot inherit the original principal's SSH-key delegation. Delegation replacement is not implemented in this foundation; it requires an explicit reviewed follow-up contract.

Enrollment issuance, the ordinary one-time enrollment row, its private ciphertext, the remote intent, and audit commit atomically. Exact delivery replay within current authority reopens the same encrypted enrollment token rather than minting another. The token is never an ordinary revealable managed secret. The enrollment ciphertext uses the existing `secretcrypto` AES-GCM implementation and binding:

- `ProjectID`: the actual project ID;
- `SecretID`: `nerocd/server-bootstrap/enrollment/v1` + NUL + immutable operation ID + NUL + immutable enrollment ID;
- `Version`: 1.

The reserved NUL-bearing purpose namespace cannot equal an ordinary PostgreSQL TEXT secret ID. Immutable validated IDs prevent delimiter aliases, and the existing length-prefixed AAD binds project, purpose, enrollment, key ID, and envelope version. No cryptographic primitive changes are introduced. A rotated keyring can retain the old key for recovery; a wrong or removed key fails closed. The exact enrollment capability list is `compose-deploy`, `image_deploy`, `image_release_ports_v1`, and `hosting_readiness_v1`, matching the frozen launcher. It grants no proxy, runtime-log, runtime-environment, or local-build capability. A separately reviewed trusted hosting deployment preset is still needed for the full repository/image-to-HTTPS journey.

The target still creates its permanent runner credential independently and consumes the enrollment through the existing stable consume-request replay contract.

Only a private, non-serializable delivery value contains decrypted SSH key/enrollment bytes. Callers must discard these after the bounded transport call and must not log them; Go does not guarantee memory erasure.

## Persistence and restore

The store locks current actor/project authority, target serialization, operation, command/stage, then purpose grant and managed-secret version. Registration takes runner SHARE before the exact enrollment row, matching readiness authority order. Existing consume first creates the runner while holding its enrollment transaction; that uncommitted runner cannot be observed by registration. Its historical replay reads the runner without acquiring a conflicting row lock. No remote I/O occurs under database locks.

Migration 0061 adds bounded operation/command history, monotonic leases/fences, purpose grants, immutable private ciphertext, deferred enrollment/intent consistency, and startup compatibility checks. No parallel task queue is introduced. A later River job will carry an opaque operation ID.

The existing offline post-restore transaction holds every bootstrap operation, including idle and terminal records, revokes purpose grants and unused enrollments, and invalidates unfinished lease delivery. It preserves immutable command/ciphertext history and does not label running workloads unhealthy. Failure rolls back all restore invalidation together. Restored records cannot resume root SSH delivery.

There is no generic uncertainty unlock, lease-expiry recovery, adoption, restart, or cleanup control in this foundation. A later exact remote-protocol integration must prove durable receipt identity and quiescence before any supported recovery can release uncertainty. Operators should retain the hold until that supported path exists.
