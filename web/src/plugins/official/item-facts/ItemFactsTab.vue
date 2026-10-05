<script setup lang="ts">
import { PluginApiError, usePlugin, type PluginItem, type PluginSourceSummary } from "@jelee/plugin-sdk";
import { computed, shallowRef } from "vue";
import { completeness, externalLinks, formatSize } from "./links";

const props = defineProps<{ item: PluginItem }>();
const plugin = usePlugin();
const { t } = plugin;

const checklist = computed(() => completeness(props.item));
const done = computed(() => checklist.value.filter((entry) => entry.present).length);
const showLinks = computed(() => plugin.settings.get<boolean>("externalLinks", false));
const links = computed(() => externalLinks(props.item));

// File summary loads on request through the restricted API (catalog.read).
type FilesState = { status: "idle" | "loading" } | { status: "done"; sources: readonly PluginSourceSummary[] } | { status: "failed"; code: string };
const files = shallowRef<FilesState>({ status: "idle" });

async function loadFiles() {
  files.value = { status: "loading" };
  try {
    files.value = { status: "done", sources: await plugin.api.itemSources(props.item.id) };
  } catch (error: unknown) {
    files.value = { status: "failed", code: error instanceof PluginApiError ? error.code : "unexpected_response" };
  }
}

const summary = computed(() => {
  if (files.value.status !== "done" || files.value.sources.length === 0) {
    return null;
  }
  const sources = files.value.sources;
  const size = sources.reduce((total, source) => total + (source.sizeBytes ?? 0), 0);
  const largest = sources.reduce<PluginSourceSummary | null>(
    (best, source) => ((source.height ?? 0) > (best?.height ?? 0) ? source : best),
    null,
  );
  return {
    text: t("files.summary", { count: sources.length, size: formatSize(size, plugin.locale()) }),
    resolution: largest?.width && largest.height ? `${largest.width}×${largest.height}` : "",
  };
});
</script>

<template>
  <div class="jl-facts">
    <section aria-labelledby="facts-completeness">
      <h3 id="facts-completeness" class="jl-facts__title">{{ t("completeness.title") }}</h3>
      <p>{{ t("completeness.summary", { done, total: checklist.length }) }}</p>
      <ul class="jl-facts__list">
        <li v-for="entry in checklist" :key="entry.key">
          <span>{{ t(entry.key) }}</span>
          <span :class="entry.present ? 'jl-facts__ok' : 'jl-facts__missing'">{{ t(entry.present ? "state.present" : "state.missing") }}</span>
        </li>
      </ul>
    </section>

    <section v-if="showLinks" aria-labelledby="facts-links">
      <h3 id="facts-links" class="jl-facts__title">{{ t("links.title") }}</h3>
      <ul v-if="links.length > 0" class="jl-facts__links">
        <li v-for="link in links" :key="link.href">
          <a :href="link.href" target="_blank" rel="noopener noreferrer">
            {{ link.label }}<span class="jl-facts__sr">{{ t("links.opensNew") }}</span>
          </a>
        </li>
      </ul>
      <p v-else class="jl-facts__muted">{{ t("links.none") }}</p>
    </section>

    <section aria-labelledby="facts-files">
      <h3 id="facts-files" class="jl-facts__title">{{ t("files.title") }}</h3>
      <button v-if="files.status === 'idle'" type="button" class="jl-facts__button" @click="loadFiles">{{ t("files.load") }}</button>
      <p v-else-if="files.status === 'loading'" role="status">{{ t("files.loading") }}</p>
      <p v-else-if="files.status === 'failed'" role="alert">{{ t("files.failed", { code: files.code }) }}</p>
      <template v-else-if="summary">
        <p role="status">{{ summary.text }}</p>
        <p v-if="summary.resolution">{{ t("files.largest", { resolution: summary.resolution }) }}</p>
      </template>
      <p v-else class="jl-facts__muted" role="status">{{ t("files.none") }}</p>
    </section>
  </div>
</template>

<style scoped>
.jl-facts {
  display: grid;
  gap: var(--jl-space-4);
}

.jl-facts__title {
  margin: 0 0 var(--jl-space-2);
  font-size: var(--jl-font-size-md);
}

.jl-facts p {
  margin: 0 0 var(--jl-space-2);
}

.jl-facts__list,
.jl-facts__links {
  display: grid;
  gap: var(--jl-space-1);
  margin: 0;
  padding: 0;
  list-style: none;
}

.jl-facts__list li {
  display: flex;
  justify-content: space-between;
  gap: var(--jl-space-4);
  max-width: 28rem;
}

.jl-facts__ok {
  color: var(--jl-color-success);
}

.jl-facts__missing,
.jl-facts__muted {
  color: var(--jl-color-text-muted);
}

.jl-facts__sr {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
}

.jl-facts__button {
  min-height: var(--jl-touch-target);
  padding: var(--jl-space-2) var(--jl-space-4);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-surface);
  color: var(--jl-color-text);
  font: inherit;
  cursor: pointer;
}
</style>
