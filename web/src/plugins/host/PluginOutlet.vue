<script setup lang="ts">
import { computed } from "vue";
import { pluginComponent } from "./asyncComponent";
import PluginBoundary from "./PluginBoundary.vue";
import { usePluginStore } from "./store";

// Renders the component contributions of one hook, each inside its own
// boundary. With `headings`, each contribution gets a section and an h2
// with its translated title (metadata.panel, settings.section).
const props = defineProps<{
  hook: "metadata.panel" | "settings.section" | "library.toolbar";
  componentProps?: Readonly<Record<string, unknown>>;
  headings?: boolean;
}>();
const store = usePluginStore();
const entries = computed(() =>
  store.contributions(props.hook).map((contribution) => ({
    key: contribution.key,
    context: contribution.context,
    title: "title" in contribution.value ? contribution.context.t(contribution.value.title) : "",
    component: pluginComponent(contribution.value.component),
  })),
);
</script>

<template>
  <template v-for="entry in entries" :key="entry.key">
    <section v-if="headings" class="jl-plugin-section" :data-plugin="entry.context.id">
      <h2 class="jl-plugin-section__title">{{ entry.title }}</h2>
      <PluginBoundary :context="entry.context">
        <component :is="entry.component" v-bind="componentProps" />
      </PluginBoundary>
    </section>
    <PluginBoundary v-else :context="entry.context">
      <component :is="entry.component" v-bind="componentProps" />
    </PluginBoundary>
  </template>
</template>

<style scoped>
.jl-plugin-section__title {
  margin: 0 0 var(--jl-space-2);
  font-size: var(--jl-font-size-lg);
}
</style>
