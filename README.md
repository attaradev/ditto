# ditto

![ditto](assets/logo.svg)

[![CI](https://github.com/attaradev/ditto/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/attaradev/ditto/actions/workflows/ci.yml)

## Real production data. Zero risk. For every run

ditto provisions isolated Postgres and MySQL copies from a scheduled production
dump — with PII obfuscated once at source, so every copy carries real schema and
real data shapes without exposing sensitive information. No shared state. No seed
scripts. No fabricated fixtures.

```sh
ditto copy run -- go test ./...
```

> **Real data, safely.** Configure obfuscation rules once and ditto bakes PII
> scrubbing into the dump file — every copy restored from it is already clean.
> Developers get production-faithful data shapes without ever seeing raw PII.

## Use cases

| Use case | What ditto does |
| --- | --- |
| **CI test isolation** | Each job gets a clean throwaway copy; no shared staging contention |
| **Migration dry-runs** | Validate `migrate up` against real data before merge |
| **Parallel test sharding** | Each shard worker calls `copy create`; the port pool handles allocation |
| **Local dev sandbox** | Every developer gets their own isolated copy; no more "who broke staging?" |
| **Compliance-safe dev** | Obfuscation rules bake PII scrubbing into the dump once — every copy is safe |
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

**Fabricated data.** Synthetic seeds don't reproduce production bugs. Edge
cases, constraint violations, and performance characteristics only appear on
real data — but real data can't go to developers because of PII.

ditto eliminates all four. Each run gets a copy restored from a production
dump with PII obfuscated at source, so the data is real but safe — and no
run shares state with the next.

When ditto is a good fit:

- You want each test run, migration, or dev session to start from a clean slate.
- Your tests need real database behavior—DDL, constraints, triggers—not mocked persistence.
- Shared staging contention or schema drift is already costing you reliability.
- You want sub-second database provisioning without standing up extra infrastructure.

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

- A Docker-compatible runtime on the same host as ditto
- A source database hostname that is reachable from that runtime
  `localhost` and `127.0.0.1` on the host are not enough for dump helpers.

Create `ditto.yaml` in the current directory or in `~/.ditto/ditto.yaml`:

```yaml
source:
  engine: postgres
  host: db.example.com
  port: 5432
  database: myapp
  user: ditto_dump
  password: secret           # dev only — use password_secret in production

dump:
  schedule: "0 * * * *"
  path: /data/dump/latest.gz
  # client_image: postgres:15-alpine   # optional helper image override

copy_ttl_seconds: 7200
port_pool_start: 5433
port_pool_end: 5600
# docker_host: unix:///var/run/docker.sock
```

ditto runs dump and restore work through the configured runtime, so you do not
need host-installed `pg_dump`, `pg_restore`, `mysqldump`, `mysql`, or the
Docker CLI.

Take a first dump, then create a copy:

```sh
ditto reseed
export DATABASE_URL=$(ditto copy create)
```

### One-time / JIT copies

`ditto copy run` handles the full lifecycle automatically — create, inject
`DATABASE_URL`, run your command, then destroy the copy on exit regardless of
whether the command succeeds or fails:

```sh
ditto copy run -- go test ./...
ditto copy run --ttl 30m -- migrate -database "$DATABASE_URL" up
ditto copy run --server=http://ditto.internal:8080 -- pytest tests/
ditto copy run --dump s3://my-bucket/latest.gz -- go test ./...
```

Two variables are available inside the command:

| Variable | Value |
| --- | --- |
| `DATABASE_URL` | Connection string for the copy |
| `DITTO_COPY_ID` | Copy ID (for debugging) |

The command's exit code is preserved, so this integrates cleanly into CI
pipelines. Copies are destroyed even when the command is interrupted with
Ctrl-C or SIGTERM.

### Manual create / delete

When you need to hold a copy across multiple steps, manage it explicitly:

```sh
COPY=$(ditto copy create --format=json)
export DATABASE_URL=$(echo "$COPY" | jq -r '.connection_string')
COPY_ID=$(echo "$COPY" | jq -r '.id')

go test ./...
ditto copy delete "$COPY_ID"
```

**Flags:**

| Flag | Purpose |
| --- | --- |
| `--format=json\|pipe\|auto` | `pipe` prints only the connection string; `json` includes the copy ID; `auto` (default) pretty-prints to a terminal and acts like `pipe` when stdout is redirected |
| `--ttl 30m` | Override copy lifetime for this copy |
| `--label <name>` | Tag the copy with a run identifier (overrides auto-detected CI env vars) |
| `--dump <uri>` | Restore from a specific file instead of the default dump — accepts a local path, `s3://bucket/key`, or `https://` URL |
| `--obfuscate` | Apply configured obfuscation rules post-restore; use with `--dump` when the source file has not already been obfuscated |

### Migration dry-runs

```sh
ditto copy run --ttl 15m -- migrate -database "$DATABASE_URL" up
```

### Local developer sandbox

See [Local development](#local-development) for a complete walkthrough.

## Local development

ditto is as useful on a laptop as it is in CI. Every developer gets their
own isolated, production-faithful database. No shared staging. No seed
scripts. No "works on my machine" data drift.

### The problem it solves locally

A shared dev or staging database means:

- One developer's experiment breaks another's session
- Seed data and fixtures diverge from the real schema over time
- Rolling back a bad migration affects everyone

With ditto, each developer gets their own copy. Changes stay local. Starting
fresh is a single command.

### One-time setup

**1. Install ditto and ensure a Docker-compatible runtime is running.**

**2. Create `~/.ditto/ditto.yaml`** pointing at your source database:

```yaml
source:
  engine: postgres
  host: db.example.com
  port: 5432
  database: myapp
  user: ditto_dump
  password_secret: env:DB_PASSWORD   # never commit passwords

dump:
  path: ~/.ditto/latest.gz           # stored locally on your machine
  schedule: "0 * * * *"             # refresh hourly while daemon runs

copy_ttl_seconds: 14400             # copies live 4 h by default
port_pool_start: 5433
port_pool_end: 5450                 # small range is fine for local use
```

If your dump contains real user data, add obfuscation rules so every copy
is safe to work with locally:

```yaml
obfuscation:
  rules:
    - table: users
      column: email
      strategy: hash       # deterministic — queries still work
    - table: users
      column: phone
      strategy: mask
      keep_last: 4
    - table: users
      column: full_name
      strategy: redact
```

**3. Take a first dump:**

```sh
DB_PASSWORD=secret ditto reseed
```

ditto connects to the source, dumps it to `~/.ditto/latest.gz`, and
disconnects. From this point forward the source database is not needed for
day-to-day work.

**4. (Optional) Run the daemon to keep the dump fresh:**

```sh
ditto daemon &
```

Or add it to your login items / launchd / systemd so it runs in the
background and refreshes the dump on the configured schedule.

### Daily workflow

**Get a fresh copy for your session:**

```sh
export DATABASE_URL=$(ditto copy create)
# postgres://ditto:ditto@127.0.0.1:5433/ditto
```

Every developer gets their own port. No coordination. No contention.

**Or scope a copy to a single command:**

```sh
ditto copy run -- rails server
ditto copy run -- python manage.py runserver
ditto copy run -- go run ./cmd/api
```

The copy is created when the command starts and destroyed when it exits.
Your application sees `DATABASE_URL` automatically.

**Check what's running:**

```sh
# Dump freshness and copy capacity at a glance
ditto status

# All active copies
ditto copy list

# Lifecycle events for a specific copy
ditto copy logs <id>
```

**Throw one away and start fresh:**

```sh
ditto copy delete <id>
export DATABASE_URL=$(ditto copy create)
```

### Shell and tooling integration

**direnv** — automatically activate a copy when you enter the project
directory. Add to `.envrc`:

```sh
export DATABASE_URL=$(ditto copy create)
```

**Makefile** — wrap common tasks:

```makefile
db:
    export DATABASE_URL=$$(ditto copy create) && echo $$DATABASE_URL

dev:
    ditto copy run -- go run ./cmd/api

test:
    ditto copy run -- go test ./...

migrate:
    ditto copy run -- migrate -database "$$DATABASE_URL" up
```

**Shell function** — quick alias for a fresh copy:

```sh
# ~/.zshrc or ~/.bashrc
ditto-fresh() {
  export DATABASE_URL=$(ditto copy create)
  echo "DATABASE_URL=$DATABASE_URL"
}
```

### Shared dump for a team

If your team's laptops can't all reach the source database directly, one
person (or CI) runs `ditto reseed` and distributes the dump:

```sh
# Sync the dump to a shared location after each reseed
ditto reseed && aws s3 cp ~/.ditto/latest.gz s3://your-bucket/ditto/latest.gz

# Each developer downloads it
aws s3 cp s3://your-bucket/ditto/latest.gz ~/.ditto/latest.gz
```

Or run a single `ditto serve` instance on a shared host that all developers
hit with `--server`:

```sh
# On the shared host
ditto serve

# On each developer's machine (no local runtime or dump file needed)
ditto copy run --server=http://ditto.internal:8080 -- go run ./cmd/api
export DITTO_TOKEN=my-token  # if the server requires auth
```

## How it works

```mermaid
flowchart TD
    SRC[(Source DB)]
    DUMP["/data/dump/latest.gz"]
    CMD["ditto copy create"]
    CTR["Database container\nport 5433"]
    APP["your app / tests"]
    DEL["ditto copy delete &lt;id&gt;"]
    FREE["container destroyed\nport freed"]

    SRC -->|"scheduled dump (+ obfuscate)"| DUMP
    DUMP --> CMD
    CMD -->|"restore"| CTR
    CTR -->|"DATABASE_URL=...5433/ditto"| APP
    APP --> DEL
    DEL --> FREE
```

ditto runs on the same host that owns the Docker-compatible runtime and the
local dump file. One SQLite database tracks copy state. There is no separate
control plane—the only long-running process is `ditto daemon`, which handles
scheduled dumps and TTL-based cleanup.

## CI integration

ditto works with any CI platform. Use the composite actions for GitHub
Actions, or pass `--server` to connect to a `ditto serve` instance from
any runner (GitHub-hosted, GitLab CI, CircleCI, Buildkite, etc.).

### GitHub Actions — self-hosted runner

```yaml
jobs:
  test:
    runs-on: self-hosted
    steps:
      - uses: actions/checkout@v4

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

### Any CI platform — server mode

Run `ditto serve` on your infrastructure and point any runner at it with
`--server`. The runner does not need Docker access:

```sh
# On your ditto host
ditto serve

# In any CI job (GitHub-hosted, GitLab CI, CircleCI, etc.)
COPY=$(ditto copy create --server=http://ditto.internal:8080 --format=json)
export DATABASE_URL=$(echo "$COPY" | jq -r '.connection_string')
COPY_ID=$(echo "$COPY" | jq -r '.id')

# ... run tests ...
ditto copy delete "$COPY_ID" --server=http://ditto.internal:8080
```

Set `DITTO_TOKEN` for authenticated servers:

```sh
export DITTO_TOKEN=my-secret-token
ditto copy create --server=http://ditto.internal:8080
```

GitHub Actions with server mode:

```yaml
- id: db
  uses: attaradev/ditto/actions/create@v1
  with:
    server_url: http://ditto.internal:8080
    ditto_token: ${{ secrets.DITTO_TOKEN }}
    ttl: 1h
```

### Go SDK

**In tests** — `NewCopy` provisions a copy and registers `t.Cleanup` to destroy it:

```go
import "github.com/attaradev/ditto/pkg/ditto"

func TestMyFeature(t *testing.T) {
    dsn := ditto.NewCopy(t,
        ditto.WithServerURL("http://ditto.internal:8080"),
        ditto.WithToken(os.Getenv("DITTO_TOKEN")),
        ditto.WithTTL(10*time.Minute),
    )
    db, _ := sql.Open("pgx", dsn)
    // copy is destroyed automatically when the test finishes
}
```

**Outside tests** — `WithCopy` scopes the copy to a function call:

```go
client := ditto.New(
    ditto.WithServerURL("http://ditto.internal:8080"),
    ditto.WithToken(os.Getenv("DITTO_TOKEN")),
)

err := client.WithCopy(ctx, func(dsn string) error {
    return runMigrations(dsn)
})
```

### Python SDK

Install with pip:

```sh
pip install "ditto-sdk[pytest]"
```

**pytest fixture** — auto-registered when the package is installed; no `conftest.py` needed:

```python
def test_my_feature(ditto_copy):
    conn = psycopg2.connect(ditto_copy)
    # copy is destroyed automatically after the test
```

Configure via environment variables: `DITTO_SERVER_URL`, `DITTO_TOKEN`, `DITTO_TTL`.

**Programmatic use:**

```python
from ditto import Client

client = Client(server_url="http://ditto.internal:8080", token="secret")

with client.with_copy() as dsn:
    run_migrations(dsn)
```

### ERD generation

Generate an Entity-Relationship Diagram directly from the live schema.

**Via a temporary copy** (default — source database is never queried at render time):

```sh
ditto erd                          # Mermaid erDiagram to stdout
ditto erd --format=dbml            # DBML (dbdiagram.io) to stdout
ditto erd --output=schema.md       # Write to file
```

**Directly from the source database:**

```sh
ditto erd --source
```

Both [Mermaid](https://mermaid.js.org/) and [DBML](https://dbml.dbdiagram.io/) output include
tables, column types, primary keys, and foreign key relationships.

### Shell environment injection

`ditto env export` creates a copy and prints eval-able shell export lines —
useful for interactive sessions where `DATABASE_URL` needs to persist across
multiple commands:

```sh
eval $(ditto env export)          # creates a copy; sets DATABASE_URL + DITTO_COPY_ID
psql $DATABASE_URL                # use from any tool
alembic upgrade head              # run migrations
ditto env destroy $DITTO_COPY_ID  # clean up when done
```

Run a single command with a throwaway copy (identical to `ditto copy run`):

```sh
ditto env -- pytest tests/
ditto env -- npm run test:integration
ditto env -- python manage.py migrate
```

Remote server support:

```sh
eval $(ditto env export --server=http://ditto.internal:8080)
```

## Configuration

### Minimal config

```yaml
source:
  engine: postgres          # or mysql
  host: db.example.com
  port: 5432
  database: myapp
  user: ditto_dump
  password: secret          # dev only

dump:
  path: /data/dump/latest.gz

copy_ttl_seconds: 7200
port_pool_start: 5433
port_pool_end: 5600
```

### Connection URL

Supply the source as a single URL instead of individual fields:

```yaml
source:
  url: postgres://ditto_dump:secret@db.example.com:5432/myapp
```

Supported schemes: `postgres`, `postgresql`, `mysql`, `mariadb`.

### Secret references

`password_secret` and `token_secret` accept a backend prefix so credentials
are never stored in config files:

| Format | Backend |
| --- | --- |
| `env:MY_VAR` | Environment variable `MY_VAR` |
| `file:/run/secrets/pw` | File contents (Docker secrets, Kubernetes mounts) |
| `arn:aws:secretsmanager:...` | AWS Secrets Manager (cached 5 min) |

```yaml
source:
  password_secret: env:DB_PASSWORD          # read from environment
  # password_secret: file:/run/secrets/pw  # read from mounted secret
  # password_secret: arn:aws:...           # AWS Secrets Manager
```

### Warm copy pool

Pre-warm N copies so `ditto copy create` returns in under a second:

```yaml
warm_pool_size: 3   # keep 3 ready copies; disable with 0 (default)
```

The daemon refills the pool in the background after each claim.

### PII obfuscation

When obfuscation rules are configured, `ditto reseed` bakes them into the dump
once — every copy restored from that file is already PII-free. No scrubbing
happens at restore time unless you explicitly pass `--obfuscate` (useful when
restoring a raw external dump via `--dump`).

Five strategies are supported:

| Strategy | Effect |
| --- | --- |
| `replace` | Deterministic format-preserving substitution — looks like real data, isn't |
| `hash` | One-way SHA-256 hex digest — preserves uniqueness for `JOIN`s |
| `mask` | Replaces characters with `*` (configurable `mask_char` and `keep_last`) |
| `redact` | Replaces the value with `[redacted]` (configurable via `with:`) |
| `nullify` | Sets the column to `NULL` |

**`replace`** is the recommended strategy for most PII. It generates realistic-looking
values derived deterministically from the original, so foreign key relationships
and `JOIN`s still work:

| `type` | Example output |
| --- | --- |
| `email` | `user483921@example.com` |
| `name` | `User74831` |
| `phone` | `+1-555-0147-3821` (NANP fictional range) |
| `ip` | `10.42.17.3` (RFC 1918 — never public) |
| `url` | `https://example.com/r/a3f92b1c8d04` |
| `uuid` | `a3f92b1c-8d04-4e2f-b3a1-9c2d8f7e1b05` |

```yaml
obfuscation:
  rules:
    - table: users
      column: email
      strategy: replace
      type: email          # user483921@example.com

    - table: users
      column: full_name
      strategy: replace
      type: name           # User74831

    - table: users
      column: phone
      strategy: replace
      type: phone          # +1-555-0147-3821

    - table: events
      column: ip_address
      strategy: replace
      type: ip             # 10.42.17.3

    - table: users
      column: ssn
      strategy: nullify    # NULL — no substitute value needed

    - table: users
      column: notes
      strategy: redact     # [redacted] — freeform text with no useful shape

    - table: payments
      column: card_number
      strategy: mask
      keep_last: 4         # ************1234
```

### Runtime settings

Override the container image used for copy containers to pin a specific version:

```yaml
copy_image: "postgres:15-alpine"   # default: postgres:16-alpine
# copy_image: "mysql:5.7"         # default: mysql:8.4
```

Override the helper image used for dump operations when the source database
needs a different client version:

```yaml
dump:
  client_image: "postgres:15-alpine"
```

Point ditto at a specific Docker-compatible daemon:

```yaml
docker_host: "unix:///var/run/docker.sock"
```

### HTTP server

```yaml
server:
  addr: ":8080"
  token: ""                        # plaintext token (dev only)
  token_secret: env:DITTO_TOKEN    # or file:/run/secrets/token, or arn:aws:...
```

### Environment variable overrides

Any config field can be overridden at runtime. The prefix is `DITTO_` and
dots become underscores:

```bash
DITTO_SOURCE_HOST=db.staging.example.com ditto copy create
```

### Full reference

See [`ditto.yaml.example`](ditto.yaml.example) for a complete annotated
configuration file.

## Operational model

A typical setup runs one host with a Docker-compatible runtime, the local dump
file, the SQLite metadata database, and `ditto daemon`. The daemon keeps the
dump fresh and removes expired copies automatically.

### Keep dumps fresh

Run `ditto daemon` as a systemd service:

```ini
[Unit]
Description=ditto daemon
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/ditto daemon
Restart=on-failure
User=runner
WorkingDirectory=/home/runner

[Install]
WantedBy=multi-user.target
```

If you run Docker Engine under systemd, add `After=docker.service` too.

Or a standalone cron job for just the dump:

```cron
0 * * * * /usr/local/bin/ditto reseed >> /var/log/ditto-reseed.log 2>&1
```

### Runner setup (GitHub Actions self-hosted)

The runner user must be able to reach the configured runtime socket. For
Docker Engine on Linux:

```bash
usermod -aG docker runner
```

## Security and data handling

- Credentials are never persisted in SQLite. Resolve them at runtime via
  `env:`, `file:`, or `arn:aws:` secret references.
- Copy containers bind to `127.0.0.1`, keeping them local to the host.
- Configure obfuscation rules so `ditto reseed` scrubs PII into the dump file
  before any copy is created. Copies restored from a pre-obfuscated dump never
  expose production data to callers.
- Access to the container runtime socket is effectively root-level on the host;
  restrict it accordingly.

See [SECURITY.md](SECURITY.md) for the full security model and disclosure policy.

## Advanced

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

**MySQL / MariaDB:**

```sql
CREATE USER 'ditto_dump'@'%' IDENTIFIED BY 'secret';
GRANT SELECT, SHOW VIEW, EVENT, TRIGGER ON myapp.* TO 'ditto_dump'@'%';
FLUSH PRIVILEGES;
```

### Adding a new engine

1. Create `engine/{name}/{name}.go`
2. Implement the `engine.Engine` interface (8 methods)
3. Add `func init() { engine.Register(&Engine{}) }`
4. Add a blank import to `cmd/ditto/main.go`

```go
// engine/sqlite/sqlite.go
package sqlite

import "github.com/attaradev/ditto/engine"

func init() { engine.Register(&Engine{}) }

type Engine struct{}

func (e *Engine) Name() string { return "sqlite" }
// ... implement the remaining 7 methods
```

No changes to core dispatch are required—registering the engine and importing
it in the CLI entrypoint is sufficient.

### Development

```bash
go test ./...
go test -race ./...
go build ./cmd/ditto
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and conventions.

### Repository landmarks

| Path | Purpose |
| --- | --- |
| `cmd/` | CLI commands and the main entrypoint |
| `engine/` | Engine interface and per-engine implementations (postgres, mysql) |
| `internal/copy/` | Copy lifecycle, port pool, warm pool, HTTP client |
| `internal/dump/` | Scheduled source dumps with atomic file replacement |
| `internal/dumpfetch/` | Dump URI resolution (local path, s3://, https://) |
| `internal/erd/` | Schema introspection and ERD rendering (Mermaid, DBML) |
| `internal/obfuscation/` | Post-restore PII scrubbing rules |
| `internal/secret/` | Secret resolution (env, file, AWS Secrets Manager) |
| `internal/server/` | HTTP API server for remote copy operations |
| `internal/store/` | SQLite metadata for copies and lifecycle events |
| `pkg/ditto/` | Go SDK — `NewCopy(t)` for use in test suites |
| `sdk/python/` | Python SDK — `Client` and pytest fixture |
| `actions/` | GitHub Actions composite actions (create, delete) |

## License

[MIT](LICENSE)
