import type { ApiClient } from "@/api/client";
import { call } from "@/api/call";
import { cachedRequest } from "@/api/requestCache";
import type { CatalogItem, ItemKind } from "@/features/items/api";

/** Results per page when the search is narrowed to one kind. */
export const searchPageSize = 40;
/** Results per kind on the overview of every kind. */
export const searchGroupSize = 12;
/** Longest query GET /api/v1/items accepts (q, maxLength 128). */
export const searchMaxLength = 128;
/** Item kinds the search view groups and narrows by, in display order. */
export const searchKinds = ["Movie", "Series", "Episode", "HomeVideo"] as const satisfies readonly ItemKind[];
export type SearchKind = (typeof searchKinds)[number];

/** What the search page shows; it lives in the URL (q, type, library). */
export interface SearchFilters {
  readonly q: string;
  readonly kind: SearchKind | null;
  readonly libraryId: string | null;
}

export interface SearchPage {
  readonly items: readonly CatalogItem[];
  readonly total: number;
}

export interface SearchGroup extends SearchPage {
  readonly kind: SearchKind;
}

export function parseSearchKind(value: unknown): SearchKind | null {
  return typeof value === "string" && (searchKinds as readonly string[]).includes(value) ? (value as SearchKind) : null;
}

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Reads the ?library= query value; anything but a UUID means every library. */
export function parseSearchLibrary(value: unknown): string | null {
  return typeof value === "string" && uuidPattern.test(value) ? value.toLowerCase() : null;
}

/** Trims the query and caps it at the server's limit; "" means no search. */
export function normalizeQuery(value: unknown): string {
  return typeof value === "string" ? Array.from(value.trim()).slice(0, searchMaxLength).join("") : "";
}

/** Same filters, same results: used to drop answers for a replaced search. */
export function sameFilters(a: SearchFilters, b: SearchFilters): boolean {
  return a.q === b.q && a.kind === b.kind && a.libraryId === b.libraryId;
}

/**
 * Searches every item the caller may see by title, optionally in one kind and
 * one library. The server matches, filters by access and counts (offset form
 * of GET /api/v1/items), so hidden items never reach the page (G48.3).
 * Identical requests share one answer for a few seconds (api/requestCache.ts).
 */
export function searchItems(
  client: ApiClient,
  filters: SearchFilters,
  offset = 0,
  limit = searchPageSize,
  force = false,
): Promise<SearchPage> {
  const query = {
    q: filters.q,
    offset,
    limit,
    sort: "name",
    order: "asc",
    ...(filters.kind ? { type: [filters.kind] } : {}),
    ...(filters.libraryId ? { libraryId: filters.libraryId } : {}),
  } as const;
  return cachedRequest(
    client,
    "search:" + JSON.stringify(query),
    async () => {
      const body = await call(client.GET("/api/v1/items", { params: { query } }));
      return { items: body.data, total: body.pagination.total ?? body.data.length };
    },
    { force },
  );
}

/** The first results of every kind, for the overview; kinds without results are left out. */
export async function searchGroups(client: ApiClient, filters: SearchFilters, force = false): Promise<readonly SearchGroup[]> {
  const groups = await Promise.all(
    searchKinds.map(async (kind) => ({ kind, ...(await searchItems(client, { ...filters, kind }, 0, searchGroupSize, force)) })),
  );
  return groups.filter((group) => group.total > 0 && group.items.length > 0);
}
