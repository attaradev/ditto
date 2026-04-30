# Operate a ditto host

Use this guide when you are running ditto on the machine that owns the dump file and the
Docker-compatible runtime.

## Host checklist

Before you start, confirm that the host has:

- a Docker-compatible runtime
- network reachability to the source database
- local storage for the dump file and SQLite metadata store
- a non-root operator account with controlled access to the Docker socket

## Minimal configuration

Create `/etc/ditto/ditto.yaml` or `~/.ditto/ditto.yaml`:

```yaml
source:
  engine: postgres
  host: db.example.com
  port: 5432
  database: myapp
  user: ditto_dump
  password_secret: env:DB_PASSWORD

dump:
  schedule: "0 * * * *"
  path: /var/lib/ditto/latest.gz
  stale_threshold: 7200

copy_ttl_seconds: 7200
port_pool_start: 5433
port_pool_end: 5600

server:
  enabled: true
  addr: ":8080"
  advertise_host: ditto.internal
  db_bind_host: 0.0.0.0
  copy_secret_secret: env:DITTO_COPY_SECRET
  auth:
    # Option A — simple shared secret (evaluation and single-operator use).
    # If static_token is set, ditto uses it and ignores the OIDC fields below.
    static_token: env:DITTO_STATIC_TOKEN
    # Option B — OIDC (recommended for production multi-user environments).
    # Remove static_token when you switch to OIDC.
    # issuer: https://issuer.example.com/
    # audience: ditto-ci
    # jwks_url: https://issuer.example.com/.well-known/jwks.json
    # admin_claim: role
    # admin_value: ditto-admin
  db_tls:
    # Required in current shared-host mode.
    cert_file: /etc/ditto/tls/server.crt
    key_file: /etc/ditto/tls/server.key
```

Use secret references instead of inline plaintext for long-lived hosts. In the current
implementation, shared-host mode also requires `server.db_tls.cert_file` and
`server.db_tls.key_file`, so provision a certificate whose subject matches `server.advertise_host`.
See
[Configuration reference](../reference/configuration.md#secret-references).

Optional target refreshes can be configured on the host. This lets an admin refresh a staging or QA
database from the cached dump:

```yaml
targets:
  staging:
    engine: postgres
    host: staging.example.com
    port: 5432
    database: myapp
    user: ditto_refresh
    password_secret: env:DITTO_TARGET_PASSWORD
    allow_destructive_refresh: true
```

## Run the controller

Run `ditto host` under a service manager so one process owns dump refresh, warm-pool refill, TTL
expiry, orphan recovery, and the `/v2` API:

```ini
[Unit]
Description=ditto host
After=network-online.target docker.service
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/ditto host
Restart=on-failure
User=runner
WorkingDirectory=/home/runner

[Install]
WantedBy=multi-user.target
```

Install and start it:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now ditto
```

If you only need scheduled dump refreshes and not automatic copy cleanup, a cron job is enough:

```cron
0 * * * * /usr/local/bin/ditto reseed >> /var/log/ditto-reseed.log 2>&1
```

Clients supply the token via `DITTO_TOKEN`:

```bash
# Static token mode
export DITTO_TOKEN="$DITTO_STATIC_TOKEN"
export DITTO_SERVER=http://ditto.internal:8080
ditto copy create

# OIDC mode (production)
export DITTO_TOKEN="$(cat oidc.jwt)"
ditto copy create --server=http://ditto.internal:8080
```

Protect the service with network policy appropriate to the published DB ports. See
[SECURITY.md](../../SECURITY.md).

Non-admin callers can list and destroy only their own copies. The shared-host `/v2/status` endpoint and target refresh endpoint require an admin-capable token.

## Runner setup

For Docker Engine on Linux, the runner user must be able to access the Docker socket:

```bash
sudo usermod -aG docker runner
```

Restart the runner service or log out and back in after changing group membership.

## Database user grants

The dump user needs read-only access. Replication privileges are not required.

PostgreSQL:

```sql
CREATE USER ditto_dump WITH PASSWORD 'secret';
GRANT CONNECT ON DATABASE myapp TO ditto_dump;
GRANT USAGE ON SCHEMA public TO ditto_dump;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO ditto_dump;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO ditto_dump;
```

MySQL / MariaDB:

```sql
CREATE USER 'ditto_dump'@'%' IDENTIFIED BY 'secret';
GRANT SELECT, SHOW VIEW, EVENT, TRIGGER ON myapp.* TO 'ditto_dump'@'%';
FLUSH PRIVILEGES;
```

## Daily operations

These commands cover most operator checks:

```bash
ditto doctor          # full diagnostic: Docker, config, dump freshness, source DB, OIDC
ditto doctor --server http://ditto.internal:8080
ditto status          # quick capacity and dump age summary
ditto copy list
ditto copy logs <id>
ditto target refresh staging --dry-run --confirm staging
```

Use `ditto reseed` for an immediate refresh outside the normal schedule.
Use `ditto target refresh staging --confirm staging` only when you intend to clean and restore that
target database.

## Related reading

- [Architecture and operating model](../explanation/architecture.md)
- [Troubleshoot ditto](troubleshoot.md)
- [Contributing](../../CONTRIBUTING.md)
