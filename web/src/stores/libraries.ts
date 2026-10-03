import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import { listLibraries, type LibraryPage, type LibrarySummary } from "@/features/libraries/api";

export const useLibrariesStore = defineStore("libraries", () => {
  const { client } = useApi();
  const libraries = shallowRef<readonly LibrarySummary[]>([]);
  const nextCursor = shallowRef("");

  function accept(page: LibraryPage, append: boolean) {
    libraries.value = append ? [...libraries.value, ...page.libraries] : page.libraries;
    nextCursor.value = page.pagination.nextCursor;
    return libraries.value;
  }

  const firstPage = useRequest(
    async () => accept(await listLibraries(client), false),
    (items) => items.length === 0,
  );
  const morePages = useRequest(async () => accept(await listLibraries(client, nextCursor.value), true));

  function reset() {
    libraries.value = [];
    nextCursor.value = "";
    firstPage.reset();
    morePages.reset();
  }

  return {
    libraries,
    nextCursor,
    state: firstPage.state,
    moreState: morePages.state,
    load: firstPage.run,
    loadMore: morePages.run,
    reset,
  };
});
