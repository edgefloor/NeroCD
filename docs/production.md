# Production deployment

`compose.production.yaml` is separate from development. It uses a digest-pinned
image, has no application/database/metrics host ports, and expects TLS through
an external reverse-proxy network.

## Prepare inputs

Copy `.env.production.example` outside the checkout. Set `NEROCD_IMAGE` to an
immutable `repository@sha256:<digest>`, set the proxy network and HTTPS public
origin, and choose distinct migration-owner and application database roles.

Create the owner database URL, app database URL, and PostgreSQL password files
at the referenced absolute paths. Keep them operator-owned and mode 0400. The
profile copies each to private runtime volumes: migrator and backup tooling see
the owner credential; the server sees the app credential.

```sh
set -a; . /secure/nerocd-production.env; set +a
NEROCD_MODE=production NEROCD_IMAGE_REF="$NEROCD_IMAGE" \
NEROCD_OWNER_DATABASE_URL_FILE="$NEROCD_DATABASE_URL_SECRET" \
NEROCD_APP_DATABASE_URL_FILE="$NEROCD_APP_DATABASE_URL_SECRET" \
NEROCD_OWNER_DATABASE_USER="$NEROCD_OWNER_DATABASE_USER" \
NEROCD_APP_DATABASE_USER="$NEROCD_APP_DATABASE_USER" nerocd doctor
docker compose --env-file /secure/nerocd-production.env -f compose.production.yaml up -d
```

`doctor` checks local configuration without connecting to PostgreSQL or printing
secrets. The profile migrates, provisions the app role, then starts the server.

## Proxy, bootstrap, and OIDC

Expose the server only through the TLS proxy on `NEROCD_PROXY_NETWORK`. Set
`NEROCD_TRUSTED_PROXY_CIDRS` only for immediate proxy ranges. Never publish
`/metrics`; it requires an authenticated `system_admin` credential.

The base Compose file does **not** forward OIDC variables or mount an OIDC
secret. To enable OIDC, an operator-reviewed Compose override for `server` must
supply issuer, client ID, and client-secret file, and mount a read-only 0400
secret reachable by UID 10001. Adding values to the env file alone does nothing.
Keep the HTTPS public origin exact. See [OIDC](oidc.md).

Bootstrap the first administrator exactly once. `database-tools` runs as UID
10001, so it cannot safely read an operator-owned `0400` host file through a
bind mount. Keep the password private on the host and redirect it to the
existing standard input option; it never becomes a command argument or a
container file. Export the public origin explicitly because this one-shot tool
needs the same production configuration as the server:

```sh
set -a; . /secure/nerocd-production.env; set +a
docker compose --env-file /secure/nerocd-production.env -f compose.production.yaml \
  --profile tools run --rm -T -e NEROCD_PUBLIC_ORIGIN="$NEROCD_PUBLIC_ORIGIN" \
  database-tools bootstrap-admin \
  --email admin@example.com --name 'Initial Admin' --password-stdin \
  < /secure/bootstrap-password
```

Remove the host password file after a successful bootstrap.

## Database operations

The tools profile is opt-in and must always select the production files:

```sh
docker compose --env-file /secure/nerocd-production.env -f compose.production.yaml \
  --profile tools run --rm -T -v /secure/nerocd-backups:/backups \
  database-tools backup --output-dir /backups
```

Create an isolated empty target for restore and follow the disposable-target
safeguards in [PostgreSQL operations](postgres-operations.md). Never restore
into the running production database.
