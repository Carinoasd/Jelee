<script setup lang="ts">
import { isHookName, SDK_VERSION, type LocalizedText } from "@jelee/plugin-sdk";
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiReorderList from "@/components/ui/UiReorderList.vue";
import { jeleeVersion, usePluginStore, type PluginView } from "@/plugins/host/store";
import { useToastStore } from "@/stores/toasts";
import { issueKey, permissionKey, problemKey, statusKey, statusTone } from "./pluginLabels";

// Plugin administration (G32.4): enable, disable and order take effect at
// once; every plugin's manifest, permissions, hooks and problems are shown,
// rejected ones with the reasons.
const { t, locale } = useI18n();
const store = usePluginStore();
const toasts = useToastStore();

function text(value: LocalizedText): string {
  return locale.value in value ? value[locale.value as keyof LocalizedText] : value["en-US"];
}

function nameOf(plugin: PluginView): string {
  return plugin.manifest === null ? plugin.key : text(plugin.manifest.name);
}

const orderItems = computed(() => store.plugins.map((plugin) => ({ id: plugin.key, label: nameOf(plugin) })));

function move(from: number, to: number) {
  const keys = store.order.slice();
  const [key] = keys.splice(from, 1);
  if (key !== undefined) {
    keys.splice(to, 0, key);
    store.setOrder(keys);
  }
}

function toggle(plugin: PluginView) {
  store.setEnabled(plugin.key, !plugin.enabled);
  toasts.push(plugin.enabled ? "plugins.disabledToast" : "plugins.enabledToast", "success", { name: nameOf(plugin) });
}

function clearSettings(plugin: PluginView) {
  store.clearSettings(plugin.key);
  toasts.push("plugins.settingsCleared", "success", { name: nameOf(plugin) });
}

function countOf(plugin: PluginView, hook: string): number {
  return isHookName(hook) ? (plugin.contributionCounts[hook] ?? 0) : 0;
}

function dependencies(plugin: PluginView): string {
  return Object.entries(plugin.manifest?.dependencies ?? {})
    .map(([id, range]) => id + " " + range)
    .join(", ");
}
</script>

