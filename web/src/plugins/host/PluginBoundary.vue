<script setup lang="ts">
import { pluginContextKey, type PluginContext } from "@jelee/plugin-sdk";
import { onErrorCaptured, provide, shallowRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import UiButton from "@/components/ui/UiButton.vue";
import { usePluginStore } from "./store";

// Error boundary around everything one plugin renders (G32.3). Errors from
// its components (setup, render, watchers, event handlers, lazy loading)
// stop here: the area shows a notice instead and the rest of the page keeps
// working. The plugin's context is provided to its components for usePlugin().
const props = defineProps<{ context: PluginContext }>();
const { t, locale } = useI18n();
const store = usePluginStore();
const failed = shallowRef(false);
provide(pluginContextKey, props.context);

watch(
  () => props.context,
  () => {
    failed.value = false;
  },
);

onErrorCaptured((error: unknown) => {
  failed.value = true;
  store.reportFailure(props.context.id, error);
  return false;
});

function pluginName(): string {
  const name = props.context.manifest.name;
  return locale.value in name ? name[locale.value as keyof typeof name] : name["en-US"];
}

function retry() {
  failed.value = false;
}
</script>

<template>
  <div v-if="failed" class="jl-plugin-fallback" role="status" data-testid="plugin-fallback">
    <p>{{ t("plugins.boundary.failed", { name: pluginName() }) }}</p>
    <UiButton variant="secondary" @click="retry">{{ t("plugins.boundary.retry") }}</UiButton>
  </div>
  <slot v-else />
</template>

<style scoped>
.jl-plugin-fallback {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-3);
  padding: var(--jl-space-3);
  border: 1px dashed var(--jl-color-border);
  border-radius: var(--jl-radius-md);
  color: var(--jl-color-text-muted);
}

.jl-plugin-fallback p {
  margin: 0;
}
</style>
