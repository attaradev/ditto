# Troubleshoot ditto

Use this guide when setup or runtime behavior does not match expectations.

## Start here: `ditto doctor`

Before diving into specific errors, run:

```bash
ditto doctor
```

This checks Docker reachability, configuration validity, dump file existence and freshness, source
database connectivity, and (when configured) the OIDC JWKS endpoint. It prints a green/red
checklist and exits non-zero if any check fails.

Fix all red items first, then re-run the command that was failing.

---

## `ditto.yaml not found or missing required fields`

Cause:

- ditto could not load a config file and you did not provide the required `DITTO_*` environment
  variables

Fix:

- create `ditto.yaml` from the [configuration reference](../reference/configuration.md)
- or set `DITTO_SOURCE_URL` and any other non-default `DITTO_*` overrides you need

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

If you set `dump.path` in `ditto.yaml`, make sure it is an absolute path. `~` is not expanded
inside YAML values.

## Dump exists but `ditto status` marks it stale

Cause:

- the dump file is older than the freshness budget for your environment

Fix:

- run `ditto reseed` immediately
- make sure `ditto host` or your cron job is actually running
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

- `DITTO_TOKEN` is missing, expired, or does not match the host's configured auth

Fix for **static token** mode:

```bash
export DITTO_TOKEN="$DITTO_STATIC_TOKEN"
ditto copy create --server=http://ditto.internal:8080
```

Fix for **OIDC** mode:

```bash
export DITTO_TOKEN="$(cat oidc.jwt)"
ditto copy create --server=http://ditto.internal:8080
```

Also verify that the host was started with the correct `server.auth.*` settings. Run `ditto doctor`
on the host to confirm the OIDC JWKS endpoint is reachable if OIDC is configured.

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

- `ditto doctor` output
- `ditto --version`
- `ditto status`
- `ditto copy logs <id>` if a specific copy failed
- OS and Docker runtime version
- the smallest reproducible config or command sequence
