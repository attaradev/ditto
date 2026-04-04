# ditto Python SDK

Python client for provisioning ephemeral database copies from a running ditto server.

## Install

```bash
pip install "ditto-sdk[pytest]"
```

For development from a repository checkout:

```bash
pip install -e "./sdk/python[pytest]"
```

## Usage

```python
from ditto import Client

client = Client(
    server_url="http://ditto.internal:8080",
    token="secret",
    ttl_seconds=600,
)

copy = client.create()
print(copy["connection_string"])
client.destroy(copy["id"])
```

Use the context manager for automatic cleanup:

```python
from ditto import Client

client = Client(server_url="http://ditto.internal:8080")

with client.with_copy() as dsn:
    print(dsn)
```

## Environment variables

`Client` reads these variables by default:

- `DITTO_SERVER_URL`
- `DITTO_TOKEN`
- `DITTO_TTL`
