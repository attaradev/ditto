# Troubleshoot ditto

Use this guide when setup or runtime behavior does not match expectations.

## `ditto.yaml not found or missing required fields`

Cause:

- ditto could not load a config file and you did not provide the required `DITTO_*` environment
  variables

Fix:

- create `ditto.yaml` from the [configuration reference](../reference/configuration.md)
- or set `DITTO_SOURCE_URL`, `DITTO_DUMP_PATH`, and the other required fields in the environment

## `source host "localhost" is not reachable from dump helper containers`

Cause:

- the source host is loopback, but dump helpers run inside containers

Fix:

- use a network-reachable hostname or address instead of `localhost`, `127.0.0.1`, or `::1`
- if the source runs on the same machine, expose it via a hostname or address the Docker runtime can
  reach

## `docker runtime: no Docker-compatible daemon found`

Cause:

- the host cannot find or connect to Docker

Fix:

- start Docker Desktop or the local Docker daemon
- or set `DOCKER_HOST` or `docker_host` in config to a reachable daemon socket

Verify:

```bash
ditto status
```

## `dump file not found ... run 'ditto reseed' first`

Cause:

- `copy create`, `copy run`, or `erd` is trying to restore from a dump path that does not exist yet

Fix:

```bash
ditto reseed
```

If you are using `--dump`, verify that the local path, `s3://` URI, or `https://` URL is valid.

## Dump exists but `ditto status` marks it stale

Cause:

- the dump file is older than the freshness budget for your environment

Fix:

- run `ditto reseed` immediately
- make sure `ditto daemon` or your cron job is actually running
- revisit `dump.schedule` and `dump.stale_threshold`

The current implementation warns when the dump age grows beyond roughly twice `dump.stale_threshold`.

## `copy create` or `copy run` hangs or fails during restore

Cause:

- the host cannot pull or start the configured container image
- the port pool is exhausted
- the dump file is large enough that restore simply takes time

Fix:

- check `ditto status` for active copies and free capacity
- run `ditto copy list` to see whether old copies are still consuming ports
- delete abandoned copies with `ditto copy delete <id>`
- make sure the configured copy image and client image are valid

## Remote server requests fail with `401` or `403`

Cause:

- `DITTO_TOKEN` is missing or does not match the server's configured token

Fix:

```bash
export DITTO_TOKEN=my-secret-token
ditto copy create --server=http://ditto.internal:8080
```

Also verify that the server was started with `server.token` or `server.token_secret` configured as
expected.

## `ditto erd --source` fails but `ditto erd` works

Cause:

- direct source introspection has different network or TLS requirements than copy-based introspection

Fix:

- prefer `ditto erd` without `--source` unless you specifically need a direct connection to the
  source database
- confirm that the source database accepts direct connections from the host running ditto

## Integration tests fail locally

Cause:

- integration tests under `./engine/...` require Docker

Fix:

```bash
go test -tags=integration -race -count=1 -timeout=15m ./engine/...
```

Make sure Docker is running before you execute that command.

## Still stuck?

Collect these details before opening an issue:

- `ditto --version`
- `ditto status`
- `ditto copy logs <id>` if a specific copy failed
- OS and Docker runtime version
- the smallest reproducible config or command sequence
