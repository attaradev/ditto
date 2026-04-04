# Operations

A typical ditto deployment runs one host with a Docker-compatible runtime, the local dump file, the
SQLite metadata database, and `ditto daemon`. The daemon keeps the dump fresh on the configured
schedule and removes expired copies automatically. See the
[architecture diagram](../README.md#how-it-works) for an overview of the full flow.

---

## Keep dumps fresh

### systemd service

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

Save to `/etc/systemd/system/ditto.service`, then:

```sh
systemctl daemon-reload
systemctl enable --now ditto
```

### Cron alternative

If you only need scheduled dumps without TTL cleanup, a cron job is sufficient:

```cron
0 * * * * /usr/local/bin/ditto reseed >> /var/log/ditto-reseed.log 2>&1
```

---

## Runner setup (GitHub Actions self-hosted)

The runner user must be able to reach the configured Docker runtime socket. For Docker Engine on Linux:

```bash
usermod -aG docker runner
```

Restart the runner after changing group membership.

---

## Database user setup

The dump user needs read-only access. Replication privileges are not required.

**PostgreSQL:**

```sql
CREATE USER ditto_dump WITH PASSWORD 'secret';
GRANT CONNECT ON DATABASE myapp TO ditto_dump;
GRANT USAGE ON SCHEMA public TO ditto_dump;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO ditto_dump;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO ditto_dump;
```

**MySQL / MariaDB:**

```sql
CREATE USER 'ditto_dump'@'%' IDENTIFIED BY 'secret';
GRANT SELECT, SHOW VIEW, EVENT, TRIGGER ON myapp.* TO 'ditto_dump'@'%';
FLUSH PRIVILEGES;
```

---

## Adding a new database engine

See [Contributing — adding a new engine](../CONTRIBUTING.md#adding-a-new-database-engine) for the
full step-by-step guide, including the eight methods to implement and how the engine registry works.
