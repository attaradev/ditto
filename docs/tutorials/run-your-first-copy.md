# Run Your First Copy

This tutorial is the fastest way to prove that ditto works in your environment.

By the end you will:

- create a dump from a real source database
- provision a throwaway copy from that dump
- confirm that ditto injects a working `DATABASE_URL`

## Before you begin

You need:

- a Docker-compatible runtime on the same machine as ditto
- a Postgres or MySQL source database
- a source host reachable from that runtime
- a read-only database user for dumping

Do not use `localhost`, `127.0.0.1`, or `::1` as the source host. Dump helpers run inside
containers, so the source must be reachable from the container runtime.

## 1. Install ditto

Choose whichever method fits your environment:

```bash
# Homebrew (macOS and Linux)
brew tap attaradev/ditto
brew install --cask ditto

# Go install (requires Go 1.26+)
go install github.com/attaradev/ditto/cmd/ditto@latest
```

Pre-built binaries and Linux packages (`.deb`, `.rpm`, `.apk`) are also available on
[GitHub Releases](https://github.com/attaradev/ditto/releases).

## 2. Point ditto at your source database

Use environment variables for the first run so you do not have to write a config file yet:

```bash
export DITTO_SOURCE_URL='postgres://ditto_dump:secret@db.example.com:5432/myapp'
export DITTO_DUMP_PATH="$PWD/.ditto/latest.gz"
```

If you are using MySQL or MariaDB, use a URL such as
`mysql://ditto_dump:secret@db.example.com:3306/myapp`.

## 3. Create the first dump

```bash
ditto reseed
```

This creates the compressed dump at `"$PWD/.ditto/latest.gz"` and only replaces the previous dump
after a successful refresh.

## 4. Run one command against a fresh copy

```bash
ditto copy run -- env | grep '^DATABASE_URL='
```

If the command succeeds, you should see a `DATABASE_URL=...` line printed to stdout. That confirms
all of the following:

- ditto found the dump
- ditto restored a fresh copy
- ditto injected the connection string into the child process
- ditto cleaned up the copy when the command exited

## 5. Confirm host state

```bash
ditto status
```

After the `copy run` command exits, `Active copies` should return to `0` unless another process is
using a copy.

## Where to go next

- Move these environment variables into `ditto.yaml` using the
  [Configuration reference](../reference/configuration.md)
- Use [local development](../how-to/use-ditto-for-local-development.md) for persistent shell sessions
- Use [CI integration](../how-to/use-ditto-in-ci.md) to scope copies to jobs or workflows
- Read [Architecture and operating model](../explanation/architecture.md) before running a shared host
