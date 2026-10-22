# Configure managed proxy routing in the WebUI

This UI uses the accepted proxy control API. This checkpoint queues proxy work;
it does not enable proxy execution on agents. A queued operation does not prove
that routing is loaded, DNS resolves, or trusted HTTPS is ready.

## Configure a server

Before configuration, the enrolled runner must advertise `proxy_apply`. An
image-only runner is rejected. Arrange supported proxy execution and enrollment
with the server operator; this UI does not broaden existing runner grants.

1. Open **Runners**, select a hosting server, and find **Managed proxy**.
2. Enter an existing immutable control project ID, ACME email, certificate
   authority, and loopback management port. Optionally enter a generated hostname
   domain. These settings and placement cannot be changed after configuration.
3. Select **Configure managed proxy**. Both server administration and owner
   permission in the control project are required. The existing system
   administrator override applies; runner administration alone is insufficient.
4. Review desired and loaded generations separately. An operation may remain
   queued until the exact enrolled runner supports and executes `proxy_apply`.
   This UI does not add that capability to enrollment presets or existing grants.

A configured control project cannot be archived, including while the proxy is
disabled. The management port is local to the runner. No upstream URL, socket,
filesystem path, credential, or CA trust override is accepted from route forms.

## Delegate hostname authority

Enter the project ID and an exact hostname, or explicitly select descendant
namespace authority. Names and descendant namespaces cannot overlap across
projects. A namespace grant authorizes individual hostnames, not wildcard
certificates. Grants do not prove DNS ownership. Arrange DNS separately.

Generated route names require both a configured generated domain and a matching
active wildcard namespace grant for the resource project. Requesting a generated
name cannot create that authority.

Revoking a grant queues withdrawal of affected routes. Review and confirm the
exact grant before submitting. Existing routing can remain loaded while the
withdrawal is pending or held. Disabling the proxy similarly queues withdrawal;
it does not delete persistent certificate data.

## Request a resource route

1. Open **Projects**, select the project and shared environment, and find the
   application’s **Resource routes** section.
2. Deploy an image and wait for a settled healthy deployment. Saved configuration
   and queued deployment records are insufficient. The prerequisite selector has
   bounded deployment-history pages; refresh or browse those pages if necessary.
3. Enter an authorized hostname and canonical literal path prefix, or request a
   generated hostname under the configured and granted namespace.
4. Select the exact settled healthy deployment and **Save desired route**.
   The server derives placement, release identity, and upstream port. Project
   maintainers and owners may submit; viewers read status only.
5. Review desired route version, operation state, HTTP readiness, and trusted
   HTTPS readiness separately. A loaded configuration can still await ACME.
   Default, self-signed, untrusted, or expired certificates do not prove trusted
   HTTPS readiness. These are recorded observations, not continuous monitoring.

Editing captures the exact current route version. A replacement preserves the
previous serving release until a verified proxy generation switches references.
Withdrawal requires confirmation naming the route, hostname, path, and version;
it does not immediately prove that prior routing has stopped.

## Recover a request

An unconfirmed response preserves the exact request body, target, and idempotency
key for explicit identical retry. Editing a frozen request cannot silently change
its intent. A conflict refreshes metadata but never automatically resubmits.
Review refreshed information explicitly before starting a separate request.
Drafts survive validation and availability failures. Identity, target, or write
permission changes discard pending form intent and ignore stale responses.
Hostname grants do not accept idempotency keys; after a failure, refresh and
inspect grants before another submission.

Mutation uncertainty retains ownership. Server administrators who also own the
control project may request cancellation of the exact proxy operation; cancellation
does not release admitted ownership. Database restore holds have no unlock API.
The UI exposes the hold and pending recovery proof rather than inventing a clear
or retry action that could authorize a new runtime mutation.

## Verification boundary

The checkpoint browser fixture verifies admission refusal for an image-only
runner and explicit prerequisite guidance at mobile and desktop widths. The
positive configure/grant/route journey is present but skipped until supported
proxy runtime integration is accepted. It is not a completed browser validation
of those positive workflows.

The browser fixture uses disposable PostgreSQL/River, supported human account
flows, actual UI enrollment and an actual image runner/container. It verifies the
image over loopback HTTP before requesting routes. Proxy operations intentionally
remain queued without `proxy_apply`; screenshots and assertions do not claim live
proxy execution or HTTPS. No enrollment secret is included in screenshots or traces.
