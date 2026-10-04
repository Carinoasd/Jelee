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
import type { LibrarySort } from "./api";
import ItemPoster from "./ItemPoster.vue";
import { kindLabelKey, parseItemView, parseLibrarySort, sortLabelKey, type ItemView } from "./labels";

const props = defineProps<{ libraryId: string }>();
const { t } = useI18n();
const route = useRoute();
const router = useRouter();
const libraries = useLibrariesStore();
const store = useLibraryItemsStore();
const skeletons = 12;
const sortId = useId();

const view = computed(() => parseItemView(route.query.view));
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
    void router.replace({ query: { ...route.query, view: next === "poster" ? undefined : next } });
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
        <ul v-if="view === 'poster'" class="jl-wall">
          <li v-for="item in store.items" :key="item.id">
            <RouterLink class="jl-card" :to="{ name: 'item', params: { itemId: item.id } }">
              <ItemPoster :item-id="item.id" :title="item.title" />
              <span class="jl-card__title">{{ item.title }}</span>
              <span class="jl-card__kind">{{ t(kindLabelKey[item.kind]) }}</span>
            </RouterLink>
          </li>
        </ul>
        <table v-else class="jl-table">
          <caption class="jl-visually-hidden">{{ t("items.listCaption", { name: libraryName }) }}</caption>
          <thead>
            <tr>
              <th scope="col" class="jl-table__thumb"><span class="jl-visually-hidden">{{ t("items.column.poster") }}</span></th>
              <th scope="col">{{ t("items.column.title") }}</th>
              <th scope="col">{{ t("items.column.kind") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in store.items" :key="item.id">
              <td class="jl-table__thumb"><ItemPoster :item-id="item.id" :title="item.title" :width="96" /></td>
              <td>
                <RouterLink :to="{ name: 'item', params: { itemId: item.id } }">{{ item.title }}</RouterLink>
              </td>
              <td>{{ t(kindLabelKey[item.kind]) }}</td>
            </tr>
          </tbody>
        </table>
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

.jl-card {
  display: grid;
  gap: var(--jl-space-1);
  color: inherit;
  text-decoration: none;
  border-radius: var(--jl-radius-md);
}

.jl-card :deep(.jl-poster) {
  transition:
    box-shadow var(--jl-motion-duration) var(--jl-motion-easing),
    transform var(--jl-motion-duration) var(--jl-motion-easing);
}

.jl-card:hover :deep(.jl-poster),
.jl-card:focus-visible :deep(.jl-poster) {
  box-shadow: var(--jl-shadow-2);
  transform: translateY(-2px);
}

.jl-card__title {
  font-weight: 600;
  overflow-wrap: anywhere;
}

.jl-card__kind {
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-table {
  width: 100%;
  margin-bottom: var(--jl-space-6);
  border-collapse: collapse;
}

.jl-table th,
.jl-table td {
  padding: var(--jl-space-2) var(--jl-space-3);
  border-bottom: 1px solid var(--jl-color-border);
  text-align: start;
  vertical-align: middle;
}

.jl-table td {
  overflow-wrap: anywhere;
}

.jl-table__thumb {
  width: 64px;
}

.jl-table__thumb :deep(.jl-poster) {
  width: 48px;
}

.jl-more {
  margin-top: var(--jl-space-2);
}
</style>
