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

If you do not want a config file yet, this is enough to get started locally:

```bash
export DITTO_SOURCE_URL='postgres://ditto_dump:secret@db.example.com:5432/myapp'
```

Add `DITTO_DUMP_PATH` only if you want a non-default dump location:

```bash
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
  schedule: "0 * * * *"

copy_ttl_seconds: 7200
port_pool_start: 5433
port_pool_end: 5600
```

If you omit `dump.path`, ditto uses its built-in default dump path. If you set `dump.path` in
YAML, use an absolute path. `~` is not expanded inside config values.

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

`source.password_secret`, `server.copy_secret_secret`, and `server.auth.static_token` support:

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
  path: /absolute/path/to/latest.gz
  stale_threshold: 7200
  client_image: ""
  schema_path: ""
  on_failure:
    webhook_url: ""
    exec: ""
```

| Field | Default | Meaning |
| --- | --- | --- |
| `dump.schedule` | hourly | Cron schedule used by `ditto host` |
| `dump.path` | built-in default: home-directory `.ditto/latest.gz` locally, otherwise `/data/dump/latest.gz` | Path for the compressed dump. If you set it explicitly in YAML, use an absolute path; `~` is not expanded. |
| `dump.stale_threshold` | `7200` | Freshness budget in seconds; staleness warnings appear once the file is roughly 2x older than this |
| `dump.client_image` | engine default | Optional helper image for dump operations |
| `dump.schema_path` | empty | Optional path for a DDL-only dump alongside the full dump |
| `dump.on_failure.webhook_url` | empty | HTTP endpoint to POST a JSON failure payload when a scheduled dump fails |
| `dump.on_failure.exec` | empty | Shell command to run when a scheduled dump fails (`webhook_url` takes precedence) |

The scheduler writes to `<path>.tmp` and then atomically renames the file into place.

### Dump failure alerts

When a scheduled dump fails, ditto can notify you immediately instead of silently serving stale data:

```yaml
dump:
  on_failure:
    webhook_url: https://hooks.slack.com/services/...
```

Or run an arbitrary command:

```yaml
dump:
  on_failure:
    exec: "echo 'dump failed' | mail ops@example.com"
```

The JSON payload posted to `webhook_url` includes `error`, `timestamp`, `last_dump_age`, and `dump_path`.

## Copy lifecycle settings

| Field | Default | Meaning |
| --- | --- | --- |
| `copy_ttl_seconds` | `7200` | Default lifetime for new copies |
| `port_pool_start` | `5433` | First host port available for copy containers |
| `port_pool_end` | `5600` | Last host port available for copy containers |
| `warm_pool_size` | `0` | Number of pre-warmed copies to keep ready |
| `copy_image` | engine default | Optional image override for copy containers |
| `docker_host` | empty | Optional Docker daemon override |

Warm pools are maintained by `ditto host`.

## `server`

```yaml
server:
  enabled: true
  addr: ":8080"
  advertise_host: ditto.internal
  db_bind_host: 0.0.0.0
  copy_secret_secret: env:DITTO_COPY_SECRET
  auth:
    # Option A — simple shared secret (evaluation and single-operator setups).
    # If static_token is set, ditto uses it and ignores the OIDC fields below.
    static_token: env:DITTO_STATIC_TOKEN
    # Option B — OIDC (recommended for production multi-user environments).
    # Remove static_token when you switch to OIDC.
    # issuer: https://issuer.example.com/
    # audience: ditto-ci
    # jwks_url: https://issuer.example.com/.well-known/jwks.json
    # admin_claim: role
    # admin_value: ditto-admin
  db_tls:
    # Required in current shared-host mode.
    cert_file: /etc/ditto/tls/server.crt
    key_file: /etc/ditto/tls/server.key
```

| Field | Default | Meaning |
| --- | --- | --- |
| `server.enabled` | `false` | Enables shared-host mode for `ditto host` |
| `server.addr` | `:8080` | Listen address for the shared-host API |
| `server.advertise_host` | empty | Hostname or address returned in remote copy DSNs |
| `server.db_bind_host` | empty | Interface used when publishing copy container ports |
| `server.copy_secret_secret` | empty | Secret reference used to derive per-copy database credentials |
| `server.auth.static_token` | empty | Single shared secret; all requests share one identity. Accepts secret references. Use for evaluation only. |
| `server.auth.issuer` | empty | Expected JWT issuer for OIDC bearer-token validation |
| `server.auth.audience` | empty | Expected JWT audience for OIDC bearer-token validation |
| `server.auth.jwks_url` | empty | JWKS endpoint used to fetch signing keys |
| `server.auth.admin_claim` | empty | Optional JWT claim name used to recognize admin callers |
| `server.auth.admin_value` | empty | Required value for `server.auth.admin_claim` |
| `server.db_tls.cert_file` | empty | Certificate mounted into remote copy containers. Required when `server.enabled` is true in the current implementation. |
| `server.db_tls.key_file` | empty | Private key mounted into remote copy containers. Required when `server.enabled` is true in the current implementation. |

`ditto host` requires `server.copy_secret_secret`, both `server.db_tls` files, and one auth mode.
If `server.auth.static_token` is set, ditto uses static-token auth. Otherwise it expects the full
OIDC block (`issuer` + `audience` + `jwks_url`). For production, omit `static_token` and configure
OIDC only.

Clients supply the token via `DITTO_TOKEN`:

```bash
export DITTO_TOKEN=my-static-secret            # static token mode
export DITTO_TOKEN="$(cat oidc.jwt)"           # OIDC mode
ditto copy create --server=http://ditto.internal:8080
```

When static token mode is active, `ditto host` emits a warning on every authenticated request as a
reminder to migrate to OIDC before production use.

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
      column: full_name
      strategy: replace
      type: name
    - table: users
      column: phone
      strategy: replace
      type: phone
    - table: users
      column: ssn
      strategy: nullify
    - table: users
      column: notes
      strategy: redact
    - table: users
      column: api_key
      strategy: hash
    - table: users
      column: account_uuid
      strategy: replace
      type: uuid
    - table: payment_methods
      column: card_number
      strategy: mask
      keep_last: 4
      mask_char: "*"
    - table: payment_methods
      column: billing_email
      strategy: replace
      type: email
    - table: audit_logs
      column: ip_address
      strategy: replace
      type: ip
    - table: audit_logs
      column: target_url
      strategy: replace
      type: url
    - table: audit_logs
      column: actor_uuid
      strategy: replace
      type: uuid
```

Optional zero-row exception:

```yaml
obfuscation:
  rules:
    - table: archived_customers
      column: email
      strategy: redact
      warn_only: true   # 0-row match emits a warning instead of failing the dump
```

ditto validates referenced columns against the source schema before starting a dump. If a rule
references a table or column that does not exist, the dump fails with a clear error naming the
missing field.

During obfuscation, a rule that updates 0 rows also fails by default. Set `warn_only: true` only to
downgrade that zero-row case to a warning. It does not bypass missing table or column checks.

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

For a full before/after walkthrough using this exact schema shape, see
[Demonstrate obfuscation end to end](../tutorials/demonstrate-obfuscation-end-to-end.md).

## Full example

See [`ditto.yaml.example`](../../ditto.yaml.example) for the annotated example committed in the
repository.
