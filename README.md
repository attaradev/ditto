# ditto

![ditto](assets/logo.svg)

[![CI](https://github.com/attaradev/ditto/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/attaradev/ditto/actions/workflows/ci.yml)

## A clean database for every run

ditto provisions throwaway Postgres or MariaDB copies from a scheduled dump.
Each run—whether a test suite, a migration dry-run, a load test, or a local
debugging session—gets an isolated, production-faithful database. No shared
state. No leftover mutations. No coordination.

```sh
COPY=$(ditto copy create --format=json)
export DATABASE_URL=$(echo "$COPY" | jq -r '.ConnectionString')
COPY_ID=$(echo "$COPY" | jq -r '.ID')

go test ./...
ditto copy delete "$COPY_ID"
```

## Use cases

| Use case | What ditto does |
| --- | --- |
| **CI test isolation** | Each job gets a clean throwaway copy; no shared staging contention |
| **Migration dry-runs** | Validate `migrate up` against real data before merge |
| **Parallel test sharding** | Each shard worker calls `copy create`; the port pool handles allocation |
| **Local dev sandbox** | Every developer gets their own copy; no more "who broke staging?" |
| **Load and perf testing** | Mutations stay in the throwaway copy; staging is never polluted |
| **Incident reproduction** | Restore a recent dump locally to reproduce and debug production bugs |

## The problem ditto solves

Databases become a reliability problem when multiple runs share the same
environment. Three root causes account for most of the pain:

**Shared mutation.** One run writes data that the next run reads. Assertions
fail based on who ran last, not on whether the code is correct.

**Schema drift.** Seed fixtures and test factories diverge from production
shapes over time. Tests pass on fabricated data and fail on real data.

**Rollback fragility.** Transaction cleanup breaks under background jobs,
multiple connections, or out-of-process workers—the exact conditions that
production runs under.

ditto eliminates all three. Each run gets a copy restored from a scheduled
source dump, with production-like schema and data shapes, and no connection
to the next run's state.

When ditto is a good fit:

- You run on self-hosted infrastructure with Docker available.
- Your tests, migrations, or tools need real database behavior—DDL, constraints,
  triggers—not mocked persistence.
- Shared staging contention or schema drift is already costing you reliability.
- You want sub-minute database provisioning without standing up extra infra.

## Install

**Homebrew** (macOS and Linux):

```bash
brew tap attaradev/ditto
brew install ditto
```

**Debian / Ubuntu** — download the `.deb` from the
[latest release](https://github.com/attaradev/ditto/releases/latest):

```bash
sudo dpkg -i ditto_<version>_linux_amd64.deb
```

**RPM** (Fedora / RHEL / Amazon Linux):

```bash
sudo rpm -i ditto_<version>_linux_amd64.rpm
```

**Alpine**:

```bash
apk add --allow-untrusted ditto_<version>_linux_amd64.apk
```

**Go install**:

```bash
go install github.com/attaradev/ditto/cmd/ditto@latest
```

**Build from source**:

```bash
git clone https://github.com/attaradev/ditto
cd ditto
go build -o /usr/local/bin/ditto ./cmd/ditto
```

## Quick start

**Prerequisites:**

- Docker on the same host as the CLI
- `pg_dump` / `pg_restore` for Postgres sources
- `mysqldump` / `mysql` for MariaDB sources
- AWS credentials with `secretsmanager:GetSecretValue` if you store passwords
  in Secrets Manager

Create `ditto.yaml` in the current directory or in `~/.ditto/ditto.yaml`:

```yaml
source:
  engine: postgres
  host: mydb.abc.us-east-1.rds.amazonaws.com
  port: 5432
  database: myapp
  user: ditto_dump
  password_secret: arn:aws:secretsmanager:us-east-1:123456789:secret:ditto-rds

dump:
  schedule: "0 * * * *"
  path: /data/dump/latest.gz
  stale_threshold: 7200

copy_ttl_seconds: 7200
port_pool_start: 5433
port_pool_end: 5600
```

### CI test isolation

Create a clean copy, run your suite, tear it down:

```sh
COPY=$(ditto copy create --format=json)
export DATABASE_URL=$(echo "$COPY" | jq -r '.ConnectionString')
COPY_ID=$(echo "$COPY" | jq -r '.ID')

go test ./...
ditto copy delete "$COPY_ID"
```

### Migration dry-runs

Validate a migration against real production-shaped data before merge:

```sh
COPY=$(ditto copy create --format=json)
export DATABASE_URL=$(echo "$COPY" | jq -r '.ConnectionString')
COPY_ID=$(echo "$COPY" | jq -r '.ID')

migrate -database "$DATABASE_URL" up
# assert schema, row counts, or constraint behavior
ditto copy delete "$COPY_ID"
```

### Local developer sandbox

Point `~/.ditto/ditto.yaml` at a shared dump path (NFS mount, S3 sync, or
a local file from `ditto reseed`). Each developer runs:

```sh
ditto copy create --format=pipe
# postgres://ditto:ditto@127.0.0.1:5433/ditto
```

No coordination needed. Every developer gets their own isolated copy on a
dedicated port. Tear it down when done:

```sh
ditto copy delete "$COPY_ID"
```

## How it works

```mermaid
flowchart TD
    RDS[(RDS Source)]
    DUMP["/data/dump/latest.gz"]
    CMD["ditto copy create"]
    CTR["Docker container\nport 5433"]
    TEST["your tests"]
    DEL["ditto copy delete &lt;id&gt;"]
    FREE["container destroyed\nport freed"]

    RDS -->|"hourly pg_dump"| DUMP
    DUMP --> CMD
    CMD -->|"restore"| CTR
    CTR -->|"DATABASE_URL=postgres://...:5433/ditto"| TEST
    TEST --> DEL
    DEL --> FREE
```

ditto runs on the same host that owns Docker and the local dump file. One
SQLite database tracks copy state. There is no separate control plane—the
only long-running process is `ditto daemon`, which handles scheduled dumps
and TTL-based cleanup.

## GitHub Actions integration

Use the composite actions for explicit setup and teardown steps in a job:

```yaml
jobs:
  test:
    runs-on: self-hosted
    steps:
      - uses: actions/checkout@v6

      - id: db
        uses: attaradev/ditto/actions/create@v1
        with:
          ttl: 1h

      - run: go test ./...
        env:
          DATABASE_URL: ${{ steps.db.outputs.database_url }}

      - uses: attaradev/ditto/actions/delete@v1
        if: always()
        with:
          copy_id: ${{ steps.db.outputs.copy_id }}
```

If your runner is dedicated to ditto-backed jobs, set `DITTO_ENABLED: true`
to use the pre-job and post-job hooks instead. The pre-job hook creates the
isolated copy and exports `DATABASE_URL`; the post-job hook deletes it even
when the job fails.

```yaml
jobs:
  test:
    runs-on: self-hosted
    env:
      DITTO_ENABLED: true
    steps:
      - uses: actions/checkout@v6
      - run: go test ./...
        env:
          DATABASE_URL: ${{ env.DATABASE_URL }}
```

## Configuration

Use individual fields in `ditto.yaml` for explicit control over engine, host,
and credentials:

```yaml
source:
  engine: postgres
  host: mydb.abc.us-east-1.rds.amazonaws.com
  port: 5432
  database: myapp
  user: ditto_dump
  password_secret: arn:aws:secretsmanager:us-east-1:123456789:secret:ditto-rds

dump:
  schedule: "0 * * * *"
  path: /data/dump/latest.gz
  stale_threshold: 7200

copy_ttl_seconds: 7200
port_pool_start: 5433
port_pool_end: 5600
```

For local development, use `password` instead of `password_secret`:

```yaml
source:
  engine: postgres
  host: localhost
  port: 5432
  database: myapp
  user: myuser
  password: mypassword
```

Environment variables override config file values. The prefix is `DITTO_` and
dots become underscores:

```bash
DITTO_SOURCE_HOST=other.rds.amazonaws.com ditto copy create
```

To supply the source as a single connection string, ditto also accepts
`source.url` for Postgres, PostgreSQL, MySQL, and MariaDB DSNs.

## Operational model

ditto is designed for teams already running self-hosted infrastructure. A
typical setup has one host running the GitHub Actions runner, Docker, the
local dump file, and the SQLite metadata database. `ditto daemon` keeps the
dump fresh and removes expired copies automatically.

### Runner setup

Install the hooks on the host:

```bash
cp hooks/pre-job.sh  /home/runner/hooks/pre-job.sh
cp hooks/post-job.sh /home/runner/hooks/post-job.sh
chmod +x /home/runner/hooks/*.sh
```

Add to the runner's systemd service unit (`/etc/systemd/system/actions-runner.service`):

```ini
[Service]
Environment=ACTIONS_RUNNER_HOOK_JOB_STARTED=/home/runner/hooks/pre-job.sh
Environment=ACTIONS_RUNNER_HOOK_JOB_COMPLETED=/home/runner/hooks/post-job.sh
```

The runner user must be in the `docker` group:

```bash
usermod -aG docker runner
```

### Keep dumps fresh

Run `ditto daemon` as a systemd service for scheduled dumps and automatic
cleanup of expired copies:

```ini
[Unit]
Description=ditto daemon
After=docker.service

[Service]
ExecStart=/usr/local/bin/ditto daemon
Restart=on-failure
User=runner
WorkingDirectory=/home/runner

[Install]
WantedBy=multi-user.target
```

Or run a standalone cron job for just the dump:

```cron
0 * * * * /usr/local/bin/ditto reseed >> /var/log/ditto-reseed.log 2>&1
```

## Security and data handling

- Source database passwords can be pulled from AWS Secrets Manager; ditto
  never persists them in SQLite.
- Copy containers bind to `127.0.0.1`, keeping them local to the host.
- Copies may contain real production data—disk encryption and host access
  control still matter.
- Access to the Docker socket is effectively root-level access on the host;
  restrict it accordingly.

See [SECURITY.md](SECURITY.md) for the full security model and disclosure policy.

## Advanced and extension material

### Database user setup

The dump user needs `SELECT` only and does not require replication privileges.

**PostgreSQL:**

```sql
CREATE USER ditto_dump WITH PASSWORD 'secret';
GRANT CONNECT ON DATABASE myapp TO ditto_dump;
GRANT USAGE ON SCHEMA public TO ditto_dump;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO ditto_dump;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO ditto_dump;
```

**MariaDB:**

```sql
CREATE USER 'ditto_dump'@'%' IDENTIFIED BY 'secret';
GRANT SELECT, SHOW VIEW, EVENT, TRIGGER ON myapp.* TO 'ditto_dump'@'%';
FLUSH PRIVILEGES;
```

### Adding a new engine

1. Create `engine/{name}/{name}.go`
2. Implement the `engine.Engine` interface (6 methods)
3. Add `func init() { engine.Register(&Engine{}) }`
4. Add a blank import to `cmd/ditto/main.go`

```go
// engine/mysql/mysql.go
package mysql

import (
    "github.com/attaradev/ditto/engine"
)

func init() { engine.Register(&Engine{}) }

type Engine struct{}

func (e *Engine) Name() string { return "mysql" }
// ... implement remaining 5 methods
```

No changes to core dispatch are required beyond registering the engine and
importing it in the CLI entrypoint.

### Development

```bash
go test ./...
go test -race ./...
go test -tags integration ./internal/copy/...
go build ./cmd/ditto
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and conventions.

### Repository landmarks

- `cmd/` — CLI commands and the main entrypoint
- `engine/` — the engine interface and database-specific implementations
- `internal/copy/` — isolated copy lifecycle and port allocation
- `internal/dump/` — scheduled source dumps with atomic file replacement
- `internal/store/` — SQLite metadata for copies and lifecycle events

## License

[MIT](LICENSE)
