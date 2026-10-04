<script setup lang="ts">
import { computed } from "vue";
import type { BarRow } from "./types";

// Horizontal bar chart in plain CSS. The data stays a real table, so screen
// readers read labels and values; the bars themselves are decorative.
const props = defineProps<{ caption: string; rows: readonly BarRow[]; labelHeader: string; valueHeader: string }>();
const max = computed(() => Math.max(0, ...props.rows.map((row) => row.value)));
const width = (value: number) => (max.value > 0 ? `${Math.max(1, Math.round((value / max.value) * 100))}%` : "0%");
</script>

<template>
  <table class="jl-bars">
    <caption class="jl-bars__caption">{{ caption }}</caption>
    <thead class="jl-visually-hidden">
      <tr>
        <th scope="col">{{ labelHeader }}</th>
        <th scope="col">{{ valueHeader }}</th>
      </tr>
    </thead>
    <tbody>
      <tr v-for="row in rows" :key="row.key" class="jl-bars__row">
        <th scope="row" class="jl-bars__label">{{ row.label }}</th>
        <td class="jl-bars__value">
          <span class="jl-bars__track" aria-hidden="true">
            <span class="jl-bars__bar" :style="{ width: width(row.value) }" />
          </span>
          <span class="jl-bars__text">{{ row.display }}</span>
        </td>
      </tr>
    </tbody>
  </table>
</template>

<style scoped>
.jl-bars {
  width: 100%;
  border-collapse: collapse;
}

.jl-bars__caption {
  margin-bottom: var(--jl-space-2);
  text-align: start;
  font-weight: 600;
}

.jl-bars__row + .jl-bars__row > * {
  border-top: 1px solid var(--jl-color-border);
}

.jl-bars__label {
  padding: var(--jl-space-2) var(--jl-space-3) var(--jl-space-2) 0;
  font-weight: normal;
  text-align: start;
  overflow-wrap: anywhere;
  width: 40%;
}

.jl-bars__value {
  display: flex;
  align-items: center;
  gap: var(--jl-space-2);
  padding: var(--jl-space-2) 0;
}

.jl-bars__track {
  flex: 1;
  height: 12px;
  border-radius: var(--jl-radius-pill);
  background: var(--jl-color-chart-track);
  overflow: hidden;
}

.jl-bars__bar {
  display: block;
  height: 100%;
  border-radius: var(--jl-radius-pill);
  background: var(--jl-color-chart);
  transition: width var(--jl-motion-duration) var(--jl-motion-easing);
}

.jl-bars__text {
  min-width: 6ch;
  font-variant-numeric: tabular-nums;
  text-align: end;
  white-space: nowrap;
}
</style>
