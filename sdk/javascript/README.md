# ditto JavaScript SDK

TypeScript client for provisioning ephemeral database copies from a running ditto server.

## Install

```bash
npm install @attaradev/ditto-sdk
```

## Usage

```ts
import { DittoClient } from "@attaradev/ditto-sdk";

const client = new DittoClient({
  serverUrl: "http://ditto.internal:8080",
  token: process.env.DITTO_TOKEN
});

const copy = await client.create({ ttlSeconds: 600 });
console.log(copy.connection_string);
await client.destroy(copy.id);
```

Use `withCopy` when you want automatic cleanup:

```ts
import { DittoClient } from "@attaradev/ditto-sdk";

const client = new DittoClient({ serverUrl: "http://ditto.internal:8080" });

await client.withCopy(async (dsn) => {
  console.log(dsn);
});
```

## Environment variables

`DittoClient` reads these variables by default:

- `DITTO_SERVER_URL`
- `DITTO_TOKEN`
- `DITTO_TTL`
