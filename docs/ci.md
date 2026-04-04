# CI integration

ditto works with any CI platform. Use the composite GitHub Actions for GitHub-hosted or self-hosted
runners, or run `ditto serve` on shared infrastructure and point any runner at it with `--server`.

---

## Copy lifecycle flags

These flags apply to `ditto copy create` and `ditto copy run`:

| Flag | Purpose |
| --- | --- |
| `--format=json\|pipe\|auto` | `pipe` prints only the connection string; `json` includes the copy ID; `auto` (default) pretty-prints to a terminal and behaves like `pipe` when stdout is redirected |
| `--ttl 30m` | Override the copy lifetime for this copy |
| `--label <name>` | Tag the copy with a run identifier (overrides auto-detected CI env vars) |
| `--dump <uri>` | Restore from a specific file instead of the configured dump — accepts a local path, `s3://bucket/key`, or `https://` URL |
| `--obfuscate` | Apply configured obfuscation rules post-restore; use with `--dump` when the source file has not already been obfuscated |

Two environment variables are injected into commands run via `ditto copy run`:

| Variable | Value |
| --- | --- |
| `DATABASE_URL` | Connection string for the copy |
| `DITTO_COPY_ID` | Copy ID (for debugging) |

---

## Manual create / delete

When you need to hold a copy across multiple pipeline steps, manage it explicitly:

```sh
COPY=$(ditto copy create --format=json)
export DATABASE_URL=$(echo "$COPY" | jq -r '.connection_string')
COPY_ID=$(echo "$COPY" | jq -r '.id')

# ... run tests, migrations, etc. ...

ditto copy delete "$COPY_ID"
```

---

## GitHub Actions — self-hosted runner

The runner host must have ditto installed and a Docker-compatible runtime available.

```yaml
jobs:
  test:
    runs-on: self-hosted
    steps:
      - uses: actions/checkout@v4

      - id: db
        uses: attaradev/ditto/actions/create@v1
        with:
          ttl: 1h

      - run: go test ./...
        env:
          DATABASE_URL: ${{ steps.db.outputs.database_url }}

      - uses: attaradev/ditto/actions/delete@v1
        if: always()
        with:
          copy_id: ${{ steps.db.outputs.copy_id }}
```

---

## Any CI platform — server mode

Run `ditto serve` on your infrastructure and point any runner at it with `--server`. The runner
does not need Docker access or a local dump file.

```sh
# On your ditto host
ditto serve

# In any CI job (GitHub-hosted, GitLab CI, CircleCI, Buildkite, etc.)
COPY=$(ditto copy create --server=http://ditto.internal:8080 --format=json)
export DATABASE_URL=$(echo "$COPY" | jq -r '.connection_string')
COPY_ID=$(echo "$COPY" | jq -r '.id')

# ... run tests ...

ditto copy delete "$COPY_ID" --server=http://ditto.internal:8080
```

Set `DITTO_TOKEN` for authenticated servers:

```sh
export DITTO_TOKEN=my-secret-token
ditto copy create --server=http://ditto.internal:8080
```

GitHub Actions with server mode:

```yaml
- id: db
  uses: attaradev/ditto/actions/create@v1
  with:
    server_url: http://ditto.internal:8080
    ditto_token: ${{ secrets.DITTO_TOKEN }}
    ttl: 1h
```

---

## Go SDK

Import `github.com/attaradev/ditto/pkg/ditto`.

**In tests** — `NewCopy` provisions a copy and registers `t.Cleanup` to destroy it when the test finishes:

```go
import "github.com/attaradev/ditto/pkg/ditto"

func TestMyFeature(t *testing.T) {
    dsn := ditto.NewCopy(t,
        ditto.WithServerURL("http://ditto.internal:8080"),
        ditto.WithToken(os.Getenv("DITTO_TOKEN")),
        ditto.WithTTL(10*time.Minute),
    )
    db, _ := sql.Open("pgx", dsn)
    // copy is destroyed automatically when the test finishes
}
```

**Outside tests** — `WithCopy` scopes the copy to a function call:

```go
client := ditto.New(
    ditto.WithServerURL("http://ditto.internal:8080"),
    ditto.WithToken(os.Getenv("DITTO_TOKEN")),
)

err := client.WithCopy(ctx, func(dsn string) error {
    return runMigrations(dsn)
})
```

---

## Python SDK

Install with pip:

```sh
pip install "ditto-sdk[pytest]"
```

**pytest fixture** — auto-registered when the package is installed; no `conftest.py` changes needed:

```python
def test_my_feature(ditto_copy):
    conn = psycopg2.connect(ditto_copy)
    # copy is destroyed automatically after the test
```

Configure via environment variables: `DITTO_SERVER_URL` (required), `DITTO_TOKEN`, `DITTO_TTL` (lifetime in seconds).

**Programmatic use:**

```python
from ditto import Client

client = Client(server_url="http://ditto.internal:8080", token="secret")

with client.with_copy() as dsn:
    run_migrations(dsn)
```
