<script setup lang="ts">
import { computed, nextTick, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";
import ClientHitsPanel from "./ClientHitsPanel.vue";
import ClientPolicyPanel from "./ClientPolicyPanel.vue";
import ClientRulesPanel from "./ClientRulesPanel.vue";
import KnownClientsPanel from "./KnownClientsPanel.vue";
import "./clients.css";

// Client control (G47.5) as ARIA tabs: arrow keys, Home and End move between
// tabs and activate them; only the active panel is rendered, so each panel
// loads its data when it is opened. The tab is kept in ?tab= for deep links.
const { t } = useI18n();
const route = useRoute();
const router = useRouter();

const tabs = [
  { id: "policy", key: "clients.tabs.policy" },
  { id: "rules", key: "clients.tabs.rules" },
  { id: "known", key: "clients.tabs.known" },
  { id: "hits", key: "clients.tabs.hits" },
] as const;
type TabId = (typeof tabs)[number]["id"];

const active = computed<TabId>(() => tabs.find((tab) => tab.id === route.query.tab)?.id ?? "policy");
const tabList = useTemplateRef<HTMLElement>("tabList");

async function select(id: TabId, focus = false) {
  if (id !== active.value) {
    await router.replace({ query: { ...route.query, tab: id } });
  }
  if (focus) {
    await nextTick();
    tabList.value?.querySelector<HTMLButtonElement>(`#clients-tab-${id}`)?.focus();
  }
}

function onKeydown(event: KeyboardEvent) {
  const index = tabs.findIndex((tab) => tab.id === active.value);
  let next: number;
  switch (event.key) {
    case "ArrowRight":
      next = (index + 1) % tabs.length;
      break;
    case "ArrowLeft":
      next = (index - 1 + tabs.length) % tabs.length;
      break;
    case "Home":
      next = 0;
      break;
    case "End":
      next = tabs.length - 1;
      break;
    default:
      return;
  }
  event.preventDefault();
  const target = tabs[next];
  if (target !== undefined) {
    void select(target.id, true);
  }
}
</script>

<template>
  <section class="jl-clients" aria-labelledby="clients-title">
    <h1 id="clients-title" tabindex="-1">{{ t("clients.title") }}</h1>
    <p class="jl-clients__intro">{{ t("clients.intro") }}</p>

    <div ref="tabList" class="jl-tabs" role="tablist" :aria-label="t('clients.tabs.label')" @keydown="onKeydown">
      <button
        v-for="tab in tabs"
        :id="`clients-tab-${tab.id}`"
        :key="tab.id"
        type="button"
        role="tab"
        class="jl-tabs__tab"
        :aria-selected="tab.id === active ? 'true' : 'false'"
        :aria-controls="`clients-panel-${tab.id}`"
        :tabindex="tab.id === active ? 0 : -1"
        @click="select(tab.id)"
      >
        {{ t(tab.key) }}
      </button>
    </div>

    <div :id="`clients-panel-${active}`" role="tabpanel" class="jl-clients__panel" :aria-labelledby="`clients-tab-${active}`" tabindex="0">
      <ClientPolicyPanel v-if="active === 'policy'" />
      <ClientRulesPanel v-else-if="active === 'rules'" />
      <KnownClientsPanel v-else-if="active === 'known'" />
      <ClientHitsPanel v-else />
    </div>
  </section>
</template>

<style scoped>
.jl-clients {
  display: grid;
  gap: var(--jl-space-4);
}

.jl-clients h1 {
  margin: 0;
}

.jl-clients__intro {
  margin: 0;
  color: var(--jl-color-text-muted);
}

.jl-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-1);
  border-bottom: 1px solid var(--jl-color-border);
}

.jl-tabs__tab {
  min-height: var(--jl-touch-target);
  padding: 0 var(--jl-space-4);
  border: 0;
  background: transparent;
  color: var(--jl-color-text);
  font: inherit;
  cursor: pointer;
}

.jl-tabs__tab[aria-selected="true"] {
  color: var(--jl-color-primary);
  font-weight: 600;
  box-shadow: inset 0 -2px 0 var(--jl-color-primary);
}

.jl-clients__panel {
  min-width: 0;
}
</style>
