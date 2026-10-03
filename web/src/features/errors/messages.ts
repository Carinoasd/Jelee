import type { ApiError } from "@/api/errors";

/** Maps a normalized API error to a catalog key; never shows raw server text. */
export function errorMessageKey(error: ApiError): string {
  switch (error.code) {
    case "network_error":
      return "errors.network";
    case "authentication_required":
      return "errors.sessionExpired";
    case "forbidden":
      return "errors.forbidden";
    case "auth_rate_limited":
      return "auth.rateLimited";
    default:
      return "errors.generic";
  }
}
