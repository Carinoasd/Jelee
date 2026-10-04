<script setup lang="ts">
import { useI18n } from "vue-i18n";
import type { ApiError } from "@/api/errors";
import { errorMessageKey } from "@/features/errors/messages";
import UiAlert from "./UiAlert.vue";
import UiButton from "./UiButton.vue";

// Localized error with the server's trace ID (for support) and an optional
// retry; the message comes from the error code, never from server text.
withDefaults(defineProps<{ error: ApiError; retryable?: boolean }>(), { retryable: true });
defineEmits<{ retry: [] }>();
const { t } = useI18n();
</script>

<template>
  <UiAlert tone="danger" class="jl-error">
    <p class="jl-error__message">{{ t(errorMessageKey(error)) }}</p>
    <p v-if="error.traceId" class="jl-error__trace">{{ t("errors.traceId", { id: error.traceId }) }}</p>
    <UiButton v-if="retryable" variant="secondary" @click="$emit('retry')">{{ t("common.retry") }}</UiButton>
  </UiAlert>
</template>

<style scoped>
.jl-error {
  display: grid;
  justify-items: start;
  gap: var(--jl-space-2);
}

.jl-error__message,
.jl-error__trace {
  margin: 0;
}

.jl-error__trace {
  font-size: var(--jl-font-size-sm);
  font-family: ui-monospace, monospace;
}
</style>
