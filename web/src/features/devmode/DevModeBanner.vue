<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useApi } from "@/api";
import { formatDateTime } from "@/i18n/format";
import { inactiveDevMode, readDevMode, type DevModeState } from "./api";

// G45.3: while developer mode is active every page shows this banner at the
// very top. It rechecks once a minute so expiry or a disable removes it.
const props = withDefaults(defineProps<{ refreshMs?: number }>(), { refreshMs: 60_000 });

const { t, locale } = useI18n();
const { client } = useApi();
const state = ref<DevModeState>(inactiveDevMode);
let timer: ReturnType<typeof setInterval> | undefined;

async function refresh() {
  state.value = await readDevMode(client);
}

onMounted(() => {
  void refresh();
  timer = setInterval(() => void refresh(), props.refreshMs);
});

onBeforeUnmount(() => {
  if (timer !== undefined) {
    clearInterval(timer);
  }
});

const expires = computed(() => formatDateTime(state.value.expiresAt, locale.value));
</script>

<template>
  <div v-if="state.active" class="jl-devmode-banner" role="alert" data-testid="devmode-banner">
    <strong>{{ t("devmode.title") }}</strong>
    <span>{{ t("devmode.warning") }}</span>
    <span v-if="expires">{{ t("devmode.expires", { time: expires }) }}</span>
  </div>
</template>

<style scoped>
.jl-devmode-banner {
  position: sticky;
  top: 0;
  z-index: calc(var(--jl-z-header) + 1);
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2) var(--jl-space-3);
  padding: var(--jl-space-2) var(--jl-space-4);
  border-bottom: 2px solid var(--jl-color-danger);
  background: var(--jl-color-danger-bg);
  color: var(--jl-color-danger);
}
</style>
