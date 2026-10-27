# Build on GitHub push

Build on push admits a build for a verified GitHub commit. By default, deployment is manual. Review the successful build's commit and artifact before deploying it. Owners can opt into [automatic deployment](hosting-auto-deploy.md) for an application with a reviewed healthy baseline and existing HTTPS routes. A failed build preserves the serving application.

Build on push supports GitHub.com HTTPS repositories and one exact saved branch. Public repositories can use unauthenticated provider reads. Private repositories require an explicitly selected provider-read credential and the existing source grant. GitHub Enterprise, custom provider URLs, tags, deleted refs, forced pushes, pull requests, fork-origin events, path filters and automatic webhook registration are unsupported. Repositories that are themselves forks are also unsupported, including owner-controlled forks.

The comparison proof supports at most 100 commits between the saved desired commit and current branch head. Larger advances block with `comparison_limit`.

## Prepare the runner and credentials

Configure the existing trusted build policy and build tools on the enrolled runner. Its current server-side enrollment must include `hosting_build_v1` and `hosting_build_trigger_revision_v1`, plus the deployment capabilities needed for manual deployment. Fresh privileged CLI registration can advertise the trigger capability. Credential-file and enrollment modes retain the capabilities already persisted by the server. Restarting an older runner with new flags does not grant a persisted capability. Use the authorized enrollment and capability configuration workflow. An unsupported current runner produces an actionable admission conflict.

The current manual enrollment helper intentionally withholds build commands until operator configuration exists. This feature does not complete runner onboarding or install build tools.

Create the following through the existing authorized managed-secret screen:

- A webhook signing secret, 32 to 256 bytes. Select an immutable version for webhook HMAC use. Use the existing authorized reveal action to copy it into GitHub's webhook setup.
- A provider-read secret when required. Its value uses the existing strict JSON credential shape, `{"username":"your-user","password":"your-read-token"}`. The password is sent as a Bearer token to the fixed GitHub REST API. Select this purpose separately from webhook signing and source checkout.

Do not put credentials into the repository URL or policy form. The same underlying managed-secret version can be used for source checkout and provider reads only when separately selected for each purpose. No runner credential delivery or reveal endpoint is used to obtain provider credentials. Public provider reads may be unauthenticated when the optional provider selector is omitted; rate limiting remains possible.

Select an active project service account with maintainer or owner permission as the automation actor. Its owning user must remain active. For a private source, create the existing exact source grant before configuring the policy. Missing provider-read credentials are a setup error, not an empty credential.

## Configure, verify and enable

1. Save the Git resource with its exact branch and bind it to the intended destination.
2. Open **Build on push** on the resource's builds page. Select the automation service account, webhook signing version, and optional provider-read version. Select the private source grant when required.
3. Explicitly acknowledge resetting the branch baseline and save. Configuration is always saved disabled. Reconfiguration clears the old verified baseline. Review the current branch before verifying again; a reset is a deliberate acceptance of a new baseline, including a divergent history.
4. Verify the repository identity and branch. The server reads the numeric repository ID and branch head from `api.github.com`, independently of webhook payload URLs. Verification establishes the baseline without creating a build. Enable within 60 seconds of successful verification.
5. Copy the fixed callback URL and manually register a GitHub JSON webhook for push events, using the selected signing value. A correctly signed ping is acknowledged as ignored. Enable the policy after verification. Disabled policies do not admit builds.
6. Read delivery history and build status on the resource. Viewers can read metadata and history; only current owners or administrators receive configuration selectors or can change policy state.

Owner commands use current generations and stable request IDs. On a response loss, retry the exact command body and request ID. A conflict requires refreshing and reviewing current state. Do not silently rebase a write onto a newer generation. Verification has an overall 20-second deadline, a 30-second attempt token, and a five-second timeout per provider request. A timeout can leave a durable command receipt or an unfinished attempt, so it does not prove that no work happened.

The callback uses the configured public origin. Configure that origin before copying the URL. Configure TLS for the public callback in the normal server deployment.

## Delivery ordering and recovery

