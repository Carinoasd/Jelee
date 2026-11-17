<script setup lang="ts" generic="T">
import { computed, onBeforeUnmount, onMounted, shallowRef, useTemplateRef, watch } from "vue";
import { countTracks, virtualWindow, type VirtualWindow } from "./virtualWindow";

// A list or grid that renders only the rows near the viewport once it holds
// more than `threshold` items (G35.3): large libraries stay cheap however far
// the user scrolls. The page itself scrolls (the header stays sticky), so the
// window follows the document's scroll position. Columns come from the CSS
// (the class's grid-template-columns, read back from the browser), and rows
// are assumed to share one height, measured from the rendered items. Items
// keep their list semantics: virtualized items carry aria-setsize and
// aria-posinset. Overscan rows above and below the viewport keep Tab and
// Shift+Tab inside the list: focusing an overscan item scrolls it into view,
// which renders the rows after it. `end` fires when the last rows come near
// the viewport, so the caller can load the next page without a button.
const props = withDefaults(
  defineProps<{
    items: readonly T[];
    itemKey: (item: T) => string;
    /** Below this many items everything is rendered. */
    threshold?: number;
    /** Row height (with the gap) assumed until a row is measured. */
    estimate?: number;
    /** Rows rendered beyond each edge of the viewport. */
    overscan?: number;
    /** Distance from the end, in pixels, at which `end` fires. */
    endMargin?: number;
  }>(),
  { threshold: 120, estimate: 80, overscan: 3, endMargin: 800 },
);
const emit = defineEmits<{ end: [] }>();
defineSlots<{ default(props: { item: T; index: number }): unknown }>();

const list = useTemplateRef<HTMLUListElement>("list");
// The end marker sits at the list's bottom edge (absolutely positioned, so
// it takes no grid cell and is hidden from assistive technology).
const sentinel = useTemplateRef<HTMLElement>("sentinel");
const virtual = computed(() => props.items.length > props.threshold);
// Until the list is measured: one column from the top of the viewport.
const range = shallowRef<VirtualWindow>(
  virtualWindow({
    count: props.items.length,
    columns: 1,
    rowHeight: props.estimate,
    viewportTop: 0,
    viewportHeight: window.innerHeight,
    overscan: props.overscan,
  }),
);
let frame = 0;

function measure(): void {
  frame = 0;
  const element = list.value;
  if (element === null || !virtual.value) {
    return;
  }
  const rect = element.getBoundingClientRect();
  const first = element.querySelector<HTMLElement>(":scope > li[data-index]");
  const gap = Number.parseFloat(getComputedStyle(element).rowGap) || 0;
  const height = first?.getBoundingClientRect().height ?? 0;
  range.value = virtualWindow({
    count: props.items.length,
    columns: countTracks(getComputedStyle(element).gridTemplateColumns),
    rowHeight: height > 0 ? height + gap : props.estimate,
    viewportTop: -rect.top,
    viewportHeight: window.innerHeight,
    overscan: props.overscan,
  });
}

function schedule(): void {
  if (frame === 0) {
    frame = requestAnimationFrame(measure);
  }
}

const shown = computed(() => {
  if (!virtual.value) {
    return props.items.map((item, index) => ({ item, index }));
  }
  return props.items.slice(range.value.start, range.value.end).map((item, offset) => ({ item, index: range.value.start + offset }));
});
const padding = computed(() =>
  virtual.value ? { paddingTop: `${range.value.before}px`, paddingBottom: `${range.value.after}px` } : undefined,
);

let observer: IntersectionObserver | null = null;
function observeEnd(): void {
  observer?.disconnect();
  observer = null;
  if (typeof IntersectionObserver !== "function" || sentinel.value === null) {
    return;
  }
  observer = new IntersectionObserver(
    (entries) => {
      if (entries.some((entry) => entry.isIntersecting)) {
        emit("end");
      }
    },
    { rootMargin: `0px 0px ${props.endMargin}px 0px` },
  );
  observer.observe(sentinel.value);
}

onMounted(() => {
  window.addEventListener("scroll", schedule, { passive: true });
  window.addEventListener("resize", schedule);
  measure();
  observeEnd();
});
onBeforeUnmount(() => {
  window.removeEventListener("scroll", schedule);
  window.removeEventListener("resize", schedule);
  if (frame !== 0) {
    cancelAnimationFrame(frame);
  }
  observer?.disconnect();
});
// New items (next page, another search) and a switch into virtual mode
// re-measure; the sentinel is observed again so a page that still fits the
// viewport keeps asking for more.
watch(
  () => [props.items, virtual.value],
  () => {
    schedule();
    observeEnd();
  },
);
</script>

<template>
  <ul ref="list" class="jl-virtual" :style="padding" :data-virtual="virtual ? '' : undefined">
    <li
      v-for="entry in shown"
      :key="itemKey(entry.item)"
      :data-index="entry.index"
      :aria-setsize="virtual ? items.length : undefined"
      :aria-posinset="virtual ? entry.index + 1 : undefined"
    >
      <slot :item="entry.item" :index="entry.index" />
    </li>
    <li ref="sentinel" class="jl-virtual__end" aria-hidden="true" />
  </ul>
</template>

<style scoped>
.jl-virtual {
  position: relative;
}

.jl-virtual__end {
  position: absolute;
  bottom: 0;
  left: 0;
  width: 1px;
  height: 1px;
  pointer-events: none;
}
</style>
