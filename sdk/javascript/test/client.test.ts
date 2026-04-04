import assert from "node:assert/strict";
import { createServer } from "node:http";
import type { IncomingMessage, ServerResponse } from "node:http";
import test from "node:test";

import { DittoClient, DittoError } from "../src/index.js";

type TestHandler = (req: IncomingMessage, res: ServerResponse<IncomingMessage>) => void;

async function withServer(
  handler: TestHandler,
  fn: (baseUrl: string) => Promise<void>,
): Promise<void> {
  const server = createServer(handler);
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", () => resolve()));
  const address = server.address();
  if (!address || typeof address === "string") {
    server.close();
    throw new Error("server address unavailable");
  }

  try {
    await fn(`http://127.0.0.1:${address.port}`);
  } finally {
    await new Promise<void>((resolve, reject) =>
      server.close((error) => (error ? reject(error) : resolve())),
    );
  }
}

async function readJson(req: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) {
    chunks.push(Buffer.from(chunk));
  }
  return JSON.parse(Buffer.concat(chunks).toString("utf8"));
}

test("create sends auth, ttl, run id, and job name", async () => {
  await withServer((req, res) => {
    assert.equal(req.method, "POST");
    assert.equal(req.url, "/v1/copies");
    assert.equal(req.headers.authorization, "Bearer secret-token");

    void (async () => {
      const body = await readJson(req);
      assert.deepEqual(body, {
        ttl_seconds: 600,
        run_id: "ci-42",
        job_name: "integration-tests",
      });

      res.writeHead(201, { "Content-Type": "application/json" });
      res.end(JSON.stringify({
        id: "copy-1",
        connection_string: "postgres://ditto:ditto@127.0.0.1:5433/ditto",
        status: "ready",
      }));
    })();
  }, async (baseUrl) => {
    const client = new DittoClient({
      serverUrl: baseUrl,
      token: "secret-token",
      ttlSeconds: 300,
    });

    const copy = await client.create({
      ttlSeconds: 600,
      runId: "ci-42",
      jobName: "integration-tests",
    });

    assert.equal(copy.id, "copy-1");
    assert.equal(copy.connection_string, "postgres://ditto:ditto@127.0.0.1:5433/ditto");
    assert.equal(copy.status, "ready");
  });
});

test("withCopy destroys the copy even when the callback throws", async () => {
  const requests: string[] = [];

  await withServer((req, res) => {
    requests.push(`${req.method} ${req.url}`);

    if (req.method === "POST" && req.url === "/v1/copies") {
      res.writeHead(201, { "Content-Type": "application/json" });
      res.end(JSON.stringify({
        id: "copy-2",
        connection_string: "postgres://ditto:ditto@127.0.0.1:5434/ditto",
      }));
      return;
    }

    if (req.method === "DELETE" && req.url === "/v1/copies/copy-2") {
      res.writeHead(204);
      res.end();
      return;
    }

    res.writeHead(500);
    res.end();
  }, async (baseUrl) => {
    const client = new DittoClient({ serverUrl: baseUrl });

    await assert.rejects(
      client.withCopy(async (dsn, copy) => {
        assert.equal(dsn, "postgres://ditto:ditto@127.0.0.1:5434/ditto");
        assert.equal(copy.id, "copy-2");
        throw new Error("boom");
      }),
      /boom/,
    );
  });

  assert.deepEqual(requests, [
    "POST /v1/copies",
    "DELETE /v1/copies/copy-2",
  ]);
});

test("status returns server health information", async () => {
  await withServer((req, res) => {
    assert.equal(req.method, "GET");
    assert.equal(req.url, "/v1/status");
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({
      version: "dev",
      active_copies: 2,
      warm_copies: 1,
      port_pool_free: 12,
    }));
  }, async (baseUrl) => {
    const client = new DittoClient({ serverUrl: baseUrl });
    const status = await client.status();

    assert.deepEqual(status, {
      version: "dev",
      active_copies: 2,
      warm_copies: 1,
      port_pool_free: 12,
    });
  });
});

test("server errors surface as DittoError with status", async () => {
  await withServer((req, res) => {
    assert.equal(req.method, "GET");
    assert.equal(req.url, "/v1/copies");
    res.writeHead(401, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ error: "unauthorized" }));
  }, async (baseUrl) => {
    const client = new DittoClient({ serverUrl: baseUrl });

    await assert.rejects(async () => {
      await client.list();
    }, (error: unknown) => {
      assert.ok(error instanceof DittoError);
      assert.equal(error.status, 401);
      assert.match(error.message, /returned HTTP 401: unauthorized/);
      return true;
    });
  });
});
