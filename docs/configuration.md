# Configuration

ditto is configured via `ditto.yaml` in the current directory or `~/.ditto/ditto.yaml`. Any field
can be overridden at runtime with an environment variable using the `DITTO_` prefix and dots
replaced by underscores:

```bash
DITTO_SOURCE_HOST=db.staging.example.com ditto copy create
```

See [`ditto.yaml.example`](../ditto.yaml.example) for a complete annotated reference.

---

## Minimal config

```yaml
source:
  engine: postgres          # or mysql
  host: db.example.com
  port: 5432
  database: myapp
  user: ditto_dump
  password: secret          # dev only — use password_secret in production

dump:
  path: /data/dump/latest.gz

copy_ttl_seconds: 7200
port_pool_start: 5433
port_pool_end: 5600
```

---

## Connection URL

Supply the source as a single URL instead of individual fields:

```yaml
source:
  url: postgres://ditto_dump:secret@db.example.com:5432/myapp
```

Supported URL schemes: `postgres`, `postgresql`, `mysql`, `mariadb`. Individual fields override URL
values when both are set.

---

## Secret references

`password_secret` and `token_secret` accept a backend prefix so credentials are never stored in config files:

| Format | Backend |
| --- | --- |
| `env:MY_VAR` | Environment variable `MY_VAR` |
| `file:/run/secrets/pw` | File contents (Docker secrets, Kubernetes mounts) |
| `arn:aws:secretsmanager:...` | AWS Secrets Manager (cached 5 min) |

```yaml
source:
  password_secret: env:DB_PASSWORD
  # password_secret: file:/run/secrets/db_password
  # password_secret: arn:aws:secretsmanager:us-east-1:123456789012:secret:name
```

---

## Warm copy pool

Pre-warm N copies so `ditto copy create` returns in under a second:

```yaml
warm_pool_size: 3   # keep 3 ready copies; 0 (default) disables the pool
```

The daemon refills the pool in the background after each claim. A value of 2–3 covers most CI parallelism patterns.

---

## PII obfuscation

When obfuscation rules are configured, `ditto reseed` bakes them into the dump file once — every
copy restored from that file is already PII-free. No scrubbing happens at restore time unless you
explicitly pass `--obfuscate` (useful when restoring a raw external dump via `--dump`).

### Strategies

| Strategy | Effect |
| --- | --- |
| `replace` | Deterministic format-preserving substitution — looks like real data, isn't |
| `hash` | One-way SHA-256 hex digest — preserves uniqueness for `JOIN`s |
| `mask` | Replaces characters with `*` (configurable `mask_char` and `keep_last`) |
| `redact` | Replaces the value with `[redacted]` (configurable via `with:`) |
| `nullify` | Sets the column to `NULL` |

### `replace` types

`replace` is the recommended strategy for most PII. It generates realistic-looking values derived
deterministically from the original, so foreign key relationships and `JOIN`s still work:

| `type` | Example output |
| --- | --- |
| `email` | `user483921@example.com` |
| `name` | `User74831` |
| `phone` | `+1-555-0147-3821` (NANP fictional range) |
| `ip` | `10.42.17.3` (RFC 1918 — never a public address) |
| `url` | `https://example.com/r/a3f92b1c8d04` |
| `uuid` | `a3f92b1c-8d04-4e2f-b3a1-9c2d8f7e1b05` |

### Example rules

```yaml
obfuscation:
  rules:
    - table: users
      column: email
      strategy: replace
      type: email           # user483921@example.com

    - table: users
      column: full_name
      strategy: replace
      type: name            # User74831

    - table: users
      column: phone
      strategy: replace
      type: phone           # +1-555-0147-3821

    - table: events
      column: ip_address
      strategy: replace
      type: ip              # 10.42.17.3

    - table: users
      column: ssn
      strategy: nullify     # NULL — no substitute value needed

    - table: users
      column: notes
      strategy: redact      # [redacted] — freeform text with no useful shape

    - table: payments
      column: card_number
      strategy: mask
      keep_last: 4          # ************1234
```

---

## Runtime settings

Override the container image used for copy containers to pin a specific version:

```yaml
copy_image: "postgres:15-alpine"   # default: postgres:16-alpine
# copy_image: "mysql:5.7"         # default: mysql:8.4
```

Override the helper image used for dump operations when the source database needs a different client version:

```yaml
dump:
  client_image: "postgres:15-alpine"
```

Point ditto at a specific Docker-compatible daemon:

```yaml
docker_host: "unix:///var/run/docker.sock"
```

---

## HTTP server

```yaml
server:
  addr: ":8080"
  token: ""                        # plaintext token (dev only — never commit real tokens)
  token_secret: env:DITTO_TOKEN    # or file:/run/secrets/token, or arn:aws:...
```

---

## Dump settings

```yaml
dump:
  schedule: "0 * * * *"         # cron — default: hourly
  path: /data/dump/latest.gz    # where to store the compressed dump
  stale_threshold: 7200         # warn if the dump file is older than this (seconds)
  client_image: ""              # optional helper image override (see Runtime settings)
  schema_path: ""               # optional path for a DDL-only dump alongside the full dump
```

The scheduler writes to `<path>.tmp` then atomically renames to `<path>`, so readers always see a complete file.
