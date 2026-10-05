import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import { listLibraryItems, type CatalogItem, type LibrarySort } from "@/features/items/api";
import { resetOnUserChange } from "./userScoped";

export const useLibraryItemsStore = defineStore("libraryItems", () => {
  const { client } = useApi();
  const libraryId = shallowRef("");
  const sort = shallowRef<LibrarySort>("name");
  const items = shallowRef<readonly CatalogItem[]>([]);
  const total = shallowRef(0);

  // Each load captures the library and order it was started for; a response
  // arriving after the user switched either is dropped instead of merged.
  async function loadPage(append: boolean) {
    const target = libraryId.value;
    const order = sort.value;
    const page = await listLibraryItems(client, target, append ? items.value.length : 0, order);
    if (target === libraryId.value && order === sort.value) {
      items.value = append ? [...items.value, ...page.items] : page.items;
      total.value = page.total;
    }
    return items.value;
  }

  const firstPage = useRequest(
    () => loadPage(false),
    (list) => list.length === 0,
  );
  const morePages = useRequest(() => loadPage(true));

  function reset() {
    items.value = [];
    total.value = 0;
    firstPage.reset();
    morePages.reset();
  }

  /** Shows a library in an order, reloading when either differs from the one on screen. */
  async function open(id: string, order: LibrarySort = "name", force = false) {
    if (id !== libraryId.value || order !== sort.value || force || firstPage.state.value.status === "error") {
      reset();
      libraryId.value = id;
      sort.value = order;
      await firstPage.run();
    }
  }

  resetOnUserChange(reset);

  return {
    libraryId,
    sort,
    items,
    total,
    state: firstPage.state,
    moreState: morePages.state,
    open,
    reload: () => open(libraryId.value, sort.value, true),
    loadMore: morePages.run,
    reset,
  };
});
