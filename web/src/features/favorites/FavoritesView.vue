<script setup lang="ts">
import { onMounted, shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import { useApi } from "@/api";
import { ApiError, networkError } from "@/api/errors";
import { useRequest } from "@/api/requestState";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import { errorMessageKey } from "@/features/errors/messages";
import ItemPoster from "@/features/items/ItemPoster.vue";
import { kindLabelKey } from "@/features/items/labels";
import { useToastStore } from "@/stores/toasts";
import { listFavorites, type FavoriteItem } from "./api";
import "@/features/collections/lists.css";

// The caller's favourites (G24.2), most recently marked first. Items the
// caller can no longer see are not listed (G48.3).
const { t } = useI18n();
const { client } = useApi();
const toasts = useToastStore();
const items = shallowRef<readonly FavoriteItem[]>([]);
const nextCursor = shallowRef("");
const busy = shallowRef(false);

const request = useRequest(
  async () => {
    const page = await listFavorites(client);
    items.value = page.items;
    nextCursor.value = page.nextCursor;
    return page.items;
  },
  (rows) => rows.length === 0,
);

async function loadMore() {
  busy.value = true;
  try {
    const page = await listFavorites(client, nextCursor.value);
    items.value = [...items.value, ...page.items];
    nextCursor.value = page.nextCursor;
  } catch (error: unknown) {
    toasts.push(errorMessageKey(error instanceof ApiError ? error : networkError(error)), "danger");
  } finally {
    busy.value = false;
  }
}

onMounted(() => {
  void request.run();
});
</script>

<template>
  <section aria-labelledby="favorites-title">
    <h1 id="favorites-title" tabindex="-1">{{ t("collections.favorites.title") }}</h1>
    <RequestStatus :state="request.state.value" @retry="request.run">
      <template #default>
        <ul class="jl-lists__rows" data-testid="favorites">
          <li v-for="item in items" :key="item.id" class="jl-lists__row">
            <ItemPoster :item-id="item.id" :title="item.title" :width="96" />
            <RouterLink class="jl-lists__title" :to="{ name: 'item', params: { itemId: item.id } }">{{ item.title }}</RouterLink>
            <UiBadge>{{ t(kindLabelKey[item.kind]) }}</UiBadge>
          </li>
        </ul>
        <UiButton v-if="nextCursor !== ''" variant="secondary" :busy="busy" @click="loadMore">{{ t("common.loadMore") }}</UiButton>
      </template>
      <template #empty>
        <UiEmptyState :title="t('collections.favorites.empty')" />
      </template>
    </RequestStatus>
  </section>
</template>
