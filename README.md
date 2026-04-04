<p align="center">
  <img src="assets/logo.svg" alt="ditto" width="320">
</p>

<h1 align="center">ditto</h1>

<p align="center">
  <a href="https://github.com/attaradev/ditto/actions/workflows/ci.yml">
    <img src="https://github.com/attaradev/ditto/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI">
  </a>
  <a href="https://github.com/attaradev/ditto/releases/latest">
    <img src="https://img.shields.io/github/v/release/attaradev/ditto" alt="Release">
  </a>
  <a href="https://pkg.go.dev/github.com/attaradev/ditto">
    <img src="https://img.shields.io/badge/go-1.26%2B-00ADD8?logo=go&logoColor=white" alt="Go 1.26+">
  </a>
  <a href="https://goreportcard.com/report/github.com/attaradev/ditto">
    <img src="https://goreportcard.com/badge/github.com/attaradev/ditto" alt="Go Report Card">
  </a>
  <a href="LICENSE">
    <img src="https://img.shields.io/github/license/attaradev/ditto" alt="License: MIT">
  </a>
</p>

## Ephemeral database copies with real schema, real data shape, and no shared state

ditto provisions isolated Postgres and MySQL copies from a scheduled source dump. It is built for
teams that want production-faithful database behavior in CI, migration dry-runs, and local
development without sharing a mutable staging database.

- One clean copy per run, job, or developer
- PII scrubbed once during `ditto reseed`, then baked into every restored copy
- Works as a local CLI or as a shared HTTP service for remote runners

## Is ditto for you?

ditto is a good fit when:

- you already have a Postgres or MySQL source database that the Docker runtime can reach
- your tests or migrations need real constraints, triggers, and data shape
- shared staging state is making CI or local development unreliable
- you want to keep real production data out of developer laptops and CI logs

ditto is not a good fit when:

- you only need synthetic fixtures or an in-memory test database
- you cannot expose a network-reachable source host to the machine running ditto
- you want a hosted service instead of operating a local or shared ditto host

## How it works

1. `ditto reseed` writes a compressed dump from the configured source database.
2. `ditto copy create` or `ditto copy run` restores that dump into an ephemeral database container.
3. `ditto daemon` keeps the dump fresh and deletes expired copies.

See [Architecture](docs/explanation/architecture.md) for the full lifecycle and trade-offs.

## Choose your mode

| Mode | Use it when | Main commands |
| --- | --- | --- |
| Local host | The same machine can run Docker and owns the dump file | `reseed`, `copy create`, `copy run`, `daemon` |
| Shared server | CI runners or developers should request copies from a central host | `serve`, `copy create --server=...`, `copy run --server=...` |

## Install

### Go install

```bash
go install github.com/attaradev/ditto/cmd/ditto@latest
```

### Build from source

```bash
git clone https://github.com/attaradev/ditto
cd ditto
go build -o ./ditto ./cmd/ditto
```

The release workflow is configured to publish packaged binaries and OS packages. See
[Releases](https://github.com/attaradev/ditto/releases).

### SDKs

| Language | Install |
| --- | --- |
| Python 3.11+ | `pip install ditto-sdk` |
| Node.js 18+ | `npm install @attaradev/ditto-sdk` |
| Go | `import "github.com/attaradev/ditto/pkg/ditto"` |

## Quickstart

Prerequisites:

- a Docker-compatible runtime on the same host as ditto
- a Postgres or MySQL source host reachable from that runtime
- a read-only dump user on that source database

If your source host is `localhost`, `127.0.0.1`, or `::1`, this quickstart will fail. Dump helpers
run inside containers, so the source must be reachable from the Docker runtime by hostname or
network address.

After installation, the shortest working path is four commands:

```bash
export DITTO_SOURCE_URL='postgres://ditto_dump:secret@db.example.com:5432/myapp'
export DITTO_DUMP_PATH="$PWD/.ditto/latest.gz"
ditto reseed
ditto copy run -- env | grep '^DATABASE_URL='
```

What you should see:

- `ditto reseed` completes without replacing the previous dump on failure
- `ditto copy run` prints a `DATABASE_URL=...` line, proving the copy was created and injected
- the copy is destroyed automatically when the command exits

Useful next commands:

```bash
ditto status
ditto erd --output schema.mmd
ditto copy run -- go test ./...
```

For a repeatable file-based setup, move the environment variables into `ditto.yaml`. See
[Run Your First Copy](docs/tutorials/run-your-first-copy.md) and
[Configuration Reference](docs/reference/configuration.md).

## Common workflows

| Task | Command |
| --- | --- |
| Run one command against a throwaway copy | `ditto copy run -- go test ./...` |
| Start a shell session with a persistent copy | `eval "$(ditto env export)"` |
| Generate an ERD from a copy | `ditto erd --output schema.mmd` |
| Hold a copy across CI steps | `ditto copy create --format=json` and later `ditto copy delete <id>` |
| Serve copies to remote runners | `ditto serve` |

## Documentation

| Section | Start here |
| --- | --- |
| Tutorials | [Run your first copy](docs/tutorials/run-your-first-copy.md) |
| How-to guides | [Local development](docs/how-to/use-ditto-for-local-development.md), [CI](docs/how-to/use-ditto-in-ci.md), [Operate a host](docs/how-to/operate-a-ditto-host.md), [Troubleshooting](docs/how-to/troubleshoot.md) |
| Reference | [Configuration](docs/reference/configuration.md), [CLI](docs/reference/cli.md) |
| Explanation | [Architecture](docs/explanation/architecture.md) |

The docs landing page is at [docs/README.md](docs/README.md).

## Trust and project health

- CI, build, integration, and markdown checks run on every push and pull request
- Security reports are handled privately; see [SECURITY.md](SECURITY.md)
- Release history is tracked in [CHANGELOG.md](CHANGELOG.md)
- Community expectations are in [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)
- Contributor workflow is documented in [CONTRIBUTING.md](CONTRIBUTING.md)

## License

[MIT](LICENSE)
