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
  addr: ":8080"
  token_secret: env:DITTO_TOKEN
```

Use secret references instead of inline plaintext for long-lived hosts. See
[Configuration reference](../reference/configuration.md#secret-references).

## Keep dumps fresh

Run the daemon under a service manager so dumps refresh and expired copies are cleaned up:

```ini
[Unit]
Description=ditto daemon
After=network-online.target docker.service
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/ditto daemon
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

## Expose remote copy creation

Run the API service when CI runners or developer machines should request copies from this host:

```bash
ditto serve
```

Clients then use:

```bash
export DITTO_TOKEN=my-secret-token
ditto copy create --server=http://ditto.internal:8080
```

Protect the service with a token and network policy. See [SECURITY.md](../../SECURITY.md).

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
ditto status
ditto copy list
ditto copy logs <id>
```

Use `ditto reseed` for an immediate refresh outside the normal schedule.

## Related reading

- [Architecture and operating model](../explanation/architecture.md)
- [Troubleshoot ditto](troubleshoot.md)
- [Contributing](../../CONTRIBUTING.md)
