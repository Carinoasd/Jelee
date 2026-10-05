<script setup lang="ts">
import { computed, onMounted, shallowRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useApi } from "@/api";
import { ApiError, networkError } from "@/api/errors";
import UiButton from "@/components/ui/UiButton.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import type { SelectOption } from "@/components/ui/types";
import { errorMessageKey } from "@/features/errors/messages";
import { useToastStore } from "@/stores/toasts";
import type { MediaSourceInfo } from "./api";
import { emptyTrackForm, fromTrackForm, getTrackPreferences, putTrackPreference, toTrackForm, type TrackForm, type TrackPreferences } from "./versions";

// The signed-in user's audio and subtitle preferences for this item (G16.5,
// G20.4): for every version at once or for one version. They only decide
// which original track a native client starts with; this page plays nothing.
const props = defineProps<{ itemId: string; sources: readonly MediaSourceInfo[] }>();
const { t } = useI18n();
const { client } = useApi();
const toasts = useToastStore();
const stored = shallowRef<TrackPreferences | null>(null);
const scope = shallowRef("");
const form = shallowRef<TrackForm>(emptyTrackForm());
const busy = shallowRef(false);
const version = computed(() => scope.value !== "");

function versionName(source: MediaSourceInfo, index: number): string {
  const name = source.version.displayName;
  return name !== "" ? name : t("items.files.version", { n: index + 1 });
}

const scopeOptions = computed<SelectOption[]>(() => [
  { value: "", label: t("versions.tracks.scopeItem") },
  ...props.sources.map((source, index) => ({ value: source.id, label: t("versions.tracks.scopeVersion", { name: versionName(source, index) }) })),
]);
const inherit = computed(() => ({ value: "", label: t("versions.tracks.inherit") }));
const commentaryOptions = computed<SelectOption[]>(() => [
  inherit.value,
  { value: "yes", label: t("versions.tracks.commentaryYes") },
  { value: "no", label: t("versions.tracks.commentaryNo") },
]);
const sdhOptions = computed<SelectOption[]>(() => [inherit.value, { value: "yes", label: t("versions.tracks.sdhYes") }, { value: "no", label: t("versions.tracks.sdhNo") }]);
const modeOptions = computed<SelectOption[]>(() => [
  inherit.value,
  { value: "auto", label: t("versions.tracks.modeAuto") },
  { value: "always", label: t("versions.tracks.modeAlways") },
  { value: "forced", label: t("versions.tracks.modeForced") },
  { value: "off", label: t("versions.tracks.modeOff") },
]);

const selected = computed(() => props.sources.find((source) => source.id === scope.value));
const languageOf = (value: string | undefined) => (value === undefined || value === "" ? t("items.files.unknownLanguage") : value);
const audioOptions = computed<SelectOption[]>(() => {
  const source = selected.value;
  if (!source) {
    return [inherit.value];
  }
  return [
    inherit.value,
    ...source.audioTracks.map((track) => ({ value: "e:" + String(track.index), label: t("versions.tracks.embeddedTrack", { n: track.index, language: languageOf(track.language) }) })),
    ...source.externalTracks
      .filter((track) => track.kind === "audio")
      .map((track) => ({ value: "x:" + track.id, label: t("versions.tracks.externalTrack", { name: track.title ?? track.format, language: languageOf(track.language) }) })),
  ];
});
const subtitleOptions = computed<SelectOption[]>(() => {
  const source = selected.value;
  if (!source) {
    return [inherit.value];
  }
  return [
    inherit.value,
    ...source.subtitleTracks.map((track) => ({ value: "e:" + String(track.index), label: t("versions.tracks.embeddedTrack", { n: track.index, language: languageOf(track.language) }) })),
    ...source.externalTracks
      .filter((track) => track.kind === "subtitle")
      .map((track) => ({ value: "x:" + track.id, label: t("versions.tracks.externalTrack", { name: track.title ?? track.format, language: languageOf(track.language) }) })),
  ];
});

function fill() {
  const value = stored.value;
  const level = scope.value === "" ? value?.item : value?.versions.find((entry) => entry.sourceId === scope.value)?.preference;
  form.value = toTrackForm(level);
}


