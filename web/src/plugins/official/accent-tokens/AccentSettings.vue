<script setup lang="ts">
import { usePlugin } from "@jelee/plugin-sdk";
import { computed, useId } from "vue";
import { accentNames, isAccentName, type AccentName } from "./accents";

const plugin = usePlugin();
const { t } = plugin;
const id = useId();
const accentKey: Readonly<Record<AccentName, string>> = { teal: "accent.teal", violet: "accent.violet", amber: "accent.amber" };
const accent = computed({
  get: () => plugin.settings.get<string>("accent", "teal"),
  set: (value: string) => {
    if (isAccentName(value)) {
      plugin.settings.set("accent", value);
    }
  },
});
const rounded = computed({
  get: () => plugin.settings.get<boolean>("rounded", false),
  set: (value: boolean) => {
    plugin.settings.set("rounded", value);
  },
});
</script>

<template>
  <div class="jl-accent">
    <fieldset class="jl-accent__group">
      <legend>{{ t("options.accent") }}</legend>
      <label v-for="name in accentNames" :key="name" class="jl-accent__option">
        <input v-model="accent" type="radio" :name="id" :value="name" />
        <span class="jl-accent__swatch" :data-accent="name" aria-hidden="true" />
        {{ t(accentKey[name]) }}
      </label>
    </fieldset>
    <label class="jl-accent__option">
      <input v-model="rounded" type="checkbox" />
      {{ t("options.rounded") }}
    </label>
    <RouterLink :to="'/x/' + plugin.id + '/preview'">{{ t("options.preview") }}</RouterLink>
  </div>
</template>

<style scoped>
.jl-accent {
  display: grid;
  gap: var(--jl-space-2);
  justify-items: start;
}

.jl-accent__group {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2) var(--jl-space-4);
  margin: 0;
  padding: 0;
  border: 0;
}

.jl-accent__group legend {
  margin-bottom: var(--jl-space-1);
  font-weight: 600;
}

.jl-accent__option {
  display: inline-flex;
  align-items: center;
  gap: var(--jl-space-2);
  min-height: var(--jl-touch-target);
}

.jl-accent__swatch {
  width: 16px;
  height: 16px;
  border-radius: var(--jl-radius-pill);
  border: 1px solid var(--jl-color-border);
}

.jl-accent__swatch[data-accent="teal"] {
  background: #0f766e;
}

.jl-accent__swatch[data-accent="violet"] {
  background: #6d28d9;
}

.jl-accent__swatch[data-accent="amber"] {
  background: #b45309;
}
</style>
