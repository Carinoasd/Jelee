<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { pluginComponent } from "./asyncComponent";
import PluginBoundary from "./PluginBoundary.vue";
import { usePluginStore } from "./store";

// A page registered with route.register. The heading comes from the host
// (focus moves to it after navigation); the plugin renders the body inside
// its boundary. Disabling the plugin removes the route.
const props = defineProps<{ contributionKey: string }>();
const { t } = useI18n();
const store = usePluginStore();
const route = computed(() => store.contributions("route.register").find((entry) => entry.key === props.contributionKey) ?? null);
const component = computed(() => (route.value === null ? null : pluginComponent(route.value.value.component)));
</script>

<template>
  <section aria-labelledby="plugin-page-title">
    <h1 id="plugin-page-title" tabindex="-1">{{ route ? route.context.t(route.value.title) : t("plugins.pageUnavailable") }}</h1>
    <PluginBoundary v-if="route && component" :context="route.context">
      <component :is="component" />
    </PluginBoundary>
  </section>
</template>
