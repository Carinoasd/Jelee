import type { ApiClient } from "@/api/client";
import { call } from "@/api/call";
import type { components } from "@/api/schema";

export type LibraryPage = components["schemas"]["LibraryPage"];
export type LibrarySummary = components["schemas"]["LibrarySummary"];

export const libraryPageSize = 50;

export async function listLibraries(client: ApiClient, cursor?: string): Promise<LibraryPage> {
  const query = cursor ? { limit: libraryPageSize, cursor } : { limit: libraryPageSize };
  const body = await call(client.GET("/api/v1/libraries", { params: { query } }));
  return body.data;
}
