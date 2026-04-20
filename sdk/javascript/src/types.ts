export interface CreateCopyResponse {
  id: string;
  status: string;
  port?: number;
  connection_string: string;
  run_id?: string;
  job_name?: string;
  error_message?: string;
  created_at: string;
  ready_at?: string | null;
  ttl_seconds: number;
  warm: boolean;
  [key: string]: unknown;
}

export interface CopySummary {
  id: string;
  status: string;
  port?: number;
  run_id?: string;
  job_name?: string;
  error_message?: string;
  created_at: string;
  ready_at?: string | null;
  destroyed_at?: string | null;
  ttl_seconds: number;
  warm: boolean;
  [key: string]: unknown;
}

export interface CopyEvent {
  action: string;
  actor: string;
  created_at: string;
  metadata?: Record<string, unknown>;
  [key: string]: unknown;
}

export interface StatusResponse {
  version: string;
  active_copies: number;
  warm_copies: number;
  port_pool_free: number;
  advertise_host?: string;
  [key: string]: unknown;
}

export interface ClientOptions {
  serverUrl?: string;
  token?: string;
  ttlSeconds?: number;
  fetch?: typeof fetch;
}

export interface CreateCopyOptions {
  ttlSeconds?: number;
  runId?: string;
  jobName?: string;
  dumpUri?: string;
  obfuscate?: boolean;
  signal?: AbortSignal;
}

export interface RequestOptions {
  signal?: AbortSignal;
}
