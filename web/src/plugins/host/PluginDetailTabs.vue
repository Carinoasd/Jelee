<script setup lang="ts">
import type { PluginItem } from "@jelee/plugin-sdk";
import { computed, nextTick, shallowRef, useId, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import { pluginComponent } from "./asyncComponent";
import PluginBoundary from "./PluginBoundary.vue";
import { usePluginStore } from "./store";

// media.detail.tabs: an ARIA tablist with roving focus (arrow keys, Home,
// End). Only the selected tab's component is mounted, so a plugin's code
// and requests run when its tab is opened.
const props = defineProps<{ item: PluginItem }>();
const { t } = useI18n();
const store = usePluginStore();
const base = useId();
const tabs = computed(() =>
  store.contributions("media.detail.tabs").map((contribution, index) => ({
    key: contribution.key,
    context: contribution.context,
    title: contribution.context.t(contribution.value.title),
    component: pluginComponent(contribution.value.component),
    tabId: `${base}-tab-${index}`,
    panelId: `${base}-panel-${index}`,
  })),
);
const selectedKey = shallowRef<string | null>(null);
const selected = computed(() => tabs.value.find((tab) => tab.key === selectedKey.value) ?? tabs.value[0] ?? null);
const tabButtons = useTemplateRef<HTMLButtonElement[]>("tabButtons");

function select(key: string) {
  selectedKey.value = key;
}

function onKeydown(event: KeyboardEvent, index: number) {
  const count = tabs.value.length;
  const target = { ArrowRight: index + 1, ArrowLeft: index - 1, Home: 0, End: count - 1 }[event.key];
  if (target === undefined) {
    return;
  }
  event.preventDefault();
  const next = tabs.value[(target + count) % count];
  if (next !== undefined) {
    select(next.key);
    void nextTick(() => tabButtons.value?.[(target + count) % count]?.focus());
  }
}
</script>

<template>
  <section v-if="tabs.length > 0" class="jl-plugin-tabs" :aria-label="t('plugins.detailTabs')">
    <div role="tablist" class="jl-plugin-tabs__list" :aria-label="t('plugins.detailTabs')">
      <button
        v-for="(tab, index) in tabs"
        :id="tab.tabId"
        :key="tab.key"
        ref="tabButtons"
        type="button"
        role="tab"
        class="jl-plugin-tabs__tab"
        :aria-selected="tab.key === selected?.key"
        :aria-controls="tab.panelId"
        :tabindex="tab.key === selected?.key ? 0 : -1"
        @click="select(tab.key)"
        @keydown="onKeydown($event, index)"
      >
        {{ tab.title }}
      </button>
    </div>
    <div v-if="selected" :id="selected.panelId" role="tabpanel" class="jl-plugin-tabs__panel" :aria-labelledby="selected.tabId" tabindex="0">
      <PluginBoundary :key="selected.key" :context="selected.context">
        <component :is="selected.component" :item="props.item" />
      </PluginBoundary>
    </div>
  </section>
</template>

<style scoped>
.jl-plugin-tabs {
  margin-top: var(--jl-space-6);
}

.jl-plugin-tabs__list {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-1);
  border-bottom: 1px solid var(--jl-color-border);
}

.jl-plugin-tabs__tab {
  min-height: var(--jl-touch-target);
  padding: 0 var(--jl-space-3);
  border: 0;
  background: none;
  color: var(--jl-color-text);
  font: inherit;
  cursor: pointer;
}

.jl-plugin-tabs__tab[aria-selected="true"] {
  color: var(--jl-color-primary);
  font-weight: 600;
  box-shadow: inset 0 -2px 0 var(--jl-color-primary);
}

.jl-plugin-tabs__panel {
  padding-top: var(--jl-space-4);
}
</style>
