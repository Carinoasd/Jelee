<script setup lang="ts">
import { computed, useId, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import ItemPoster from "@/features/items/ItemPoster.vue";
import { kindLabelKey } from "@/features/items/labels";
import { useSearchStore } from "@/stores/search";
import { normalizeQuery, parseSearchKind, searchKinds, type SearchKind } from "./api";

const { t } = useI18n();
const route = useRoute();
const router = useRouter();
const store = useSearchStore();
const kindId = useId();

const query = computed(() => normalizeQuery(route.query.q));
const kind = computed(() => parseSearchKind(route.query.type));
const kindOptions = searchKinds.map((value) => ({ value, key: kindLabelKey[value] }));

function setKind(event: Event) {
  const next = parseSearchKind((event.target as HTMLSelectElement).value);
  void router.replace({ query: { ...route.query, type: next ?? undefined } });
}

watch(
  [query, kind],
  ([q, k]: [string, SearchKind | null]) => {
    void store.search(q, k);
  },
  { immediate: true },
);
</script>

<template>
  <section aria-labelledby="search-title">
    <div class="jl-search__head">
      <h1 id="search-title" tabindex="-1">{{ query ? t("search.titleFor", { query }) : t("search.title") }}</h1>
      <span class="jl-search__filter">
        <label :for="kindId">{{ t("search.kind") }}</label>
        <select :id="kindId" class="jl-search__select" :value="kind ?? ''" @change="setKind">
          <option value="">{{ t("search.kindAll") }}</option>
          <option v-for="option in kindOptions" :key="option.value" :value="option.value">{{ t(option.key) }}</option>
        </select>
      </span>
    </div>

    <p v-if="!query" class="jl-search__hint">{{ t("search.hint") }}</p>
    <RequestStatus v-else :state="store.state" @retry="store.retry">
      <template #loading>
        <ul class="jl-results" aria-hidden="true">
          <li v-for="n in 6" :key="n" class="jl-result">
            <UiSkeleton shape="poster" width="64px" />
            <UiSkeleton shape="title" width="40%" />
          </li>
        </ul>
      </template>
      <template #default>
        <p class="jl-search__count" role="status">{{ t("search.count", { count: store.items.length, total: store.total }) }}</p>
        <ul class="jl-results">
          <li v-for="item in store.items" :key="item.id" class="jl-result">
            <ItemPoster class="jl-result__poster" :item-id="item.id" :title="item.title" :width="96" />
            <span class="jl-result__text">
              <RouterLink class="jl-result__title" :to="{ name: 'item', params: { itemId: item.id } }">{{ item.title }}</RouterLink>
              <span class="jl-result__meta">
                {{ t(kindLabelKey[item.kind]) }}<template v-if="item.productionYear"> · {{ item.productionYear }}</template>
              </span>
            </span>
          </li>
        </ul>
        <RequestStatus v-if="store.moreState.status !== 'idle'" :state="store.moreState" @retry="store.loadMore">
          <template #default />
        </RequestStatus>
        <UiButton
          v-if="store.items.length < store.total && store.moreState.status !== 'loading'"
          class="jl-search__more"
          variant="secondary"
          @click="store.loadMore"
        >
          {{ t("common.loadMore") }}
        </UiButton>
      </template>
      <template #empty>
        <UiEmptyState :title="t('search.empty', { query })">
          <p>{{ t("search.emptyHint") }}</p>
        </UiEmptyState>
      </template>
    </RequestStatus>
  </section>
</template>

<style scoped>
.jl-search__head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--jl-space-4);
}

.jl-search__head h1 {
  margin: var(--jl-space-2) 0;
  overflow-wrap: anywhere;
}

.jl-search__filter {
  display: flex;
  align-items: center;
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
  color: var(--jl-color-text-muted);
}

.jl-results {
  display: grid;
  gap: var(--jl-space-2);
  margin: 0 0 var(--jl-space-4);
  padding: 0;
  list-style: none;
}

.jl-result {
  display: grid;
  grid-template-columns: 64px 1fr;
  align-items: center;
  gap: var(--jl-space-4);
  padding: var(--jl-space-2);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-surface);
}

.jl-result__text {
  display: grid;
  gap: var(--jl-space-1);
  min-width: 0;
}

.jl-result__title {
  font-weight: 600;
  overflow-wrap: anywhere;
}

.jl-result__meta {
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}
</style>
