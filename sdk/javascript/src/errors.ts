export class DittoError extends Error {
  readonly status: number | undefined;
  readonly details: unknown;

  constructor(message: string, options: { status?: number; details?: unknown } = {}) {
    super(message);
    this.name = "DittoError";
    this.status = options.status;
    this.details = options.details;
  }
}
