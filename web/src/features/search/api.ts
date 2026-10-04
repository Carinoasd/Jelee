import type { ApiClient } from "@/api/client";
import { call } from "@/api/call";
import type { CatalogItem, ItemKind } from "@/features/items/api";

/** Results per page of the search view. */
export const searchPageSize = 40;
/** Longest query GET /api/v1/items accepts (q, maxLength 128). */
export const searchMaxLength = 128;
/** Item kinds the search view can narrow to. */
export const searchKinds = ["Movie", "Series", "Episode", "HomeVideo"] as const satisfies readonly ItemKind[];
export type SearchKind = (typeof searchKinds)[number];

export interface SearchPage {
  readonly items: readonly CatalogItem[];
  readonly total: number;
}

export function parseSearchKind(value: unknown): SearchKind | null {
  return typeof value === "string" && (searchKinds as readonly string[]).includes(value) ? (value as SearchKind) : null;
}

/** Trims the query and caps it at the server's limit; "" means no search. */
export function normalizeQuery(value: unknown): string {
  return typeof value === "string" ? Array.from(value.trim()).slice(0, searchMaxLength).join("") : "";
}

/**
 * Searches every item the caller may see by title. The server matches,
 * filters by access and counts (offset form of GET /api/v1/items), so hidden
 * items never reach the page (G48.3).
 */
export async function searchItems(client: ApiClient, q: string, offset = 0, kind: SearchKind | null = null): Promise<SearchPage> {
  const body = await call(
    client.GET("/api/v1/items", {
      params: { query: { q, offset, limit: searchPageSize, sort: "name", order: "asc", ...(kind ? { type: [kind] } : {}) } },
    }),
  );
  return { items: body.data, total: body.pagination.total ?? body.data.length };
}
