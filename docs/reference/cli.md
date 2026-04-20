# CLI Reference

This page summarizes the current command surface exposed by `ditto`.

## Root command

```text
ditto [command]
```

Global flags:

| Flag | Meaning |
| --- | --- |
| `--config <path>` | Path to `ditto.yaml` |
| `--db <path>` | Path to the SQLite metadata database |
| `--version` | Print version information |

## `ditto copy`

### `ditto copy create`

Create a clean copy from the latest dump or from `--dump`.

```bash
ditto copy create
ditto copy create --format=json --ttl 30m
ditto copy create --dump s3://bucket/latest.gz
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--dump <uri>` | Restore from a local path, `s3://`, or `https://` source; with `--server`, the host resolves it |
| `--format <mode>` | Output style: `auto`, `pipe`, or `json` |
| `--label <name>` | Run identifier override |
| `--obfuscate` | Apply configured obfuscation post-restore |
| `--ttl <duration>` | Override lifetime for this copy |
| `--server <url>` | Use a shared ditto host; bearer token comes from `DITTO_TOKEN` |

### `ditto copy run`

Create a copy, run one command with `DATABASE_URL` injected, and destroy the copy on exit.

```bash
ditto copy run -- go test ./...
ditto copy run --ttl 30m -- migrate -database "$DATABASE_URL" up
```

Injected environment variables:

- `DATABASE_URL`
- `DITTO_COPY_ID`

Flags:

| Flag | Meaning |
| --- | --- |
| `--dump <uri>` | Restore from a specific dump source |
| `--label <name>` | Run identifier override |
| `--obfuscate` | Apply configured obfuscation post-restore |
| `--ttl <duration>` | Override lifetime for this copy |
| `--server <url>` | Use a shared ditto host; bearer token comes from `DITTO_TOKEN` |

### Other `copy` subcommands

```bash
ditto copy list
ditto copy delete <id>
ditto copy logs <id>
```

Use `copy list` to inspect active copies and `copy logs` for lifecycle events tied to a specific ID.

## `ditto reseed`

Refresh the local dump immediately:

```bash
ditto reseed
```

This command succeeds only after a new dump completes successfully.

## `ditto host`

Run the shared-host controller:

```bash
ditto host
```

Responsibilities:

- refresh the dump on the configured schedule
- delete copies after TTL expiry
- refill the warm pool when enabled
- recover stuck copies and orphan containers on startup
- serve the authenticated `/v2` API for remote callers

## `ditto status`

Show dump freshness and copy capacity:

```bash
ditto status
```

This is the first command to run when you want to verify host health.

## `ditto env`

Use `env` when you want shell-friendly environment management.

### `ditto env export`

Create a copy and print shell export lines:

```bash
eval "$(ditto env export)"
```

This sets `DATABASE_URL` and `DITTO_COPY_ID` in the current shell.

### `ditto env destroy`

Destroy a copy created with `env export`:

```bash
ditto env destroy "$DITTO_COPY_ID"
```

### `ditto env -- <command>`

Run one command with `DATABASE_URL` injected:

```bash
ditto env -- pytest tests/
```

`env --` is effectively the same lifecycle as `copy run`.

## `ditto erd`

Generate an ERD from a temporary copy or directly from the source database:

```bash
ditto erd
ditto erd --format=dbml --output schema.dbml
ditto erd --source
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--format <type>` | Output format: `mermaid` or `dbml` |
| `--output <path>` | Write output to a file |
| `--source` | Connect to the source database directly |
| `--server <url>` | Create the temporary copy via a shared ditto host |
