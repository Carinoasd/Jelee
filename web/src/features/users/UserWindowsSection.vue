<script setup lang="ts">
import { computed, nextTick, shallowRef, useTemplateRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import type { SelectOption } from "@/components/ui/types";
import { useAccessPolicyStore } from "@/stores/accessPolicy";
import { useUserAdminContentStore } from "@/stores/userAdminContent";
import type { ContentAccessView } from "./api";
import { useAdminFeedback } from "./feedback";
import {
  maxWindows,
  newWindowDraft,
  weekdays,
  windowDraftOf,
  windowProblems,
  windowsBody,
  windowsOf,
  windowValid,
  type WindowDraft,
  type WindowMode,
} from "./windows";

// Restricted time windows of one account (G48.7): inside a window the
// account sees nothing, or only items up to a rating ceiling. Item rules
// that allow an item do not reach past a window.
const props = defineProps<{ access: ContentAccessView }>();
const { t } = useI18n();
const store = useUserAdminContentStore();
const policy = useAccessPolicyStore();
const feedback = useAdminFeedback();
const root = useTemplateRef<HTMLElement>("root");

const drafts = shallowRef<WindowDraft[]>([]);
const submitted = shallowRef(false);

// Only a change of the stored windows resets the list, so saving the other
// content settings keeps unsaved edits here.
watch(
  () => JSON.stringify(windowsOf(props.access.windows)),
  () => {
    drafts.value = props.access.windows.map(windowDraftOf);
    submitted.value = false;
  },
  { immediate: true },
);

void policy.ensureRatings();

/** Catalog keys of the weekday names, 0 is Sunday; full literals for the i18n gate. */
const weekdayKey: readonly string[] = [
  "contentRules.windows.day.sun",
  "contentRules.windows.day.mon",
  "contentRules.windows.day.tue",
  "contentRules.windows.day.wed",
  "contentRules.windows.day.thu",
  "contentRules.windows.day.fri",
  "contentRules.windows.day.sat",
];

const modeOptions = computed<SelectOption[]>(() => [
  { value: "hide", label: t("contentRules.windows.modeHide") },
  { value: "ceiling", label: t("contentRules.windows.modeCeiling") },
]);

const ceilingOptions = computed<SelectOption[]>(() => {
  const options: SelectOption[] = [{ value: "", label: t("contentRules.windows.ceilingNone") }];
  for (const level of policy.levels) {
    options.push({ value: String(level.level), label: t("users.content.ceilingOption", { level: level.level, codes: level.codes.join(", ") }) });
  }
  for (const draft of drafts.value) {
    if (draft.ratingMax !== "" && !options.some((option) => option.value === draft.ratingMax)) {
      options.push({ value: draft.ratingMax, label: t("users.content.ceilingAge", { level: draft.ratingMax }) });
    }
  }
  return options;
});

const changed = computed(() => JSON.stringify(windowsBody(drafts.value)) !== JSON.stringify(windowsOf(props.access.windows)));
const valid = computed(() => drafts.value.every(windowValid));

function edit(key: number, change: Partial<Omit<WindowDraft, "key">>) {
  drafts.value = drafts.value.map((draft) => (draft.key === key ? { ...draft, ...change } : draft));
}

function setDay(draft: WindowDraft, day: number, on: boolean) {
  const days = draft.weekdays.filter((entry) => entry !== day);
  edit(draft.key, { weekdays: on ? [...days, day].sort((a, b) => a - b) : days });
}

function setMode(draft: WindowDraft, value: string) {
  if (value === "hide" || value === "ceiling") {
    edit(draft.key, { mode: value satisfies WindowMode });
  }
}

async function add() {
  const draft = newWindowDraft();
  drafts.value = [...drafts.value, draft];
  await nextTick();
  root.value?.querySelector<HTMLElement>(`[data-window="${draft.key}"] input`)?.focus();
}

async function remove(key: number) {
  drafts.value = drafts.value.filter((draft) => draft.key !== key);
  await nextTick();
  root.value?.querySelector<HTMLElement>("[data-add-window] button")?.focus();
}

function problem(draft: WindowDraft, field: keyof ReturnType<typeof windowProblems>): string | null {
  if (!submitted.value) {
    return null;
  }
  const key = windowProblems(draft)[field];
  return key === null ? null : t(key);
}

async function save() {
  submitted.value = true;
  if (!valid.value) {
    await nextTick();
    root.value?.querySelector<HTMLElement>("[aria-invalid='true']")?.focus();
    return;
  }
  try {
    await store.saveWindows(windowsBody(drafts.value));
    await feedback.done("done", "contentRules.windows.saved");
  } catch (error: unknown) {
    feedback.failed(error);
  }
}
</script>

<template>
  <section ref="root" class="jl-card" aria-labelledby="user-windows-title">
    <h2 id="user-windows-title">{{ t("contentRules.windows.title") }}</h2>
    <div class="jl-card__impact">
      <p>{{ t("contentRules.windows.impact") }}</p>
      <p>{{ t("contentRules.windows.daysNote") }}</p>
    </div>

    <p v-if="drafts.length === 0" class="jl-card__muted">{{ t("contentRules.windows.none") }}</p>
    <ol v-else class="jl-windows">
      <li v-for="(draft, index) in drafts" :key="draft.key" class="jl-windows__item" :data-window="draft.key">
        <fieldset class="jl-windows__days">
          <legend>{{ t("contentRules.windows.days", { n: index + 1 }) }}</legend>
          <UiCheckbox
            v-for="day in weekdays"
            :key="day"
            :model-value="draft.weekdays.includes(day)"
            :label="t(weekdayKey[day] ?? '')"
            @update:model-value="setDay(draft, day, $event)"
          />
          <p class="jl-card__muted jl-windows__every">{{ t("contentRules.windows.everyDayHint") }}</p>
        </fieldset>
        <div class="jl-card__fields">
          <UiTextField
            :model-value="draft.start"
            :label="t('contentRules.windows.start', { n: index + 1 })"
            :hint="t('contentRules.windows.clockHint')"
            :error="problem(draft, 'start')"
            inputmode="numeric"
            :maxlength="5"
            autocomplete="off"
            @update:model-value="edit(draft.key, { start: $event })"
          />
          <UiTextField
            :model-value="draft.end"
            :label="t('contentRules.windows.end', { n: index + 1 })"
            :hint="t('contentRules.windows.endHint')"
            :error="problem(draft, 'end')"
            inputmode="numeric"
            :maxlength="5"
            autocomplete="off"
            @update:model-value="edit(draft.key, { end: $event })"
          />
          <UiTextField
            :model-value="draft.timeZone"
            :label="t('contentRules.windows.timeZone', { n: index + 1 })"
            :hint="t('contentRules.windows.timeZoneHint')"
            :error="problem(draft, 'timeZone')"
            autocomplete="off"
            @update:model-value="edit(draft.key, { timeZone: $event })"
          />
          <UiSelectField
            :model-value="draft.mode"
            :label="t('contentRules.windows.mode', { n: index + 1 })"
            :options="modeOptions"
            @update:model-value="setMode(draft, $event)"
          />
          <div v-if="draft.mode === 'ceiling'" class="jl-windows__ceiling">
            <UiSelectField
              :model-value="draft.ratingMax"
              :label="t('contentRules.windows.ceiling', { n: index + 1 })"
              :options="ceilingOptions"
              :hint="t('contentRules.windows.ceilingHint')"
              @update:model-value="edit(draft.key, { ratingMax: $event })"
            />
            <p v-if="problem(draft, 'ratingMax')" class="jl-windows__error" role="alert">{{ problem(draft, "ratingMax") }}</p>
          </div>
        </div>
        <div class="jl-card__actions">
          <UiButton variant="ghost" :aria-label="t('contentRules.windows.removeLabel', { n: index + 1 })" @click="remove(draft.key)">
            {{ t("users.content.remove") }}
          </UiButton>
        </div>
      </li>
    </ol>

    <div class="jl-card__actions">
      <span data-add-window>
        <UiButton variant="secondary" :disabled="drafts.length >= maxWindows" @click="add">{{ t("contentRules.windows.add") }}</UiButton>
      </span>
      <span v-if="drafts.length >= maxWindows" class="jl-card__muted">{{ t("contentRules.windows.limit", { max: maxWindows }) }}</span>
    </div>
    <p class="jl-card__muted">{{ t("contentRules.windows.saveNote") }}</p>
    <div class="jl-card__actions">
      <UiButton :disabled="!changed" :busy="store.savingWindows" @click="save">{{ t("contentRules.windows.save") }}</UiButton>
    </div>
  </section>
</template>

<style scoped src="./sections.css"></style>
<style scoped>
.jl-windows {
  display: grid;
  gap: var(--jl-space-4);
  margin: 0;
  padding: 0;
  list-style: none;
}

.jl-windows__item {
  display: grid;
  gap: var(--jl-space-3);
  padding: var(--jl-space-4);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
}

.jl-windows__days {
  display: flex;
  flex-wrap: wrap;
  gap: 0 var(--jl-space-4);
  margin: 0;
  padding: 0;
  border: 0;
}

.jl-windows__days legend {
  margin-bottom: var(--jl-space-2);
  padding: 0;
  font-weight: 600;
}

.jl-windows__every {
  flex-basis: 100%;
}

.jl-windows__ceiling {
  display: grid;
  gap: var(--jl-space-1);
}

.jl-windows__error {
  margin: 0;
  color: var(--jl-color-danger);
  font-size: var(--jl-font-size-sm);
}
</style>
