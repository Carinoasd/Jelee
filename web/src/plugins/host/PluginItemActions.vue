<script setup lang="ts">
import type { PluginItem } from "@jelee/plugin-sdk";
import { computed, shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import UiButton from "@/components/ui/UiButton.vue";
import { useToastStore } from "@/stores/toasts";
import { usePluginStore, type Contribution } from "./store";

// item.action: buttons in the item page's action bar. A throwing or
// rejecting action is reported against its plugin and shown as an error
// toast; the page is unaffected.
const props = defineProps<{ item: PluginItem }>();
const { t } = useI18n();
const store = usePluginStore();
const toasts = useToastStore();
const busy = shallowRef<string | null>(null);

function applies(contribution: Contribution<"item.action">): boolean {
  try {
    return contribution.value.when?.(props.item) ?? true;
  } catch (error: unknown) {
    store.reportFailure(contribution.pluginId, error);
    return false;
  }
}

const actions = computed(() => store.contributions("item.action").filter(applies));

async function run(contribution: Contribution<"item.action">) {
  busy.value = contribution.key;
  try {
    await contribution.value.run(props.item, contribution.context.ui);
  } catch (error: unknown) {
    store.reportFailure(contribution.pluginId, error);
    toasts.push("plugins.actionFailed", "danger", { name: contribution.context.t(contribution.value.label) });
  } finally {
    busy.value = null;
  }
}
</script>

<template>
  <div v-if="actions.length > 0" class="jl-plugin-actions" role="group" :aria-label="t('plugins.itemActions')">
    <UiButton v-for="action in actions" :key="action.key" variant="secondary" :busy="busy === action.key" @click="run(action)">
      {{ action.context.t(action.value.label) }}
    </UiButton>
  </div>
</template>

<style scoped>
.jl-plugin-actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
  margin: 0 0 var(--jl-space-4);
}
</style>
