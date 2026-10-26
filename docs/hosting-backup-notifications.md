# Hosting backup notifications

Project notification channels deliver accepted PostgreSQL backup failures and remote upload failures through SMTP email or a generic HTTPS webhook. An upload failure leaves the successful backup and its local-copy records intact. Notification delivery runs after the source transaction commits.

This guide covers project setup, failure history, and explicit recovery for the two supported PostgreSQL failure events. The contract and qualification limits follow the workflow.

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

1. Open **Notification delivery history** and identify the backup or upload event. **Inspect delivery** shows the attempts, their safe failure codes, and timestamps. The panel lists the latest 100 deliveries.
2. Distinguish source outcome from delivery outcome. `delivered` means the receiver acknowledged the notification. A failed or uncertain backup remains failed or uncertain even when its alert was delivered. An upload failure can leave a successful local backup available.
3. Follow **Open authenticated backup recovery** from the message or history. Sign in if required. The page checks current project, environment, resource, backup, and transfer scope before showing linked history. Opening a link does not retry an upload or start a restore. Viewers can inspect authorized history. Owners perform recovery actions.
4. For an upload failure with an available local copy, inspect the linked successful backup and failed transfer. Use **Retry upload for** that backup only after checking the target. The retry sends the same archive without another dump. See [remote backup recovery](hosting-database-remote-backups.md).
5. To recover data, select the completed backup and an available archive source, enter **New database name**, and select **Restore into new database**. See [PostgreSQL backup and restore](hosting-database-backups.md). Verify the restored application can read the snapshot and make new writes. Preserve the source database until that proof is complete.

A failed dump does not create a completed archive to restore. An uncertain source requires inspection before another operation. If delivery ends as `failed` or `suppressed`, inspect its safe error and current channel configuration. Fix configuration and send a distinct explicit test. Three consecutive real-event configuration failures disable the channel. Edit and save it to reset the counter and deliberately enable delivery again. A new test does not replay an old failed real event.

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

