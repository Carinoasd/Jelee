import type { ApiClient } from "@/api/client";
import { call, callNoContent } from "@/api/call";
import type { components, paths } from "@/api/schema";

export type Webhook = components["schemas"]["Webhook"];
export type WebhookInput = components["schemas"]["WebhookInput"];
export type WebhookWithSecret = components["schemas"]["WebhookWithSecret"];
export type WebhookTestResult = components["schemas"]["WebhookTestResult"];
export type WebhookDelivery = components["schemas"]["WebhookDelivery"];
export type WebhookDeliveryDetail = components["schemas"]["WebhookDeliveryDetail"];
export type WebhookAttempt = components["schemas"]["WebhookAttempt"];
export type WebhookOutcome = WebhookAttempt["outcome"];
export type DeliveryState = WebhookDelivery["state"];
/** Event type codes; the list itself always comes from the server's catalog. */
export type WebhookEvent = Webhook["events"][number];

type ListBody = paths["/api/v1/webhooks"]["get"]["responses"][200]["content"]["application/json"]["data"];
type DeliveryPageBody = paths["/api/v1/webhooks/{id}/deliveries"]["get"]["responses"][200]["content"]["application/json"]["data"];

export interface WebhookList {
  readonly webhooks: readonly Webhook[];
  /** Event types the server can deliver, in the server's order. */
  readonly events: readonly WebhookEvent[];
}

export type DeliveryPage = DeliveryPageBody;

/** Page size of the delivery log. */
export const deliveryPageSize = 50;
/** Default grace period of a secret rotation (one day), as the server's. */
export const defaultGraceSeconds = 86400;

/** The settings a replace must send to keep everything but what changes. */
export function toInput(webhook: Webhook): WebhookInput {
  return {
    name: webhook.name,
    url: webhook.url,
    enabled: webhook.enabled,
    events: [...webhook.events],
    timeoutSeconds: webhook.timeoutSeconds,
    retry: webhook.retry,
  };
}

export async function listWebhooks(client: ApiClient): Promise<WebhookList> {
  const body = await call(client.GET("/api/v1/webhooks", {}));
  const data: ListBody = body.data;
  return { webhooks: data.webhooks, events: data.events };
}

/** Creates an endpoint; the returned secret is shown to the user only once. */
export async function createWebhook(client: ApiClient, input: WebhookInput): Promise<WebhookWithSecret> {
  const body = await call(client.POST("/api/v1/webhooks", { body: input }));
  return body.data;
}

export async function getWebhook(client: ApiClient, id: string): Promise<Webhook> {
  const body = await call(client.GET("/api/v1/webhooks/{id}", { params: { path: { id } } }));
  return body.data;
}

/** Replaces the settings; omitting headers keeps the stored ones. */
export async function updateWebhook(client: ApiClient, id: string, input: WebhookInput): Promise<Webhook> {
  const body = await call(client.PUT("/api/v1/webhooks/{id}", { params: { path: { id } }, body: input }));
  return body.data;
}

export async function deleteWebhook(client: ApiClient, id: string): Promise<void> {
  await callNoContent(client.DELETE("/api/v1/webhooks/{id}", { params: { path: { id } } }));
}

export async function rotateWebhookSecret(client: ApiClient, id: string, graceSeconds: number): Promise<WebhookWithSecret> {
  const body = await call(client.POST("/api/v1/webhooks/{id}/rotate-secret", { params: { path: { id } }, body: { graceSeconds } }));
  return body.data;
}

export async function testWebhook(client: ApiClient, id: string): Promise<WebhookTestResult> {
  const body = await call(client.POST("/api/v1/webhooks/{id}/test", { params: { path: { id } }, body: {} }));
  return body.data;
}

export async function listDeliveries(client: ApiClient, id: string, state: DeliveryState | "", cursor = ""): Promise<DeliveryPage> {
  const query: { limit: number; state?: DeliveryState; cursor?: string } = { limit: deliveryPageSize };
  if (state !== "") {
    query.state = state;
  }
  if (cursor !== "") {
    query.cursor = cursor;
  }
  const body = await call(client.GET("/api/v1/webhooks/{id}/deliveries", { params: { path: { id }, query } }));
  return body.data;
}

export async function getDelivery(client: ApiClient, id: string, deliveryId: string): Promise<WebhookDeliveryDetail> {
  const body = await call(client.GET("/api/v1/webhooks/{id}/deliveries/{deliveryId}", { params: { path: { id, deliveryId } } }));
  return body.data;
}

/** Queues a delivered or dead delivery again with the same event ID. */
export async function replayDelivery(client: ApiClient, id: string, deliveryId: string): Promise<WebhookDelivery> {
  const body = await call(
    client.POST("/api/v1/webhooks/{id}/deliveries/{deliveryId}/replay", { params: { path: { id, deliveryId } }, body: {} }),
  );
  return body.data;
}
