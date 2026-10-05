import { defineStore } from "pinia";
import { shallowRef } from "vue";
import { useApi } from "@/api";
import type { ApiClient } from "@/api/client";
import { useRequest } from "@/api/requestState";
import {
  clearMyHistory,
  isEmptyReport,
  rangeFor,
  readAllWatchStats,
  readMyWatchStats,
  type WatchPeriod,
  type WatchStatsQuery,
  type WatchStatsReport,
} from "@/features/stats/api";
import { resetOnUserChange } from "./userScoped";

function statsStore(read: (client: ApiClient, query: WatchStatsQuery) => Promise<WatchStatsReport>) {
  return () => {
    const { client } = useApi();
    const period = shallowRef<WatchPeriod>("day");
    const top = shallowRef(10);
    const range = shallowRef(rangeFor("day"));
    const request = useRequest(() => read(client, { period: period.value, top: top.value, ...range.value }), isEmptyReport);

    /** Loads the report for a grouping and top-list size over the matching range. */
    async function load(nextPeriod: WatchPeriod = period.value, nextTop: number = top.value) {
      period.value = nextPeriod;
      top.value = nextTop;
      range.value = rangeFor(nextPeriod);
      await request.run();
    }

    function reset() {
      period.value = "day";
      top.value = 10;
      range.value = rangeFor("day");
      request.reset();
    }

    resetOnUserChange(reset);
    return { period, top, range, state: request.state, load, reload: () => load(), reset };
  };
}

const myStats = statsStore(readMyWatchStats);

export const useMyWatchStatsStore = defineStore("myWatchStats", () => {
  const { client } = useApi();
  const store = myStats();
  const clearing = shallowRef(false);

  /** Clears the caller's history, then reloads the (now empty) report. */
  async function clearHistory(): Promise<void> {
    clearing.value = true;
    try {
      await clearMyHistory(client);
    } finally {
      clearing.value = false;
    }
    await store.reload();
  }

  return { ...store, clearing, clearHistory };
});

export const useAllWatchStatsStore = defineStore("allWatchStats", () => {
  return statsStore(readAllWatchStats)();
});
