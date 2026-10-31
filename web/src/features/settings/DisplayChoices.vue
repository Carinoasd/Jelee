<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import UiChoiceGroup from "@/components/ui/UiChoiceGroup.vue";
import { itemViews, type ItemView } from "@/features/items/labels";
import { listDensityIds, posterRatioIds, posterSizeIds, themes, type ListDensity, type PosterRatio, type PosterSize, type Theme } from "@/stores/preferences";
import { palettes, posterRatios } from "@/theme/tokens";

// The theme and display tiles (G33.2), shared by a user's own settings and
// the administrator's site defaults. The theme tiles draw the preset's own
// colors and the ratio tiles the poster shape, so the options need no
// description. The default slot sits between the theme and the poster rows.
const theme = defineModel<Theme>("theme", { required: true });
const posterRatio = defineModel<PosterRatio>("posterRatio", { required: true });
const posterSize = defineModel<PosterSize>("posterSize", { required: true });
const listDensity = defineModel<ListDensity>("listDensity", { required: true });
const view = defineModel<ItemView>("view", { required: true });

const { t } = useI18n();

const themeKey: Readonly<Record<Theme, string>> = {
  system: "settings.theme.system",
  light: "settings.theme.light",
  dark: "settings.theme.dark",
  sepia: "settings.theme.sepia",
  contrast: "settings.theme.contrast",
};
const ratioKey: Readonly<Record<PosterRatio, string>> = {
  portrait: "settings.poster.portrait",
  square: "settings.poster.square",
  landscape: "settings.poster.landscape",
};
const sizeKey: Readonly<Record<PosterSize, string>> = {
  small: "settings.poster.small",
  medium: "settings.poster.medium",
  large: "settings.poster.large",
};
const listKey: Readonly<Record<ListDensity, string>> = {
  comfortable: "settings.density.comfortable",
  compact: "settings.density.compact",
};
const viewKey: Readonly<Record<ItemView, string>> = {
  poster: "items.view.poster",
  list: "items.view.list",
};

const options = <V extends string>(values: readonly V[], keys: Readonly<Record<V, string>>) =>
  computed(() => values.map((value) => ({ value, label: t(keys[value]) })));
const themeOptions = options(themes, themeKey);
const ratioOptions = options(posterRatioIds, ratioKey);
const sizeOptions = options(posterSizeIds, sizeKey);
const listOptions = options(listDensityIds, listKey);
const viewOptions = options(itemViews, viewKey);

function swatch(value: Theme): Record<string, string> {
  if (value === "system") {
    return {
      background: `linear-gradient(135deg, ${palettes.light.colors["color-bg"]} 50%, ${palettes.dark.colors["color-bg"]} 50%)`,
      borderColor: palettes.light.colors["color-border"],
    };
  }
  const colors = palettes[value].colors;
  return { background: colors["color-bg"], borderColor: colors["color-border"] };
}

function swatchPart(value: Theme, token: "color-surface" | "color-primary" | "color-text"): Record<string, string> {
  const colors = palettes[value === "system" ? "light" : value].colors;
  return { background: colors[token] };
}
</script>

<template>
  <div class="jl-display-choices">
    <UiChoiceGroup v-model="theme" :legend="t('settings.theme.label')" :options="themeOptions">
      <template #preview="{ value }">
        <span class="jl-swatch" :style="swatch(value)" aria-hidden="true">
          <span class="jl-swatch__card" :style="swatchPart(value, 'color-surface')">
            <span class="jl-swatch__line" :style="swatchPart(value, 'color-text')" />
            <span class="jl-swatch__accent" :style="swatchPart(value, 'color-primary')" />
          </span>
        </span>
      </template>
    </UiChoiceGroup>
    <slot />
    <div class="jl-display-choices__row">
      <UiChoiceGroup v-model="posterRatio" :legend="t('settings.poster.ratio')" :options="ratioOptions">
        <template #preview="{ value }">
          <span class="jl-ratio" :style="{ aspectRatio: posterRatios[value] }" aria-hidden="true" />
        </template>
      </UiChoiceGroup>
      <UiChoiceGroup v-model="posterSize" :legend="t('settings.poster.size')" :options="sizeOptions" />
    </div>
    <div class="jl-display-choices__row">
      <UiChoiceGroup v-model="view" :legend="t('settings.view')" :options="viewOptions" />
      <UiChoiceGroup v-model="listDensity" :legend="t('settings.listDensity')" :options="listOptions" />
    </div>
  </div>
</template>

<style scoped>
.jl-display-choices {
  display: grid;
  gap: var(--jl-space-6);
}

.jl-display-choices__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-6) var(--jl-space-8);
}

.jl-swatch {
  display: grid;
  place-items: center;
  width: 4.5rem;
  height: 3rem;
  border: 1px solid;
  border-radius: var(--jl-radius-sm);
}

.jl-swatch__card {
  display: grid;
  gap: 4px;
  width: 70%;
  padding: 4px;
  border-radius: 2px;
}

.jl-swatch__line {
  height: 4px;
  border-radius: 2px;
}

.jl-swatch__accent {
  width: 50%;
  height: 4px;
  border-radius: 2px;
}

.jl-ratio {
  display: block;
  height: 2.5rem;
  max-width: 4.5rem;
  border-radius: 2px;
  background: var(--jl-color-text-muted);
}
</style>
