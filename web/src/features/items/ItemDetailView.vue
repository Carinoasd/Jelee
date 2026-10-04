<script setup lang="ts">
import { computed, defineAsyncComponent, watch } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { loadLazyMessages } from "@/i18n";
import { formatDateTime } from "@/i18n/format";
import PluginDetailTabs from "@/plugins/host/PluginDetailTabs.vue";
import PluginItemActions from "@/plugins/host/PluginItemActions.vue";
import PluginOutlet from "@/plugins/host/PluginOutlet.vue";
import { toPluginItem } from "@/plugins/host/restrictedApi";
import { useAuthStore } from "@/stores/auth";
import { useItemDetailStore } from "@/stores/itemDetail";
import { useLayoutStore } from "@/stores/layout";
import { useLibrariesStore } from "@/stores/libraries";
import type { ItemDetails, MediaSourceInfo } from "./api";
import { codecsOf, formatBitrate, formatBytes, formatDuration, resolutionOf } from "./files";
import ItemPoster from "./ItemPoster.vue";
import { kindLabelKey, nfoStatusKey } from "./labels";

// Read-only item page. The web client browses and manages; it never offers
// playback (G27): there is no player, no media element and no action that
// starts one. Watching happens in native clients. File information describes
// the originals only; the server sends no delivery route to web sessions.
const props = defineProps<{ itemId: string }>();
const auth = useAuthStore();
const { t, locale } = useI18n();
const i18nGlobal = useI18n({ useScope: "global" });
// Version decisions and track preferences load with their own chunks, only
// when an item with versions is shown.
const ItemVersionsPanel = defineAsyncComponent(async () => (await Promise.all([import("./ItemVersionsPanel.vue"), loadLazyMessages(i18nGlobal, "versions")]))[0]);
const TrackPreferencesPanel = defineAsyncComponent(async () => (await Promise.all([import("./TrackPreferencesPanel.vue"), loadLazyMessages(i18nGlobal, "versions")]))[0]);
const store = useItemDetailStore();
const libraries = useLibrariesStore();
const layout = useLayoutStore();
// Panels in the user's order (G33.5); hidden panels are not rendered.
const panels = computed(() => layout.visibleIds("detail"));

// Plugins see a frozen copy of the display data, never the store's object.
let pluginItemCache: { source: ItemDetails; item: ReturnType<typeof toPluginItem> } | null = null;
function pluginItem(details: ItemDetails) {
  if (pluginItemCache?.source !== details) {
    pluginItemCache = { source: details, item: toPluginItem(details) };
  }
  return pluginItemCache.item;
}

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
const nfoFields = computed(() => new Set<string>(loaded.value?.item.nfo.fields ?? []));

function versionName(source: MediaSourceInfo, index: number): string {
  const name = source.version.displayName;
  return name !== "" ? name : t("items.files.version", { n: index + 1 });
}

/** Facts of one source in display order; unknown values are left out. */
function facts(source: MediaSourceInfo): { key: string; value: string }[] {
  const rows = [
    { key: "items.files.container", value: source.container },
    { key: "items.files.duration", value: formatDuration(source.durationMicros) },
    { key: "items.files.resolution", value: resolutionOf(source) },
    { key: "items.files.videoCodec", value: codecsOf(source.videoTracks) },
    { key: "items.files.audioCodecs", value: codecsOf(source.audioTracks) },
    { key: "items.files.bitrate", value: formatBitrate(source.bitRate, locale.value) },
    { key: "items.files.size", value: formatBytes(source.sizeBytes, locale.value) },
  ];
  return rows.filter((row) => row.value !== "");
}

