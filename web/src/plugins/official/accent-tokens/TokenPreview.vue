<script setup lang="ts">
import { usePlugin } from "@jelee/plugin-sdk";

const { t } = usePlugin();
const swatches = [
  { key: "preview.primary", token: "primary" },
  { key: "preview.focus", token: "focus" },
  { key: "preview.chart", token: "chart" },
  { key: "preview.surface", token: "surface" },
] as const;
</script>

<template>
  <div class="jl-preview">
    <p>{{ t("preview.intro") }}</p>
    <ul class="jl-preview__swatches">
      <li v-for="swatch in swatches" :key="swatch.token">
        <span class="jl-preview__swatch" :data-token="swatch.token" aria-hidden="true" />
        {{ t(swatch.key) }}
      </li>
    </ul>
    <button type="button" class="jl-preview__button">{{ t("preview.sample") }}</button>
  </div>
</template>

<style scoped>
.jl-preview {
  display: grid;
  gap: var(--jl-space-4);
  justify-items: start;
}

.jl-preview__swatches {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-4);
  margin: 0;
  padding: 0;
  list-style: none;
}

.jl-preview__swatches li {
  display: grid;
  gap: var(--jl-space-1);
  justify-items: center;
}

.jl-preview__swatch {
  width: 64px;
  height: 64px;
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
}

.jl-preview__swatch[data-token="primary"] {
  background: var(--jl-color-primary);
}

.jl-preview__swatch[data-token="focus"] {
  background: var(--jl-color-focus);
}

.jl-preview__swatch[data-token="chart"] {
  background: var(--jl-color-chart);
}

.jl-preview__swatch[data-token="surface"] {
  background: var(--jl-color-surface);
}

.jl-preview__button {
  min-height: var(--jl-touch-target);
  padding: var(--jl-space-2) var(--jl-space-4);
  border: 0;
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-primary);
  color: var(--jl-color-primary-text);
  font: inherit;
}
</style>