The signature is HMAC-SHA256 over the exact bounded raw request body and is checked before parsing or retaining delivery metadata. GitHub's [signature validation guide](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries) describes the signing contract. The body is limited to 1 MiB. Signatures, raw payloads, payload URLs and plaintext credentials are not retained in delivery rows, audit metadata or job arguments.

A push can admit only its own `after` commit, and only while that commit equals the independently verified current branch head. If delayed event A arrives after the branch has advanced to B, history records `state=ignored`, `reason=obsolete`, and no build ID. Desired B stays unchanged. Another delivery for the same verified B commit coalesces with the existing B build. Event arrival order, timestamps and lexical SHA order never determine desired state.

The desired commit and pinned build admission commit in the same PostgreSQL transaction using the existing build admission checks. The immutable comparison validates the base, merge base, status, counts and terminal returned commit. It uses the fixed API version `2026-03-10` and the documented [branch](https://docs.github.com/en/rest/branches/branches#get-a-branch) and [comparison](https://docs.github.com/en/rest/commits/commits#compare-two-commits) contracts. Unsupported or inconsistent proof blocks admission.

Provider rate limits, unavailable heads and unavailable ancestry leave desired state unchanged. The policy and delivery show a closed reason such as `rate_limited`, `repository_or_branch_unavailable`, `comparison_limit` or `ancestry_unverified`. Automatic retry starts at 10 seconds and backs off to at most 300 seconds. Each pending delivery has at most 32 attempts and a 24-hour window. Exhaustion becomes `blocked_exhausted` and releases pending capacity. After correcting the provider or credentials, a current owner can explicitly retry a delivery for one new bounded window. Repeating that retry request ID returns the original renewed window; it does not extend it again. Retry does not enable a policy, reconfigure it, or erase durable build deduplication. Disable and reconfigure explicitly when source intent or delegation must change.

There are at most 16 pending deliveries per resource and 64 per project. Retained delivery receipts are capped at 256 per resource and 1,024 per project, with terminal retention up to 72 hours. New ingress returns a capacity error with `Retry-After: 10` rather than evicting pending work. Terminal receipts can be removed earlier under those caps. The durable policy-generation/source-generation/commit build identity survives receipt cleanup, independently of River job retention. The existing bounded hosting queue and recovery loop process opaque intent IDs.

Secret access rechecks current policy, source, actor, project, grants and selected versions before decrypting. A bounded idempotent audit entry records authorization of each immutable policy-generation/secret-purpose binding. It records the authorized scope, not every rejected unauthenticated request. Revocation prevents subsequent authorized use; it cannot erase bytes already used by a completed request. Plaintext is held only for the bounded request and owned byte buffers are cleared where possible. Go cannot guarantee erasure of string or runtime copies.

## Local qualification

The test fixture uses disposable owned PostgreSQL and Docker-in-Docker resources, the compiled runner, real Git checkout and build tools, and registered API routes. Its GitHub provider is simulated over controlled TLS. Fixture-only DNS, source routing policy and certificate trust bind the exact GitHub.com source URL to a local Git CGI server. This is not a live GitHub integration claim. No production route exposes fixture commands or accepts caller-selected fixture targets.

The browser fixture exercises configuration, verification, enablement, duplicate and delayed deliveries, explicit manual artifact deployment and a genuinely failed later build. An independent application HTTP probe must confirm that the exact B artifact still serves after C fails. Metadata or a readiness label alone does not establish that result.

Run `bash acceptance/runtime-hosting-github-push/gate.sh` from the assigned repository root with the task-owned PostgreSQL fixture on port 32963. The script verifies its exact container ID and owner label, compiles the runner and API test, and creates a separate owned DinD daemon. It checks cgroup v2 delegation without sharing host PIDs or a host Docker socket. Dockerfile strategy is exercised; the otherwise unused planner image identity does not establish Railpack execution support.

The combined backend and UI checkout can run the same gate with `NEROCD_GITHUB_PUSH_BROWSER=1`. Build and embed that checkout's current web assets first. The browser bridge exposes only fixed commands after current owner-session, Origin and normal CSRF checks. Its metadata contains resource and commit identities, not credentials. On interruption, the gate signals only the verified fixture executable and waits for its runner and API to join before removing the owned daemon and anonymous volumes. It preserves the separately owned PostgreSQL container and shared images.
