# Enterprise OIDC sign-in

NeroCD supports one explicitly configured OpenID Connect provider for browser sign-in. Local password sign-in stays available as an operator recovery path. OIDC users use the existing 12-hour revocable NeroCD sessions and the existing local role and project membership model.

## Provider configuration

Set all of these values or none of them:

```sh
NEROCD_OIDC_ISSUER_URL=https://idp.example/realms/operations
NEROCD_OIDC_CLIENT_ID=nerocd
NEROCD_OIDC_CLIENT_SECRET_FILE=/run/secrets/nerocd-oidc-client-secret
NEROCD_PUBLIC_ORIGIN=https://nerocd.example.com
```

The client secret file must be a regular owner-only file. NeroCD derives the only allowed callback URL from the public origin:

```text
https://nerocd.example.com/api/v1/oidc/callback
```

Register that exact callback with the provider and use the authorization-code flow. NeroCD always sends an S256 PKCE challenge and verifies the signed ID token's issuer, audience, expiry, and nonce. Production requires HTTPS for the issuer, discovered endpoints, and callback. Development permits HTTP only on loopback addresses.

The issuer is an exact protocol identifier. Copy the provider's discovery issuer exactly, including a trailing slash when it has one. Provider authorization, token, and JWKS endpoints may use different HTTPS hosts. NeroCD rejects endpoint user information, fragments, insecure production endpoints, and HTTPS redirects that downgrade to HTTP.

Configuration shape is validated at startup, but network discovery is lazy and timeout-bounded. A transient provider or discovery outage makes OIDC login fail with a generic error while local recovery sign-in continues to work. `nerocd doctor` validates local configuration without contacting the provider.

### Keycloak example

For a Keycloak realm named `operations`, use its realm issuer, commonly:

```text
https://keycloak.example.com/realms/operations
```

Create a confidential OpenID Connect client, enable the standard authorization-code flow, disable direct access grants, and register only the exact NeroCD callback URL. Put the generated client secret in the owner-only file. Keycloak's optional `session_state` and RFC 9207 `iss` callback parameters are accepted; the issuer parameter is checked exactly when present.

## Explicit identity provisioning

NeroCD never creates or links users from ID-token email, name, group, or role claims. Provision the provider-owned `sub` value before the user signs in:

```sh
NEROCD_DATABASE_URL='postgres://...' nerocd oidc-provision \
  --issuer 'https://idp.example/realms/operations' \
  --subject 'provider-owned-stable-subject' \
  --email 'operator@example.com' \
  --name 'Operations User'
```

This creates an active nonprivileged local user with the `user` global role and an atomic audited binding. Assign project membership through NeroCD's existing authorization controls. To bind a provider subject to an existing local user without changing its name, email, password, or roles:

```sh
NEROCD_DATABASE_URL='postgres://...' nerocd oidc-provision \
  --issuer 'https://idp.example/realms/operations' \
  --subject 'provider-owned-stable-subject' \
  --user-id 'usr_existing'
```

Issuer and subject are exact identifiers. Surrounding whitespace in a subject is rejected instead of normalized. Identity, user, or email collisions fail; NeroCD does not rebind them.

## Security and operating limits

Login state, nonce, and PKCE verifier hashes are stored in a five-minute single-use database transaction. The plaintext verifier exists only in a short-lived host-only, HttpOnly, SameSite=Lax browser cookie scoped to `/api/v1/oidc`. The database does not store the verifier or any access, refresh, or ID token. Callback errors and audit reason codes are bounded and do not contain provider descriptions, token values, authorization codes, raw subjects, nonces, or client secrets.

Unknown or disabled identities cannot create sessions. State, browser binding, nonce, token verification, expiry, and replay failures fail closed. Provider logout, identity removal, SCIM, SAML, LDAP, automatic signup, claim-driven role or group mapping, multiple issuers, and provider administration UI are outside this first release. NeroCD does not currently expose a user-disable or external-identity removal command. When offboarding, first disable access at the identity provider. Before the final session-revocation pass, allow already-started sign-ins to settle: wait through NeroCD's five-minute login-flow window and the provider's authorization-code validity period, plus the bounded token exchange already in progress. Then a system administrator must list and revoke the user's remaining active sessions through `GET /api/v1/sessions` and `POST /api/v1/sessions/revoke`. Removing or disabling the provider-side account alone does not revoke an already issued NeroCD session. If the identity is bound to a user with local credentials, operators must address those credentials separately because NeroCD has no user-disable command.
