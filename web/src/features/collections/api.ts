import type { ApiClient } from "@/api/client";
import { call, callNoContent } from "@/api/call";
import type { components } from "@/api/schema";

// Collections and playlists (G02.1). The server lists only the items the
// caller may see (G48.3); this page never plays anything (G27).

export type Collection = components["schemas"]["Collection"];
export type CollectionView = components["schemas"]["CollectionView"];
export type CollectionMember = components["schemas"]["CollectionMember"];
export type Playlist = components["schemas"]["Playlist"];
export type PlaylistView = components["schemas"]["PlaylistView"];
export type PlaylistEntry = components["schemas"]["PlaylistEntry"];

/** Kinds a collection holds manually; the server refuses others. */
export const collectionKinds: readonly string[] = ["Movie", "Series", "HomeVideo"];
/** Playable kinds a playlist holds; the server refuses others. */
export const playlistKinds: readonly string[] = ["Movie", "Episode", "HomeVideo"];

/** Listing page size; the server accepts at most 200. */
export const pageSize = 100;

export async function listCollections(client: ApiClient, cursor = ""): Promise<{ items: readonly Collection[]; nextCursor: string }> {
  const query = cursor === "" ? { limit: pageSize } : { limit: pageSize, cursor };
  const body = await call(client.GET("/api/v1/collections", { params: { query } }));
  return { items: body.data.collections, nextCursor: body.data.nextCursor };
}

export async function getCollection(client: ApiClient, id: string): Promise<CollectionView> {
  const body = await call(client.GET("/api/v1/collections/{id}", { params: { path: { id } } }));
  return body.data;
}

export async function createCollection(client: ApiClient, name: string, nfoName: string | null): Promise<CollectionView> {
  const body = await call(client.POST("/api/v1/collections", { body: { name, overview: "", nfoName } }));
  return body.data;
}

export async function updateCollection(client: ApiClient, id: string, input: { name: string; overview: string; nfoName: string | null }): Promise<CollectionView> {
  const body = await call(client.PUT("/api/v1/collections/{id}", { params: { path: { id } }, body: input }));
  return body.data;
}

export async function deleteCollection(client: ApiClient, id: string): Promise<void> {
  await callNoContent(client.DELETE("/api/v1/collections/{id}", { params: { path: { id } } }));
}

export async function addToCollection(client: ApiClient, id: string, itemIds: readonly string[]): Promise<CollectionView> {
  const body = await call(client.POST("/api/v1/collections/{id}/items", { params: { path: { id } }, body: { itemIds: [...itemIds] } }));
  return body.data;
}

export async function removeFromCollection(client: ApiClient, id: string, itemId: string): Promise<CollectionView> {
  const body = await call(client.DELETE("/api/v1/collections/{id}/items/{itemId}", { params: { path: { id, itemId } } }));
  return body.data;
}

/** Creates collections for NFO collection names that have none; returns how many. */
export async function syncNfoCollections(client: ApiClient): Promise<number> {
  const body = await call(client.POST("/api/v1/collections/nfo-sync", { body: {} }));
  return body.data.created;
}

export async function listPlaylists(client: ApiClient, cursor = ""): Promise<{ items: readonly Playlist[]; nextCursor: string }> {
  const query = cursor === "" ? { limit: pageSize } : { limit: pageSize, cursor };
  const body = await call(client.GET("/api/v1/playlists", { params: { query } }));
  return { items: body.data.playlists, nextCursor: body.data.nextCursor };
}

export async function getPlaylist(client: ApiClient, id: string): Promise<PlaylistView> {
  const body = await call(client.GET("/api/v1/playlists/{id}", { params: { path: { id } } }));
  return body.data;
}

export async function createPlaylist(client: ApiClient, name: string, isPublic: boolean): Promise<PlaylistView> {
  const body = await call(client.POST("/api/v1/playlists", { body: { name, public: isPublic } }));
  return body.data;
}

export async function updatePlaylist(client: ApiClient, id: string, name: string, isPublic: boolean): Promise<PlaylistView> {
  const body = await call(client.PUT("/api/v1/playlists/{id}", { params: { path: { id } }, body: { name, public: isPublic } }));
  return body.data;
}

export async function deletePlaylist(client: ApiClient, id: string): Promise<void> {
  await callNoContent(client.DELETE("/api/v1/playlists/{id}", { params: { path: { id } } }));
}

export async function addToPlaylist(client: ApiClient, id: string, itemIds: readonly string[]): Promise<PlaylistView> {
  const body = await call(client.POST("/api/v1/playlists/{id}/items", { params: { path: { id } }, body: { itemIds: [...itemIds] } }));
  return body.data;
}

export async function removePlaylistEntry(client: ApiClient, id: string, entryId: string): Promise<PlaylistView> {
  const body = await call(client.DELETE("/api/v1/playlists/{id}/entries/{entryId}", { params: { path: { id, entryId } } }));
  return body.data;
}

/** Moves an entry in front of another one, or to the end with null. */
export async function movePlaylistEntry(client: ApiClient, id: string, entryId: string, beforeEntryId: string | null): Promise<PlaylistView> {
  const body = await call(client.POST("/api/v1/playlists/{id}/entries/{entryId}/move", { params: { path: { id, entryId } }, body: { beforeEntryId } }));
  return body.data;
}

/**
 * The entry a move up (-1) or down (+1) places the entry in front of, among
 * the entries the caller sees: undefined when it cannot move, null for the
 * end.
 */
export function moveTarget(entries: readonly PlaylistEntry[], index: number, step: -1 | 1): string | null | undefined {
  const to = index + step;
  if (to < 0 || to >= entries.length) {
    return undefined;
  }
  if (step < 0) {
    return entries[to]?.entryId;
  }
  return entries[to + 1]?.entryId ?? null;
}
