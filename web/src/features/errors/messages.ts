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
    case "invalid_password":
      return "settings.password.wrongCurrent";
    // The twoFactor catalog loads with the screens that can meet these codes.
    case "invalid_two_factor_code":
      return "twoFactor.errors.wrongCode";
    case "login_challenge_invalid":
      return "twoFactor.errors.challengeInvalid";
    case "two_factor_unavailable":
      return "twoFactor.errors.unavailable";
    case "app_password_required":
      return "twoFactor.errors.appPasswordRequired";
    case "custom_css_rejected":
      return "appearance.serverRejected";
    case "auth_rate_limited":
      return "auth.rateLimited";
    case "session_limit":
      return "auth.sessionLimit";
    case "not_found":
      return "errors.notFound";
    case "invalid_request":
    case "body_too_large":
      return "errors.invalidRequest";
    case "conflict":
    case "precondition_failed":
      return "errors.conflict";
    case "last_admin":
      return "errors.lastAdmin";
    case "stats_export_limit":
      return "errors.exportLimit";
    case "webhook_target_denied":
      return "errors.webhookTargetDenied";
    case "request_timeout":
    case "lookup_timeout":
      return "errors.timeout";
    case "not_ready":
      return "errors.notReady";
    case "setup_required":
      return "errors.setupRequired";
    case "confirmation_required":
      return "errors.confirmationRequired";
    case "devmode_inactive":
      return "errors.devModeInactive";
    case "devmode_toggle_unavailable":
      return "errors.devModeToggleUnavailable";
    case "account_busy":
    case "image_busy":
    case "jobs_busy":
      return "errors.busy";
    case "version_identity_conflict":
      return "versions.errors.identity";
    case "version_merge_incompatible":
      return "versions.errors.incompatible";
    case "version_undo_unavailable":
      return "versions.errors.undo";
    case "version_item_busy":
      return "versions.errors.busy";
    case "internal_error":
      return "errors.server";
    default:
      return "errors.generic";
  }
}
