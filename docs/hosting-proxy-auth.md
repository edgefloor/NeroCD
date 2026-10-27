# Hosting proxy mutation authorization

Proxy mutations recheck persisted authority inside the transaction that writes
proxy state, audit evidence, and queued work. Application authorization still
runs before the repository call.

Human proxy administrators need both a current server administration role
(`runner_admin` or `system_admin`) and ownership of the control project.
`system_admin` retains its existing project membership override. Resource route
mutations require the resource project's maintainer role. Existing project,
destination, proxy, and resource ownership locks remain in place.

Legacy API tokens authenticate as their own durable token IDs. An active,
unrevoked legacy token with the persisted `system_admin` role can use the same
global override already granted by application authorization. The repository
requires `service_account_id IS NULL` and checks that the target project still
exists and is not archived. A `runner_admin`, `owner`, or other role string does
not create project membership. Tokens never inherit their issuer's membership
or current roles.

Managed service-account credentials authenticate as the stable account ID and
retain their existing project scope, role, and active-owner requirements. They
do not enter the legacy-token branch. Runner credentials cannot use the legacy
administrator path either. Each credential type retains its own authentication and authority checks.

The legacy token row is locked for the authorizing transaction. Revocation and
role changes therefore serialize with the mutation: a change committed first
is observed by admission; a mutation holding the token lock can commit before
the waiting revocation. Expiry does not require a writer, so the repository
checks `clock_timestamp()` again after all potentially blocking work,
immediately before commit or an idempotent return. This includes destination,
proxy, resource, queue, and audit waits. Expired transactions roll back desired
routes, generation increments, operation rows, River jobs, and audit writes.

A replay still requires current authority. Successful replays retain the
existing operation and do not create another generation or audit event.
Authorization queries select roles and validity only; they do not read token
plaintext or hashes, and this path adds no credential logging or public fields.
