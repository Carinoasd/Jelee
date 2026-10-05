<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiBarChart from "@/components/ui/UiBarChart.vue";
import UiColumnChart from "@/components/ui/UiColumnChart.vue";
import type { BarRow } from "@/components/ui/types";
import { kindLabelKey } from "@/features/items/labels";
import { formatDate, formatDuration, formatNumber, formatPercent } from "@/i18n/format";
import type { WatchStatsReport } from "./api";

// Read-only presentation of a statistics report: totals, time per period,
// top items, and per library and kind breakdowns. Items link to their detail
// page only; nothing here offers or describes media delivery.
const props = defineProps<{ report: WatchStatsReport }>();
const { t, locale } = useI18n();

const duration = (seconds: number) => formatDuration(seconds, locale.value);
const totals = computed(() => {
  const value = props.report.totals;
  return [
    { key: "time", label: t("stats.totals.time"), value: duration(value.effectiveSeconds) },
    { key: "views", label: t("stats.totals.views"), value: formatNumber(value.views, locale.value) },
    { key: "sessions", label: t("stats.totals.sessions"), value: formatNumber(value.sessions, locale.value) },
    { key: "completion", label: t("stats.totals.completion"), value: formatPercent(value.completionRate, locale.value) },
    { key: "first", label: t("stats.totals.firstViews"), value: formatNumber(value.firstPlays, locale.value) },
    { key: "again", label: t("stats.totals.rewatches"), value: formatNumber(value.rewatches, locale.value) },
  ];
});

const periodStyle = computed(() => {
  switch (props.report.period) {
    case "year":
      return "year";
    case "month":
      return "month";
    default:
      return "day";
  }
});

const periodRows = computed<BarRow[]>(() =>
  props.report.periods.map((entry) => ({
    key: entry.start,
    label: formatDate(entry.start, locale.value, periodStyle.value),
    value: entry.effectiveSeconds,
    display: duration(entry.effectiveSeconds),
  })),
);

function kindLabel(kind: string): string {
  return kind in kindLabelKey ? t(kindLabelKey[kind as keyof typeof kindLabelKey]) : kind;
}

const libraryRows = computed<BarRow[]>(() =>
  props.report.libraries.map((entry) => ({
    key: entry.libraryId,
    label: entry.name,
    value: entry.effectiveSeconds,
    display: duration(entry.effectiveSeconds),
  })),
);
const kindRows = computed<BarRow[]>(() =>
  props.report.kinds.map((entry) => ({
    key: entry.kind,
    label: kindLabel(entry.kind),
    value: entry.effectiveSeconds,
    display: duration(entry.effectiveSeconds),
  })),
);
const topMax = computed(() => Math.max(0, ...props.report.topItems.map((entry) => entry.effectiveSeconds)));
const share = (seconds: number) => (topMax.value > 0 ? `${Math.max(1, Math.round((seconds / topMax.value) * 100))}%` : "0%");
</script>

<template>
  <div class="jl-report">
    <section aria-labelledby="stats-totals">
      <h2 id="stats-totals">{{ t("stats.totals.heading") }}</h2>
      <dl class="jl-report__tiles">
        <div v-for="tile in totals" :key="tile.key" class="jl-report__tile">
          <dt>{{ tile.label }}</dt>
          <dd>{{ tile.value }}</dd>
        </div>
      </dl>
      <p class="jl-report__note">{{ t("stats.timeZone", { zone: report.timeZone }) }}</p>
    </section>

    <section v-if="periodRows.length > 0" class="jl-report__card">
      <UiColumnChart
        :caption="t('stats.chart.periods')"
        :rows="periodRows"
        :label-header="t('stats.chart.period')"
        :value-header="t('stats.totals.time')"
      />
    </section>

    <section v-if="report.topItems.length > 0" class="jl-report__card" aria-labelledby="stats-top">
      <h2 id="stats-top">{{ t("stats.topItems") }}</h2>
      <table class="jl-report__table">
        <thead>
          <tr>
            <th scope="col">{{ t("stats.column.rank") }}</th>
            <th scope="col">{{ t("stats.column.title") }}</th>
            <th scope="col">{{ t("stats.column.kind") }}</th>
            <th scope="col">{{ t("stats.totals.time") }}</th>
            <th scope="col">{{ t("stats.totals.views") }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(entry, index) in report.topItems" :key="entry.itemId">
            <td>{{ index + 1 }}</td>
            <td>
              <RouterLink :to="{ name: 'item', params: { itemId: entry.itemId } }">{{ entry.title }}</RouterLink>
              <UiBadge v-if="entry.userData?.played" class="jl-report__badge">{{ t("stats.finished") }}</UiBadge>
            </td>
            <td>{{ kindLabel(entry.kind) }}</td>
            <td class="jl-report__time">
              <span class="jl-report__track" aria-hidden="true"><span class="jl-report__bar" :style="{ width: share(entry.effectiveSeconds) }" /></span>
              {{ duration(entry.effectiveSeconds) }}
            </td>
            <td>{{ formatNumber(entry.views, locale) }}</td>
          </tr>
        </tbody>
      </table>
    </section>

    <slot name="extra" />

    <div class="jl-report__split">
      <section v-if="libraryRows.length > 0" class="jl-report__card">
        <UiBarChart
          :caption="t('stats.byLibrary')"
          :rows="libraryRows"
          :label-header="t('stats.column.library')"
          :value-header="t('stats.totals.time')"
        />
      </section>
      <section v-if="kindRows.length > 0" class="jl-report__card">
        <UiBarChart
          :caption="t('stats.byKind')"
          :rows="kindRows"
          :label-header="t('stats.column.kind')"
          :value-header="t('stats.totals.time')"
        />
      </section>
    </div>
  </div>
</template>

<style scoped>
.jl-report {
  display: grid;
  gap: var(--jl-space-6);
}

.jl-report h2 {
  margin: 0 0 var(--jl-space-3);
  font-size: var(--jl-font-size-lg);
}

.jl-report__tiles {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: var(--jl-space-3);
  margin: 0;
}

.jl-report__tile {
  padding: var(--jl-space-3) var(--jl-space-4);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-surface);
}

.jl-report__tile dt {
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-report__tile dd {
  margin: var(--jl-space-1) 0 0;
  font-size: var(--jl-font-size-xl);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.jl-report__note {
  margin: var(--jl-space-2) 0 0;
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-report__card {
  padding: var(--jl-space-4) var(--jl-space-6);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-surface);
  min-width: 0;
  overflow-x: auto;
}

.jl-report__table {
  width: 100%;
  border-collapse: collapse;
}

.jl-report__table th,
.jl-report__table td {
  padding: var(--jl-space-2) var(--jl-space-3);
  border-bottom: 1px solid var(--jl-color-border);
  text-align: start;
  vertical-align: middle;
}

.jl-report__badge {
  margin-inline-start: var(--jl-space-2);
}

.jl-report__time {
  white-space: nowrap;
}

.jl-report__track {
  display: inline-block;
  width: 80px;
  height: 8px;
  margin-inline-end: var(--jl-space-2);
  border-radius: var(--jl-radius-pill);
  background: var(--jl-color-chart-track);
  overflow: hidden;
  vertical-align: middle;
}

.jl-report__bar {
  display: block;
  height: 100%;
  background: var(--jl-color-chart);
}

.jl-report__split {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: var(--jl-space-6);
}
</style>
