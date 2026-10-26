# Hosting backup notifications

Project notification channels deliver accepted PostgreSQL backup failures and remote upload failures through SMTP email or a generic HTTPS webhook. An upload failure leaves the successful backup and its local-copy records intact. Notification delivery runs after the source transaction commits.

This document describes the backend contract. Physical dump, remote upload, compiled-runner, and browser qualification remain separate acceptance evidence.

## Source events

`backup_failed` covers an accepted backup operation that settles as `failed` or `uncertain`. `upload_failed` covers an accepted upload transfer with either outcome. The immutable `source_state` records the distinction. Uncertain messages say that the outcome needs attention.

The source transaction inserts an event, the selected channel generations, and a safe audit entry together. The event references the exact accepted runner receipt. Controller decisions retain a separate immutable source receipt with the operation state, failure code, and plan hash. These decisions include upload admission denied after queueing and retirement of an already accepted scheduled backup.

The event identity is unique for the source kind, source ID, and event. Each selected channel generation has one delivery identity. Replayed source receipts cannot add deliveries for later channel generations.

Validation, restore, download, retention, and discovery-only missed occurrences do not produce failure alerts. A separate `test` event records an explicit owner request. Test attempts never change the real-event configuration failure counter.

## HTTP contract

The API returns direct JSON objects or bounded arrays, matching the existing hosting backup endpoints. Lists contain at most 100 rows. The handlers reject undeclared query parameters, duplicate JSON fields, unknown fields, missing required fields, and oversized bodies. Responses use `Cache-Control: no-store`.

| Method | Route | Permission |
| --- | --- | --- |
| GET | `/api/v1/projects/{project_id}/notification-channels` | Project viewer |
| POST | `/api/v1/projects/{project_id}/notification-channels` | Project owner |
| PUT | `/api/v1/projects/{project_id}/notification-channels/{channel_id}` | Project owner |
| POST | `/api/v1/projects/{project_id}/notification-channels/{channel_id}/tests` | Project owner |
| GET | `/api/v1/projects/{project_id}/notification-deliveries` | Project viewer |
| GET | `/api/v1/projects/{project_id}/notification-deliveries/{delivery_id}` | Project viewer |

A channel request contains `request_id`, `expected_generation`, `name`, `type`, `enabled`, and `events`. The type is `email` or `webhook` and cannot change after creation. Creation requires generation zero. Updates require the exact current positive generation. Event selectors are `backup_failed`, `upload_failed`, or both. An empty array selects no real events.

Creation also requires `configuration`. An update that omits configuration retains the existing encrypted document. An explicit configuration replaces the whole document. Webhook configuration contains `webhook_url`. Email configuration contains `smtp_host`, `smtp_port`, `smtp_security`, `from_address`, and `recipient_user_ids`. The security mode is `starttls` or `tls`. Authentication uses both `smtp_username` and `smtp_password`, or neither. Recipient IDs identify current project members through the existing authorized project-member API.

Channel responses contain only `id`, `project_id`, `name`, `type`, `enabled`, `generation`, `events`, `configured`, `consecutive_failures`, `created_at`, and `updated_at`. No read response reveals the endpoint, SMTP configuration, credentials, recipient IDs, or recipient email addresses. The whole configuration document is encrypted with the existing `secretcrypto` keyring and immutable secret identity and version.

A test request contains `request_id` and `expected_generation`. It requires an enabled channel and returns HTTP 202 with a delivery. Tests use the same current-member restriction as real email. New tests have a ten-second per-channel cooldown. Repeating the same request returns the original delivery identity.

Delivery responses contain stable event, delivery, channel, project, and source IDs. They also contain the selected channel generation, `event`, `source_kind`, `source_state`, `state`, `attempt_count`, `max_attempts`, `failure_code`, `local_copy_available`, `recovery_url`, and timestamps. Every string is present, including empty failure codes. Detail responses include attempts when available. Each attempt has a number, state, safe failure code, start time, and optional finish time.

Delivery states are `pending`, `sending`, `delivered`, `failed`, and `suppressed`. Attempt states also include `uncertain`. Test source kind and state are both `test`. A real source state is `failed` or `uncertain`.

## Delivery authority and recovery

River jobs contain only a delivery ID. The existing hosting recovery loop republishes pending deliveries independently of River job retention or retry exhaustion. The store resolves the immutable event and selected channel generation before sending.

A dedicated PostgreSQL session lock prevents two healthy workers from sending the same delivery concurrently. A durable claim includes a random fence, attempt number, and thirty-second expiry. Every message checks the claim, original deadline, current channel generation, enablement, event selection, and exact active secret version. Obsolete queued configuration is suppressed rather than redirected to the new target.

Email resolves current active project members at each message. Removed or disabled recipients receive no message. If no selected recipients remain, the delivery is suppressed. Revocation of the actor that originally requested the backup does not suppress a project-owned alert to remaining recipients.

Each delivery has at most five attempts and an original one-hour deadline. Recovery does not reset either budget. An expired sending attempt becomes uncertain before another attempt starts. Transient receiver, DNS, or authority-probe outages remain retryable. Definitive revocation suppresses delivery. Permanent payload rejection and configuration errors end the delivery without further sends.