function failed(error: unknown) {
  toasts.push(errorMessageKey(error instanceof ApiError ? error : networkError(error)), "danger");
}

async function load() {
  try {
    stored.value = await getTrackPreferences(client, props.itemId);
    fill();
  } catch (error: unknown) {
    failed(error);
  }
}

async function save(clear: boolean) {
  busy.value = true;
  try {
    const body = fromTrackForm(clear ? emptyTrackForm() : form.value, version.value);
    stored.value = await putTrackPreference(client, props.itemId, scope.value === "" ? null : scope.value, body);
    fill();
    toasts.push(clear ? "versions.tracks.cleared" : "versions.tracks.saved", "success");
  } catch (error: unknown) {
    failed(error);
  } finally {
    busy.value = false;
  }
}

// Every member is a string; the select options only offer valid values.
function model(key: keyof TrackForm) {
  return computed({
    get: (): string => form.value[key],
    set: (value: string) => {
      form.value = { ...form.value, [key]: value };
    },
  });
}
const audioLanguage = model("audioLanguage");
const audioCommentary = model("audioCommentary");
const audioTrack = model("audioTrack");
const subtitleMode = model("subtitleMode");
const subtitleLanguage = model("subtitleLanguage");
const subtitleSdh = model("subtitleSdh");
const subtitleTrack = model("subtitleTrack");

watch(scope, fill);
onMounted(load);
</script>

<template>
  <section class="jl-tracks" aria-labelledby="tracks-title">
    <h3 id="tracks-title" class="jl-tracks__title">{{ t("versions.tracks.title") }}</h3>
    <p class="jl-tracks__muted">{{ t("versions.tracks.intro") }}</p>
    <form class="jl-tracks__form" @submit.prevent="save(false)">
      <UiSelectField v-model="scope" :label="t('versions.tracks.scope')" :options="scopeOptions" />
      <fieldset class="jl-tracks__group">
        <legend>{{ t("versions.tracks.audio") }}</legend>
        <UiTextField v-model="audioLanguage" :label="t('versions.tracks.language')" :hint="t('versions.tracks.languageHint')" :maxlength="35" />
        <UiSelectField v-model="audioCommentary" :label="t('versions.tracks.commentary')" :options="commentaryOptions" />
        <UiSelectField v-if="version" v-model="audioTrack" :label="t('versions.tracks.track')" :options="audioOptions" />
      </fieldset>
      <fieldset class="jl-tracks__group">
        <legend>{{ t("versions.tracks.subtitles") }}</legend>
        <UiSelectField v-model="subtitleMode" :label="t('versions.tracks.mode')" :options="modeOptions" :hint="t('versions.tracks.modeHint')" />
        <UiTextField v-model="subtitleLanguage" :label="t('versions.tracks.language')" :hint="t('versions.tracks.languageHint')" :maxlength="35" />
        <UiSelectField v-model="subtitleSdh" :label="t('versions.tracks.sdh')" :options="sdhOptions" />
        <UiSelectField v-if="version" v-model="subtitleTrack" :label="t('versions.tracks.track')" :options="subtitleOptions" />
      </fieldset>
      <div class="jl-tracks__actions">
        <UiButton type="submit" :busy="busy">{{ t("versions.tracks.save") }}</UiButton>
        <UiButton variant="secondary" :disabled="busy" @click="save(true)">{{ t("versions.tracks.clear") }}</UiButton>
      </div>
    </form>
  </section>
</template>

<style scoped>
.jl-tracks {
  display: grid;
  gap: var(--jl-space-2);
  margin-top: var(--jl-space-6);
}

.jl-tracks__title {
  margin: 0;
  font-size: var(--jl-font-size-md);
}

.jl-tracks__muted {
  margin: 0;
  color: var(--jl-color-text-muted);
}

.jl-tracks__form {
  display: grid;
  gap: var(--jl-space-3);
  max-width: 36rem;
}

.jl-tracks__group {
  display: grid;
  gap: var(--jl-space-2);
  margin: 0;
  padding: var(--jl-space-3);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
}

.jl-tracks__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
}
</style>
