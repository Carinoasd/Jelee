<script setup lang="ts">
import { computed, onMounted } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { formatDuration, formatNumber } from "@/i18n/format";
import { useAllWatchStatsStore } from "@/stores/watchStats";
import { exportUrl, type WatchPeriod } from "./api";
import StatsControls from "./StatsControls.vue";
import WatchStatsSummary from "./WatchStatsSummary.vue";

// Statistics of every user (administrators; the server re-checks) with the
// top users and download links for the daily roll-up of the shown range.
const { t, locale } = useI18n();
const store = useAllWatchStatsStore();

const period = computed({
  get: () => store.period,
  set: (value: WatchPeriod) => void store.load(value, store.top),
});
const top = computed({
  get: () => store.top,
  set: (value: number) => void store.load(store.period, value),
});
const csvUrl = computed(() => exportUrl(store.range, "csv"));
const ndjsonUrl = computed(() => exportUrl(store.range, "ndjson"));

onMounted(() => {
  void store.load();
});
</script>

<template>
  <section class="jl-stats" aria-labelledby="admin-stats-title">
    <h1 id="admin-stats-title" tabindex="-1">{{ t("stats.allTitle") }}</h1>
    <p class="jl-stats__intro">{{ t("stats.intro") }}</p>
    <StatsControls v-model:period="period" v-model:top="top" :range="store.range" />

    <section class="jl-stats__export" aria-labelledby="stats-export">
      <h2 id="stats-export">{{ t("stats.export.heading") }}</h2>
      <p class="jl-stats__intro">{{ t("stats.export.hint") }}</p>
      <p class="jl-stats__links">
        <a :href="csvUrl" download>{{ t("stats.export.csv") }}</a>
        <a :href="ndjsonUrl" download>{{ t("stats.export.ndjson") }}</a>
      </p>
    </section>

    <RequestStatus :state="store.state" @retry="store.reload">
      <template #loading>
        <div class="jl-stats__skeleton" aria-hidden="true">
          <UiSkeleton v-for="n in 3" :key="n" shape="block" />
        </div>
      </template>
      <template #default="{ data }">
        <WatchStatsSummary :report="data">
          <template #extra>
            <section v-if="data.topUsers && data.topUsers.length > 0" class="jl-stats__card" aria-labelledby="stats-top-users">
              <h2 id="stats-top-users">{{ t("stats.topUsers") }}</h2>
              <table class="jl-stats__table">
                <thead>
                  <tr>
                    <th scope="col">{{ t("stats.column.rank") }}</th>
                    <th scope="col">{{ t("stats.column.user") }}</th>
                    <th scope="col">{{ t("stats.totals.time") }}</th>
                    <th scope="col">{{ t("stats.totals.views") }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="(entry, index) in data.topUsers" :key="entry.userId">
                    <td>{{ index + 1 }}</td>
                    <td>
                      <RouterLink :to="{ name: 'admin-user', params: { userId: entry.userId } }">{{ entry.userName }}</RouterLink>
                    </td>
                    <td>{{ formatDuration(entry.effectiveSeconds, locale) }}</td>
                    <td>{{ formatNumber(entry.views, locale) }}</td>
                  </tr>
                </tbody>
              </table>
            </section>
          </template>
        </WatchStatsSummary>
      </template>
      <template #empty>
        <UiEmptyState :title="t('stats.allEmpty')">
          <p>{{ t("stats.emptyHint") }}</p>
        </UiEmptyState>
      </template>
    </RequestStatus>
  </section>
</template>

<style scoped>
.jl-stats {
  display: grid;
  gap: var(--jl-space-6);
}

.jl-stats h1 {
  margin: 0;
}

.jl-stats h2 {
  margin: 0 0 var(--jl-space-3);
  font-size: var(--jl-font-size-lg);
}

.jl-stats__intro {
  margin: 0;
  color: var(--jl-color-text-muted);
}

.jl-stats__skeleton {
  display: grid;
  gap: var(--jl-space-3);
}

.jl-stats__export,
.jl-stats__card {
  padding: var(--jl-space-4) var(--jl-space-6);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-surface);
  overflow-x: auto;
}

.jl-stats__links {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-4);
  margin: var(--jl-space-3) 0 0;
}

.jl-stats__links a {
  display: inline-flex;
  align-items: center;
  min-height: var(--jl-touch-target);
}

.jl-stats__table {
  width: 100%;
  border-collapse: collapse;
}

.jl-stats__table th,
.jl-stats__table td {
  padding: var(--jl-space-2) var(--jl-space-3);
  border-bottom: 1px solid var(--jl-color-border);
  text-align: start;
}
</style>
