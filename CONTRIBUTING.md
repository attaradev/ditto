# Contributing to ditto

ditto is a CLI and service for provisioning isolated Postgres and MySQL copies from a scheduled
source dump. Contributions are welcome across product, docs, tests, and engine support.

## Good contributions

Useful contributions include:

- fixing bugs or reducing operational risk
- improving the CLI, copy lifecycle, or error messages
- tightening documentation, examples, and troubleshooting guidance
- expanding test coverage around config, copy orchestration, or engine behavior
- adding or improving database engine support

If you are new to the project, start with documentation improvements, focused bug fixes, or tests
around existing behavior before taking on schema, engine, or lifecycle changes.

## Development setup

Prerequisites:

- Go 1.26+
- Docker or another Docker-compatible runtime for integration tests

Clone the repository and build the CLI:

```bash
git clone https://github.com/attaradev/ditto
cd ditto
go mod download
go build ./cmd/ditto
```

Smoke-test the built command surface:

```bash
go run ./cmd/ditto --help
go run ./cmd/ditto copy run --help
go run ./cmd/ditto erd --help
```

## Recommended workflow

1. Branch from `main`.
2. Use a focused branch name such as `feat/<topic>`, `fix/<topic>`, `docs/<topic>`, or `chore/<topic>`.
3. Make the smallest change that solves the problem completely.
4. Run the relevant checks locally.
5. Open a focused pull request with clear validation evidence.

## Commit messages

Use Conventional Commits with concise subjects:

- `feat: add mysql dump retry logging`
- `fix: keep copy cleanup on interrupted runs`
- `docs: reorganize docs into diataxis structure`

Avoid generic subjects such as `update`, `misc`, or `changes`.

## Local validation

Run these before opening a pull request:

```bash
go build ./cmd/ditto
go test -race ./...
go test -tags=integration -race -count=1 -timeout=15m ./engine/...
```

If your change affects docs, check links and fenced code blocks as part of the review. If you have
`markdownlint-cli2` installed locally, run it before opening the PR.

## Pull request checklist

Before you request review, confirm that:

- the PR is one logical change
- new behavior is covered by tests or a concrete validation note
- docs were updated if flags, config, or workflows changed
- errors include enough context for operators to act on them
- database schema or engine interface changes were discussed first

When you open the PR, explain:

- what changed
- why it was needed
- risks or compatibility concerns
- exactly how you validated it

## Good first issues

Look for issues labeled `good first issue` or `help wanted` if those labels are available. If the
issue tracker is quiet, these are strong first contributions:

- tighten README or troubleshooting guidance after trying the project locally
- add focused unit tests around config loading, store behavior, or CLI helpers
- improve command output and error text without changing core lifecycle behavior
- add missing docs when a feature exists in code but is hard to discover

If you plan to work on something non-trivial, open an issue or discussion first so the design can be
aligned before you invest implementation time.

## Project layout

| Path | Purpose |
| --- | --- |
| `cmd/` | Cobra CLI commands and the main entrypoint |
| `engine/` | Engine interface and per-engine implementations |
| `internal/config/` | `ditto.yaml` loading, defaults, and validation |
| `internal/copy/` | Copy lifecycle, port pool, warm pool, HTTP client |
| `internal/dump/` | Dump scheduler and atomic file replacement |
| `internal/dumpfetch/` | Alternate dump sources such as local files, `s3://`, and `https://` |
| `internal/erd/` | Schema introspection and ERD rendering |
| `internal/obfuscation/` | PII scrubbing rules baked into dumps or applied post-restore |
| `internal/secret/` | Secret resolution for `env:`, `file:`, and AWS Secrets Manager |
| `internal/server/` | HTTP API server for remote copy operations |
| `internal/store/` | SQLite metadata for copy state and lifecycle events |
| `pkg/ditto/` | Go SDK for tests and programmatic copy lifecycle |
| `sdk/javascript/` | TypeScript SDK for Node.js clients |
| `sdk/python/` | Python SDK and pytest fixture |
| `actions/` | Composite GitHub Actions bundled in the repository |
| `docs/` | Diataxis documentation set |

## Documentation expectations

Documentation is part of the product surface. When behavior changes, update the relevant docs in the
same pull request.

Keep docs changes to these standards:

- every command must be executable
- code fences must include a language tag
- examples must match the real CLI surface
- task guides belong under `docs/how-to/`
- reference material belongs under `docs/reference/`
- tutorials should teach one path from start to finish

## Adding a new database engine

Each engine is a self-contained package that registers itself at startup:

1. Create `engine/{name}/{name}.go`.
1. Implement all eight methods of `engine.Engine`.
1. Add `func init() { engine.Register(&Engine{}) }`.
1. Add a blank import in `cmd/ditto/main.go`:

```go
_ "github.com/attaradev/ditto/engine/{name}"
```

1. Write tests in `engine/{name}/{name}_test.go` and tag integration tests with
   `//go:build integration`.

The `engine.Engine` interface is treated as stable. Do not add methods casually. Open an issue or
design discussion first if you need to change it.

The eight required methods are:

| Method | Notes |
| --- | --- |
| `Name() string` | Key used in `ditto.yaml` under `source.engine` |
| `ContainerImage() string` | Pin the image tag; never rely on `latest` |
| `ContainerEnv() []string` | Environment needed to initialise a copy container |
| `ConnectionString(host, port) string` | DSN for a copy using the fixed `ditto` credentials |
| `Dump(...) error` | Produce a compressed dump from the configured source |
| `DumpFromContainer(...) error` | Re-dump a running container, used when baking obfuscation |
| `Restore(...) error` | Restore into a running copy container |
| `WaitReady(port, timeout) error` | Block until the container accepts real connections |

## Code conventions

- Add doc comments for exported functions and types.
- Wrap errors with context, for example `fmt.Errorf("copy.Create: %w", err)`.
- Keep SQL parameterized; do not build SQL with string interpolation.
- Propagate context cancellation into blocking calls.
- Keep lifecycle fixes small and explicit. Subtle cleanup regressions are expensive.

## Reporting issues

Open a GitHub issue with:

- the ditto version from `ditto --version`
- OS and Docker runtime version
- the smallest reproducible example you can provide
- output from `ditto status` and `ditto copy logs <id>` when relevant

For security issues, follow [SECURITY.md](SECURITY.md) instead of filing a public issue.
