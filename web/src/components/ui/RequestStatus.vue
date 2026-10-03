<script setup lang="ts" generic="T">
import { useI18n } from "vue-i18n";
import type { RequestState } from "@/api/requestState";
import { errorMessageKey } from "@/features/errors/messages";
import UiAlert from "./UiAlert.vue";
import UiButton from "./UiButton.vue";

// Renders the loading / error / empty branches of a request uniformly; the
// default slot receives data only in the success state.
defineProps<{ state: RequestState<T> }>();
defineEmits<{ retry: [] }>();
defineSlots<{
  default?(props: { data: T }): unknown;
  empty?(): unknown;
}>();
const { t } = useI18n();
</script>

<template>
  <div class="jl-request" :aria-busy="state.status === 'loading'">
    <p v-if="state.status === 'loading'" role="status">{{ t("common.loading") }}</p>
    <UiAlert v-else-if="state.status === 'error'" tone="danger">
      <p>{{ t(errorMessageKey(state.error)) }}</p>
      <p v-if="state.error.traceId" class="jl-request__trace">
        {{ t("errors.traceId", { id: state.error.traceId }) }}
      </p>
      <UiButton variant="secondary" @click="$emit('retry')">{{ t("common.retry") }}</UiButton>
    </UiAlert>
    <slot v-else-if="state.status === 'empty'" name="empty" />
    <slot v-else-if="state.status === 'success'" :data="state.data" />
  </div>
</template>

<style scoped>
.jl-request__trace {
  font-size: var(--jl-font-size-sm);
}
</style>
