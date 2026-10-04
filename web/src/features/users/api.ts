// Administrator account management (G07, G48.7). Every call is authorized
// again by the server; the web client only shapes requests and responses.
import type { ApiClient } from "@/api/client";
import { call, callNoContent } from "@/api/call";
import type { components } from "@/api/schema";
import { listLibraries, type LibrarySummary } from "@/features/libraries/api";

export type User = components["schemas"]["User"];
export type UserPage = components["schemas"]["UserPage"];
export type CreateUserInput = components["schemas"]["CreateUser"];
export type UserSettings = components["schemas"]["UserSettings"];
export type UserLocale = User["locale"];
export type DeliveryLimits = components["schemas"]["DeliveryLimits"];
export type LibraryGrant = components["schemas"]["LibraryGrant"];
export type ContentAccess = components["schemas"]["ContentAccess"];
export type ContentAccessView = components["schemas"]["ContentAccessView"];
export type ItemAccessRule = components["schemas"]["ItemAccessRule"];
export type ItemAccessEffect = components["schemas"]["ItemAccessRuleInput"]["effect"];
export type CatalogItem = components["schemas"]["CatalogItem"];

export const usersPageSize = 50;
/** Results the item picker of a new rule shows. */
export const itemSearchLimit = 10;
/** Library pages read for the grant checklist (100 per page, 1000 grants at most). */
const maxLibraryPages = 20;

export async function listUsers(
  client: ApiClient,
  options: { cursor?: string; includeDeleted: boolean },
): Promise<UserPage> {
  const query = {
    limit: usersPageSize,
    includeDeleted: options.includeDeleted,
    ...(options.cursor ? { cursor: options.cursor } : {}),
  };
  const body = await call(client.GET("/api/v1/users", { params: { query } }));
  return body.data;
}

/**
 * Creates an account. The server replays the first result for a repeated
 * idempotency key, so a retry after a lost response never creates twice.
 */
export async function createUser(client: ApiClient, input: CreateUserInput, idempotencyKey: string): Promise<User> {
  const body = await call(
    client.POST("/api/v1/users", { params: { header: { "Idempotency-Key": idempotencyKey } }, body: input }),
  );
  return body.data;
}

export async function getUser(client: ApiClient, id: string): Promise<User> {
  const body = await call(client.GET("/api/v1/users/{id}", { params: { path: { id } } }));
  return body.data;
}

/** Replaces every setting; omitted optional fields would reset on the server. */
export async function updateUser(client: ApiClient, id: string, settings: Required<UserSettings>): Promise<User> {
  const body = await call(client.PUT("/api/v1/users/{id}", { params: { path: { id } }, body: settings }));
  return body.data;
}

export async function setNativeAccess(client: ApiClient, id: string, allowNative: boolean): Promise<User> {
  const body = await call(client.PUT("/api/v1/users/{id}/native", { params: { path: { id } }, body: { allowNative } }));
  return body.data;
}

export async function getDeliveryLimits(client: ApiClient, id: string): Promise<DeliveryLimits> {
  const body = await call(client.GET("/api/v1/users/{id}/delivery-limits", { params: { path: { id } } }));
  return body.data;
}

export async function putDeliveryLimits(client: ApiClient, id: string, limits: DeliveryLimits): Promise<DeliveryLimits> {
  const body = await call(client.PUT("/api/v1/users/{id}/delivery-limits", { params: { path: { id } }, body: limits }));
  return body.data;
}

export async function unlockUser(client: ApiClient, id: string): Promise<void> {
  await callNoContent(client.POST("/api/v1/users/{id}/unlock", { params: { path: { id } }, body: {} }));
}

export async function deleteUser(client: ApiClient, id: string): Promise<void> {
  await callNoContent(client.DELETE("/api/v1/users/{id}", { params: { path: { id } } }));
}

export async function restoreUser(client: ApiClient, id: string): Promise<User> {
  const body = await call(client.POST("/api/v1/users/{id}/restore", { params: { path: { id } }, body: {} }));
  return body.data;
}

export async function listGrants(client: ApiClient, id: string): Promise<readonly LibraryGrant[]> {
  const body = await call(client.GET("/api/v1/users/{id}/libraries", { params: { path: { id } } }));
  return body.data;
}

export async function putGrants(client: ApiClient, id: string, libraryIds: readonly string[]): Promise<void> {
  await callNoContent(
    client.PUT("/api/v1/users/{id}/libraries", { params: { path: { id } }, body: { libraryIds: [...libraryIds] } }),
  );
}

/** Every registered library, following the cursor up to a fixed bound. */
export async function listAllLibraries(client: ApiClient): Promise<readonly LibrarySummary[]> {
  const all: LibrarySummary[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < maxLibraryPages; page++) {
    const result = await listLibraries(client, cursor);
    all.push(...result.libraries);
    if (result.pagination.nextCursor === "") {
      break;
    }
    cursor = result.pagination.nextCursor;
  }
  return all;
}

export async function getContentAccess(client: ApiClient, id: string): Promise<ContentAccessView> {
  const body = await call(client.GET("/api/v1/users/{id}/content-access", { params: { path: { id } } }));
  return body.data;
}

export async function putContentAccess(client: ApiClient, id: string, access: ContentAccess): Promise<ContentAccessView> {
  const body = await call(client.PUT("/api/v1/users/{id}/content-access", { params: { path: { id } }, body: access }));
  return body.data;
}

export async function putItemRule(
  client: ApiClient,
  id: string,
  itemId: string,
  effect: ItemAccessEffect,
): Promise<ItemAccessRule> {
  const body = await call(
    client.PUT("/api/v1/users/{id}/content-access/items/{itemId}", { params: { path: { id, itemId } }, body: { effect } }),
  );
  return body.data;
}

export async function deleteItemRule(client: ApiClient, id: string, itemId: string): Promise<void> {
  await callNoContent(
    client.DELETE("/api/v1/users/{id}/content-access/items/{itemId}", { params: { path: { id, itemId } } }),
  );
}

/** Title search over the items the administrator can see. */
export async function searchItems(client: ApiClient, q: string): Promise<readonly CatalogItem[]> {
  const body = await call(client.GET("/api/v1/items", { params: { query: { q, limit: itemSearchLimit } } }));
  return body.data;
}
