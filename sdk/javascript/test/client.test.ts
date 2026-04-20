import assert from "node:assert/strict";
import test from "node:test";

import { DittoClient, DittoError } from "../src/index.js";

interface CapturedRequest {
  method: string;
  url: string;
  headers: Headers;
  body: unknown;
}

type FetchHandler = (request: CapturedRequest) => Response | Promise<Response>;

function createFetch(handler: FetchHandler): typeof fetch {
  return async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const request = new Request(input, init);
    const bodyText = request.method === "GET" || request.method === "DELETE"
      ? ""
      : await request.text();

    return handler({
      method: request.method,
      url: new URL(request.url).pathname,
      headers: request.headers,
      body: bodyText ? JSON.parse(bodyText) : undefined,
    });
  };
}

test("create sends auth, ttl, run id, and job name", async () => {
  const client = new DittoClient({
    serverUrl: "https://ditto.example.com",
    token: "secret-token",
    ttlSeconds: 300,
    fetch: createFetch((req) => {
      assert.equal(req.method, "POST");
      assert.equal(req.url, "/v2/copies");
      assert.equal(req.headers.get("authorization"), "Bearer secret-token");
      assert.deepEqual(req.body, {
        ttl_seconds: 600,
        run_id: "ci-42",
        job_name: "integration-tests",
      });

      return Response.json({
        id: "copy-1",
        status: "ready",
        connection_string: "postgres://ditto:ditto@127.0.0.1:5433/ditto",
        created_at: "2026-04-20T00:00:00Z",
        ttl_seconds: 600,
        warm: false,
      }, { status: 201 });
    }),
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

test("withCopy destroys the copy even when the callback throws", async () => {
  const requests: string[] = [];
  const client = new DittoClient({
    serverUrl: "https://ditto.example.com",
    fetch: createFetch((req) => {
      requests.push(`${req.method} ${req.url}`);

      if (req.method === "POST" && req.url === "/v2/copies") {
        return Response.json({
          id: "copy-2",
          status: "ready",
          connection_string: "postgres://ditto:ditto@127.0.0.1:5434/ditto",
          created_at: "2026-04-20T00:00:00Z",
          ttl_seconds: 600,
          warm: false,
        }, { status: 201 });
      }

      if (req.method === "DELETE" && req.url === "/v2/copies/copy-2") {
        return new Response(null, { status: 204 });
      }

      return new Response(null, { status: 500 });
    }),
  });

  await assert.rejects(
    client.withCopy(async (dsn, copy) => {
      assert.equal(dsn, "postgres://ditto:ditto@127.0.0.1:5434/ditto");
      assert.equal(copy.id, "copy-2");
      throw new Error("boom");
    }),
    /boom/,
  );

  assert.deepEqual(requests, [
    "POST /v2/copies",
    "DELETE /v2/copies/copy-2",
  ]);
});

test("status returns server health information", async () => {
  const client = new DittoClient({
    serverUrl: "https://ditto.example.com",
    fetch: createFetch((req) => {
      assert.equal(req.method, "GET");
      assert.equal(req.url, "/v2/status");

      return Response.json({
        version: "dev",
        active_copies: 2,
        warm_copies: 1,
        port_pool_free: 12,
      });
    }),
  });

  const status = await client.status();
  assert.deepEqual(status, {
    version: "dev",
    active_copies: 2,
    warm_copies: 1,
    port_pool_free: 12,
  });
});

test("server errors surface as DittoError with status", async () => {
  const client = new DittoClient({
    serverUrl: "https://ditto.example.com",
    fetch: createFetch((req) => {
      assert.equal(req.method, "GET");
      assert.equal(req.url, "/v2/copies");
      return Response.json({ error: "unauthorized" }, { status: 401 });
    }),
  });

  await assert.rejects(async () => {
    await client.list();
  }, (error: unknown) => {
    assert.ok(error instanceof DittoError);
    assert.equal(error.status, 401);
    assert.match(error.message, /returned HTTP 401: unauthorized/);
    return true;
  });
});

test("events returns copy lifecycle events", async () => {
  const client = new DittoClient({
    serverUrl: "https://ditto.example.com",
    fetch: createFetch((req) => {
      assert.equal(req.method, "GET");
      assert.equal(req.url, "/v2/copies/copy-9/events");
      return Response.json([
        {
          action: "ready",
          actor: "ditto-host",
          created_at: "2026-04-20T00:00:00Z",
          metadata: { warm: false },
        },
      ]);
    }),
  });

  const events = await client.events("copy-9");
  assert.deepEqual(events, [
    {
      action: "ready",
      actor: "ditto-host",
      created_at: "2026-04-20T00:00:00Z",
      metadata: { warm: false },
    },
  ]);
});
