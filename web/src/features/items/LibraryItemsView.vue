<script setup lang="ts">
import { computed, useId, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import PluginOutlet from "@/plugins/host/PluginOutlet.vue";
import { useLibrariesStore } from "@/stores/libraries";
import { useLibraryItemsStore } from "@/stores/libraryItems";
import { usePreferencesStore } from "@/stores/preferences";
import type { LibrarySort } from "./api";
import ItemResults from "./ItemResults.vue";
import { parseItemView, parseLibrarySort, sortLabelKey, type ItemView } from "./labels";

const props = defineProps<{ libraryId: string }>();
const { t } = useI18n();
const route = useRoute();
const router = useRouter();
const libraries = useLibrariesStore();
const store = useLibraryItemsStore();
const preferences = usePreferencesStore();
const skeletons = 12;
const sortId = useId();

// ?view= wins; without it the user's default view applies.
const view = computed(() => parseItemView(route.query.view, preferences.display.view));
const sort = computed(() => parseLibrarySort(route.query.sort));
const sorts = (Object.keys(sortLabelKey) as LibrarySort[]).map((value) => ({ value, key: sortLabelKey[value] }));
const libraryName = computed(() => libraries.find(props.libraryId)?.name ?? t("libraries.library"));
const views: readonly { value: ItemView; key: string }[] = [
  { value: "poster", key: "items.view.poster" },
  { value: "list", key: "items.view.list" },
];

// The view lives in the URL so it survives reloads and deep links.
function setView(next: ItemView) {
  if (next !== view.value) {
    void router.replace({ query: { ...route.query, view: next === preferences.display.view ? undefined : next } });
  }
}

// Scrolling near the end loads the next page; the button stays for
// browsers without IntersectionObserver and after a failed page.
function loadMore() {
  if (store.items.length < store.total && store.moreState.status !== "loading") {
    void store.loadMore();
  }
}

// The order lives in the URL as well; the server sorts, so changing it
// reloads the first page.
function setSort(event: Event) {
  const next = parseLibrarySort((event.target as HTMLSelectElement).value);
  if (next !== sort.value) {
    void router.replace({ query: { ...route.query, sort: next === "name" ? undefined : next } });
  }
}

watch(
  [() => props.libraryId, sort],
  ([id, order]) => {
    void store.open(id, order);
    void libraries.ensureLoaded();
  },
  { immediate: true },
);
</script>

<template>
  <section aria-labelledby="library-title">
    <nav class="jl-crumbs" :aria-label="t('common.breadcrumbs')">
      <RouterLink :to="{ name: 'libraries' }">{{ t("libraries.title") }}</RouterLink>
    </nav>
    <div class="jl-toolbar">
      <h1 id="library-title" tabindex="-1" class="jl-toolbar__title">{{ libraryName }}</h1>
      <div class="jl-toolbar__controls">
        <span class="jl-sort">
          <label :for="sortId">{{ t("items.sort.label") }}</label>
          <select :id="sortId" class="jl-sort__select" :value="sort" @change="setSort">
            <option v-for="option in sorts" :key="option.value" :value="option.value">{{ t(option.key) }}</option>
          </select>
        </span>
        <div class="jl-toolbar__views" role="group" :aria-label="t('items.view.label')">
          <UiButton
            v-for="option in views"
            :key="option.value"
            variant="secondary"
            :pressed="view === option.value"
            @click="setView(option.value)"
          >
            {{ t(option.key) }}
          </UiButton>
        </div>
        <PluginOutlet hook="library.toolbar" :component-props="{ libraryId }" />
      </div>
    </div>

    <RequestStatus :state="store.state" @retry="store.reload">
      <template #loading>
        <ul v-if="view === 'poster'" class="jl-wall" aria-hidden="true">
          <li v-for="n in skeletons" :key="n">
            <UiSkeleton shape="poster" />
            <UiSkeleton width="80%" />
          </li>
        </ul>
        <div v-else aria-hidden="true">
          <UiSkeleton v-for="n in skeletons" :key="n" shape="title" />
        </div>
      </template>
      <template #default>
        <p class="jl-count" aria-live="polite">{{ t("items.count", { count: store.items.length, total: store.total }) }}</p>
        <ItemResults :items="store.items" :view="view" :label="libraryName" @end="loadMore" />
        <RequestStatus v-if="store.moreState.status !== 'idle'" :state="store.moreState" @retry="store.loadMore">
          <template #default />
        </RequestStatus>
        <UiButton
          v-if="store.items.length < store.total && store.moreState.status !== 'loading'"
          class="jl-more"
          variant="secondary"
          @click="store.loadMore"
        >
          {{ t("common.loadMore") }}
        </UiButton>
      </template>
      <template #empty>
        <UiEmptyState :title="t('items.empty')">
          <p>{{ t("items.emptyHint") }}</p>
          <template #action>
            <RouterLink :to="{ name: 'libraries' }">{{ t("libraries.back") }}</RouterLink>
          </template>
        </UiEmptyState>
      </template>
    </RequestStatus>
  </section>
</template>

<style scoped>
.jl-crumbs {
  font-size: var(--jl-font-size-sm);
}

.jl-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--jl-space-4);
  margin-bottom: var(--jl-space-4);
}

.jl-toolbar__title {
  margin: var(--jl-space-2) 0;
  overflow-wrap: anywhere;
}

.jl-toolbar__controls {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-4);
}

.jl-sort {
  display: flex;
  align-items: center;
  gap: var(--jl-space-2);
}

.jl-sort__select {
  min-height: var(--jl-touch-target);
  padding: 0 var(--jl-space-2);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-sm);
  background: var(--jl-color-surface);
  color: var(--jl-color-text);
  font: inherit;
}

.jl-toolbar__views {
  display: flex;
  gap: var(--jl-space-2);
}

.jl-count {
  margin: 0 0 var(--jl-space-4);
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-wall {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(var(--jl-poster-min-width), 1fr));
  gap: var(--jl-space-6) var(--jl-space-4);
  margin: 0 0 var(--jl-space-6);
  padding: 0;
  list-style: none;
}

.jl-more {
  margin-top: var(--jl-space-2);
}
</style>
