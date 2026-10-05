<script setup lang="ts">
import { computed, onMounted } from "vue";
import { useI18n } from "vue-i18n";
import { ApiError, networkError } from "@/api/errors";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { useToastStore } from "@/stores/toasts";
import { useMyWatchStatsStore } from "@/stores/watchStats";
import type { WatchPeriod } from "./api";
import StatsControls from "./StatsControls.vue";
import WatchStatsSummary from "./WatchStatsSummary.vue";

const { t } = useI18n();
const store = useMyWatchStatsStore();
const toasts = useToastStore();

const period = computed({
  get: () => store.period,
  set: (value: WatchPeriod) => void store.load(value, store.top),
});
const top = computed({
  get: () => store.top,
  set: (value: number) => void store.load(store.period, value),
});

onMounted(() => {
  void store.load();
});

async function clearHistory() {
  try {
    await store.clearHistory();
    toasts.push("stats.clear.done", "success");
  } catch (error: unknown) {
    toasts.push(errorMessageKey(error instanceof ApiError ? error : networkError(error)), "danger");
  }
}
</script>

<template>
  <section class="jl-stats" aria-labelledby="stats-title">
    <h1 id="stats-title" tabindex="-1">{{ t("stats.title") }}</h1>
    <p class="jl-stats__intro">{{ t("stats.intro") }}</p>
    <StatsControls v-model:period="period" v-model:top="top" :range="store.range" />

    <RequestStatus :state="store.state" @retry="store.reload">
      <template #loading>
        <div class="jl-stats__skeleton" aria-hidden="true">
          <UiSkeleton v-for="n in 3" :key="n" shape="block" />
        </div>
      </template>
      <template #default="{ data }">
        <WatchStatsSummary :report="data" />
      </template>
      <template #empty>
        <UiEmptyState :title="t('stats.empty')">
          <p>{{ t("stats.emptyHint") }}</p>
        </UiEmptyState>
      </template>
    </RequestStatus>

    <section class="jl-stats__privacy" aria-labelledby="stats-history">
      <h2 id="stats-history">{{ t("stats.clear.heading") }}</h2>
      <p class="jl-stats__intro">{{ t("stats.clear.hint") }}</p>
      <UiConfirmButton
        :label="t('stats.clear.button')"
        :confirm-label="t('stats.clear.confirm')"
        :prompt="t('stats.clear.prompt')"
        :busy="store.clearing"
        @confirm="clearHistory"
      />
    </section>
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

.jl-stats__intro {
  margin: 0;
  color: var(--jl-color-text-muted);
}

.jl-stats__skeleton {
  display: grid;
  gap: var(--jl-space-3);
}

.jl-stats__privacy {
  display: grid;
  justify-items: start;
  gap: var(--jl-space-3);
  padding: var(--jl-space-6);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-surface);
}

.jl-stats__privacy h2 {
  margin: 0;
  font-size: var(--jl-font-size-lg);
}
</style>
