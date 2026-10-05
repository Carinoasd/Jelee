import type { ApiClient } from "@/api/client";
import { call } from "@/api/call";
import type { components } from "@/api/schema";

export type CatalogItem = components["schemas"]["CatalogItem"];
export type ItemKind = CatalogItem["kind"];
export type ItemDetails = components["schemas"]["CatalogItemDetails"];
export type MediaSourceInfo = components["schemas"]["MediaSourceInfo"];

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
 * exactly one request whatever the size of the catalog.
 */
export async function listLibraryItems(client: ApiClient, libraryId: string, offset = 0, order: LibrarySort = "name"): Promise<LibraryItemsPage> {
  const { sort, order: direction } = librarySorts[order];
  const body = await call(
    client.GET("/api/v1/items", { params: { query: { parentId: libraryId, sort, order: direction, offset, limit: libraryItemsPageSize } } }),
  );
  return { items: body.data, total: body.pagination.total ?? body.data.length };
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

/**
 * Same-origin artwork URL (G40). The browser sends the HttpOnly session
 * cookie with the image request; the server re-checks access to the item.
 */
export function posterUrl(id: string, width: number): string {
  return `/images/Primary/${encodeURIComponent(id)}?width=${width}`;
}
