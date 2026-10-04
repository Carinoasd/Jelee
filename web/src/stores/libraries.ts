import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import { listLibraries, type LibraryPage, type LibrarySummary } from "@/features/libraries/api";
import { resetOnUserChange } from "./userScoped";

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

  function find(id: string): LibrarySummary | undefined {
    return libraries.value.find((library) => library.id === id);
  }

  /** Loads the first page unless it is already on screen. */
  async function ensureLoaded() {
    if (firstPage.state.value.status === "idle" || firstPage.state.value.status === "error") {
      await firstPage.run();
    }
  }

  /** Loads every page, for pickers that offer all libraries; stops at a failure. */
  async function ensureAll() {
    await ensureLoaded();
    while (nextCursor.value !== "" && firstPage.state.value.status === "success") {
      const before = libraries.value.length;
      await morePages.run();
      if (morePages.state.value.status !== "success" || libraries.value.length === before) {
        break;
      }
    }
  }

  resetOnUserChange(reset);

  return {
    libraries,
    find,
    ensureLoaded,
    ensureAll,
    nextCursor,
    state: firstPage.state,
    moreState: morePages.state,
    load: firstPage.run,
    loadMore: morePages.run,
    reset,
  };
});
