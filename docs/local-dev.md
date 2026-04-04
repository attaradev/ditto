# Local development

ditto is as useful on a laptop as it is in CI. Every developer gets their own isolated,
production-faithful database copy. No shared staging. No seed scripts. No "works on my machine"
data drift.

A shared dev or staging database means one developer's experiment breaks another's session, seed
data drifts from the real schema over time, and rolling back a bad migration affects everyone. With
ditto, each developer gets their own copy — changes stay local, and starting fresh is a single
command.

---

## One-time setup

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
  path: ~/.ditto/latest.gz
  schedule: "0 * * * *"             # refresh hourly while daemon runs

copy_ttl_seconds: 14400             # copies live 4 h by default
port_pool_start: 5433
port_pool_end: 5450                 # small range is fine for local use
```

If your dump contains real user data, add obfuscation rules so every copy is safe to work with
locally. See [Configuration — PII obfuscation](configuration.md#pii-obfuscation) for the full
reference.

**3. Take a first dump:**

```sh
DB_PASSWORD=secret ditto reseed
```

ditto connects to the source, dumps it to `~/.ditto/latest.gz`, and disconnects. From this point
forward the source database is not needed for day-to-day work.

**4. (Optional) Run the daemon to keep the dump fresh:**

```sh
ditto daemon &
```

Or add it to your login items / launchd / systemd so it runs in the background and refreshes the
dump on the configured schedule. See [Operations — keeping dumps fresh](operations.md#keep-dumps-fresh)
for a systemd unit.

---

## Daily workflow

**Get a fresh copy for your session:**

```sh
export DATABASE_URL=$(ditto copy create)
# postgres://ditto:ditto@127.0.0.1:5433/ditto
```

Every developer gets their own port from the pool. No coordination. No contention.

**Or scope a copy to a single command:**

```sh
ditto copy run -- rails server
ditto copy run -- python manage.py runserver
ditto copy run -- go run ./cmd/api
```

The copy is created when the command starts and destroyed when it exits. `DATABASE_URL` is injected automatically.

**Check what's running:**

```sh
ditto status               # dump freshness and copy capacity at a glance
ditto copy list            # all active copies
ditto copy logs <id>       # lifecycle events for a specific copy
```

**Throw one away and start fresh:**

```sh
ditto copy delete <id>
export DATABASE_URL=$(ditto copy create)
```

---

## Shell environment injection

`ditto env export` creates a copy and prints eval-able shell export lines — useful for interactive
sessions where `DATABASE_URL` needs to persist across multiple commands:

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

---

## Shell and tooling integration

**direnv** — automatically activate a copy when you enter the project directory. Add to `.envrc`:

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

---

## ERD generation

Generate an Entity-Relationship Diagram directly from the live schema.

**Via a temporary copy** (default — source database is never queried at render time):

```sh
ditto erd                          # Mermaid erDiagram to stdout
ditto erd --format=dbml            # DBML (dbdiagram.io) to stdout
ditto erd --output=schema.md       # write to a file
```

**Directly from the source database:**

```sh
ditto erd --source
```

Both [Mermaid](https://mermaid.js.org/) and [DBML](https://dbml.dbdiagram.io/) output include
tables, column types, primary keys, and foreign key relationships.

---

## Shared dump for a team

If your team's machines can't all reach the source database directly, one person (or CI) runs
`ditto reseed` and distributes the dump:

```sh
# Sync to a shared location after each reseed
ditto reseed && aws s3 cp ~/.ditto/latest.gz s3://your-bucket/ditto/latest.gz

# Each developer downloads it
aws s3 cp s3://your-bucket/ditto/latest.gz ~/.ditto/latest.gz
```

Alternatively, run a single `ditto serve` instance on a shared host that all developers connect to:

```sh
# On the shared host
ditto serve

# On each developer's machine (no local runtime or dump file needed)
ditto copy run --server=http://ditto.internal:8080 -- go run ./cmd/api
export DITTO_TOKEN=my-token  # if the server requires auth
```
