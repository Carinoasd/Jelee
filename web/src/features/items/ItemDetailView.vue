<script setup lang="ts">
import { computed, watch } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { formatDateTime } from "@/i18n/format";
import { useItemDetailStore } from "@/stores/itemDetail";
import { useLibrariesStore } from "@/stores/libraries";
import ItemPoster from "./ItemPoster.vue";
import { kindLabelKey, nfoStatusKey } from "./labels";

// Read-only item page. The web client browses and manages; it never offers
// playback (G27): there is no player, no media element and no action that
// starts one. Watching happens in native clients.
const props = defineProps<{ itemId: string }>();
const { t, locale } = useI18n();
const store = useItemDetailStore();
const libraries = useLibrariesStore();

watch(
  () => props.itemId,
  (id) => {
    void store.open(id);
    void libraries.ensureLoaded();
  },
  { immediate: true },
);

const loaded = computed(() => (store.state.status === "success" ? store.state.data : null));
const libraryName = computed(() => {
  const id = loaded.value?.item.libraryId;
  return id === undefined ? "" : (libraries.find(id)?.name ?? t("libraries.library"));
});
</script>

<template>
  <article class="jl-detail" aria-labelledby="item-title">
    <RequestStatus :state="store.state" @retry="store.reload">
      <template #loading>
        <div class="jl-detail__layout" aria-hidden="true">
          <UiSkeleton shape="poster" />
          <div>
            <UiSkeleton shape="title" width="50%" />
            <UiSkeleton width="30%" />
            <UiSkeleton shape="block" />
          </div>
        </div>
      </template>
      <template #default="{ data }">
        <nav class="jl-crumbs" :aria-label="t('common.breadcrumbs')">
          <RouterLink :to="{ name: 'libraries' }">{{ t("libraries.title") }}</RouterLink>
          <span aria-hidden="true">/</span>
          <RouterLink :to="{ name: 'library', params: { libraryId: data.item.libraryId } }">{{ libraryName }}</RouterLink>
          <template v-if="data.item.parentId">
            <span aria-hidden="true">/</span>
            <RouterLink :to="{ name: 'item', params: { itemId: data.item.parentId } }">{{ t("items.detail.parent") }}</RouterLink>
          </template>
        </nav>
        <div class="jl-detail__layout">
          <ItemPoster :item-id="data.item.id" :title="data.item.title" :width="480" eager />
          <div class="jl-detail__body">
            <h1 id="item-title" tabindex="-1" class="jl-detail__title">{{ data.item.title }}</h1>
            <p class="jl-detail__facts">
              <UiBadge>{{ t(kindLabelKey[data.item.kind]) }}</UiBadge>
              <span v-if="data.metadata?.year">{{ t("items.detail.yearValue", { year: data.metadata.year }) }}</span>
            </p>

            <template v-if="data.metadata">
              <p v-if="data.metadata.originalTitle && data.metadata.originalTitle !== data.item.title" class="jl-detail__original">
                {{ t("items.detail.originalTitle") }}: {{ data.metadata.originalTitle }}
                <UiBadge v-if="data.metadata.nfoFields.has('originalTitle')" tone="accent">{{ t("items.detail.nfoBadge") }}</UiBadge>
              </p>
              <p v-if="data.metadata.tagline" class="jl-detail__tagline">{{ data.metadata.tagline }}</p>

              <section class="jl-detail__section" aria-labelledby="overview-title">
                <h2 id="overview-title">
                  {{ t("items.detail.overview") }}
                  <UiBadge v-if="data.metadata.nfoFields.has('overview')" tone="accent">{{ t("items.detail.nfoBadge") }}</UiBadge>
                </h2>
                <p v-if="data.metadata.overview" class="jl-detail__overview">{{ data.metadata.overview }}</p>
                <p v-else class="jl-detail__muted">{{ t("items.detail.noOverview") }}</p>
              </section>

              <section v-if="data.metadata.genres.length > 0" class="jl-detail__section" aria-labelledby="genres-title">
                <h2 id="genres-title">{{ t("items.detail.genres") }}</h2>
                <ul class="jl-detail__tags">
                  <li v-for="genre in data.metadata.genres" :key="genre"><UiBadge>{{ genre }}</UiBadge></li>
                </ul>
              </section>

              <section class="jl-detail__section" aria-labelledby="ids-title">
                <h2 id="ids-title">
                  {{ t("items.detail.externalIds") }}
                  <UiBadge v-if="data.metadata.nfoFields.has('uniqueIds')" tone="accent">{{ t("items.detail.nfoBadge") }}</UiBadge>
                </h2>
                <dl v-if="data.metadata.externalIds.length > 0" class="jl-detail__ids">
                  <template v-for="id in data.metadata.externalIds" :key="id.type + ':' + id.value">
                    <dt>{{ id.type }}</dt>
                    <dd>
                      <code>{{ id.value }}</code>
                      <UiBadge v-if="id.isDefault">{{ t("items.detail.defaultId") }}</UiBadge>
                    </dd>
                  </template>
                </dl>
                <p v-else class="jl-detail__muted">{{ t("items.detail.noExternalIds") }}</p>
              </section>

              <section class="jl-detail__section" aria-labelledby="nfo-title">
                <h2 id="nfo-title">{{ t("items.detail.nfoSource") }}</h2>
                <p v-if="data.metadata.nfoStatus">
                  {{ t(nfoStatusKey[data.metadata.nfoStatus]) }}
                  <span v-if="data.metadata.nfoReadAt" class="jl-detail__muted">
                    {{ t("items.detail.nfoReadAt", { date: formatDateTime(data.metadata.nfoReadAt, locale) }) }}
                  </span>
                </p>
                <p v-else class="jl-detail__muted">{{ t("items.detail.nfoNone") }}</p>
              </section>
            </template>
            <UiAlert v-else-if="data.metadataAccess === 'admin-only'" tone="info">
              <p>{{ t("items.detail.metadataAdminOnly") }}</p>
            </UiAlert>
            <UiAlert v-else tone="info">
              <p>{{ t("items.detail.metadataUnavailable") }}</p>
            </UiAlert>

            <section class="jl-detail__section" aria-labelledby="files-title">
              <h2 id="files-title">{{ t("items.detail.files") }}</h2>
              <p class="jl-detail__muted">{{ t("items.detail.filesUnavailable") }}</p>
            </section>

            <UiAlert tone="info" class="jl-detail__native">
              <p class="jl-detail__native-title">{{ t("items.detail.nativeTitle") }}</p>
              <p>{{ t("items.detail.nativeBody") }}</p>
            </UiAlert>
          </div>
        </div>
      </template>
    </RequestStatus>
  </article>
