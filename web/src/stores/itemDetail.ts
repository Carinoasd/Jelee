import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import { getItemDetails, getItemSources, type ItemDetails, type MediaSourceInfo } from "@/features/items/api";
import { resetOnUserChange } from "./userScoped";

export interface ItemDetail {
  readonly item: ItemDetails;
  /** File information; null when it could not be loaded. */
  readonly sources: readonly MediaSourceInfo[] | null;
}

export const useItemDetailStore = defineStore("itemDetail", () => {
  const { client } = useApi();
  const itemId = shallowRef("");

  const detail = useRequest<ItemDetail>(async () => {
    const id = itemId.value;
    const [item, sources] = await Promise.all([
      getItemDetails(client, id),
      // The item itself is readable; missing file information only hides a
      // section.
      getItemSources(client, id).catch(() => null),
    ]);
    return { item, sources };
  });

  async function open(id: string) {
    itemId.value = id;
    await detail.run();
  }

  resetOnUserChange(detail.reset);

  return { itemId, state: detail.state, open, reload: () => open(itemId.value), reset: detail.reset };
});
