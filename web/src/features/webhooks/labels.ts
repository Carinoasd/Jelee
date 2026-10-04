// Catalog keys of the webhook enumerations, spelled out in full for
// scripts/check-i18n.mjs. Event types have no keys: the UI shows the codes
// the server lists in its catalog.
import { ApiError, networkError } from "@/api/errors";
import type { DeliveryState, WebhookOutcome } from "./api";

export const outcomeKeys: Readonly<Record<WebhookOutcome, string>> = {
  delivered: "webhooks.outcome.delivered",
  http: "webhooks.outcome.http",
  timeout: "webhooks.outcome.timeout",
  network: "webhooks.outcome.network",
  blocked: "webhooks.outcome.blocked",
  tls: "webhooks.outcome.tls",
  invalid: "webhooks.outcome.invalid",
};

export const stateKeys: Readonly<Record<DeliveryState, string>> = {
  pending: "webhooks.deliveries.states.pending",
  delivered: "webhooks.deliveries.states.delivered",
  dead: "webhooks.deliveries.states.dead",
};

/** Grace periods offered when rotating a secret, in seconds. */
export const graceChoices = [
  { seconds: 0, key: "webhooks.rotate.graceNone" },
  { seconds: 3600, key: "webhooks.rotate.graceHour" },
  { seconds: 86400, key: "webhooks.rotate.graceDay" },
  { seconds: 604800, key: "webhooks.rotate.graceWeek" },
] as const;

export function asApiError(error: unknown): ApiError {
  return error instanceof ApiError ? error : networkError(error);
}
