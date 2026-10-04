// Share links (G48.6): administration of the links and their access log,
// and what a guest session may read about its own share. Redeeming a link
// lives with the other session calls in features/auth/api.ts.
import type { ApiClient } from "@/api/client";
import { call } from "@/api/call";
import type { components } from "@/api/schema";

export type Share = components["schemas"]["Share"];
export type ShareInput = components["schemas"]["ShareInput"];
export type ShareGrant = components["schemas"]["ShareGrant"];
export type ShareState = Share["state"];
export type ShareAccessRecord = components["schemas"]["ShareAccessRecord"];
export type ShareAccessPage = components["schemas"]["ShareAccessPage"];
export type ShareEvent = ShareAccessRecord["event"];
export type ShareRefusal = NonNullable<ShareAccessRecord["reason"]>;
export type GuestShare = components["schemas"]["GuestShare"];

/** Page size of the access log. */
export const shareAccessPageSize = 50;
/** Bounds the server puts on a link's lifetime and its concurrency cap. */
export const shareMinLifetimeMs = 5 * 60 * 1000;
export const shareMaxLifetimeMs = 90 * 24 * 60 * 60 * 1000;
export const shareMaxConcurrency = 16;

/**
 * The web link of a token. The token goes in the fragment, which browsers
 * never send to the server, so it cannot end up in request logs.
 */
export function shareLink(token: string, origin: string = globalThis.location.origin): string {
  return `${origin}/share#${encodeURIComponent(token)}`;
}

/** The newest 200 links. */
export async function listShares(client: ApiClient): Promise<readonly Share[]> {
  const body = await call(client.GET("/api/v1/shares", {}));
  return body.data;
}

/** Creates a link; the grant's token is returned only by this call. */
export async function createShare(client: ApiClient, input: ShareInput): Promise<ShareGrant> {
  const body = await call(client.POST("/api/v1/shares", { body: input }));
  return body.data;
}

export async function revokeShare(client: ApiClient, id: string): Promise<Share> {
  const body = await call(client.POST("/api/v1/shares/{id}/revoke", { params: { path: { id } }, body: {} }));
  return body.data;
}

/** One page of a link's audited events, newest first. */
export async function listShareAccess(client: ApiClient, id: string, cursor = ""): Promise<ShareAccessPage> {
  const query = cursor === "" ? { limit: shareAccessPageSize } : { limit: shareAccessPageSize, cursor };
  const body = await call(client.GET("/api/v1/shares/{id}/access", { params: { path: { id }, query } }));
  return body.data;
}

/** The share behind the current guest session; 404 for any other session. */
export async function currentShare(client: ApiClient): Promise<GuestShare> {
  const body = await call(client.GET("/api/v1/shares/current", {}));
  return body.data;
}