function language(value: string | undefined): string {
  return value === undefined || value === "" ? t("items.files.unknownLanguage") : value;
}
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
              <span v-if="data.item.productionYear">{{ t("items.detail.yearValue", { year: data.item.productionYear }) }}</span>
            </p>

            <p v-if="data.item.originalTitle && data.item.originalTitle !== data.item.title" class="jl-detail__original">
              {{ t("items.detail.originalTitle") }}: {{ data.item.originalTitle }}
              <UiBadge v-if="nfoFields.has('originalTitle')" tone="accent">{{ t("items.detail.nfoBadge") }}</UiBadge>
            </p>
            <p v-if="data.item.tagline" class="jl-detail__tagline">{{ data.item.tagline }}</p>
            <PluginItemActions :item="pluginItem(data.item)" />

            <template v-for="panel in panels" :key="panel">
              <section v-if="panel === 'overview'" class="jl-detail__section" aria-labelledby="overview-title">
                <h2 id="overview-title">
                  {{ t("items.detail.overview") }}
                  <UiBadge v-if="nfoFields.has('overview')" tone="accent">{{ t("items.detail.nfoBadge") }}</UiBadge>
                </h2>
                <p v-if="data.item.overview" class="jl-detail__overview">{{ data.item.overview }}</p>
                <p v-else class="jl-detail__muted">{{ t("items.detail.noOverview") }}</p>
              </section>

              <section v-else-if="panel === 'genres' && data.item.genres.length > 0" class="jl-detail__section" aria-labelledby="genres-title">
                <h2 id="genres-title">
                  {{ t("items.detail.genres") }}
                  <UiBadge v-if="nfoFields.has('genres')" tone="accent">{{ t("items.detail.nfoBadge") }}</UiBadge>
                </h2>
                <ul class="jl-detail__tags">
                  <li v-for="genre in data.item.genres" :key="genre"><UiBadge>{{ genre }}</UiBadge></li>
                </ul>
              </section>

              <section v-else-if="panel === 'externalIds'" class="jl-detail__section" aria-labelledby="ids-title">
                <h2 id="ids-title">
                  {{ t("items.detail.externalIds") }}
                  <UiBadge v-if="nfoFields.has('uniqueIds')" tone="accent">{{ t("items.detail.nfoBadge") }}</UiBadge>
                </h2>
                <dl v-if="data.item.externalIds.length > 0" class="jl-detail__ids">
                  <template v-for="id in data.item.externalIds" :key="id.type + ':' + id.value">
                    <dt>{{ id.type }}</dt>
                    <dd>
                      <code>{{ id.value }}</code>
                      <UiBadge v-if="id.default">{{ t("items.detail.defaultId") }}</UiBadge>
                    </dd>
                  </template>
                </dl>
                <p v-else class="jl-detail__muted">{{ t("items.detail.noExternalIds") }}</p>
              </section>

              <section v-else-if="panel === 'nfo'" class="jl-detail__section" aria-labelledby="nfo-title">
                <h2 id="nfo-title">{{ t("items.detail.nfoSource") }}</h2>
                <p :class="{ 'jl-detail__muted': data.item.nfo.status === 'unread' }">
                  {{ t(nfoStatusKey[data.item.nfo.status]) }}
                  <span v-if="data.item.nfo.readAt" class="jl-detail__muted">
                    {{ t("items.detail.nfoReadAt", { date: formatDateTime(data.item.nfo.readAt, locale) }) }}
                  </span>
                </p>
              </section>

              <section v-else-if="panel === 'files'" class="jl-detail__section" aria-labelledby="files-title">
                <h2 id="files-title">{{ t("items.detail.files") }}</h2>
                <p v-if="data.sources === null" class="jl-detail__muted">{{ t("items.files.unavailable") }}</p>
                <p v-else-if="data.sources.length === 0" class="jl-detail__muted">{{ t("items.files.none") }}</p>
                <div v-for="(source, index) in data.sources ?? []" :key="source.id" class="jl-file">
                  <h3 class="jl-file__title">{{ versionName(source, index) }}</h3>
                  <p v-if="!source.probed" class="jl-detail__muted">{{ t("items.files.unprobed") }}</p>
                  <dl class="jl-detail__ids">
                    <template v-for="row in facts(source)" :key="row.key">
                      <dt>{{ t(row.key) }}</dt>
                      <dd>{{ row.value }}</dd>
                    </template>
                  </dl>
                  <template v-if="source.audioTracks.length > 0">
                    <h4 class="jl-file__subtitle">{{ t("items.files.embeddedAudio") }}</h4>
                    <ul class="jl-file__tracks">
                      <li v-for="track in source.audioTracks" :key="'a' + track.index">
                        {{ language(track.language) }} · {{ track.codec ?? "" }}<template v-if="track.channelLayout"> · {{ track.channelLayout }}</template>
                        <UiBadge v-if="track.default">{{ t("items.files.flagDefault") }}</UiBadge>
                        <UiBadge v-if="track.forced">{{ t("items.files.flagForced") }}</UiBadge>
                      </li>
                    </ul>
                  </template>
                  <template v-if="source.subtitleTracks.length > 0">
                    <h4 class="jl-file__subtitle">{{ t("items.files.embeddedSubtitles") }}</h4>
                    <ul class="jl-file__tracks">
                      <li v-for="track in source.subtitleTracks" :key="'s' + track.index">
                        {{ language(track.language) }} · {{ track.format ?? track.codec ?? "" }}
                        <UiBadge v-if="track.default">{{ t("items.files.flagDefault") }}</UiBadge>
                        <UiBadge v-if="track.forced">{{ t("items.files.flagForced") }}</UiBadge>
                      </li>
                    </ul>
                  </template>
                  <template v-if="source.externalTracks.length > 0">
                    <h4 class="jl-file__subtitle">{{ t("items.files.externalFiles") }}</h4>
                    <ul class="jl-file__tracks">
                      <li v-for="track in source.externalTracks" :key="track.id">
                        {{ t(track.kind === "subtitle" ? "items.files.kindSubtitle" : "items.files.kindAudio") }} ·
                        {{ language(track.language) }} · {{ track.format }}<template v-if="track.title"> · {{ track.title }}</template>
                        <UiBadge v-if="track.default">{{ t("items.files.flagDefault") }}</UiBadge>
                        <UiBadge v-if="track.forced">{{ t("items.files.flagForced") }}</UiBadge>
                        <UiBadge v-if="track.sdh">{{ t("items.files.flagSdh") }}</UiBadge>
                        <UiBadge v-if="track.commentary">{{ t("items.files.flagCommentary") }}</UiBadge>
                      </li>
                    </ul>
                  </template>
                </div>
                <template v-if="data.sources && data.sources.length > 0">
                  <ItemVersionsPanel v-if="auth.isAdmin" :key="'v' + data.item.id" :item-id="data.item.id" :sources="data.sources" @changed="store.reload" />
                  <TrackPreferencesPanel :key="data.item.id" :item-id="data.item.id" :sources="data.sources" />
                </template>
              </section>
              <div v-else-if="panel === 'pluginPanels'" class="jl-detail__plugins">
                <PluginOutlet hook="metadata.panel" headings :component-props="{ item: pluginItem(data.item) }" />
              </div>
              <PluginDetailTabs v-else-if="panel === 'pluginTabs'" :item="pluginItem(data.item)" />
            </template>

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

.jl-file {
  margin-top: var(--jl-space-4);
}

.jl-file__title {
  margin: 0 0 var(--jl-space-2);
  font-size: var(--jl-font-size-md);
  overflow-wrap: anywhere;
}

.jl-file__subtitle {
  margin: var(--jl-space-3) 0 var(--jl-space-1);
  font-size: var(--jl-font-size-sm);
}

.jl-file__tracks {
  display: grid;
  gap: var(--jl-space-1);
  margin: 0;
  padding-inline-start: var(--jl-space-4);
  overflow-wrap: anywhere;
}

.jl-file__tracks li {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-1);
}

.jl-detail__plugins {
  display: grid;
  gap: var(--jl-space-6);
  margin-top: var(--jl-space-6);
}

.jl-detail__native {
  margin-top: var(--jl-space-8);
}

.jl-detail__native-title {
  font-weight: 600;
}
</style>