The reference checkout is Coolify commit `b12d1386f3d458618b4c579babcd1e339c6c40b3`. Its [DatabaseBackupJob](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Jobs/DatabaseBackupJob.php#L466-L544) catches dump failure separately, records local success even when upload fails, and catches notification exceptions without rewriting that success. [BackupFailed](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Notifications/Database/BackupFailed.php) and [BackupSuccessWithS3Warning](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Notifications/Database/BackupSuccessWithS3Warning.php) both select `backup_failure`. The latter's webhook reports local success and an upload warning.

NeroCD replaces Laravel notification jobs with [transactional source events](../internal/store/postgres_hosting_backup_notification_events.go), [durable claims and settlement](../internal/store/postgres_hosting_backup_notification_worker.go), and the [River delivery-ID job](../internal/hostingjobs/backup_notifications.go). It preserves source receipt authority and selects channel generations in PostgreSQL before sending. The [transport](../internal/notificationtransport/transport.go) sends safe message fields and uses NeroCD recovery locators. PHP classes, serialized jobs, payload schemas, and configuration are not reused as a compatibility format.

| Reference behavior | NeroCD behavior |
| --- | --- |
| `DatabaseBackupJob` distinguishes dump failure from successful local dump followed by failed S3 upload. Notification exceptions do not rewrite a completed backup. | Accepted source outcomes and notification delivery commit independently. Upload alerts retain the successful backup and expose safe local-copy availability. |
| `BackupFailed` and `BackupSuccessWithS3Warning` both select `backup_failure` in `HasNotificationSettings`. | Selecting both explicit failure events provides the equivalent coverage. Uncertain outcomes have explicit text and state. |
| [EmailChannel](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Notifications/Channels/EmailChannel.php) resolves `Team.getRecipients` and validates team membership for real notifications. | Selected recipient user IDs resolve to current active project members before each message. Tests keep this restriction too. |
| [NotificationPolicy](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Policies/NotificationPolicy.php) permits member reads and administrator updates and tests. The Email and Webhook Livewire settings persist channel options. | Viewers read safe metadata. Project owners configure, disable, and test. Configuration remains write-only for every role. |
| [CustomEmailNotification](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Notifications/CustomEmailNotification.php) and [SendWebhookJob](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Jobs/SendWebhookJob.php) bound queue attempts at five. Webhook jobs are encrypted and validate safe URLs at send time. | Durable attempt history and budgets survive River retries. Jobs contain only opaque IDs. Each actual dial uses the protected address policy. |
| [HandlesNotificationResponse](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Traits/HandlesNotificationResponse.php) retries rate limits and server failures, counts configuration failures, and does not count payload failures. | Safe permanent and transient classifications retain those distinctions without storing raw receiver bodies or errors. |
| [TracksDeliveryFailures](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Traits/TracksDeliveryFailures.php) disables after three configuration failures, resets on success or settings changes, and escalates through other channels. | Disable and reset behavior is implemented. Escalation remains an explicit gap. |
| [Test](https://github.com/coollabsio/coolify/blob/b12d1386f3d458618b4c579babcd1e339c6c40b3/app/Notifications/Test.php) bypasses event selectors and does not count real delivery failures. | An explicit durable test event bypasses selectors and leaves real-event counters unchanged. |

The implementation uses no new cryptographic algorithm. Configuration replay compares decrypted documents inside the existing encryption boundary, rather than persisting a secret-derived public hash. Source receipts and event selection use NeroCD transactions and existing fenced runner authority.

## Verification boundary

Focused local tests cover strict documents, project permissions, encryption binding, read redaction, HTTPS identity, DNS rebinding, private-address policy, redirect rejection, certificate validation, SMTP TLS, stable identifiers, and safe errors. Race tests cover the transport, hosting jobs, API boundary, and persistence cases.

The retained backend persistence run used a task-owned pinned PostgreSQL 17.6 container, loopback publication, bounded resources, and no host mounts. The real tests cover atomic source and audit rollback, receipt replay, channel generation drift, serial sends, actor revocation, current SMTP recipients, expired claims, original retry budgets, configuration failure counters, accepted controller transitions, and River recovery. The persistence suite contains nine top-level `TestPostgresBackupNotification*` tests, including two direct-controller subcases. The last two tests cover transient SMTP TLS failures and a current member changing email address. At backend `d19b6095f8b639af2f76c856996b2abad65a09e7`, the retained race receipt `/Users/bubble/.local/share/nerocd/acceptance/postgresql-backup-notifications-20261026/accepted-runtime/retained-backend/nerocd-backup-notifications-core-repair-round1-pg.log` records all nine passing tests and `ok nerocd/internal/store 25.097s`. Its SHA-256 is `5e0e59a3efdc3b3bfb2d3f7542e14d2ebb835c7e881afb2be80200c6e4942b19`. The separate fixture owns physical dump and upload proof.

The broader store SQL-placement test still fails on the same thirteen files as the base commit. The notification files add no violations. This existing failure is reported rather than repaired by this feature.

## Accepted qualification and durable evidence

The Leader accepted production `1eb2d01c9262c6bad5a03579545226189f88f1be`, tree `efd94f19a4cb615056d5ef2273f1990367533088`. The full compiled-runner gate passed with ten groups, 33 markers, six browser profiles, and no failed or skipped tests. The notification group `TestHostingBackupNotificationsCompiledRunner` passed in 178.45 seconds. The full observed interval was 675.524 seconds, measured from evidence-directory birth to terminal result modification time. It is not a precise command-start measurement.

The notification browser configures SMTP and HTTPS webhook channels and sends distinct explicit tests. The real runner then produces a successful local dump followed by failed upload. Both transports receive that actual event with stable identities and scoped recovery links. Receipt replay and controller restart preserve delivery identity. The retained archive digest remains unchanged. Opening the link starts no operation. An owner explicitly restores into a fresh private database. A separate application reads the snapshot, writes and reads a new record, and confirms that the source remains readable.

The physical dump failure uses the matching current source container and verifies a nonzero `pg_dump` daemon event. This run settles as `uncertain`, so both messages and final browser history must retain that exact source state. The contract also models definite failure, but this physical dump scenario does not prove both terminal states. The fixture separately proves retry exhaustion through an explicit webhook test, channel disablement and generation-change suppression, removed-recipient suppression, and current project authority. These explicit tests do not substitute for the two actual source events.

Qualification covers native Linux arm64, PostgreSQL 17.11, trusted rootful Docker with the classic image store, and pinned Silo on one destination at explicitly trusted loopback HTTP. Notification receivers use a disposable private CA and explicit fixture TLS origins. This does not qualify production public-CA deployment or production S3 HTTPS. Other engines, architectures, providers, notification channels, deployment/resource/reachability/disk-pressure events, cross-channel escalation, in-place restore, and cross-host or disk-loss recovery remain open. No full R63, A25, or notification matrix acceptance is claimed.

Durable receipts live under `/Users/bubble/.local/share/nerocd/acceptance/postgresql-backup-notifications-20261026/accepted-runtime/`.

| Receipt | SHA-256 | Purpose |
| --- | --- | --- |
| [runtime-test](/Users/bubble/.local/share/nerocd/acceptance/postgresql-backup-notifications-20261026/accepted-runtime/runtime-test) | `30b9b6da3b40ac5416f8d0281059abf96702ac618ba66447c65eadc6396e8c2a` | Named tests and actual event/browser markers |
| [verification-summary.json](/Users/bubble/.local/share/nerocd/acceptance/postgresql-backup-notifications-20261026/accepted-runtime/verification-summary.json) | `6e927682afbce3ee840444d53ca2d048575ad251cd36cc459217db536afea18c` | Accepted profile, prior-check retention, review provenance, and failed-attempt timings |
| [integration-verification.json](/Users/bubble/.local/share/nerocd/acceptance/postgresql-backup-notifications-20261026/accepted-runtime/integration-verification.json) | `6601eda76117e515464a55a147b1cf0dd24f75c7de9a5c32474c4547b5fa9451` | Terminal pass, browser profiles, timing boundary, and preservation checks |
| [integration-source-manifest.json](/Users/bubble/.local/share/nerocd/acceptance/postgresql-backup-notifications-20261026/accepted-runtime/integration-source-manifest.json) | `59a94d39fd10986ed7c42c83666874a1959f60e0aa5eb6eac9977d4d10afff1f` | Exact production source blob and SHA-256 identities |
| [evidence-sha256.json](/Users/bubble/.local/share/nerocd/acceptance/postgresql-backup-notifications-20261026/accepted-runtime/evidence-sha256.json) | `a28d163c67819063ba6bd944a6565ebdb9d2e0b61bd62e9057653ddd6c75a7eb` | Hashes for 51 archived receipts |

[Original receipt mapping](/Users/bubble/.local/share/nerocd/acceptance/postgresql-backup-notifications-20261026/accepted-runtime/original-receipt-mapping.json) preserves the original paths, revisions, and hashes for `retained-backend/`, `combined-1b01/`, and `final-integration/`. Backend race evidence remains labeled `d19b6095f8b639af2f76c856996b2abad65a09e7`. Its 31 approved backend paths, modules, migrations, store, and transport remain byte-identical. Combined checks remain labeled `1b01fa49fc9b149681832668ea2cb6d538ebe23f`, whose relevant production and dependency inputs are unchanged. Later fixture changes have focused reruns, and the final gate rebuilds the actual binaries. The earlier default-concurrency frontend timing failure remains a limit; the bounded full frontend run with `--maxWorkers=2` passed.

The preservation receipt reports no actual changes to the baseline 35 containers and 210 volumes. The recorded user UI identity, image, configuration, host configuration, mounts, state, and network settings are unchanged, and task-owned resources are absent after cleanup. Documentation checks do not rerun these runtime operations.

## Measured delivery cost

Six earlier full candidate attempts failed while repairing the acceptance fixture. These timings are an observed subset of the work, not total feature labor. Each directory is under `/tmp/nerocd-database-runner-20261024.` and retains its gate output.

| Candidate | Directory suffix | Seconds | Observed failure |
| --- | --- | ---: | --- |
| `1b01fa49fc9b149681832668ea2cb6d538ebe23f` | `wo28gynn` | 50.286 | TLS fixture |
| `a0a101f8038b6fc9499fb9edd94274bcaa90ca66` | `z3i6p6mu` | 649.826 | Label selector |
| `0f4d319d19ace21056370ca5fc84cafd87cbcb3f` | `ofjjy9xr` | 48.424 | Test actor permission |
| `5b8023c0dc88b1ab764e4a0d9424c89b47df6d66` | `nim41rwt` | 94.185 | Recovery query order |
| `5360e17130dfb434c20c9807a022cc0771210eb5` | `vg756gnf` | 198.310 | Stale fixture container |
| `5b08349d6cc5286656522f44075cb7ade327f06e` | `bnzw0315` | 210.574 | Wrong history outcome assertion |

Fixture reviews at `1a8b4d1b13972fef2a4665b535ed0ae6ae4bd9bc` took 52, 50, and 60 seconds concurrently. Adding those durations does not measure elapsed review time. Backend and UI work each had one earlier review repair. Token and dollar allocation is unavailable, and these measurements do not identify a model or infrastructure bottleneck.
