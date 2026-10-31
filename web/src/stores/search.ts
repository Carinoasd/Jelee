import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import type { CatalogItem } from "@/features/items/api";
import { sameFilters, searchGroups, searchItems, type SearchFilters, type SearchGroup } from "@/features/search/api";
import { resetOnUserChange } from "./userScoped";

const noSearch: SearchFilters = { q: "", kind: null, libraryId: null };

/**
 * Search results (G34.3). Without a kind the page shows an overview: the
 * first results of every kind with each kind's total. Narrowed to one kind it
 * pages through all of them. Responses for filters the user has already
 * replaced are dropped; the request state machine also ignores superseded runs.
 */
export const useSearchStore = defineStore("search", () => {
  const { client } = useApi();
  const filters = shallowRef<SearchFilters>(noSearch);
  const groups = shallowRef<readonly SearchGroup[]>([]);
  const items = shallowRef<readonly CatalogItem[]>([]);
  const total = shallowRef(0);
  let fresh = false;

  async function loadFirst(): Promise<number> {
    const current = filters.value;
    const force = fresh;
    fresh = false;
    if (current.kind === null) {
      const found = await searchGroups(client, current, force);
      if (sameFilters(current, filters.value)) {
        groups.value = found;
      }
      return found.length;
    }
    const page = await searchItems(client, current, 0, undefined, force);
    if (sameFilters(current, filters.value)) {
      items.value = page.items;
      total.value = page.total;
    }
    return page.items.length;
  }

  async function loadMore(): Promise<void> {
    const current = filters.value;
    if (current.kind === null || items.value.length >= total.value) {
      return;
    }
    const page = await searchItems(client, current, items.value.length);
    if (sameFilters(current, filters.value)) {
      items.value = [...items.value, ...page.items];
      total.value = page.total;
    }
  }

  const firstPage = useRequest(loadFirst, (count) => count === 0);
  const morePages = useRequest(loadMore);

  function clearResults() {
    groups.value = [];
    items.value = [];
    total.value = 0;
    morePages.reset();
  }

  function reset() {
    filters.value = noSearch;
    clearResults();
    firstPage.reset();
  }

  /** Runs a search unless the same one is already on screen; an empty query clears. */
  async function search(next: SearchFilters, force = false) {
    const status = firstPage.state.value.status;
    if (!force && sameFilters(next, filters.value) && status !== "error" && status !== "idle") {
      return;
    }
    clearResults();
    filters.value = next;
    if (next.q === "") {
      firstPage.reset();
      return;
    }
    fresh = force;
    await firstPage.run();
  }

  /** Loads the next page of a one-kind search unless one is loading or all are shown. */
  async function more() {
    if (morePages.state.value.status !== "loading" && filters.value.kind !== null && items.value.length < total.value) {
      await morePages.run();
    }
  }

  resetOnUserChange(reset);

  return {
    filters,
    groups,
    items,
    total,
    state: firstPage.state,
    moreState: morePages.state,
    search,
    retry: () => search(filters.value, true),
    loadMore: more,
    reset,
  };
});
