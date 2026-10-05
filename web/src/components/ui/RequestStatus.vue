<script setup lang="ts" generic="T">
import { useI18n } from "vue-i18n";
import type { RequestState } from "@/api/requestState";
import UiErrorState from "./UiErrorState.vue";

// Renders the loading / error / empty branches of a request uniformly; the
// default slot receives data only in the success state. The optional
// "loading" slot replaces the text indicator with a skeleton of the content.
defineProps<{ state: RequestState<T> }>();
defineEmits<{ retry: [] }>();
defineSlots<{
  default?(props: { data: T }): unknown;
  empty?(): unknown;
  loading?(): unknown;
}>();
const { t } = useI18n();
</script>

<template>
  <div class="jl-request" :aria-busy="state.status === 'loading'">
    <template v-if="state.status === 'loading'">
      <p role="status" :class="{ 'jl-visually-hidden': $slots.loading }">{{ t("common.loading") }}</p>
      <slot name="loading" />
    </template>
    <UiErrorState v-else-if="state.status === 'error'" :error="state.error" @retry="$emit('retry')" />
    <slot v-else-if="state.status === 'empty'" name="empty" />
    <slot v-else-if="state.status === 'success'" :data="state.data" />
  </div>
</template>
