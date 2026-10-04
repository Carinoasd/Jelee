<script setup lang="ts">
import { usePlugin } from "@jelee/plugin-sdk";
import { computed, useId } from "vue";

const plugin = usePlugin();
const { t } = plugin;
const id = useId();
const externalLinks = computed({
  get: () => plugin.settings.get<boolean>("externalLinks", false),
  set: (value: boolean) => {
    plugin.settings.set("externalLinks", value);
  },
});
</script>

<template>
  <div class="jl-facts-settings">
    <input :id="id" v-model="externalLinks" type="checkbox" :aria-describedby="id + '-hint'" />
    <label :for="id">{{ t("options.externalLinks") }}</label>
    <p :id="id + '-hint'" class="jl-facts-settings__hint">{{ t("options.externalLinksHint") }}</p>
  </div>
</template>

<style scoped>
.jl-facts-settings {
  display: grid;
  grid-template-columns: auto 1fr;
  align-items: center;
  column-gap: var(--jl-space-2);
  min-height: var(--jl-touch-target);
}

.jl-facts-settings input {
  width: 20px;
  height: 20px;
  margin: 0;
  accent-color: var(--jl-color-primary);
}

.jl-facts-settings__hint {
  grid-column: 2;
  margin: 0;
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}
</style>
