import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { ApiError } from "@/api/errors";
import { useRequest } from "@/api/requestState";
import { getItem, getItemMetadata, type CatalogItem } from "@/features/items/api";
import { summarizeMetadata, type MetadataSummary } from "@/features/items/metadata";
import { useAuthStore } from "./auth";
import { resetOnUserChange } from "./userScoped";

/**
 * "admin-only": the account cannot read item metadata (G35.2 is enforced by
 * the server; the UI does not even ask). "unavailable": the request failed.
 */
export type MetadataAccess = "available" | "admin-only" | "unavailable";

export interface ItemDetail {
  readonly item: CatalogItem;
  readonly metadata: MetadataSummary | null;
  readonly metadataAccess: MetadataAccess;
}

export const useItemDetailStore = defineStore("itemDetail", () => {
  const { client } = useApi();
  const auth = useAuthStore();
  const itemId = shallowRef("");

  const detail = useRequest<ItemDetail>(async () => {
    const id = itemId.value;
    const item = await getItem(client, id);
    if (!auth.isAdmin) {
      return { item, metadata: null, metadataAccess: "admin-only" };
    }
    try {
      return { item, metadata: summarizeMetadata(await getItemMetadata(client, id)), metadataAccess: "available" };
    } catch (error: unknown) {
      // The item itself is readable; missing metadata only hides a section.
      const forbidden = error instanceof ApiError && error.status === 403;
      return { item, metadata: null, metadataAccess: forbidden ? "admin-only" : "unavailable" };
    }
  });

  async function open(id: string) {
    itemId.value = id;
    await detail.run();
  }

  resetOnUserChange(detail.reset);

  return { itemId, state: detail.state, open, reload: () => open(itemId.value), reset: detail.reset };
});
