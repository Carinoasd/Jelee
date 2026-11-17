<script setup lang="ts">
import { computed, shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import { useApi } from "@/api";
import { ApiError, networkError } from "@/api/errors";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import type { SelectOption } from "@/components/ui/types";
import { errorMessageKey } from "@/features/errors/messages";
import { useToastStore } from "@/stores/toasts";
import { checkItemSources, type MediaSourceInfo, type PlaybackDecision, type SourcesCheck } from "./api";
import { emptyCustomForm, presets, toCapabilities, trackRows, type CustomForm, type PresetId, type TrackDelivery } from "./directCheck";

// Why a version would or would not play as it is, for a declared client
// (G10.5). Read only: the server answers with verdicts and reason codes and
// starts nothing; this page plays nothing and no conversion is ever offered.
const props = defineProps<{ itemId: string; sources: readonly MediaSourceInfo[] }>();
const { t } = useI18n();
const { client } = useApi();
const toasts = useToastStore();
const preset = shallowRef<PresetId>("browser");
const form = shallowRef<CustomForm>(emptyCustomForm());
const busy = shallowRef(false);
const result = shallowRef<SourcesCheck | null>(null);
const invalid = shallowRef(false);

const presetOptions = computed<SelectOption[]>(() => [
  { value: "browser", label: t("versions.check.presetBrowser") },
  { value: "android", label: t("versions.check.presetAndroid") },
  { value: "apple", label: t("versions.check.presetApple") },
  { value: "custom", label: t("versions.check.presetCustom") },
]);

const reasonKey: Record<string, string> = {
  container_unsupported: "versions.check.reasonContainer",
  source_not_probed: "versions.check.reasonNotProbed",
  video_codec_unsupported: "versions.check.reasonVideo",
  audio_codec_unsupported: "versions.check.reasonAudio",
  bitrate_exceeds_client: "versions.check.reasonBitrate",
};
const trackReasonKey: Record<string, string> = {
  audio_codec_unsupported: "versions.check.trackReasonAudio",
  subtitle_format_unsupported: "versions.check.trackReasonSubtitle",
  track_not_probed: "versions.check.trackReasonNotProbed",
};
const deliveryKey: Record<TrackDelivery, string> = {
  embedded: "versions.check.deliveryEmbedded",
  separate: "versions.check.deliverySeparate",
  unreadable: "versions.check.deliveryUnreadable",
  unprobed: "versions.check.deliveryUnprobed",
  failed: "versions.check.deliveryFailed",
};
const layoutKey: Record<string, string> = {
  moov_before_mdat: "versions.check.layoutFaststart",
  moov_at_end: "versions.check.layoutMoovAtEnd",
  fragmented: "versions.check.layoutFragmented",
  cues_before_clusters: "versions.check.layoutCuesFirst",
  cues_after_clusters: "versions.check.layoutCuesLast",
  no_cues: "versions.check.layoutNoCues",
  unknown: "versions.check.layoutUnknown",
};
const hintKey: Record<string, string> = {
  faststart_recommended: "versions.panel.hintFaststart",
  no_seek_index: "versions.panel.hintNoSeekIndex",
};

function versionName(source: MediaSourceInfo, index: number): string {
  const name = source.version.displayName;
  return name !== "" ? name : t("items.files.version", { n: index + 1 });
}
const language = (value: string | undefined) => (value === undefined || value === "" ? t("items.files.unknownLanguage") : value);
const decisionOf = (source: MediaSourceInfo): PlaybackDecision | undefined => result.value?.decisions.find((decision) => decision.sourceId === source.id);
const reasonsOf = (decision: PlaybackDecision | undefined) => (decision?.reasons ?? []).filter((reason) => reason in reasonKey).map((reason) => reasonKey[reason] as string);
const hintsOf = (source: MediaSourceInfo) => (source.layoutHints ?? []).filter((hint) => hint in hintKey).map((hint) => hintKey[hint] as string);

async function run() {
  const capabilities = preset.value === "custom" ? toCapabilities(form.value) : presets[preset.value];
  invalid.value = capabilities === null;
  if (capabilities === null) {
    return;
  }
  busy.value = true;
  try {
    result.value = await checkItemSources(client, props.itemId, capabilities);
  } catch (error: unknown) {
    toasts.push(errorMessageKey(error instanceof ApiError ? error : networkError(error)), "danger");
  } finally {
    busy.value = false;
  }
}
function setField(field: keyof CustomForm, value: string) {
  form.value = { ...form.value, [field]: value };
}
</script>

<template>
  <section class="jl-check" aria-labelledby="direct-check-title" data-testid="direct-check">
    <h3 id="direct-check-title" class="jl-check__title">{{ t("versions.check.title") }}</h3>
    <p class="jl-check__muted">{{ t("versions.check.intro") }}</p>
    <div class="jl-check__form">
      <UiSelectField v-model="preset" :label="t('versions.check.client')" :options="presetOptions" />
      <UiButton :busy="busy" @click="run">{{ t("versions.check.run") }}</UiButton>
    </div>
    <div v-if="preset === 'custom'" class="jl-check__custom">
      <UiTextField :model-value="form.containers" :label="t('versions.check.containers')" :hint="t('versions.check.listHint')" :maxlength="1024" @update:model-value="setField('containers', $event)" />
      <UiTextField :model-value="form.videoCodecs" :label="t('versions.check.videoCodecs')" :maxlength="1024" @update:model-value="setField('videoCodecs', $event)" />
      <UiTextField :model-value="form.audioCodecs" :label="t('versions.check.audioCodecs')" :maxlength="1024" @update:model-value="setField('audioCodecs', $event)" />
      <UiTextField :model-value="form.subtitleFormats" :label="t('versions.check.subtitleFormats')" :maxlength="1024" @update:model-value="setField('subtitleFormats', $event)" />
      <UiTextField :model-value="form.maxMbit" :label="t('versions.check.maxBitrate')" :hint="t('versions.check.maxBitrateHint')" :maxlength="12" @update:model-value="setField('maxMbit', $event)" />
    </div>
    <p v-if="invalid" class="jl-check__error" role="alert">{{ t("versions.check.invalid") }}</p>

    <div v-if="result" class="jl-check__results" aria-live="polite">
      <p v-if="result.directPlayable" class="jl-check__muted">{{ t("versions.check.someDirect") }}</p>
      <p v-else class="jl-check__muted">{{ t("versions.check.noneDirect") }}</p>
      <article v-for="(source, index) in result.sources" :key="source.id" class="jl-check__source" :data-source="source.id">
        <h4 class="jl-check__name">
          {{ versionName(source, index) }}
          <UiBadge v-if="decisionOf(source)?.directPlay" tone="accent">{{ t("versions.check.direct") }}</UiBadge>
          <UiBadge v-else tone="danger">{{ t("versions.check.notDirect") }}</UiBadge>
        </h4>
        <ul v-if="reasonsOf(decisionOf(source)).length > 0" class="jl-check__list" data-testid="check-reasons">
          <li v-for="key in reasonsOf(decisionOf(source))" :key="key">{{ t(key) }}</li>
        </ul>
        <p v-if="source.layout" class="jl-check__muted" data-testid="check-layout">{{ t("versions.check.layout") }}: {{ t(layoutKey[source.layout] ?? "versions.check.layoutUnknown") }}</p>
        <p v-for="key in hintsOf(source)" :key="key" class="jl-check__muted" data-testid="check-hint">{{ t(key) }}</p>
        <ul class="jl-check__list" data-testid="check-tracks">
          <li v-for="row in trackRows(source, decisionOf(source))" :key="row.key" :data-delivery="row.delivery">
            <template v-if="row.kind === 'audio'">{{ t("items.files.kindAudio") }}</template><template v-else>{{ t("items.files.kindSubtitle") }}</template>
            <span class="jl-check__muted">(<template v-if="row.external">{{ t("versions.check.external") }}</template><template v-else>{{ t("versions.check.embedded") }}</template>)</span>
            · {{ language(row.language) }} · {{ row.codec }}<template v-if="row.title"> · {{ row.title }}</template>
            <template v-if="row.charset"> · {{ row.charset === "undetermined" ? t("versions.check.charsetUndetermined") : row.charset }}</template>
            <br />
            <span class="jl-check__muted">{{ t(deliveryKey[row.delivery]) }}<template v-if="row.delivery === 'unreadable' && row.reason && row.reason in trackReasonKey"> {{ t(trackReasonKey[row.reason] as string) }}</template></span>
          </li>
        </ul>
      </article>
    </div>
  </section>
</template>

<style scoped>
.jl-check {
  display: grid;
  gap: var(--jl-space-2);
  margin-top: var(--jl-space-4);
}

.jl-check__title {
  margin: 0;
  font-size: var(--jl-font-size-md);
}

.jl-check__muted {
  margin: 0;
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-check__error {
  margin: 0;
  color: var(--jl-color-danger);
}

.jl-check__form {
  display: flex;
  flex-wrap: wrap;
  align-items: end;
  gap: var(--jl-space-2);
}

.jl-check__custom,
.jl-check__results {
  display: grid;
  gap: var(--jl-space-2);
}

.jl-check__source {
  display: grid;
  gap: var(--jl-space-1);
  padding: var(--jl-space-2);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-sm);
}

.jl-check__name {
  margin: 0;
  font-size: var(--jl-font-size-sm);
  overflow-wrap: anywhere;
}

.jl-check__list {
  display: grid;
  gap: var(--jl-space-1);
  margin: 0;
  padding-left: var(--jl-space-4);
}
</style>
