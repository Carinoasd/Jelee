import type { components } from "./schema";

export type ErrorCode = components["schemas"]["ErrorCode"];
type ErrorEnvelope = components["schemas"]["Error"];

/** Client-side failures that never reach the server's error envelope. */
export type ClientErrorCode = "network_error" | "unexpected_response";

/** Normalized error for every API call; callers never inspect raw responses. */
export class ApiError extends Error {
  readonly code: ErrorCode | ClientErrorCode;
  readonly status: number;
  readonly traceId: string;

  constructor(code: ErrorCode | ClientErrorCode, status: number, message: string, traceId = "") {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
    this.traceId = traceId;
  }
}

function isEnvelope(value: unknown): value is ErrorEnvelope {
  if (typeof value !== "object" || value === null || !("error" in value)) {
    return false;
  }
  const inner: unknown = value.error;
  return (
    typeof inner === "object" &&
    inner !== null &&
    "code" in inner &&
    typeof inner.code === "string" &&
    "message" in inner &&
    typeof inner.message === "string"
  );
}

export function toApiError(body: unknown, response: Response | undefined): ApiError {
  const status = response?.status ?? 0;
  if (isEnvelope(body)) {
    return new ApiError(body.error.code, status, body.error.message, body.error.traceId);
  }
  return new ApiError("unexpected_response", status, "unexpected response");
}

export function networkError(cause: unknown): ApiError {
  const error = new ApiError("network_error", 0, "network error");
  error.cause = cause;
  return error;
}