Configuration errors include rejected SMTP authentication and HTTP 401, 403, 404, or 410. Three consecutive real-event configuration failures disable the channel. Successful delivery or a channel update resets the counter. Cross-channel escalation and instance-administrator fallback email are not implemented.

SMTP uses a stable `Message-ID` derived from the delivery ID. Webhooks carry an `Idempotency-Key` with that ID. A lost external acknowledgement can produce a duplicate after recovery. Delivery is at least once, and no exactly-once guarantee is claimed. Multiple email recipients can therefore receive duplicates after a partial attempt.

The sender has a fifteen-second attempt timeout, bounded messages and response reads, and owned cancellation. The store allows a separate bounded settlement attempt after cancellation. `StartHostingExecution` owns the River worker and the recovery loop. Shutdown drains and cancels them through the existing bounded stop function. A stop timeout retains ownership of the pool.

## Outbound policy

Webhook delivery requires HTTPS and rejects redirects and environment proxies. SMTP requires verified implicit TLS or STARTTLS, with no plaintext fallback. Both transports require TLS 1.2 or later, resolve DNS again before a dial, validate every address, and dial a validated address directly. Metadata, link-local, multicast, unspecified, and special-use address ranges remain forbidden.

Protected operator configuration can grant a private target only by exact origin and CIDR. `NEROCD_NOTIFICATION_PRIVATE_ORIGINS` is a JSON object whose keys are origins and whose values are arrays of CIDRs. Webhook origins use `https://host[:port]`. SMTP origins use `smtp+tls://host:port` or `smtp+starttls://host:port`. Project owners cannot modify these grants.

`NEROCD_NOTIFICATION_CA_FILE` names a bounded PEM file that extends the system trust roots. Fixture code can inject controlled CA roots and explicit loopback TLS origins through `notificationtransport.Policy.FixtureTLSOrigins`. Production environment configuration exposes no loopback or plaintext convenience switch.

External URLs require the validated absolute HTTPS `NEROCD_PUBLIC_ORIGIN`. Request Host headers never supply the origin. Missing or invalid notification origin makes channel admission, explicit tests, and sends unavailable. The API returns HTTP 503 with `backup_notifications_unavailable` and a safe configuration instruction. Other hosting work remains available. Recovery links contain the project, environment, resource, and backup IDs, plus the transfer ID for an upload. They contain no authority. The destination UI must validate current authorization before selection or recovery actions.

## Upstream mapping

The reference checkout is Coolify commit `b12d1386f3d458618b4c579babcd1e339c6c40b3`.

| Reference behavior | NeroCD behavior |
| --- | --- |
| `DatabaseBackupJob` distinguishes dump failure from successful local dump followed by failed S3 upload. Notification exceptions do not rewrite a completed backup. | Accepted source outcomes and notification delivery commit independently. Upload alerts retain the successful backup and expose safe local-copy availability. |
| `BackupFailed` and `BackupSuccessWithS3Warning` both select `backup_failure` in `HasNotificationSettings`. | Selecting both explicit failure events provides the equivalent coverage. Uncertain outcomes have explicit text and state. |
| `EmailChannel` resolves `Team.getRecipients` and validates team membership for real notifications. | Selected recipient user IDs resolve to current active project members before each message. Tests keep this restriction too. |
| `NotificationPolicy` permits member reads and administrator updates and tests. The Email and Webhook Livewire settings persist channel options. | Viewers read safe metadata. Project owners configure, disable, and test. Configuration remains write-only for every role. |
| `CustomEmailNotification` and `SendWebhookJob` bound queue attempts at five. Webhook jobs are encrypted and validate safe URLs at send time. | Durable attempt history and budgets survive River retries. Jobs contain only opaque IDs. Each actual dial uses the protected address policy. |
| `HandlesNotificationResponse` retries rate limits and server failures, counts configuration failures, and does not count payload failures. | Safe permanent and transient classifications retain those distinctions without storing raw receiver bodies or errors. |
| `TracksDeliveryFailures` disables after three configuration failures, resets on success or settings changes, and escalates through other channels. | Disable and reset behavior is implemented. Escalation remains an explicit gap. |
| `Test` bypasses event selectors and does not count real delivery failures. | An explicit durable test event bypasses selectors and leaves real-event counters unchanged. |

The implementation uses no new cryptographic algorithm. Configuration replay compares decrypted documents inside the existing encryption boundary, rather than persisting a secret-derived public hash. Source receipts and event selection use NeroCD transactions and existing fenced runner authority.

## Verification boundary

Focused local tests cover strict documents, project permissions, encryption binding, read redaction, HTTPS identity, DNS rebinding, private-address policy, redirect rejection, certificate validation, SMTP TLS, stable identifiers, and safe errors. Race tests cover the transport, hosting jobs, API boundary, and persistence cases.

The qualified PostgreSQL run uses a task-owned pinned PostgreSQL 17.6 container, loopback publication, bounded resources, and no host mounts. The real tests cover atomic source and audit rollback, receipt replay, channel generation drift, serial sends, actor revocation, current SMTP recipients, expired claims, original retry budgets, configuration failure counters, accepted controller transitions, and River recovery. The run contains seven top-level persistence tests and two controller subcases. The separate fixture owns physical dump and upload proof.

The broader store SQL-placement test still fails on the same thirteen files as the base commit. The notification files add no violations. This existing failure is reported rather than repaired by this feature.
