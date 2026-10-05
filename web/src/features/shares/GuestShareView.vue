<script setup lang="ts">
import { computed, onMounted, shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import { useApi } from "@/api";
import { ApiError, networkError } from "@/api/errors";
import { useRequest } from "@/api/requestState";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { listLibraryItems, type CatalogItem } from "@/features/items/api";
import ItemPoster from "@/features/items/ItemPoster.vue";
import { kindLabelKey } from "@/features/items/labels";
import { formatDateTime } from "@/i18n/format";
import { useGuestShareStore } from "@/stores/guestShare";
import type { GuestShare } from "./api";

// Landing page of a share guest: what was shared, until when, and the items
// it contains. The server already narrows GET /api/v1/items to the share, so
// the list is the share's top level (the library's, or the shared series'
// seasons). Browsing only: the web never plays (G27).
const { t, locale } = useI18n();
const { client } = useApi();
const items = shallowRef<readonly CatalogItem[]>([]);
const total = shallowRef(0);
const loadingMore = shallowRef(false);
const moreError = shallowRef<ApiError | null>(null);
const guest = useGuestShareStore();
const share = shallowRef<GuestShare | null>(null);

/** Parent whose children the list shows: the shared item, else the library. */
const parentId = computed(() => share.value?.itemId ?? share.value?.libraryId ?? "");

const request = useRequest(async () => {
  share.value = await guest.fetch();
  const page = await listLibraryItems(client, parentId.value);
  items.value = page.items;
  total.value = page.total;
  return share.value;
});

async function loadMore() {
  loadingMore.value = true;
  moreError.value = null;
  try {
    const page = await listLibraryItems(client, parentId.value, items.value.length);
    items.value = [...items.value, ...page.items];
    total.value = page.total;
  } catch (error: unknown) {
    moreError.value = error instanceof ApiError ? error : networkError(error);
  } finally {
    loadingMore.value = false;
  }
}

onMounted(() => {
  void request.run();
});
</script>

<template>
  <section class="jl-guest" aria-labelledby="guest-title">
    <h1 id="guest-title" tabindex="-1">{{ t("shares.guest.title") }}</h1>
    <RequestStatus :state="request.state.value" @retry="request.run">
      <template #loading>
        <div aria-hidden="true">
          <UiSkeleton shape="title" width="40%" />
          <UiSkeleton shape="block" />
        </div>
      </template>
      <template #default="{ data }">
        <div class="jl-guest__summary" data-testid="guest-share">
          <p v-if="data.itemId">
            {{ t("shares.guest.item", { library: data.libraryName }) }}
            <RouterLink :to="{ name: 'item', params: { itemId: data.itemId } }">{{ data.itemTitle || data.itemId }}</RouterLink>
          </p>
          <p v-else>{{ t("shares.guest.library", { name: data.libraryName }) }}</p>
          <p>{{ t("shares.guest.expires", { time: formatDateTime(data.expiresAt, locale) }) }}</p>
          <p>
            <UiBadge v-if="data.readOnly">{{ t("shares.guest.readOnly") }}</UiBadge>
          </p>
          <p class="jl-guest__muted">{{ t("shares.guest.browseOnly") }}</p>
        </div>

        <h2 class="jl-guest__heading">{{ t("shares.guest.items") }}</h2>
        <UiEmptyState v-if="items.length === 0" :title="t('shares.guest.empty')" />
        <template v-else>
          <p class="jl-guest__muted" aria-live="polite">{{ t("items.count", { count: items.length, total }) }}</p>
          <ul class="jl-guest__wall">
            <li v-for="item in items" :key="item.id">
              <RouterLink class="jl-guest__card" :to="{ name: 'item', params: { itemId: item.id } }">
                <ItemPoster :item-id="item.id" :title="item.title" :width="300" />
                <span class="jl-guest__title">{{ item.title }}</span>
                <span class="jl-guest__muted">{{ t(kindLabelKey[item.kind]) }}</span>
              </RouterLink>
            </li>
          </ul>
          <UiAlert v-if="moreError" tone="danger">
            <p>{{ t(errorMessageKey(moreError)) }}</p>
          </UiAlert>
          <UiButton v-if="items.length < total" variant="secondary" :busy="loadingMore" @click="loadMore">{{ t("common.loadMore") }}</UiButton>
        </template>
      </template>
    </RequestStatus>
  </section>
</template>

<style scoped>
.jl-guest {
  display: grid;
  gap: var(--jl-space-4);
}

.jl-guest h1,
.jl-guest__heading {
  margin: 0;
}

.jl-guest__heading {
  font-size: var(--jl-font-size-lg);
}

.jl-guest__summary {
  display: grid;
  gap: var(--jl-space-2);
  padding: var(--jl-space-4);
  background: var(--jl-color-surface);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
}

.jl-guest__summary p {
  margin: 0;
}

.jl-guest__muted {
  margin: 0;
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-guest__wall {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(var(--jl-poster-min-width), 1fr));
  gap: var(--jl-space-6) var(--jl-space-4);
  margin: 0;
  padding: 0;
  list-style: none;
}

.jl-guest__card {
  display: grid;
  gap: var(--jl-space-1);
  color: inherit;
  text-decoration: none;
}

.jl-guest__title {
  font-weight: 600;
  overflow-wrap: anywhere;
}
</style>
