import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import { getHitStats, listHits, type ClientHit, type ClientHitStats, type HitMode, type StatsPeriod } from "@/features/clients/api";
import { resetOnUserChange } from "./userScoped";

/** Hit statistics for a period and the paged hit log filtered by mode. */
export const useClientHitsStore = defineStore("clientsHits", () => {
  const { client } = useApi();
  const hours = shallowRef<StatsPeriod>(24);
  const mode = shallowRef<HitMode | "">("");
  const hits = shallowRef<readonly ClientHit[]>([]);
  const nextCursor = shallowRef("");
  let generation = 0;

  const stats = useRequest<ClientHitStats>(() => getHitStats(client, hours.value));

  async function loadPage(append: boolean) {
    const current = append ? generation : ++generation;
    const page = await listHits(client, mode.value, append ? nextCursor.value : "");
    if (current === generation) {
      hits.value = append ? [...hits.value, ...page.hits] : page.hits;
      nextCursor.value = page.pagination.nextCursor;
    }
    return hits.value;
  }

  const firstPage = useRequest(
    () => loadPage(false),
    (list) => list.length === 0,
  );
  const morePages = useRequest(() => loadPage(true));

  async function setPeriod(next: StatsPeriod) {
    hours.value = next;
    await stats.run();
  }

  async function setMode(next: HitMode | "") {
    mode.value = next;
    morePages.reset();
    await firstPage.run();
  }

  function reset() {
    generation++;
    hours.value = 24;
    mode.value = "";
    hits.value = [];
    nextCursor.value = "";
    stats.reset();
    firstPage.reset();
    morePages.reset();
  }

  resetOnUserChange(reset);

  return {
    hours,
    mode,
    hits,
    nextCursor,
    statsState: stats.state,
    hitsState: firstPage.state,
    moreState: morePages.state,
    loadStats: stats.run,
    loadHits: firstPage.run,
    loadMore: morePages.run,
    setPeriod,
    setMode,
    reset,
  };
});
