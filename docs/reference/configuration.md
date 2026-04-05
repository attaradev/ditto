# Configuration Reference

ditto loads configuration from `ditto.yaml` and from environment variables prefixed with `DITTO_`.

Search order for config files:

1. the path passed with `--config`
2. `./ditto.yaml`
3. `~/.ditto/ditto.yaml`
4. `/etc/ditto/ditto.yaml`

Environment variables override file values. For example:

```bash
DITTO_SOURCE_HOST=db.staging.example.com ditto copy create
```

## Smallest environment-only setup

If you do not want a config file yet, these environment variables are enough to run locally:

```bash
export DITTO_SOURCE_URL='postgres://ditto_dump:secret@db.example.com:5432/myapp'
export DITTO_DUMP_PATH="$PWD/.ditto/latest.gz"
```

If you are not using `source.url`, you must provide the individual source fields instead.

## Minimal file-based config

```yaml
source:
  engine: postgres
  host: db.example.com
  port: 5432
  database: myapp
  user: ditto_dump
  password_secret: env:DB_PASSWORD

dump:
  path: /data/dump/latest.gz

copy_ttl_seconds: 7200
port_pool_start: 5433
port_pool_end: 5600
```

## `source`

### `source.url`

Use a single URL instead of individual fields:

```yaml
source:
  url: postgres://ditto_dump:secret@db.example.com:5432/myapp
```

Supported URL schemes:

- `postgres`
- `postgresql`
- `mysql`
- `mariadb`

If both `source.url` and individual `source.*` fields are set, the individual fields win.

### Required source fields

If you do not use `source.url`, these fields are required:

| Field | Notes |
| --- | --- |
| `source.engine` | `postgres` or `mysql` |
| `source.host` | Must be reachable from the Docker runtime; loopback hosts are rejected |
| `source.database` | Database name to dump |
| `source.user` | Read-only dump user |
| `source.password` or `source.password_secret` | Plaintext for dev only; secret reference for real hosts |

`source.port` defaults to `5432` for Postgres and `3306` for MySQL when omitted.

### Secret references

`password_secret` and `token_secret` support:

| Format | Backend |
| --- | --- |
| `env:MY_VAR` | Environment variable |
| `file:/run/secrets/db_password` | File contents |
| `arn:aws:secretsmanager:...` | AWS Secrets Manager |

Example:

```yaml
source:
  password_secret: env:DB_PASSWORD
```

## `dump`

```yaml
dump:
  schedule: "0 * * * *"
  path: /data/dump/latest.gz
  stale_threshold: 7200
  client_image: ""
  schema_path: ""
```

| Field | Default | Meaning |
| --- | --- | --- |
| `dump.schedule` | hourly | Cron schedule used by `ditto daemon` |
| `dump.path` | `/data/dump/latest.gz` | Local path for the compressed dump |
| `dump.stale_threshold` | `7200` | Freshness budget in seconds; staleness warnings appear once the file is roughly 2x older than this |
| `dump.client_image` | engine default | Optional helper image for dump operations |
| `dump.schema_path` | empty | Optional path for a DDL-only dump alongside the full dump |

The scheduler writes to `<path>.tmp` and then atomically renames the file into place.

## Copy lifecycle settings

| Field | Default | Meaning |
| --- | --- | --- |
| `copy_ttl_seconds` | `7200` | Default lifetime for new copies |
| `port_pool_start` | `5433` | First host port available for copy containers |
| `port_pool_end` | `5600` | Last host port available for copy containers |
| `warm_pool_size` | `0` | Number of pre-warmed copies to keep ready |
| `copy_image` | engine default | Optional image override for copy containers |
| `docker_host` | empty | Optional Docker daemon override |

Warm pools are maintained by `ditto daemon` or `ditto serve`.

## `server`

```yaml
server:
  addr: ":8080"
  token: ""
  token_secret: env:DITTO_TOKEN
```

| Field | Default | Meaning |
| --- | --- | --- |
| `server.addr` | `:8080` | Listen address for `ditto serve` |
| `server.token` | empty | Plaintext bearer token for development only |
| `server.token_secret` | empty | Secret reference for the bearer token |

If no token is configured, `ditto serve` accepts unauthenticated requests.

## Obfuscation

Obfuscation rules can be baked into the dump during `ditto reseed`. That is the safest path because
every later copy is restored from an already-scrubbed dump.

```yaml
obfuscation:
  rules:
    - table: users
      column: email
      strategy: replace
      type: email
    - table: users
      column: phone
      strategy: replace
      type: phone
    - table: payments
      column: card_number
      strategy: mask
      keep_last: 4
      mask_char: "*"
```

Supported strategies:

| Strategy | Effect |
| --- | --- |
| `replace` | Deterministic format-preserving substitution |
| `hash` | One-way SHA-256 hex digest |
| `mask` | Replace characters with `mask_char` (default `*`); `keep_last` preserves a trailing suffix |
| `redact` | Replace the value with `[redacted]` or `with:` |
| `nullify` | Set the column to `NULL` |

Supported `replace` types:

- `email`
- `name`
- `phone`
- `ip`
- `url`
- `uuid`

## Full example

See [`ditto.yaml.example`](../../ditto.yaml.example) for the annotated example committed in the
repository.
