# CLI Reference

This page summarizes the current command surface exposed by `ditto`.

## Root command

```text
ditto [command]
```

Global flags:

| Flag | Meaning |
| --- | --- |
| `--config <path>` | Path to `ditto.yaml`. If omitted, ditto searches `./ditto.yaml`, `~/.ditto/ditto.yaml`, then `/etc/ditto/ditto.yaml`. |
| `--db <path>` | Path to the SQLite metadata database |
| `--version` | Print version information |

Shared-host clients may set `DITTO_SERVER` instead of passing `--server` on every supported command.
An explicit `--server` flag takes precedence.

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
| `--dump <uri>` | Restore from a local path, `s3://`, or `https://` source. In remote mode (`--server`), local filesystem paths are rejected — use a URI. |
| `--format <mode>` | Output style: `auto`, `pipe`, or `json` |
| `--label <name>` | Run identifier override |
| `--obfuscate` | Apply configured obfuscation post-restore |
| `--ttl <duration>` | Override lifetime for this copy |
| `--server <url>` | Use a shared ditto host; bearer token comes from `DITTO_TOKEN` |

### `ditto copy run`

Create a copy, run one command with `DATABASE_URL` injected, and destroy the copy on exit.

```bash
ditto copy run -- go test ./...
ditto copy run --ttl 30m -- sh -c 'migrate -database "$DATABASE_URL" up'
```

If the child command needs shell expansion, invoke a shell explicitly as in the second example.

Injected environment variables:

- `DATABASE_URL`
- `DITTO_COPY_ID`

Flags:

| Flag | Meaning |
| --- | --- |
| `--dump <uri>` | Restore from a specific source. In remote mode (`--server`), must be a URI (`s3://`, `https://`), not a local path. |
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

## `ditto doctor`

Check that all prerequisites are satisfied before running other commands:

```bash
ditto doctor
ditto doctor --server http://ditto.internal:8080
```

Verifies:

- Docker daemon is reachable
- Configuration is loaded and source fields are present
- Dump file exists and is not stale
- Source database is reachable (TCP + query)
- OIDC JWKS endpoint is reachable (when `server.auth` is configured)

Prints a green/red checklist and exits non-zero if any check fails. Run this first when something is not working.

With `--server` or `DITTO_SERVER`, `doctor` validates the shared host and bearer token without
requiring local Docker, local source config, or a local dump file.

## `ditto init`

Generate a starter `ditto.yaml` in the current directory:

```bash
ditto init
ditto init --output ~/.ditto/ditto.yaml
```

If `DITTO_SOURCE_URL` is set, source fields are pre-populated from it. Obfuscation rule stubs are
included as comments. After editing, verify the result with `ditto doctor`.

Flags:

| Flag | Meaning |
| --- | --- |
| `--output <path>` | Write path (default: `ditto.yaml` in the current directory) |

## `ditto status`

Show dump freshness and copy capacity:

```bash
ditto status
```

Use `ditto doctor` for a deeper diagnostic that includes source connectivity and OIDC checks.

## `ditto target`

Refresh a configured target database from the latest dump:

```bash
ditto target refresh staging --confirm staging
ditto target refresh staging --dump s3://bucket/latest.gz --confirm staging
ditto target refresh staging --dry-run --confirm staging
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--confirm <name>` | Required confirmation; must match the target name |
| `--dump <uri>` | Restore from a local path, `s3://`, or `https://` source. In remote mode, local paths are rejected. |
| `--dry-run` | Validate the request without cleaning or restoring |
| `--obfuscate` | Apply configured obfuscation rules after restore |
| `--server <url>` | Use a shared ditto host; bearer token comes from `DITTO_TOKEN` |

Target refresh is destructive. The target must set `allow_destructive_refresh: true` in
`ditto.yaml`.

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
