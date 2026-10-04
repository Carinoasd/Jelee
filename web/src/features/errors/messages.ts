import type { ApiError } from "@/api/errors";

/**
 * Maps a normalized API error to a catalog key by its stable code; the UI
 * never shows raw server text.
 */
export function errorMessageKey(error: ApiError): string {
  switch (error.code) {
    case "network_error":
      return "errors.network";
    case "authentication_required":
      return "errors.sessionExpired";
    case "forbidden":
      return "errors.forbidden";
    case "csrf_failed":
      return "errors.csrf";
    case "client_blocked":
      return "errors.clientBlocked";
    case "client_pending_approval":
      return "errors.clientPending";
    case "client_read_only":
      return "errors.clientReadOnly";
    case "client_rate_limited":
      return "errors.clientRateLimited";
    case "auth_rate_limited":
      return "auth.rateLimited";
    case "session_limit":
      return "auth.sessionLimit";
    case "not_found":
      return "errors.notFound";
    case "invalid_request":
      return "errors.invalidRequest";
    case "request_timeout":
    case "lookup_timeout":
      return "errors.timeout";
    case "not_ready":
      return "errors.notReady";
    case "setup_required":
      return "errors.setupRequired";
    case "account_busy":
    case "image_busy":
    case "jobs_busy":
      return "errors.busy";
    case "internal_error":
      return "errors.server";
    default:
      return "errors.generic";
  }
}
