export interface Copy {
  id: string;
  status?: string;
  port?: number;
  container_id?: string;
  connection_string: string;
  run_id?: string;
  job_name?: string;
  error_message?: string;
  created_at?: string;
  ready_at?: string | null;
  destroyed_at?: string | null;
  ttl_seconds?: number;
  warm?: boolean;
  [key: string]: unknown;
}

export interface StatusResponse {
  version: string;
  active_copies: number;
  warm_copies: number;
  port_pool_free: number;
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
  signal?: AbortSignal;
}

export interface RequestOptions {
  signal?: AbortSignal;
}
