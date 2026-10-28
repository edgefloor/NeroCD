# Hosting backup notifications

Project notification channels deliver accepted backup and remote upload failures for PostgreSQL, native MySQL, and owned-upload storage. Channels use SMTP email or a generic HTTPS webhook. An upload failure leaves the successful backup and its local-copy records intact. Notification delivery runs after the source transaction commits.

## Configure project alerts

Before configuring a channel, ask the operator to enable managed secrets and the notification sender and set `NEROCD_PUBLIC_ORIGIN` to the absolute HTTPS origin users open. A missing origin produces a setup instruction instead of admitting a channel or sending a message. For private receivers, the operator must configure the exact origin and CIDR grant described under [Outbound policy](#outbound-policy). Certificates must validate against the system roots or the operator's additional CA file.

1. Sign in as a project owner and open **Projects**. In **Project backup notifications**, choose **Notifications project**.
2. Select **Add notification channel**, enter **Channel name**, choose **Channel type**, and enable **Channel enabled**.
3. Select **Backup failed** and **Upload failed, local backup available** to cover both supported real events. Selecting no events still allows an explicit test.
4. For email, enter **SMTP host**, **SMTP port**, **SMTP security**, and **From address**. Choose TLS or STARTTLS and select current project recipients. Supply both SMTP username and password if authentication is required. For a webhook, enter the complete HTTPS **Webhook URL**.
5. Select **Save notification channel**. The channel shows its generation and **Configuration saved, values hidden**. Saved endpoints, credentials, and recipient selections cannot be read back.
6. Select **Send test for** the channel and then **Refresh notification history**. Inspect the resulting `test` delivery and verify receipt at the configured receiver. A successful test proves that request's delivery, not an actual dump or upload failure.

To change an existing channel, select **Edit** for that channel. Save metadata or event changes while retaining the existing configuration, or select **Replace entire delivery configuration** and supply every configuration field. Saving a change creates a new generation. Pending work selected for an obsolete generation is suppressed. Clear **Channel enabled** and save to disable delivery.

If a save or test response is lost, inspect refreshed history before using the retained exact-request retry. A generation conflict requires current metadata. An explicit test does not reset or increment the real-event configuration failure counter.

## Inspect a failure and recover explicitly

1. Open **Notification delivery history** and identify the backup or upload event. **Inspect delivery** shows the attempts, safe failure guidance, and timestamps. The panel lists the latest 100 deliveries.
2. Distinguish source outcome from delivery outcome. `delivered` means the receiver acknowledged the notification. A failed or uncertain backup remains failed or uncertain even when its alert was delivered. An upload failure can leave a successful local backup available.
3. Follow **Open authenticated backup recovery** from the message or history. Sign in if required. The page checks current project, environment, resource, backup, and transfer scope before showing linked history. Opening a link does not retry an upload or start a restore. Viewers can inspect authorized history. Owners perform recovery actions.
4. For a PostgreSQL or native MySQL upload failure with an available local copy, inspect the linked successful backup and failed transfer. Use **Retry upload for** that backup only after checking the target. The retry sends the same archive without another dump. See [remote backup recovery](hosting-database-remote-backups.md).
5. To recover PostgreSQL or native MySQL data, select the completed backup and a retained local archive or [recorded remote upload](hosting-database-remote-backups.md#restore-a-recorded-remote-backup), enter **New database name**, and select **Restore into new database**. Verify the restored application can read the snapshot and make new writes. Database recovery does not restore application uploads. Preserve the source database until that proof is complete.

For owned uploads, a delivered storage alert selects the authorized resource's
upload panel and exact archive or failed transfer. Use **Retry remote upload of
the same archive**, then the explicit new-resource restore controls described in
[storage remote recovery](hosting-storage-remote-backups.md#restore-and-verify-a-new-resource).
An archive-failure link selects its source and recovery context; opening it does
not release a hold or start another operation.

A failed dump does not create a completed archive to restore. An uncertain source requires inspection before another operation. If delivery ends as `failed` or `suppressed`, inspect its safe failure guidance and current channel configuration. Fix configuration and send a distinct explicit test. Three consecutive real-event configuration failures disable the channel. Edit and save it to reset the counter and deliberately enable delivery again. A new test does not replay an old failed real event.

## Source events

`backup_failed` covers an accepted backup operation that settles as `failed` or `uncertain`. `upload_failed` covers an accepted upload transfer with either outcome. The immutable `source_state` records the distinction. Uncertain messages say that the outcome needs attention.

The source transaction inserts an event, the selected channel generations, and a safe audit entry together. The event references the exact accepted runner receipt. Controller decisions retain a separate immutable source receipt with the operation state, failure code, and plan hash. These decisions include upload admission denied after queueing and retirement of an already accepted scheduled backup.

Storage adapters use `storage_backup` and `storage_transfer` source kinds with
the same `backup_failed` and `upload_failed` selectors. Accepted storage
`reconciliation_required` evidence projects an uncertain notification state and
keeps ownership held. Restore, download, validation, and discovery-only skips
remain excluded. Database source behavior is unchanged.

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

## Verification boundary

The [transactional event adapters](../internal/store/postgres_hosting_backup_notification_events.go),
[delivery worker](../internal/store/postgres_hosting_backup_notification_worker.go),
[transport](../internal/notificationtransport/transport.go), and
[PostgreSQL tests](../internal/store/postgres_hosting_backup_notifications_test.go)
define source association, current authority, retry budgets, encryption binding,
recipient resolution, and safe failures. Local transport tests cover certificate
validation, DNS rebinding, private-address policy, redirect rejection, SMTP TLS,
and redaction. These checks are separate from actual backup-failure delivery.

<a id="accepted-qualification-and-durable-evidence"></a>
<a id="accepted-scheduled-storage-alerts"></a>
<a id="upstream-mapping"></a>
<a id="measured-delivery-cost"></a>

## Tests and supported environments

The PostgreSQL profile of the [runtime scenario](../internal/api/hosting_backup_notifications_runtime_integration_test.go)
checks SMTP and HTTPS webhook configuration, explicit tests, and delivery of real
backup and upload failures. Receipt replay and controller restart preserve
notification identity. Opening a delivered recovery link starts no operation;
an owner explicitly restores into a fresh database, and a distinct application
reads the snapshot and makes new writes.

The physical PostgreSQL dump-failure scenario settles as `uncertain`. Messages and browser
history retain that state. It does not prove both definite and uncertain dump
failure. Explicit test events separately exercise retry exhaustion, channel
disablement, generation changes, and removed-recipient suppression.
[Storage schedules](hosting-storage-backup-schedules.md) test actual archive and
upload failures that settle as `failed`; uncertain storage receipts and held
ownership have separate PostgreSQL test coverage.

The tested profile is native Linux arm64, PostgreSQL 17.11, trusted rootful
classic-store Docker, and same-destination Silo through loopback HTTP. SMTP and
webhook receivers use a private CA and explicit fixture TLS origins. Production
public-CA deployment, production S3 HTTPS, other engines, architectures, providers,
events, channels, and cross-channel escalation are unqualified.


## Native MySQL notification qualification

At `38f12664` on 2026-10-28, the native MySQL profile sent actual `mysqldump` and
remote-upload failures to TLS email and HTTPS webhook receivers. The denied
upload preserved the successful local dump. Source receipt replay and controller
restart preserved event and delivery identities. Actual alerts retried transient
receiver rejection. Explicit test events exercised retry exhaustion, channel
revision, and current-recipient safeguards through the existing notification flow. The source application retained records, new writes, exact
uploaded files, and its selected trusted HTTPS route.

The browser configured both channels, inspected real failure history and
authenticated recovery context, and checked viewer restrictions and responsive
layouts at 320, 390, 430, and 1440 pixels. These were assertions, not screenshots.
It did not execute MySQL recovery. Actual same-artifact upload retry retains the
[earlier remote-copy qualification](hosting-database-remote-backups.md#native-mysql-remote-copy-qualification).

Physical source failures were immediate. Scheduled failure, accepted retirement,
and skipped-discovery distinctions have focused PostgreSQL-backed tests rather
than timed runtime proof. Unique event and delivery rows do not make external
email or webhook effects exactly once; delivery remains at least once after a
lost acknowledgment. Validation, restore, download, retention, and discovery-only
skips remain excluded. This adds no Compose MySQL backup support.

The profile uses native Linux arm64, rootful same-host Docker, fixture-CA HTTPS,
and controlled TLS receivers. Production providers and public-CA deployment
remain unqualified. Native full lint reported seven existing proxy findings, and
Linux arm64 full lint reported one existing proxy-test finding. Both new-finding
checks passed against the accepted base; affected tests, build, and exact reviews
passed. These full-lint failures remain recorded in the
[evidence index](/Users/bubble/.local/share/nerocd/acceptance/runtime-hosting-mysql-notifications-20261028/38f1266437a24b6ef8a78b8e2b8b09e492db789a/evidence-index.json).
