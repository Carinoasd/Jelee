// Server-wide content access policy and the parental rating table (G48).
import type { ApiClient } from "@/api/client";
import { call, callNoContent } from "@/api/call";
import type { components } from "@/api/schema";

export type AccessPolicy = components["schemas"]["AccessPolicy"];
export type ParentalRating = components["schemas"]["ParentalRating"];

export async function getAccessPolicy(client: ApiClient): Promise<AccessPolicy> {
  const body = await call(client.GET("/api/v1/access/policy"));
  return body.data;
}

export async function putAccessPolicy(client: ApiClient, policy: AccessPolicy): Promise<AccessPolicy> {
  const body = await call(client.PUT("/api/v1/access/policy", { body: policy }));
  return body.data;
}

export async function listParentalRatings(client: ApiClient): Promise<readonly ParentalRating[]> {
  const body = await call(client.GET("/api/v1/access/parental-ratings"));
  return body.data;
}

export interface RatingLevel {
  /** Minimum age the level stands for. */
  readonly level: number;
  readonly codes: readonly string[];
}

/** Groups rating codes by level, lowest level first, codes in server order. */
export function ratingLevels(ratings: readonly ParentalRating[]): RatingLevel[] {
  const byLevel = new Map<number, string[]>();
  for (const rating of ratings) {
    const codes = byLevel.get(rating.level) ?? [];
    codes.push(rating.code);
    byLevel.set(rating.level, codes);
  }
  return [...byLevel.entries()].sort(([a], [b]) => a - b).map(([level, codes]) => ({ level, codes }));
}

// Network rules (G48.6): while a library has enabled rules, a request sees
// it only if it matches at least one of them, every condition of the rule
// holding. Administrators are subject only to rules that include them.
export type NetworkRule = components["schemas"]["NetworkRule"];
export type NetworkRuleInput = components["schemas"]["NetworkRuleInput"];
export type NetworkKind = NetworkRule["network"];
export type SessionKind = NetworkRule["clientKinds"][number];

/** Limits of one rule's address list, as the server enforces them. */
export const maxRuleCidrs = 64;
export const maxCidrLength = 64;

/** Splits the address field: one address or prefix per line, blanks and repeats dropped. */
export function parseCidrs(text: string): string[] {
  const seen = new Set<string>();
  for (const line of text.split(/\r?\n/)) {
    const value = line.trim();
    if (value !== "") {
      seen.add(value);
    }
  }
  return [...seen];
}

export async function listNetworkRules(client: ApiClient): Promise<readonly NetworkRule[]> {
  const body = await call(client.GET("/api/v1/access/network-rules", {}));
  return body.data;
}

export async function createNetworkRule(client: ApiClient, input: NetworkRuleInput): Promise<NetworkRule> {
  const body = await call(client.POST("/api/v1/access/network-rules", { body: input }));
  return body.data;
}

export async function updateNetworkRule(client: ApiClient, id: string, input: NetworkRuleInput): Promise<NetworkRule> {
  const body = await call(client.PUT("/api/v1/access/network-rules/{id}", { params: { path: { id } }, body: input }));
  return body.data;
}

export async function deleteNetworkRule(client: ApiClient, id: string): Promise<void> {
  await callNoContent(client.DELETE("/api/v1/access/network-rules/{id}", { params: { path: { id } } }));
}
