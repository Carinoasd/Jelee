<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import { formatDate } from "@/i18n/format";
import { isWatchPeriod, topSizes, watchPeriods, type WatchPeriod } from "./api";

// Grouping and top-list size of a statistics report; the range follows the
// grouping (see periodDays) and is shown under the controls.
const props = defineProps<{ range: { from: string; to: string } }>();
const period = defineModel<WatchPeriod>("period", { required: true });
const top = defineModel<number>("top", { required: true });
const { t, locale } = useI18n();

const periodKey: Readonly<Record<WatchPeriod, string>> = {
  day: "stats.period.day",
  week: "stats.period.week",
  month: "stats.period.month",
  year: "stats.period.year",
};
const periodOptions = computed(() => watchPeriods.map((value) => ({ value, label: t(periodKey[value]) })));
const topOptions = computed(() => topSizes.map((value) => ({ value: String(value), label: t("stats.topN", { n: value }) })));
const periodModel = computed({
  get: () => period.value,
  set: (value: string) => {
    if (isWatchPeriod(value)) {
      period.value = value;
    }
  },
});
const topModel = computed({
  get: () => String(top.value),
  set: (value: string) => {
    top.value = Number(value);
  },
});
const rangeText = computed(() =>
  t("stats.range", { from: formatDate(props.range.from, locale.value), to: formatDate(props.range.to, locale.value) }),
);
</script>

<template>
  <div class="jl-stats-controls">
    <UiSelectField v-model="periodModel" :label="t('stats.period.label')" :options="periodOptions" :hint="rangeText" />
    <UiSelectField v-model="topModel" :label="t('stats.topLabel')" :options="topOptions" />
  </div>
</template>

<style scoped>
.jl-stats-controls {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-4);
}
</style>
