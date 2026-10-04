import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import type { CatalogItem } from "@/features/items/api";
import { searchItems, type SearchKind } from "@/features/search/api";
import { resetOnUserChange } from "./userScoped";

export const useSearchStore = defineStore("search", () => {
  const { client } = useApi();
  const query = shallowRef("");
  const kind = shallowRef<SearchKind | null>(null);
  const items = shallowRef<readonly CatalogItem[]>([]);
  const total = shallowRef(0);

  // A response for a query the user has already replaced is dropped; the
  // request state machine also ignores superseded runs.
  async function loadPage(append: boolean) {
    const q = query.value;
    const k = kind.value;
    const page = await searchItems(client, q, append ? items.value.length : 0, k);
    if (q === query.value && k === kind.value) {
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
    query.value = "";
    kind.value = null;
    items.value = [];
    total.value = 0;
    firstPage.reset();
    morePages.reset();
  }

  /** Runs a search unless the same one is already on screen; "" clears. */
  async function search(q: string, k: SearchKind | null, force = false) {
    if (!force && q === query.value && k === kind.value && firstPage.state.value.status !== "error" && firstPage.state.value.status !== "idle") {
      return;
    }
    items.value = [];
    total.value = 0;
    morePages.reset();
    query.value = q;
    kind.value = k;
    if (q === "") {
      firstPage.reset();
      return;
    }
    await firstPage.run();
  }

  resetOnUserChange(reset);

  return {
    query,
    kind,
    items,
    total,
    state: firstPage.state,
    moreState: morePages.state,
    search,
    retry: () => search(query.value, kind.value, true),
    loadMore: morePages.run,
    reset,
  };
});
