import type { ApiClient } from "@/api/client";
import { call } from "@/api/call";
import type { CatalogItem } from "@/features/items/api";

/** Items shown in the home page's "latest premieres" block. */
export const latestLimit = 12;

/** Most recent premieres across every library the caller may see. */
export async function listLatest(client: ApiClient): Promise<readonly CatalogItem[]> {
  const body = await call(client.GET("/api/v1/items", { params: { query: { sort: "premiereDate", order: "desc", offset: 0, limit: latestLimit } } }));
  return body.data;
}
