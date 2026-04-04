# Use ditto for local development

Use this guide when each developer needs a private, resettable database that still matches
production schema and data shape.

## One-time setup

Prerequisites:

- ditto installed
- a Docker-compatible runtime running locally
- a source database reachable from that runtime

Create `~/.ditto/ditto.yaml`:

```yaml
source:
  engine: postgres
  host: db.example.com
  port: 5432
  database: myapp
  user: ditto_dump
  password_secret: env:DB_PASSWORD

dump:
  path: ~/.ditto/latest.gz
  schedule: "0 * * * *"

copy_ttl_seconds: 14400
port_pool_start: 5433
port_pool_end: 5450
```

If the source contains sensitive data, configure obfuscation before you distribute or reuse the dump.
See [Configuration reference](../reference/configuration.md#obfuscation).

Create the first dump:

```bash
DB_PASSWORD=secret ditto reseed
```

If you want the dump refreshed automatically, run the daemon in the background or install it as a
service:

```bash
ditto daemon
```

For a long-running service setup, see [Operate a ditto host](operate-a-ditto-host.md).

## Start a shell session with a copy

For interactive work, create one copy and keep it for the duration of your shell session:

```bash
eval "$(ditto env export)"
echo "$DATABASE_URL"
```

This sets:

- `DATABASE_URL`
- `DITTO_COPY_ID`

When you are done:

```bash
ditto env destroy "$DITTO_COPY_ID"
```

## Run one command in a throwaway copy

Use `copy run` when you want automatic setup and cleanup around a single command:

```bash
ditto copy run -- go test ./...
ditto copy run -- python manage.py test
ditto copy run -- rails test
```

The child process receives:

- `DATABASE_URL`
- `DITTO_COPY_ID`

## Inspect current state

```bash
ditto status
ditto copy list
ditto copy logs <id>
```

Use `ditto copy delete <id>` if you need to throw away a persistent copy and start fresh.

## Generate an ERD from a copy

```bash
ditto erd --output schema.mmd
ditto erd --format=dbml --output schema.dbml
```

By default, `ditto erd` creates a temporary copy, reads the schema, and destroys the copy on exit.
Use `--source` only when you intentionally want to query the source database directly.

## Integrate with shells and tooling

`direnv`:

```bash
eval "$(ditto env export)"
```

Shell helper:

```bash
ditto-fresh() {
  eval "$(ditto env export)"
  echo "DATABASE_URL=$DATABASE_URL"
}
```

## Share a dump or central host with your team

If not every laptop can reach the source database directly, one trusted host can refresh the dump and
distribute it:

```bash
ditto reseed
aws s3 cp ~/.ditto/latest.gz s3://your-bucket/ditto/latest.gz
```

Developers can then download the dump locally and continue using `copy create`, `copy run`, and
`env export`.

If you want developers to avoid local Docker entirely, run `ditto serve` on a shared host and point
their commands at it:

```bash
export DITTO_TOKEN=my-token
ditto copy run --server=http://ditto.internal:8080 -- go test ./...
```

## Troubleshooting

Start with [Troubleshoot ditto](troubleshoot.md) if setup fails or the dump cannot be created.
