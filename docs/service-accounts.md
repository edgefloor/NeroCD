# Service accounts

Managed service accounts give automation a stable project identity. Each account belongs
to exactly one project and has one of that project's existing roles: `owner`,
`maintainer`, or `viewer`. The assigned role governs project operations, including
updates and archival.

Managed service accounts have no global roles. They cannot manage service accounts,
create projects, manage human membership or offboarding, or use global administration
operations. Those operations require an eligible human.

## Lifecycle

A system administrator or a human owner of the project can manage its service accounts
in **Settings → Service accounts**. Creating an account returns one bearer token. NeroCD
displays that plaintext token once; copy it into the automation's secret store before
closing the dialog. The database stores only its hash.

Credentials expire after 90 days by default. An explicit expiry must be in the future
and no more than 365 days away.

Rotate an account to atomically revoke every previous credential and receive a new
one-time token. Revoke the account to invalidate its credential and prevent new
authentication. You can change the name or project role through Settings. The account
ID, project and owning user are immutable.

Send the credential with API requests in the standard header:

```text
Authorization: Bearer ncd_...
```

The service account's stable ID is recorded as the actor on runs and deployments.
Rotation changes only the credential, so audit and provenance records continue to
identify the same account. Authorization reads the account's current project role for
every request; role changes and revocation therefore take effect without waiting for an
existing token to expire.

## Ownership and offboarding

The human user who creates a service account is its immutable owner. Disabling that user
atomically revokes their sessions, legacy API tokens, and every managed service account
they own. The account, credentials, and historical actor references remain for
auditability. Transfer of service-account ownership is not supported; create a
replacement account under an active owner when responsibility changes.

Legacy API tokens retain their existing issuer-owned behavior and role claims. They
remain distinct from managed service accounts and do not acquire a project identity.