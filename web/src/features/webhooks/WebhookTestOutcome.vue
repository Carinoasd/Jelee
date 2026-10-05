<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { WebhookTestResult } from "./api";
import { outcomeKeys } from "./labels";

// Renders nothing until a test has run.
const props = defineProps<{ result: WebhookTestResult | undefined }>();
const { t } = useI18n();
const text = computed(() => {
  const result = props.result;
  if (result === undefined) {
    return "";
  }
  const outcome = t(outcomeKeys[result.outcome]);
  return result.statusCode === undefined
    ? t("webhooks.test.result", { outcome, ms: result.durationMs })
    : t("webhooks.test.resultStatus", { outcome, status: result.statusCode, ms: result.durationMs });
});
</script>

<template>
  <p v-if="result" class="jl-test" :class="{ 'jl-test--ok': result.outcome === 'delivered' }">{{ text }}</p>
</template>

<style scoped>
.jl-test {
  margin: 0;
  color: var(--jl-color-danger);
  font-size: var(--jl-font-size-sm);
}

.jl-test--ok {
  color: var(--jl-color-success);
}
</style>
