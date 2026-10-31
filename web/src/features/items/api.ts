import type { ApiClient } from "@/api/client";
import { call } from "@/api/call";
import { cachedRequest } from "@/api/requestCache";
import type { components } from "@/api/schema";

export type CatalogItem = components["schemas"]["CatalogItem"];
export type ItemKind = CatalogItem["kind"];
export type ItemDetails = components["schemas"]["CatalogItemDetails"];
export type MediaSourceInfo = components["schemas"]["MediaSourceInfo"];
export type ClientCapabilities = components["schemas"]["ClientCapabilities"];
export type PlaybackDecision = components["schemas"]["PlaybackDecision"];

/** Largest page GET /api/v1/items accepts. */
export const itemsPageSize = 100;
/** Items a library view shows per page. */
export const libraryItemsPageSize = 60;

/** Orders a library view offers; each maps to a server sort key and direction. */
export const librarySorts = {
  name: { sort: "name", order: "asc" },
  newest: { sort: "premiereDate", order: "desc" },
  year: { sort: "productionYear", order: "desc" },
} as const;
export type LibrarySort = keyof typeof librarySorts;

export interface LibraryItemsPage {
  readonly items: readonly CatalogItem[];
  /** Every top-level item of the library the caller may see. */
  readonly total: number;
}

/**
 * Lists one page of the top-level items of one library. The server filters,
 * sorts and counts (parentId selects the library's top level), so a page is
 * exactly one request whatever the size of the catalog. Identical requests
 * share one answer for a few seconds (api/requestCache.ts); force skips it.
 */
export function listLibraryItems(
  client: ApiClient,
  libraryId: string,
  offset = 0,
  order: LibrarySort = "name",
  force = false,
): Promise<LibraryItemsPage> {
  const { sort, order: direction } = librarySorts[order];
  const query = { parentId: libraryId, sort, order: direction, offset, limit: libraryItemsPageSize };
  return cachedRequest(
    client,
    "items:" + JSON.stringify(query),
    async () => {
      const body = await call(client.GET("/api/v1/items", { params: { query } }));
      return { items: body.data, total: body.pagination.total ?? body.data.length };
    },
    { force },
  );
}

/** Display metadata every user who may see the item can read. */
export async function getItemDetails(client: ApiClient, id: string): Promise<ItemDetails> {
  const body = await call(client.GET("/api/v1/items/{id}/details", { params: { path: { id } } }));
  return body.data;
}

/** File information: containers, tracks and sizes; never a delivery route. */
export async function getItemSources(client: ApiClient, id: string): Promise<readonly MediaSourceInfo[]> {
  const body = await call(client.GET("/api/v1/items/{id}/sources", { params: { path: { id } } }));
  return body.data.sources;
}

export interface SourcesCheck {
  readonly directPlayable: boolean;
  readonly decisions: readonly PlaybackDecision[];
  readonly sources: readonly MediaSourceInfo[];
}

/**
 * Asks why each version would or would not play as it is for a declared
 * client (G10.5). Read only: the answer holds verdicts and reason codes, never
 * a delivery route, and the server starts nothing.
 */
export async function checkItemSources(client: ApiClient, id: string, capabilities: ClientCapabilities): Promise<SourcesCheck> {
  const body = await call(client.POST("/api/v1/items/{id}/sources/check", { params: { path: { id } }, body: capabilities }));
  return body.data;
}

/**
 * Same-origin artwork URL (G40). The browser sends the HttpOnly session
 * cookie with the image request; the server re-checks access to the item.
 */
export function posterUrl(id: string, width: number): string {
  return `/images/Primary/${encodeURIComponent(id)}?width=${width}`;
}

/** Poster widths offered to the browser's srcset choice. */
export const posterWidths = [120, 240, 360, 480, 720] as const;

/** srcset of the poster widths; the server fits the image within each width. */
export function posterSrcset(id: string): string {
  return posterWidths.map((width) => `${posterUrl(id, width)} ${width}w`).join(", ");
}
