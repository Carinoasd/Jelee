import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import { listLibraryItems, type CatalogItem } from "@/features/items/api";
import { resetOnUserChange } from "./userScoped";

export const useLibraryItemsStore = defineStore("libraryItems", () => {
  const { client } = useApi();
  const libraryId = shallowRef("");
  const items = shallowRef<readonly CatalogItem[]>([]);
  const nextCursor = shallowRef("");

  // Each load captures the library it was started for; a response arriving
  // after the user switched libraries is dropped instead of merged.
  async function loadPage(append: boolean) {
    const target = libraryId.value;
    const page = await listLibraryItems(client, target, append ? nextCursor.value : "");
    if (target === libraryId.value) {
      items.value = append ? [...items.value, ...page.items] : page.items;
      nextCursor.value = page.nextCursor;
    }
    return items.value;
  }

  const firstPage = useRequest(
    () => loadPage(false),
    (list) => list.length === 0 && nextCursor.value === "",
  );
  const morePages = useRequest(() => loadPage(true));

  function reset() {
    items.value = [];
    nextCursor.value = "";
    firstPage.reset();
    morePages.reset();
  }

  /** Shows a library, reloading when it differs from the one on screen. */
  async function open(id: string, force = false) {
    if (id !== libraryId.value || force || firstPage.state.value.status === "error") {
      reset();
      libraryId.value = id;
      await firstPage.run();
    }
  }

  resetOnUserChange(reset);

  return {
    libraryId,
    items,
    nextCursor,
    state: firstPage.state,
    moreState: morePages.state,
    open,
    reload: () => open(libraryId.value, true),
    loadMore: morePages.run,
    reset,
  };
});
