import { DittoError } from "./errors.js";
import type {
  ClientOptions,
  CopyEvent,
  CopySummary,
  CreateCopyOptions,
  CreateCopyResponse,
  RequestOptions,
  StatusResponse,
} from "./types.js";

interface ErrorBody {
  error?: unknown;
}

export class DittoClient {
  private readonly baseUrl: string;
  private readonly token: string;
  private readonly ttlSeconds: number;
  private readonly fetchImpl: typeof fetch;

  constructor(options: ClientOptions = {}) {
    const serverUrl = (process.env.DITTO_SERVER_URL ?? options.serverUrl ?? "").trim();
    if (!serverUrl) {
      throw new DittoError(
        "serverUrl is required - pass it explicitly or set DITTO_SERVER_URL",
      );
    }

    const token = process.env.DITTO_TOKEN ?? options.token ?? "";
    const ttlEnv = process.env.DITTO_TTL;
    const ttlSeconds = ttlEnv ? Number.parseInt(ttlEnv, 10) : (options.ttlSeconds ?? 0);
    if (Number.isNaN(ttlSeconds) || ttlSeconds < 0) {
      throw new DittoError("DITTO_TTL must be a non-negative integer");
    }

    if (typeof options.fetch === "function") {
      this.fetchImpl = options.fetch;
    } else if (typeof globalThis.fetch === "function") {
      this.fetchImpl = globalThis.fetch.bind(globalThis);
    } else {
      throw new DittoError("fetch is not available - supply a fetch implementation");
    }

    this.baseUrl = serverUrl.replace(/\/+$/, "");
    this.token = token;
    this.ttlSeconds = ttlSeconds;
  }

  async create(options: CreateCopyOptions = {}): Promise<CreateCopyResponse> {
    const ttlSeconds = options.ttlSeconds ?? this.ttlSeconds;
    const body: Record<string, unknown> = {};
    if (ttlSeconds > 0) {
      body.ttl_seconds = ttlSeconds;
    }
    if (options.runId) {
      body.run_id = options.runId;
    }
    if (options.jobName) {
      body.job_name = options.jobName;
    }
    if (options.dumpUri) {
      body.dump_uri = options.dumpUri;
    }
    if (options.obfuscate) {
      body.obfuscate = true;
    }

    return this.request<CreateCopyResponse>("/v2/copies", {
      method: "POST",
      body: JSON.stringify(body),
      ...(options.signal ? { signal: options.signal } : {}),
    }, [201]);
  }

  async destroy(copyId: string, options: RequestOptions = {}): Promise<void> {
    if (!copyId) {
      throw new DittoError("copyId is required");
    }

    await this.request("/v2/copies/" + encodeURIComponent(copyId), {
      method: "DELETE",
      ...(options.signal ? { signal: options.signal } : {}),
    }, [204]);
  }

  async list(options: RequestOptions = {}): Promise<CopySummary[]> {
    return this.request<CopySummary[]>("/v2/copies", {
      method: "GET",
      ...(options.signal ? { signal: options.signal } : {}),
    }, [200]);
  }

  async events(copyId: string, options: RequestOptions = {}): Promise<CopyEvent[]> {
    if (!copyId) {
      throw new DittoError("copyId is required");
    }

    return this.request<CopyEvent[]>("/v2/copies/" + encodeURIComponent(copyId) + "/events", {
      method: "GET",
      ...(options.signal ? { signal: options.signal } : {}),
    }, [200]);
  }

  async status(options: RequestOptions = {}): Promise<StatusResponse> {
    return this.request<StatusResponse>("/v2/status", {
      method: "GET",
      ...(options.signal ? { signal: options.signal } : {}),
    }, [200]);
  }

  async withCopy<T>(
    fn: (dsn: string, copy: CreateCopyResponse) => Promise<T> | T,
    options: CreateCopyOptions = {},
  ): Promise<T> {
    const copy = await this.create(options);
    try {
      return await fn(copy.connection_string, copy);
    } finally {
      try {
        await this.destroy(copy.id);
      } catch {
        // Best effort only. The server will still enforce TTL cleanup.
      }
    }
  }

  private async request<T>(
    path: string,
    init: RequestInit,
    expectedStatuses: number[],
  ): Promise<T> {
    const headers = new Headers(init.headers);
    headers.set("Content-Type", "application/json");
    if (this.token) {
      headers.set("Authorization", "Bearer " + this.token);
    }

    let response: Response;
    try {
      response = await this.fetchImpl(this.baseUrl + path, {
        ...init,
        headers,
      });
    } catch (error) {
      throw new DittoError(
        "ditto: " + (init.method ?? "GET") + " " + path + " failed: " + this.describeNetworkError(error),
        { details: error },
      );
    }

    if (!expectedStatuses.includes(response.status)) {
      const details = await this.parseBody(response);
      const suffix = this.describeErrorBody(details);
      throw new DittoError(
        "ditto: " + (init.method ?? "GET") + " " + path + " returned HTTP " + response.status + suffix,
        { status: response.status, details },
      );
    }

    if (response.status === 204) {
      return undefined as T;
    }

    const text = await response.text();
    if (!text) {
      return undefined as T;
    }

    return JSON.parse(text) as T;
  }

  private async parseBody(response: Response): Promise<unknown> {
    const text = await response.text();
    if (!text) {
      return undefined;
    }

    try {
      return JSON.parse(text) as unknown;
    } catch {
      return text;
    }
  }

  private describeErrorBody(details: unknown): string {
    if (typeof details === "string" && details) {
      return ": " + details;
    }

    if (details && typeof details === "object") {
      const error = (details as ErrorBody).error;
      if (typeof error === "string" && error) {
        return ": " + error;
      }
    }

    return "";
  }

  private describeNetworkError(error: unknown): string {
    if (error instanceof Error && error.message) {
      return error.message;
    }
    return String(error);
  }
}
