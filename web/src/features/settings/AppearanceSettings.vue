<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { ApiError, networkError } from "@/api/errors";
import UiChoiceGroup from "@/components/ui/UiChoiceGroup.vue";
import { errorMessageKey } from "@/features/errors/messages";
import type { ItemView } from "@/features/items/labels";
import { useAuthStore } from "@/stores/auth";
import { densities, usePreferencesStore, type Density, type ListDensity, type PosterRatio, type PosterSize, type Theme } from "@/stores/preferences";
import { useToastStore } from "@/stores/toasts";
import DisplayChoices from "./DisplayChoices.vue";

// Theme, density and display choices (G33.2). Each applies at once and is
// stored with the account (stores/preferences.ts); display members the user
// never chose show the site default. The tiles are DisplayChoices, shared
// with the administrator's site defaults.
const { t } = useI18n();
const auth = useAuthStore();
const preferences = usePreferencesStore();
const toasts = useToastStore();

const densityKey: Readonly<Record<Density, string>> = {
  comfortable: "settings.density.comfortable",
  compact: "settings.density.compact",
};
const densityOptions = computed(() => densities.map((value) => ({ value, label: t(densityKey[value]) })));

function failure(error: unknown): ApiError {
  return error instanceof ApiError ? error : networkError(error);
}

async function stored(save: Promise<void>, savedKey: string) {
  try {
    await save;
    if (auth.user !== null) {
      toasts.push(savedKey, "success");
    }
  } catch (error: unknown) {
    toasts.push(errorMessageKey(failure(error)), "danger");
  }
}

// Display choices apply at once; only a failed save is reported.
async function displayStored(save: Promise<void>) {
  try {
    await save;
  } catch (error: unknown) {
    toasts.push(errorMessageKey(failure(error)), "danger");
  }
}

const theme = computed({
  get: () => preferences.theme,
  set: (value: Theme) => void stored(preferences.setTheme(value), "settings.theme.saved"),
});
const density = computed({
  get: () => preferences.density,
  set: (value: Density) => void stored(preferences.setDensity(value), "settings.density.saved"),
});
const posterRatio = computed({
  get: () => preferences.display.posterRatio,
  set: (value: PosterRatio) => {
    void displayStored(preferences.setDisplay({ posterRatio: value }));
  },
});
const posterSize = computed({
  get: () => preferences.display.posterSize,
  set: (value: PosterSize) => {
    void displayStored(preferences.setDisplay({ posterSize: value }));
  },
});
const listDensity = computed({
  get: () => preferences.display.listDensity,
  set: (value: ListDensity) => {
    void displayStored(preferences.setDisplay({ listDensity: value }));
  },
});
const view = computed({
  get: () => preferences.display.view,
  set: (value: ItemView) => {
    void displayStored(preferences.setDisplay({ view: value }));
  },
});
</script>

<template>
  <DisplayChoices v-model:theme="theme" v-model:poster-ratio="posterRatio" v-model:poster-size="posterSize" v-model:list-density="listDensity" v-model:view="view">
    <UiChoiceGroup v-model="density" :legend="t('settings.density.label')" :options="densityOptions" />
  </DisplayChoices>
</template>
