<script setup lang="ts">
import { computed, onMounted, shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiBarChart from "@/components/ui/UiBarChart.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiErrorState from "@/components/ui/UiErrorState.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import type { BarRow } from "@/components/ui/types";
import { useApi } from "@/api";
import { errorMessageKey } from "@/features/errors/messages";
import { formatDateTime } from "@/i18n/format";
import { useClientHitsStore } from "@/stores/clientsHits";
import { useClientRulesStore } from "@/stores/clientsRules";
import { useToastStore } from "@/stores/toasts";
import { downloadHits, statsPeriods, type ClientHit, type ClientHitStats, type HitMode, type StatsPeriod } from "./api";
import { actionKeys, asApiError, codeLabel, dimensionKeys, modeKeys, surfaceKeys } from "./labels";

const { t, locale } = useI18n();
const store = useClientHitsStore();
const rules = useClientRulesStore();
const { client } = useApi();
const toasts = useToastStore();

const periodKeys: Readonly<Record<StatsPeriod, string>> = {
  1: "clients.hits.periods.hour",
  24: "clients.hits.periods.day",
  168: "clients.hits.periods.week",
  720: "clients.hits.periods.month",
};

onMounted(() => {
  void store.loadStats();
  void store.loadHits();
  // Rule names for the "top rules" chart and the hit log; optional.
  if (rules.state.status === "idle") {
    void rules.load();
  }
});

const periodOptions = computed(() => statsPeriods.map((hours) => ({ value: String(hours), label: t(periodKeys[hours]) })));
const period = computed({
  get: () => String(store.hours),
  set: (value: string) => {
    const next = statsPeriods.find((hours) => String(hours) === value);
    if (next !== undefined) {
      void store.setPeriod(next);
    }
  },
});

const modeOptions = computed(() => [
  { value: "", label: t("clients.hits.modeAll") },
  ...(Object.keys(modeKeys) as HitMode[]).map((value) => ({ value, label: t(modeKeys[value]) })),
]);
const mode = computed({
  get: () => store.mode,
  set: (value: string) => {
    const next = (Object.keys(modeKeys) as HitMode[]).find((candidate) => candidate === value) ?? "";
    void store.setMode(next);
  },
});

// Fetched rather than linked, so a refused export (over the limit, expired
// session) shows a localized message instead of being saved as the file.
const exporting = shallowRef(false);
async function exportHits() {
  exporting.value = true;
  try {
    await downloadHits(client, store.mode);
  } catch (error: unknown) {
    toasts.push(errorMessageKey(asApiError(error)), "danger");
  } finally {
    exporting.value = false;
  }
}
const tr = (key: string) => t(key);

function ruleName(id: string | undefined): string {
  if (id === undefined) {
    return "";
  }
  const rule = rules.rules.find((entry) => entry.id === id);
  return rule === undefined ? id : `${t(dimensionKeys[rule.dimension])}: ${rule.pattern}`;
}

function hitRule(hit: ClientHit): string {
  if (hit.ruleId !== undefined) {
    return ruleName(hit.ruleId);
  }
  return hit.mode === "default" ? t("clients.hits.defaultPolicy") : t("clients.hits.deletedRule");
}

function rows(list: readonly { value: string; hits: number }[], label: (value: string) => string): BarRow[] {
  return list.map((entry) => ({
    key: entry.value,
    label: label(entry.value) || "—",
    value: entry.hits,
    display: entry.hits.toLocaleString(locale.value),
  }));
}

function charts(stats: ClientHitStats) {
  return [
    { key: "action", caption: t("clients.hits.byAction"), rows: rows(stats.byAction, (value) => codeLabel(actionKeys, value, tr)) },
    { key: "ua", caption: t("clients.hits.topUserAgents"), rows: rows(stats.topUserAgents, (value) => value) },
    { key: "ip", caption: t("clients.hits.topIps"), rows: rows(stats.topIps, (value) => value) },
    { key: "rule", caption: t("clients.hits.topRules"), rows: rows(stats.topRules, ruleName) },
  ];
}
</script>

<template>
  <div class="jl-hits">
    <section class="jl-cc-card" aria-labelledby="clients-stats-title">
      <div class="jl-cc-head">
        <h2 id="clients-stats-title">{{ t("clients.hits.statsTitle") }}</h2>
        <UiSelectField v-model="period" :label="t('clients.hits.period')" :options="periodOptions" />
      </div>
      <RequestStatus :state="store.statsState" @retry="store.loadStats">
        <template #loading>
          <UiSkeleton shape="block" />
        </template>
        <template #default="{ data }">
          <p class="jl-cc-muted">{{ t("clients.hits.since", { date: formatDateTime(data.since, locale) }) }}</p>
          <dl class="jl-hits__totals">
            <div>
              <dt>{{ t("clients.hits.total") }}</dt>
              <dd>{{ data.total.toLocaleString(locale) }}</dd>
            </div>
            <div>
              <dt>{{ t("clients.hits.blocked") }}</dt>
              <dd>{{ data.blocked.toLocaleString(locale) }}</dd>
            </div>
            <div>
              <dt>{{ t("clients.hits.observed") }}</dt>
              <dd>{{ data.observed.toLocaleString(locale) }}</dd>
            </div>
          </dl>
          <p v-if="data.total === 0" class="jl-cc-muted">{{ t("clients.hits.noData") }}</p>
          <div v-else class="jl-hits__charts">
            <template v-for="chart in charts(data)" :key="chart.key">
              <UiBarChart
                v-if="chart.rows.length > 0"
                :caption="chart.caption"
                :rows="chart.rows"
                :label-header="t('clients.hits.item')"
                :value-header="t('clients.hits.count')"
              />
            </template>
          </div>
        </template>
      </RequestStatus>
    </section>

    <section class="jl-cc-card" aria-labelledby="clients-log-title">
      <div class="jl-cc-head">
        <h2 id="clients-log-title">{{ t("clients.hits.logTitle") }}</h2>
        <div class="jl-cc-actions">
          <UiSelectField v-model="mode" :label="t('clients.hits.mode')" :options="modeOptions" />
          <UiButton class="jl-hits__export" variant="secondary" :busy="exporting" aria-describedby="clients-export-hint" @click="exportHits">
            {{ t("clients.hits.export") }}
          </UiButton>
        </div>
      </div>
      <p id="clients-export-hint" class="jl-cc-muted">{{ t("clients.hits.exportHint") }}</p>
      <RequestStatus :state="store.hitsState" @retry="store.loadHits">
        <template #loading>
          <UiSkeleton v-for="n in 3" :key="n" shape="block" />
        </template>
        <template #empty>
          <UiEmptyState :title="t('clients.hits.empty')" />
        </template>
        <template #default>
          <div class="jl-cc-table-wrap">
            <table class="jl-cc-table">
              <caption class="jl-visually-hidden">{{ t("clients.hits.logTitle") }}</caption>
              <thead>
                <tr>
                  <th scope="col">{{ t("clients.hits.time") }}</th>
                  <th scope="col">{{ t("clients.hits.mode") }}</th>
                  <th scope="col">{{ t("clients.hits.action") }}</th>
                  <th scope="col">{{ t("clients.hits.rule") }}</th>
                  <th scope="col">{{ t("clients.hits.client") }}</th>
                  <th scope="col">{{ t("clients.hits.network") }}</th>
                  <th scope="col">{{ t("clients.hits.surface") }}</th>
                  <th scope="col" class="jl-cc-num">{{ t("clients.hits.count") }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="hit in store.hits" :key="hit.id">
                  <th scope="row">{{ formatDateTime(hit.bucket, locale) }}</th>
                  <td>{{ codeLabel(modeKeys, hit.mode, tr) }}</td>
                  <td>{{ codeLabel(actionKeys, hit.action, tr) }}</td>
                  <td class="jl-cc-code">{{ hitRule(hit) }}</td>
                  <td>
                    <span v-if="hit.appName">{{ hit.appName }}<br /></span>
                    <span class="jl-cc-code">{{ hit.userAgent || "—" }}</span>
                  </td>
                  <td class="jl-cc-code">{{ hit.network || "—" }}</td>
                  <td>{{ codeLabel(surfaceKeys, hit.surface, tr) }}</td>
                  <td class="jl-cc-num">{{ hit.hits.toLocaleString(locale) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <UiErrorState v-if="store.moreState.status === 'error'" :error="store.moreState.error" @retry="store.loadMore" />
          <UiButton v-if="store.nextCursor !== ''" variant="secondary" :busy="store.moreState.status === 'loading'" @click="store.loadMore">
            {{ t("common.loadMore") }}
          </UiButton>
        </template>
      </RequestStatus>
    </section>
  </div>
</template>

<style scoped>
.jl-hits {
  display: grid;
  gap: var(--jl-space-6);
}

.jl-hits__totals {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
  gap: var(--jl-space-3);
  margin: 0;
}

.jl-hits__totals > div {
  padding: var(--jl-space-3) var(--jl-space-4);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
}

.jl-hits__totals dt {
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-hits__totals dd {
  margin: 0;
  font-size: var(--jl-font-size-xl);
  font-variant-numeric: tabular-nums;
}

.jl-hits__charts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(18rem, 1fr));
  gap: var(--jl-space-6);
}

.jl-hits__export {
  align-self: end;
}
</style>
