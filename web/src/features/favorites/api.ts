import type { ApiClient } from "@/api/client";
import { call } from "@/api/call";
import type { components } from "@/api/schema";

// Favourites (G24.2). The server lists and reports only the items the
// caller may see (G48.3); a hidden item is answered like a missing one.

export type FavoriteItem = components["schemas"]["FavoriteItem"];
export type UserItemData = components["schemas"]["UserItemData"];

/** Favourites shown per page; the server accepts at most 500. */
export const favoritesPageSize = 60;

export interface FavoritesPage {
  readonly items: readonly FavoriteItem[];
  readonly total: number;
  /** Continues the list; empty after the last page. */
  readonly nextCursor: string;
}

/** Marks (favorite true) or unmarks an item; answers the caller's data of it. */
export async function setFavorite(client: ApiClient, id: string, favorite: boolean): Promise<UserItemData> {
  const params = { params: { path: { id } } };
  const body = favorite ? await call(client.PUT("/api/v1/items/{id}/favorite", { ...params, body: {} })) : await call(client.DELETE("/api/v1/items/{id}/favorite", params));
  return body.data;
}

/** One page of the caller's favourites, most recently marked first. */
export async function listFavorites(client: ApiClient, cursor = ""): Promise<FavoritesPage> {
  const query = cursor === "" ? { limit: favoritesPageSize } : { limit: favoritesPageSize, cursor };
  const body = await call(client.GET("/api/v1/users/me/favorites", { params: { query } }));
  return { items: body.data.items, total: body.data.total, nextCursor: body.data.nextCursor };
}
