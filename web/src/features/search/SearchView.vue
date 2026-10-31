<script setup lang="ts">
import { computed, useId, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import ItemResults from "@/features/items/ItemResults.vue";
import { kindLabelKey } from "@/features/items/labels";
import { useLibrariesStore } from "@/stores/libraries";
import { usePreferencesStore } from "@/stores/preferences";
import { useSearchStore } from "@/stores/search";
import { normalizeQuery, parseSearchKind, parseSearchLibrary, searchKinds, type SearchFilters, type SearchKind } from "./api";

// Search page (G34.3). The query, kind and library live in the URL. Without
// a kind the results are grouped by kind, each group showing its first
// results under a heading that links to the whole kind with its total;
// narrowed to one kind, every result is listed (virtualized, next pages load
// while scrolling). The header search box updates the query as the user
// types (debounced) and ArrowDown moves from it into the results.
const { t } = useI18n();
const route = useRoute();
const router = useRouter();
const store = useSearchStore();
const libraries = useLibrariesStore();
const preferences = usePreferencesStore();
const libraryId = useId();

const filters = computed<SearchFilters>(() => ({
  q: normalizeQuery(route.query.q),
  kind: parseSearchKind(route.query.type),
  libraryId: parseSearchLibrary(route.query.library),
}));
const kindOptions = computed(() => [
  { value: null, label: t("search.kindAll") },
  ...searchKinds.map((value) => ({ value, label: t(kindLabelKey[value]) })),
]);
const resultCount = computed(() => store.groups.reduce((sum, group) => sum + group.total, 0));

function setKind(next: SearchKind | null) {
  if (next !== filters.value.kind) {
    void router.replace({ query: { ...route.query, type: next ?? undefined } });
  }
}

function setLibrary(event: Event) {
  const next = parseSearchLibrary((event.target as HTMLSelectElement).value);
  void router.replace({ query: { ...route.query, library: next ?? undefined } });
}

function kindLink(kind: SearchKind) {
  return { name: "search", query: { ...route.query, type: kind } };
}

function loadMore() {
  void store.loadMore();
}

watch(
  filters,
  (next) => {
    void store.search(next);
  },
  { immediate: true },
);
void libraries.ensureAll();
</script>

<template>
  <section aria-labelledby="search-title">
    <h1 id="search-title" tabindex="-1" class="jl-search__title">
      {{ filters.q ? t("search.titleFor", { query: filters.q }) : t("search.title") }}
    </h1>
    <div class="jl-search__filters">
      <div class="jl-search__kinds" role="group" :aria-label="t('search.kind')">
        <UiButton
          v-for="option in kindOptions"
          :key="option.value ?? 'all'"
          variant="secondary"
          :pressed="filters.kind === option.value"
          @click="setKind(option.value)"
        >
          {{ option.label }}
        </UiButton>
      </div>
      <span class="jl-search__library">
        <label :for="libraryId" class="jl-visually-hidden">{{ t("search.library") }}</label>
        <select :id="libraryId" class="jl-search__select" :value="filters.libraryId ?? ''" @change="setLibrary">
          <option value="">{{ t("search.libraryAll") }}</option>
          <option v-for="library in libraries.libraries" :key="library.id" :value="library.id">{{ library.name }}</option>
        </select>
      </span>
    </div>

    <p v-if="!filters.q" class="jl-search__hint">{{ t("search.hint") }}</p>
    <RequestStatus v-else :state="store.state" @retry="store.retry">
      <template #loading>
        <ul class="jl-search__skeleton" aria-hidden="true">
          <li v-for="n in 6" :key="n">
            <UiSkeleton shape="poster" />
            <UiSkeleton width="80%" />
          </li>
        </ul>
      </template>
      <template #default>
        <template v-if="filters.kind === null">
          <p class="jl-search__count" role="status">{{ t("search.found", { total: resultCount }) }}</p>
          <section v-for="group in store.groups" :key="group.kind" class="jl-search__group" :aria-labelledby="`search-group-${group.kind}`">
            <h2 :id="`search-group-${group.kind}`" class="jl-search__group-title">
              <RouterLink :to="kindLink(group.kind)">{{ t(kindLabelKey[group.kind]) }}</RouterLink>
              <span class="jl-search__total">{{ group.total }}</span>
            </h2>
            <ItemResults :items="group.items" :view="preferences.display.view" :label="t(kindLabelKey[group.kind])" hide-kind />
          </section>
        </template>
        <template v-else>
          <p class="jl-search__count" role="status">{{ t("search.count", { count: store.items.length, total: store.total }) }}</p>
          <ItemResults :items="store.items" :view="preferences.display.view" :label="t(kindLabelKey[filters.kind])" hide-kind @end="loadMore" />
          <RequestStatus v-if="store.moreState.status !== 'idle'" :state="store.moreState" @retry="store.loadMore">
            <template #default />
          </RequestStatus>
          <UiButton
            v-if="store.items.length < store.total && store.moreState.status !== 'loading'"
            class="jl-search__more"
            variant="secondary"
            @click="loadMore"
          >
            {{ t("common.loadMore") }}
          </UiButton>
        </template>
      </template>
      <template #empty>
        <UiEmptyState :title="t('search.empty', { query: filters.q })">
          <p>{{ t("search.emptyHint") }}</p>
        </UiEmptyState>
      </template>
    </RequestStatus>
  </section>
</template>

<style scoped>
.jl-search__title {
  margin: var(--jl-space-2) 0 var(--jl-space-4);
  overflow-wrap: anywhere;
}

.jl-search__filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--jl-space-4);
  margin-bottom: var(--jl-space-6);
}

.jl-search__kinds {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
}

.jl-search__select {
  min-height: var(--jl-touch-target);
  padding: 0 var(--jl-space-2);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-sm);
  background: var(--jl-color-surface);
  color: var(--jl-color-text);
  font: inherit;
}

.jl-search__hint,
.jl-search__count {
  margin: 0 0 var(--jl-space-4);
  color: var(--jl-color-text-muted);
}

.jl-search__group + .jl-search__group {
  margin-top: var(--jl-space-8);
}

.jl-search__group-title {
  display: flex;
  align-items: baseline;
  gap: var(--jl-space-3);
  margin: 0 0 var(--jl-space-4);
  font-size: var(--jl-font-size-lg);
}

.jl-search__total {
  padding: 0 var(--jl-space-2);
  border-radius: var(--jl-radius-pill);
  background: var(--jl-color-badge-bg);
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
  font-weight: 400;
}

.jl-search__skeleton {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(var(--jl-poster-min-width), 1fr));
  gap: var(--jl-space-6) var(--jl-space-4);
  margin: 0;
  padding: 0;
  list-style: none;
}
</style>
