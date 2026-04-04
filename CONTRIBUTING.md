# Contributing to ditto

## Development setup

**Prerequisites:**

- Go 1.26+
- Docker (required for integration tests; ditto runs pg_dump/mysqldump inside containers — no host tools needed)

```bash
git clone https://github.com/attaradev/ditto
cd ditto
go mod download
go build ./...
```

## Running tests

Unit tests have no external dependencies and run everywhere:

```bash
go test ./...
go test -race ./...   # always run with the race detector before opening a PR
```

Integration tests require Docker and are gated by a build tag:

```bash
go test -tags=integration -timeout=15m ./engine/...
```

Individual packages:

```bash
go test ./internal/store/...   # SQLite store (schema, copies, events)
go test ./engine/...           # engine registry
go test ./internal/config/...  # config loading + env overrides
go test ./internal/dump/...    # dump scheduler + atomic swap
go test ./internal/copy/...    # port pool
```

## Project layout

| Path | Purpose |
| --- | --- |
| `cmd/` | CLI commands and the main entrypoint |
| `engine/` | Engine interface and per-engine implementations (postgres, mysql) |
| `internal/config/` | `ditto.yaml` parsing (Viper) |
| `internal/copy/` | Copy lifecycle, port pool, warm pool, HTTP client |
| `internal/dump/` | Dump scheduler with atomic file replacement |
| `internal/dumpfetch/` | Dump URI resolution (local path, `s3://`, `https://`) |
| `internal/erd/` | Schema introspection and ERD rendering (Mermaid, DBML) |
| `internal/obfuscation/` | Post-restore PII scrubbing rules |
| `internal/secret/` | Secret resolution (`env:`, `file:`, `arn:aws:...`) |
| `internal/server/` | HTTP API server for remote copy operations |
| `internal/store/` | SQLite metadata for copies and lifecycle events |
| `pkg/ditto/` | Go SDK — `NewCopy(t)` for use in test suites |
| `sdk/python/` | Python SDK — `Client` and pytest fixture |
| `actions/` | GitHub Actions composite actions (create, delete) |

## Adding a new database engine

Each engine is a self-contained package that registers itself at startup:

1. Create `engine/{name}/{name}.go`
2. Implement all eight methods of `engine.Engine`
3. Add `func init() { engine.Register(&Engine{}) }` at the bottom
4. Add a blank import in `cmd/ditto/main.go`:

   ```go
   _ "github.com/attaradev/ditto/engine/{name}"
   ```

5. Write tests in `engine/{name}/{name}_test.go`; tag integration tests with
   `//go:build integration`

No changes to any other package are required — the engine registry handles dispatch.

The eight methods to implement:

| Method | Notes |
| --- | --- |
| `Name() string` | Key used in `ditto.yaml` under `source.engine` |
| `ContainerImage() string` | Pin the tag — never use `latest` |
| `ContainerEnv() []string` | Env vars to initialise the database in a copy container |
| `ConnectionString(host, port) string` | DSN for a copy; always uses user `ditto`, password `ditto`, db `ditto` |
| `Dump(ctx, docker, clientImage, src, destPath, opts) error` | Write a compressed dump; pass `DumpOptions{SchemaOnly: true}` for DDL-only |
| `DumpFromContainer(ctx, docker, containerName, destPath, opts) error` | Re-dump a running container (used for obfuscation baking) |
| `Restore(ctx, docker, dumpPath, containerName) error` | Restore into a running container; dump dir bind-mounted at `/dump/` |
| `WaitReady(port, timeout) error` | TCP dial then `SELECT 1`; poll every 500ms |

## Code conventions

- All exported functions and types have doc comments.
- Errors wrap with context: `fmt.Errorf("copy.Create: %w", err)`.
- The `engine.Engine` interface is stable — do not add methods without an ADR.
- SQLite queries use `?` placeholders; no string interpolation in SQL.
- The port pool lock is held for the full duration of allocation including the TCP dial check —
  do not release it in between.
- Context cancellation must propagate to all blocking operations (`exec.CommandContext`,
  Docker API calls, `WaitReady` loops).

## Opening a pull request

- Keep PRs focused: one logical change per PR.
- All unit tests must pass with `-race`.
- Include a brief description of what changed and why.
- For changes to the Engine interface or SQLite schema, open an issue first.

## Reporting issues

Open an issue on GitHub with:

- ditto version (`ditto --version`)
- OS and Docker version
- Minimal steps to reproduce
- Relevant output from `ditto status` and `ditto copy logs <id>`
