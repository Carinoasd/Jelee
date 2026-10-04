import type { ApiClient } from "@/api/client";
import { call, callNoContent } from "@/api/call";
import type { components } from "@/api/schema";

export type ClientPolicy = components["schemas"]["ClientPolicy"];
export type ClientPolicyInput = components["schemas"]["ClientPolicyInput"];
export type UnknownClientsPolicy = ClientPolicy["unknownClients"];
export type ClientRule = components["schemas"]["ClientRule"];
export type ClientRuleInput = components["schemas"]["ClientRuleInput"];
export type RuleDimension = ClientRule["dimension"];
export type RuleMatch = ClientRule["match"];
export type RuleAction = ClientRule["action"];
export type RuleIntent = NonNullable<ClientRule["intent"]>;
export type KnownClient = components["schemas"]["KnownClient"];
export type KnownClientPage = components["schemas"]["KnownClientPage"];
export type KnownClientUpdate = components["schemas"]["KnownClientUpdate"];
export type ClientHit = components["schemas"]["ClientHit"];
export type ClientHitPage = components["schemas"]["ClientHitPage"];
export type HitMode = ClientHit["mode"];
export type ClientHitStats = components["schemas"]["ClientHitStats"];

/** Page size of the known client and hit lists. */
export const clientPageSize = 50;
/** Rows each "top" list of the statistics shows. */
export const statsTop = 10;
/** Periods the statistics offer, in hours. */
export const statsPeriods = [1, 24, 168, 720] as const;
export type StatsPeriod = (typeof statsPeriods)[number];

/** Same-origin download of the masked hit log; the session cookie authorizes it. */
export const hitsExportPath = "/api/v1/client-control/hits/export";

// The endpoints below require an empty JSON object as their body (the server
// decodes strictly and refuses a missing Content-Type), but the OpenAPI
// document declares no request body for them, so the generated types only
// allow an absent body. The cast is confined to this one constant.
const emptyBody = {} as never;

/** Observe and shadow rules record hits but do not enforce their intent. */
export function isObserving(rule: Pick<ClientRule, "action">): boolean {
  return rule.action === "observe" || rule.action === "shadow";
}

export async function getPolicy(client: ApiClient): Promise<ClientPolicy> {
  const body = await call(client.GET("/api/v1/client-control/policy", {}));
  return body.data;
}

export async function setPolicy(client: ApiClient, input: ClientPolicyInput): Promise<ClientPolicy> {
  const body = await call(client.PUT("/api/v1/client-control/policy", { body: input }));
  return body.data;
}

/** Every rule in precedence order (priority descending). */
export async function listRules(client: ApiClient): Promise<readonly ClientRule[]> {
  const body = await call(client.GET("/api/v1/client-control/rules", {}));
  return body.data;
}

export async function createRule(client: ApiClient, input: ClientRuleInput): Promise<ClientRule> {
  const body = await call(client.POST("/api/v1/client-control/rules", { body: input }));
  return body.data;
}

export async function updateRule(client: ApiClient, id: string, input: ClientRuleInput): Promise<ClientRule> {
  const body = await call(client.PUT("/api/v1/client-control/rules/{id}", { params: { path: { id } }, body: input }));
  return body.data;
}

export async function deleteRule(client: ApiClient, id: string): Promise<void> {
  await callNoContent(client.DELETE("/api/v1/client-control/rules/{id}", { params: { path: { id } } }));
}

/** Switches an observe or shadow rule to enforcing its intent. */
export async function enforceRule(client: ApiClient, id: string): Promise<ClientRule> {
  const body = await call(client.POST("/api/v1/client-control/rules/{id}/enforce", { params: { path: { id } }, body: emptyBody }));
  return body.data;
}

/** Switches an enforcing rule back to observing its action. */
export async function observeRule(client: ApiClient, id: string): Promise<ClientRule> {
  const body = await call(client.POST("/api/v1/client-control/rules/{id}/observe", { params: { path: { id } }, body: emptyBody }));
  return body.data;
}

export async function listKnownClients(client: ApiClient, cursor = ""): Promise<KnownClientPage> {
  const query = cursor === "" ? { limit: clientPageSize } : { limit: clientPageSize, cursor };
  const body = await call(client.GET("/api/v1/client-control/clients", { params: { query } }));
  return body.data;
}

export async function updateKnownClient(client: ApiClient, id: string, update: KnownClientUpdate): Promise<KnownClient> {
  const body = await call(client.PATCH("/api/v1/client-control/clients/{id}", { params: { path: { id } }, body: update }));
  return body.data;
}

/** Adds an exact deny rule for the client's device ID (or user agent). */
export async function blockKnownClient(client: ApiClient, id: string): Promise<ClientRule> {
  const body = await call(client.POST("/api/v1/client-control/clients/{id}/block", { params: { path: { id } }, body: emptyBody }));
  return body.data;
}

/** Revokes every active session of the client; returns how many. */
export async function kickKnownClient(client: ApiClient, id: string): Promise<number> {
  const body = await call(client.POST("/api/v1/client-control/clients/{id}/kick", { params: { path: { id } }, body: emptyBody }));
  return body.data.sessionsRevoked;
}

export async function getHitStats(client: ApiClient, hours: StatsPeriod): Promise<ClientHitStats> {
  const body = await call(client.GET("/api/v1/client-control/stats", { params: { query: { hours, top: statsTop } } }));
  return body.data;
}

export async function listHits(client: ApiClient, mode: HitMode | "", cursor = ""): Promise<ClientHitPage> {
  const query: { limit: number; mode?: HitMode; cursor?: string } = { limit: clientPageSize };
  if (mode !== "") {
    query.mode = mode;
  }
  if (cursor !== "") {
    query.cursor = cursor;
  }
  const body = await call(client.GET("/api/v1/client-control/hits", { params: { query } }));
  return body.data;
}

/** Link target of the hit export for the same filter as the list. */
export function hitsExportHref(mode: HitMode | ""): string {
  return mode === "" ? hitsExportPath : `${hitsExportPath}?${new URLSearchParams({ mode }).toString()}`;
}
