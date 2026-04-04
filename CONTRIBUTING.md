# Contributing to ditto

## Development setup

**Prerequisites:**

- Go 1.26+
- Docker (for integration tests)
- `pg_dump` / `pg_restore` (for Postgres integration tests)
- `mysqldump` / `mysql` (for MariaDB integration tests)

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
go test -tags integration ./internal/copy/...
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

```text
engine/              Engine interface + registry + per-engine implementations
  postgres/          PostgreSQL: pg_dump, pg_restore, WaitReady
  mariadb/           MariaDB: mysqldump, mysql restore, WaitReady
internal/
  config/            ditto.yaml parsing (viper)
  store/             SQLite metadata (copies table, events log)
  copy/              Port pool, copy lifecycle manager
  dump/              Dump scheduler with atomic file swap
cmd/
  ditto/main.go      CLI entry point; blank engine imports
  *.go               cobra command implementations
actions/             GitHub Actions composite actions (create, delete)
```

## Adding a new database engine

Each engine is a self-contained package that registers itself at startup:

1. Create `engine/{name}/{name}.go`
2. Implement all six methods of `engine.Engine`
3. Add `func init() { engine.Register(&Engine{}) }` at the bottom
4. Add a blank import in `cmd/ditto/main.go`:

   ```go
   _ "github.com/attaradev/ditto/engine/{name}"
   ```

5. Write tests in `engine/{name}/{name}_test.go`; tag integration tests with
   `//go:build integration`

No changes to any other package are required — the engine registry handles dispatch.

The six methods to implement:

| Method | Notes |
| --- | --- |
| `Name() string` | Key used in `ditto.yaml` under `source.engine` |
| `ContainerImage() string` | Pin the tag — never use `latest` |
| `ConnectionString(host, port) string` | DSN for a copy; always uses user `ditto`, password `ditto`, db `ditto` |
| `Dump(ctx, src, destPath) error` | Write a compressed dump to `destPath`; respect context cancellation |
| `Restore(ctx, dumpPath, port) error` | Restore into running container; dump dir is bind-mounted at `/dump/` |
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
