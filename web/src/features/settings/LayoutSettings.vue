<script setup lang="ts">
import { computed, shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import UiButton from "@/components/ui/UiButton.vue";
import UiReorderList from "@/components/ui/UiReorderList.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import {
  builtInPresetIds,
  customPresetLimit,
  presetNameMaxLength,
  useLayoutStore,
  type BuiltInPresetId,
  type DetailPanelId,
  type HomeBlockId,
} from "@/stores/layout";
import { useToastStore } from "@/stores/toasts";

// Layout section of the settings page (G33.5): home blocks and item page
// panels (order and visibility), built-in and saved presets.
const { t } = useI18n();
const layout = useLayoutStore();
const toasts = useToastStore();

const blockKey: Readonly<Record<HomeBlockId, string>> = {
  welcome: "layout.blocks.welcome",
  libraries: "layout.blocks.libraries",
  latest: "layout.blocks.latest",
};
const panelKey: Readonly<Record<DetailPanelId, string>> = {
  overview: "layout.panels.overview",
  genres: "layout.panels.genres",
  externalIds: "layout.panels.externalIds",
  nfo: "layout.panels.nfo",
  files: "layout.panels.files",
  pluginPanels: "layout.panels.pluginPanels",
  pluginTabs: "layout.panels.pluginTabs",
};
const presetKey: Readonly<Record<BuiltInPresetId, string>> = {
  standard: "layout.presets.standard",
  focused: "layout.presets.focused",
  metadata: "layout.presets.metadata",
};

const homeItems = computed(() => layout.layout.home.map((entry) => ({ id: entry.id, label: t(blockKey[entry.id]), visible: entry.visible })));
const detailItems = computed(() => layout.layout.detail.map((entry) => ({ id: entry.id, label: t(panelKey[entry.id]), visible: entry.visible })));

const customValue = "";
const presetOptions = computed(() => [
  ...(layout.activePreset() === null ? [{ value: customValue, label: t("layout.presets.modified") }] : []),
  ...builtInPresetIds.map((id) => ({ value: id, label: t(presetKey[id]) })),
  ...layout.presets.map((preset) => ({ value: preset.id, label: preset.name })),
]);
const preset = computed({
  get: () => layout.activePreset() ?? customValue,
  set: (value: string) => {
    if (value !== customValue && layout.applyPreset(value)) {
      toasts.push("layout.presets.applied", "success");
    }
  },
});
const activeCustom = computed(() => layout.presets.find((entry) => entry.id === layout.activePreset()) ?? null);

const presetName = shallowRef("");
const nameError = shallowRef<string | null>(null);
function savePreset() {
  if (presetName.value.trim() === "") {
    nameError.value = t("layout.presets.nameRequired");
    return;
  }
  if (layout.savePreset(presetName.value) === null) {
    nameError.value = t("layout.presets.full", { max: customPresetLimit });
    return;
  }
  nameError.value = null;
  presetName.value = "";
  toasts.push("layout.presets.saved", "success");
}
</script>

<template>
  <section class="jl-settings__card jl-layout" aria-labelledby="settings-layout">
    <h2 id="settings-layout">{{ t("layout.title") }}</h2>
    <p class="jl-layout__hint">{{ t("layout.storageNote") }}</p>

    <div class="jl-layout__presets">
      <UiSelectField v-model="preset" :label="t('layout.presets.label')" :options="presetOptions" />
      <UiButton v-if="activeCustom" variant="danger" @click="layout.deletePreset(activeCustom.id)">
        {{ t("layout.presets.delete", { name: activeCustom.name }) }}
      </UiButton>
      <UiButton variant="secondary" @click="layout.reset()">{{ t("layout.reset") }}</UiButton>
    </div>
    <form class="jl-layout__save" novalidate @submit.prevent="savePreset">
      <UiTextField v-model="presetName" :label="t('layout.presets.name')" :maxlength="presetNameMaxLength" :error="nameError" />
      <div>
        <UiButton type="submit" variant="secondary">{{ t("layout.presets.save") }}</UiButton>
      </div>
    </form>

    <h3>{{ t("layout.home.blocks") }}</h3>
    <UiReorderList
      :items="homeItems"
      :label="t('layout.home.blocks')"
      toggleable
      @move="(from: number, to: number) => layout.move('home', from, to)"
      @toggle="(id: string, visible: boolean) => layout.setVisible('home', id, visible)"
    />
    <h3>{{ t("layout.detailPanels") }}</h3>
    <UiReorderList
      :items="detailItems"
      :label="t('layout.detailPanels')"
      toggleable
      @move="(from: number, to: number) => layout.move('detail', from, to)"
      @toggle="(id: string, visible: boolean) => layout.setVisible('detail', id, visible)"
    />
  </section>
</template>

<style scoped>
.jl-layout h3 {
  margin: var(--jl-space-2) 0 0;
  font-size: var(--jl-font-size-md);
}

.jl-layout__hint {
  margin: 0;
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}

.jl-layout__presets,
.jl-layout__save {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: var(--jl-space-3);
}
</style>
