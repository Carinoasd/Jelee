// Server-wide content access policy and the parental rating table (G48).
import type { ApiClient } from "@/api/client";
import { call } from "@/api/call";
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
