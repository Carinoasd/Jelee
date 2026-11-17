<script setup lang="ts">
import { useI18n } from "vue-i18n";
import UiVirtualList from "@/components/ui/UiVirtualList.vue";
import { countTracks } from "@/components/ui/virtualWindow";
import type { CatalogItem } from "./api";
import ItemPoster from "./ItemPoster.vue";
import { kindLabelKey, type ItemView } from "./labels";

// Items as a poster wall or as rows (G33.2, G34.3), shared by library pages
// and search. Both views virtualize long lists (UiVirtualList) and ask for
// the next page through `end`. Every item is one link; arrow keys move
// between them (up and down by a whole row of the wall), Home and End jump
// to the first and last rendered item. Wall titles are clamped to two lines
// so rows share one height; hovering or focusing a card shows the whole
// title with kind and year over the poster.
const props = withDefaults(
  defineProps<{
    items: readonly CatalogItem[];
    view: ItemView;
    /** Accessible name of the list. */
    label: string;
    /** Leave the kind out when the surrounding group already names it. */
    hideKind?: boolean;
    threshold?: number;
  }>(),
  { hideKind: false, threshold: undefined },
);
defineEmits<{ end: [] }>();
const { t } = useI18n();

const key = (item: CatalogItem) => item.id;

function meta(item: CatalogItem): string {
  const parts = props.hideKind ? [] : [t(kindLabelKey[item.kind])];
  if (item.productionYear) {
    parts.push(String(item.productionYear));
  }
  return parts.join(" · ");
}

function onKey(event: KeyboardEvent) {
  const link = (event.target as HTMLElement | null)?.closest<HTMLElement>("[data-index]");
  const list = event.currentTarget as HTMLElement;
  if (link === null || link === undefined) {
    return;
  }
  const index = Number(link.dataset.index);
  const columns = props.view === "poster" ? countTracks(getComputedStyle(list).gridTemplateColumns) : 1;
  const rendered = Array.from(list.querySelectorAll<HTMLElement>(":scope > li[data-index]"));
  const steps: Partial<Record<string, number>> = {
    ArrowRight: 1,
    ArrowLeft: -1,
    ArrowDown: columns,
    ArrowUp: -columns,
  };
  let target: HTMLElement | undefined;
  if (event.key === "Home") {
    target = rendered[0];
  } else if (event.key === "End") {
    target = rendered.at(-1);
  } else {
    const step = steps[event.key];
    if (step === undefined) {
      return;
    }
    target = rendered.find((entry) => Number(entry.dataset.index) === index + step);
  }
  event.preventDefault();
  target?.querySelector<HTMLElement>("a")?.focus();
}
</script>

<template>
  <UiVirtualList
    v-if="view === 'poster'"
    class="jl-wall"
    :items="items"
    :item-key="key"
    :estimate="320"
    :threshold="threshold"
    :aria-label="label"
    @keydown="onKey"
    @end="$emit('end')"
  >
    <template #default="{ item }">
      <RouterLink class="jl-card" :to="{ name: 'item', params: { itemId: item.id } }" data-result-link>
        <span class="jl-card__art">
          <ItemPoster :item-id="item.id" :title="item.title" sizes="(max-width: 480px) 45vw, 220px" />
          <span class="jl-card__hover" aria-hidden="true">
            <span class="jl-card__hover-title">{{ item.title }}</span>
            <span>{{ meta(item) }}</span>
          </span>
        </span>
        <span class="jl-card__title">{{ item.title }}</span>
        <span class="jl-card__kind">{{ meta(item) }}</span>
      </RouterLink>
    </template>
  </UiVirtualList>
  <UiVirtualList
    v-else
    class="jl-rows"
    :items="items"
    :item-key="key"
    :estimate="72"
    :threshold="threshold"
    :aria-label="label"
    @keydown="onKey"
    @end="$emit('end')"
  >
    <template #default="{ item }">
      <RouterLink class="jl-row" :to="{ name: 'item', params: { itemId: item.id } }" data-result-link>
        <ItemPoster class="jl-row__thumb" :item-id="item.id" :title="item.title" :width="96" />
        <span class="jl-row__title" :title="item.title">{{ item.title }}</span>
        <span class="jl-row__meta">{{ meta(item) }}</span>
      </RouterLink>
    </template>
  </UiVirtualList>
</template>

<style scoped>
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

.jl-card__art {
  position: relative;
  display: block;
}

.jl-card__art :deep(.jl-poster) {
  transition:
    box-shadow var(--jl-motion-duration) var(--jl-motion-easing),
    transform var(--jl-motion-duration) var(--jl-motion-easing);
}

.jl-card:hover .jl-card__art :deep(.jl-poster),
.jl-card:focus-visible .jl-card__art :deep(.jl-poster) {
  box-shadow: var(--jl-shadow-2);
  transform: translateY(-2px);
}

.jl-card__hover {
  position: absolute;
  inset: auto 0 0;
  display: grid;
  gap: var(--jl-space-1);
  padding: var(--jl-space-3);
  border-radius: 0 0 var(--jl-radius-md) var(--jl-radius-md);
  background: var(--jl-color-overlay);
  color: var(--jl-color-overlay-text);
  font-size: var(--jl-font-size-sm);
  opacity: 0;
  transform: translateY(-2px);
  transition: opacity var(--jl-motion-duration) var(--jl-motion-easing);
  pointer-events: none;
}

.jl-card:hover .jl-card__hover,
.jl-card:focus-visible .jl-card__hover {
  opacity: 1;
}

.jl-card__hover-title {
  font-weight: 600;
  overflow-wrap: anywhere;
}

.jl-card__title {
  display: -webkit-box;
  overflow: hidden;
  font-weight: 600;
  overflow-wrap: anywhere;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  min-height: calc(2 * 1.5em);
}

.jl-card__kind {
  min-height: 1.5em;
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-rows {
  display: grid;
  margin: 0 0 var(--jl-space-6);
  padding: 0;
  list-style: none;
  border-top: 1px solid var(--jl-color-border);
}

.jl-row {
  display: grid;
  grid-template-columns: var(--jl-list-thumb-width) minmax(0, 1fr) auto;
  align-items: center;
  gap: var(--jl-space-4);
  min-height: var(--jl-touch-target);
  padding: var(--jl-list-row-padding) var(--jl-space-3);
  border-bottom: 1px solid var(--jl-color-border);
  color: var(--jl-color-text);
  text-decoration: none;
}

.jl-row:hover {
  background: var(--jl-color-badge-bg);
}

.jl-row__title {
  overflow: hidden;
  color: var(--jl-color-primary);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.jl-row__meta {
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
  white-space: nowrap;
}
</style>
