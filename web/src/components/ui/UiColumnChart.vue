<script setup lang="ts">
import { computed } from "vue";
import type { BarRow } from "./types";

// Column chart over time in inline SVG. The SVG is decorative; the same
// figures are listed in a visually hidden table for assistive technology.
const props = defineProps<{ caption: string; rows: readonly BarRow[]; labelHeader: string; valueHeader: string }>();
const height = 120;
const gap = 4;
const max = computed(() => Math.max(0, ...props.rows.map((row) => row.value)));
const columnWidth = computed(() => Math.max(4, Math.floor(600 / Math.max(1, props.rows.length)) - gap));
const viewWidth = computed(() => props.rows.length * (columnWidth.value + gap));
const bars = computed(() =>
  props.rows.map((row, index) => {
    const h = max.value > 0 ? Math.max(row.value > 0 ? 2 : 0, Math.round((row.value / max.value) * height)) : 0;
    return { ...row, x: index * (columnWidth.value + gap), y: height - h, h };
  }),
);
</script>

<template>
  <figure class="jl-columns">
    <figcaption class="jl-columns__caption">{{ caption }}</figcaption>
    <svg
      class="jl-columns__svg"
      :viewBox="`0 0 ${Math.max(viewWidth, 1)} ${height}`"
      preserveAspectRatio="none"
      aria-hidden="true"
      focusable="false"
    >
      <rect v-for="bar in bars" :key="bar.key" :x="bar.x" :y="bar.y" :width="columnWidth" :height="bar.h" class="jl-columns__bar">
        <title>{{ bar.label }}: {{ bar.display }}</title>
      </rect>
    </svg>
    <div class="jl-columns__axis" aria-hidden="true">
      <span>{{ rows[0]?.label }}</span>
      <span>{{ rows.at(-1)?.label }}</span>
    </div>
    <table class="jl-visually-hidden">
      <thead>
        <tr>
          <th scope="col">{{ labelHeader }}</th>
          <th scope="col">{{ valueHeader }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="row in rows" :key="row.key">
          <th scope="row">{{ row.label }}</th>
          <td>{{ row.display }}</td>
        </tr>
      </tbody>
    </table>
  </figure>
</template>

<style scoped>
.jl-columns {
  margin: 0;
}

.jl-columns__caption {
  margin-bottom: var(--jl-space-2);
  font-weight: 600;
}

.jl-columns__svg {
  display: block;
  width: 100%;
  height: 160px;
  border-bottom: 1px solid var(--jl-color-border);
}

.jl-columns__bar {
  fill: var(--jl-color-chart);
}

.jl-columns__axis {
  display: flex;
  justify-content: space-between;
  margin-top: var(--jl-space-1);
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}
</style>
