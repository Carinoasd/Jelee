import type { ApiClient } from "@/api/client";
import { call } from "@/api/call";
import type { components } from "@/api/schema";

export type CatalogItem = components["schemas"]["CatalogItem"];
export type ItemKind = CatalogItem["kind"];

/** Largest page GET /api/v1/items accepts. */
export const itemsPageSize = 100;
/** Items a library view tries to collect before showing a page. */
export const libraryItemsTarget = 60;
/** Upper bound on server pages read for one library page. */
export const maxPagesPerLoad = 10;

export interface LibraryItemsPage {
  readonly items: readonly CatalogItem[];
  /** Cursor into the global catalog; empty when the catalog is exhausted. */
  readonly nextCursor: string;
}

/**
 * Lists the top-level items of one library.
 *
 * GET /api/v1/items has no library filter or sort parameter yet (it accepts
 * only cursor and limit), so this walks the caller's visible catalog in
 * server order and keeps the items of the requested library that have no
 * parent. A single call reads at most maxPagesPerLoad pages; the returned
 * cursor continues the walk.
 */
export async function listLibraryItems(client: ApiClient, libraryId: string, cursor = ""): Promise<LibraryItemsPage> {
  const items: CatalogItem[] = [];
  let next = cursor;
  let pages = 0;
  do {
    const query = next === "" ? { limit: itemsPageSize } : { limit: itemsPageSize, cursor: next };
    const body = await call(client.GET("/api/v1/items", { params: { query } }));
    for (const item of body.data) {
      if (item.libraryId === libraryId && item.parentId === undefined) {
        items.push(item);
      }
    }
    next = body.pagination.nextCursor;
    pages++;
  } while (next !== "" && items.length < libraryItemsTarget && pages < maxPagesPerLoad);
  return { items, nextCursor: next };
}

export async function getItem(client: ApiClient, id: string): Promise<CatalogItem> {
  const body = await call(client.GET("/api/v1/items/{id}", { params: { path: { id } } }));
  return body.data;
}

/** Administrator-only: persisted fields, provider IDs and NFO provenance. */
export async function getItemMetadata(client: ApiClient, id: string) {
  const body = await call(client.GET("/api/v1/items/{id}/metadata", { params: { path: { id } } }));
  return body.data;
}

/**
 * The metadata response as openapi-fetch delivers it (its readable-type
 * mapping reshapes the fact union, so the raw component type does not match).
 */
export type ItemMetadata = Awaited<ReturnType<typeof getItemMetadata>>;

/**
 * Same-origin artwork URL (G40). The browser sends the HttpOnly session
 * cookie with the image request; the server re-checks access to the item.
 */
export function posterUrl(id: string, width: number): string {
  return `/images/Primary/${encodeURIComponent(id)}?width=${width}`;
}