</template>

<style scoped>
.jl-crumbs {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
  margin-bottom: var(--jl-space-4);
  font-size: var(--jl-font-size-sm);
}

.jl-detail__layout {
  display: grid;
  grid-template-columns: minmax(160px, 280px) 1fr;
  gap: var(--jl-space-8);
  align-items: start;
}

@media (max-width: 640px) {
  .jl-detail__layout {
    grid-template-columns: 1fr;
  }

  .jl-detail__layout > :first-child {
    max-width: 200px;
  }
}

.jl-detail__title {
  margin: 0 0 var(--jl-space-2);
  overflow-wrap: anywhere;
}

.jl-detail__facts {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-3);
  margin: 0 0 var(--jl-space-4);
  color: var(--jl-color-text-muted);
}

.jl-detail__original,
.jl-detail__tagline {
  margin: 0 0 var(--jl-space-2);
}

.jl-detail__tagline {
  font-style: italic;
}

.jl-detail__section {
  margin-top: var(--jl-space-6);
}

.jl-detail__section h2 {
  display: flex;
  align-items: center;
  gap: var(--jl-space-2);
  margin: 0 0 var(--jl-space-2);
  font-size: var(--jl-font-size-lg);
}

.jl-detail__overview {
  white-space: pre-line;
  overflow-wrap: anywhere;
}

.jl-detail__muted {
  color: var(--jl-color-text-muted);
}

.jl-detail__tags {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.jl-detail__ids {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: var(--jl-space-1) var(--jl-space-4);
  margin: 0;
}

.jl-detail__ids dt {
  font-weight: 600;
}

.jl-detail__ids dd {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
  margin: 0;
  overflow-wrap: anywhere;
}

.jl-detail__native {
  margin-top: var(--jl-space-8);
}

.jl-detail__native-title {
  font-weight: 600;
}
</style>