<template>
  <section class="jl-plugins" aria-labelledby="plugins-title">
    <h1 id="plugins-title" tabindex="-1">{{ t("plugins.title") }}</h1>
    <p class="jl-plugins__muted">{{ t("plugins.intro", { sdk: SDK_VERSION, version: jeleeVersion }) }}</p>
    <UiAlert tone="info">{{ t("plugins.storageNote") }}</UiAlert>

    <UiEmptyState v-if="store.plugins.length === 0" :title="t('plugins.empty')" />
    <template v-else>
      <section class="jl-plugins__order" aria-labelledby="plugins-order">
        <h2 id="plugins-order">{{ t("plugins.order") }}</h2>
        <p class="jl-plugins__muted">{{ t("plugins.orderHint") }}</p>
        <UiReorderList :items="orderItems" :label="t('plugins.order')" @move="move" />
      </section>

      <article v-for="plugin in store.plugins" :key="plugin.key" class="jl-plugin" :aria-labelledby="'plugin-' + plugin.key" :data-plugin="plugin.key">
        <header class="jl-plugin__head">
          <h2 :id="'plugin-' + plugin.key">{{ nameOf(plugin) }}</h2>
          <UiBadge :tone="statusTone[plugin.status]">{{ t(statusKey[plugin.status]) }}</UiBadge>
          <UiBadge v-if="plugin.official">{{ t("plugins.official") }}</UiBadge>
        </header>
        <p v-if="plugin.manifest">{{ text(plugin.manifest.description) }}</p>

        <UiAlert v-if="plugin.issues.length > 0" tone="danger">
          <p>{{ t("plugins.rejected") }}</p>
          <ul>
            <li v-for="(issue, index) in plugin.issues" :key="index">{{ t(issueKey[issue.code], issue.params) }}</li>
          </ul>
        </UiAlert>
        <UiAlert v-if="plugin.problems.length > 0" tone="danger">
          <ul>
            <li v-for="(problem, index) in plugin.problems" :key="index">{{ t(problemKey[problem.code], problem.params) }}</li>
          </ul>
        </UiAlert>
        <UiAlert v-for="warning in plugin.warnings" :key="warning.hook" tone="info">
          {{ t("plugins.deprecatedHook", { hook: warning.hook, since: warning.deprecation.since, removed: warning.deprecation.removedIn, replacement: warning.deprecation.replacement ?? "-" }) }}
        </UiAlert>
        <UiAlert v-if="plugin.failures > 0" tone="danger">
          {{ t("plugins.failures", { count: plugin.failures }) }}
          <code class="jl-plugin__code">{{ plugin.lastFailure }}</code>
        </UiAlert>

        <dl v-if="plugin.manifest" class="jl-plugin__facts">
          <dt>{{ t("plugins.fields.id") }}</dt>
          <dd><code>{{ plugin.manifest.id }}</code></dd>
          <dt>{{ t("plugins.fields.version") }}</dt>
          <dd>{{ plugin.manifest.version }}</dd>
          <dt>{{ t("plugins.fields.sdkVersion") }}</dt>
          <dd><code>{{ plugin.manifest.sdkVersion }}</code></dd>
          <dt>{{ t("plugins.fields.minJelee") }}</dt>
          <dd>{{ plugin.manifest.minJeleeVersion }}</dd>
          <dt>{{ t("plugins.fields.entry") }}</dt>
          <dd><code>{{ plugin.manifest.entry }}</code></dd>
          <template v-if="plugin.manifest.author">
            <dt>{{ t("plugins.fields.author") }}</dt>
            <dd>{{ plugin.manifest.author }}</dd>
          </template>
          <dt>{{ t("plugins.fields.dependencies") }}</dt>
          <dd>{{ dependencies(plugin) || t("plugins.none") }}</dd>
          <dt>{{ t("plugins.fields.permissions") }}</dt>
          <dd>
            <ul v-if="plugin.manifest.permissions.length > 0" class="jl-plugin__list">
              <li v-for="permission in plugin.manifest.permissions" :key="permission">
                <code>{{ permission }}</code> — {{ t(permissionKey[permission]) }}
              </li>
            </ul>
            <span v-else>{{ t("plugins.none") }}</span>
          </dd>
          <dt>{{ t("plugins.fields.hooks") }}</dt>
          <dd>
            <ul class="jl-plugin__list">
              <li v-for="hook in plugin.manifest.hooks" :key="hook">
                <code>{{ hook }}</code>
                <span v-if="plugin.status === 'active'"> — {{ t("plugins.contributions", { count: countOf(plugin, hook) }) }}</span>
              </li>
            </ul>
          </dd>
        </dl>

        <div v-if="plugin.manifest" class="jl-plugin__actions">
          <UiButton :variant="plugin.enabled ? 'secondary' : 'primary'" :pressed="plugin.enabled" @click="toggle(plugin)">
            {{ t("plugins.enabled", { name: nameOf(plugin) }) }}
          </UiButton>
          <UiButton v-if="plugin.status === 'failed' || plugin.failures > 0" variant="secondary" @click="store.restart(plugin.key)">
            {{ t("plugins.restart") }}
          </UiButton>
          <UiConfirmButton
            v-if="plugin.manifest.permissions.includes('settings.storage')"
            :label="t('plugins.clearSettings')"
            :confirm-label="t('plugins.clearSettingsConfirm')"
            :prompt="t('plugins.clearSettingsPrompt', { name: nameOf(plugin) })"
            @confirm="clearSettings(plugin)"
          />
        </div>
      </article>
    </template>
  </section>
</template>

<style scoped>
.jl-plugins {
  display: grid;
  gap: var(--jl-space-6);
}

.jl-plugins h1,
.jl-plugins h2 {
  margin: 0;
}

.jl-plugins h2 {
  font-size: var(--jl-font-size-lg);
}

.jl-plugins__muted {
  margin: 0;
  color: var(--jl-color-text-muted);
}

.jl-plugins__order {
  display: grid;
  gap: var(--jl-space-2);
  max-width: 560px;
}

.jl-plugin {
  display: grid;
  gap: var(--jl-space-3);
  padding: var(--jl-space-6);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-surface);
}

.jl-plugin p {
  margin: 0;
}

.jl-plugin__head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-2);
}

.jl-plugin__facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: var(--jl-space-1) var(--jl-space-4);
  margin: 0;
}

.jl-plugin__facts dt {
  font-weight: 600;
}

.jl-plugin__facts dd {
  margin: 0;
  overflow-wrap: anywhere;
}

.jl-plugin__list {
  margin: 0;
  padding-inline-start: var(--jl-space-4);
}

.jl-plugin__code {
  display: block;
  margin-top: var(--jl-space-1);
  overflow-wrap: anywhere;
}

.jl-plugin__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
}
</style>
