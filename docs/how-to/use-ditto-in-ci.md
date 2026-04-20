# Use ditto in CI

Use this guide when a CI job needs a clean database with real schema and data shape.

## Choose the right lifecycle

Use `copy run` when one command needs one copy:

```bash
ditto copy run -- go test ./...
```

Use `copy create` and `copy delete` when a job needs to hold a copy across multiple steps.

Use `--server` when the CI runner should talk to a shared ditto host instead of running locally.

## Hold a copy across multiple steps

Create the copy and capture both the connection string and ID:

```bash
COPY=$(ditto copy create --format=json --ttl 1h)
DATABASE_URL=$(echo "$COPY" | python3 -c "import sys, json; print(json.load(sys.stdin)['connection_string'])")
COPY_ID=$(echo "$COPY" | python3 -c "import sys, json; print(json.load(sys.stdin)['id'])")
```

Run your workflow:

```bash
DATABASE_URL="$DATABASE_URL" go test ./...
DATABASE_URL="$DATABASE_URL" ./migrate up
```

Delete the copy even if a later step fails:

```bash
ditto copy delete "$COPY_ID"
```

## GitHub Actions on a self-hosted runner

This path assumes the runner host already has ditto installed and can access the Docker runtime.

```yaml
jobs:
  test:
    runs-on: self-hosted
    steps:
      - uses: actions/checkout@v4

      - name: Create copy
        id: db
        shell: bash
        run: |
          COPY=$(ditto copy create --format=json --ttl 1h)
          DATABASE_URL=$(echo "$COPY" | python3 -c "import sys, json; print(json.load(sys.stdin)['connection_string'])")
          COPY_ID=$(echo "$COPY" | python3 -c "import sys, json; print(json.load(sys.stdin)['id'])")
          echo "::add-mask::${DATABASE_URL}"
          echo "DATABASE_URL=${DATABASE_URL}" >> "$GITHUB_ENV"
          echo "COPY_ID=${COPY_ID}" >> "$GITHUB_ENV"

      - name: Run tests
        run: go test ./...

      - name: Delete copy
        if: always()
        run: ditto copy delete "$COPY_ID"
```

## Use a shared ditto host from any CI system

On the host that owns the dump file and Docker runtime:

```bash
ditto host
```

In the CI job:

```bash
export DITTO_TOKEN="$(cat oidc.jwt)"
COPY=$(ditto copy create --server=http://ditto.internal:8080 --format=json --ttl 1h)
DATABASE_URL=$(echo "$COPY" | python3 -c "import sys, json; print(json.load(sys.stdin)['connection_string'])")
COPY_ID=$(echo "$COPY" | python3 -c "import sys, json; print(json.load(sys.stdin)['id'])")
```

Run your job and delete the copy:

```bash
DATABASE_URL="$DATABASE_URL" go test ./...
ditto copy delete "$COPY_ID" --server=http://ditto.internal:8080
```

## Common flags

`copy create` and `copy run` share the most important lifecycle flags:

| Flag | Purpose |
| --- | --- |
| `--ttl 30m` | Override the copy lifetime |
| `--label <name>` | Tag the copy with a run identifier |
| `--dump <uri>` | Restore from a specific local path, `s3://`, or `https://` source |
| `--obfuscate` | Apply configured obfuscation rules after restore |

See [CLI reference](../reference/cli.md) for the full command surface.

## Use the Go SDK

Import `github.com/attaradev/ditto/pkg/ditto`.

In tests:

```go
import "github.com/attaradev/ditto/pkg/ditto"

func TestMyFeature(t *testing.T) {
    dsn := ditto.NewCopy(t,
        ditto.WithServerURL("http://ditto.internal:8080"),
        ditto.WithToken(os.Getenv("DITTO_TOKEN")),
        ditto.WithTTL(10*time.Minute),
    )
    db, _ := sql.Open("pgx", dsn)
    _ = db
}
```

Outside tests:

```go
client := ditto.New(
    ditto.WithServerURL("http://ditto.internal:8080"),
    ditto.WithToken(os.Getenv("DITTO_TOKEN")),
)

err := client.WithCopy(ctx, func(dsn string) error {
    return runMigrations(dsn)
})
```

## Use the Python SDK

The Python SDK requires Python 3.11+.

Install from PyPI:

```bash
pip install "ditto-sdk[pytest]"
```

Pytest fixture:

```python
def test_my_feature(ditto_copy):
    conn = psycopg2.connect(ditto_copy)
    assert conn is not None
```

Programmatic use:

```python
import os

from ditto import Client

client = Client(server_url="http://ditto.internal:8080", token=os.environ["DITTO_TOKEN"])

with client.with_copy() as dsn:
    run_migrations(dsn)
```

The Python SDK reads `DITTO_SERVER_URL`, `DITTO_TOKEN`, and `DITTO_TTL` from the environment when
they are present. In shared-host mode, `DITTO_TOKEN` is typically a short-lived OIDC JWT.

## Use the JavaScript / TypeScript SDK

Install from npm:

```bash
npm install @attaradev/ditto-sdk
```

Example:

```ts
import { DittoClient } from "@attaradev/ditto-sdk";

const client = new DittoClient({
  serverUrl: "http://ditto.internal:8080",
  token: process.env.DITTO_TOKEN,
  ttlSeconds: 600,
});

await client.withCopy(async (dsn) => {
  await runMigrations(dsn);
});
```

The JavaScript SDK reads `DITTO_SERVER_URL`, `DITTO_TOKEN`, and `DITTO_TTL` from the environment
when they are present. In shared-host mode, `DITTO_TOKEN` is typically a short-lived OIDC JWT.
