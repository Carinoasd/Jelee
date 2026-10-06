<script setup lang="ts">
import { computed, onMounted, shallowRef, useId, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import { ApiError, networkError } from "@/api/errors";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import type { SelectOption } from "@/components/ui/types";
import { errorMessageKey } from "@/features/errors/messages";
import {
  ceilingText,
  checkKeyword,
  checkTag,
  maxBlockedKeywords,
  maxBlockedTags,
  unratedChoice,
  type UnratedChoice,
} from "@/features/users/form";
import { unratedKey } from "@/features/users/labels";
import TermListEditor from "@/features/users/TermListEditor.vue";
import { useAccessMatrixStore } from "@/stores/accessMatrix";
import { useAccessPolicyStore } from "@/stores/accessPolicy";
import type { AccessTemplate, LibraryGrant } from "./api";
import { templateInput, templateNameProblem } from "./templates";

// Creates or edits one access template. The tag and keyword editors hold
// forms of their own, so this editor is a group with a save button rather
// than a form.
const props = defineProps<{ template: AccessTemplate | null; libraries: readonly LibraryGrant[] }>();
const emit = defineEmits<{ saved: [template: AccessTemplate]; cancel: [] }>();
const { t } = useI18n();
const store = useAccessMatrixStore();
const policy = useAccessPolicyStore();
const root = useTemplateRef<HTMLElement>("root");
const titleId = useId();

const source = props.template;
const name = shallowRef(source?.name ?? "");
const libraryIds = shallowRef<readonly string[]>(source?.libraryIds ?? []);
const ceiling = shallowRef(ceilingText(source?.parentalRatingMax));
const unrated = shallowRef<UnratedChoice>(unratedChoice(source?.blockUnrated));
const tags = shallowRef<readonly string[]>(source?.blockedTags ?? []);
const keywords = shallowRef<readonly string[]>(source?.blockedKeywords ?? []);
const submitted = shallowRef(false);
const saving = shallowRef(false);
const serverError = shallowRef<ApiError | null>(null);

void policy.ensureRatings();

const nameError = computed(() => {
  if (!submitted.value) {
    return null;
  }
  const key = templateNameProblem(name.value, store.templates, source?.id ?? null);
  return key === null ? null : t(key);
});

const ceilingOptions = computed<SelectOption[]>(() => {
  const options: SelectOption[] = [{ value: "", label: t("users.content.noCeiling") }];
  for (const level of policy.levels) {
    options.push({ value: String(level.level), label: t("users.content.ceilingOption", { level: level.level, codes: level.codes.join(", ") }) });
  }
  if (ceiling.value !== "" && !options.some((option) => option.value === ceiling.value)) {
    options.push({ value: ceiling.value, label: t("users.content.ceilingAge", { level: ceiling.value }) });
  }
  return options;
});
const unratedOptions = computed<SelectOption[]>(() =>
  (["policy", "hide", "show"] as const).map((value) => ({ value, label: t(unratedKey[value]) })),
);
const unratedModel = computed({
  get: () => unrated.value,
  set: (value: string) => {
    if (value === "policy" || value === "hide" || value === "show") {
      unrated.value = value;
    }
  },
});

/** Libraries in matrix order, then any granted library the matrix no longer lists. */
const libraryChoices = computed(() => {
  const listed = props.libraries.map((library) => ({ id: library.libraryId, name: library.name }));
  const missing = libraryIds.value.filter((id) => !listed.some((library) => library.id === id)).map((id) => ({ id, name: id }));
  return [...listed, ...missing];
});

function setLibrary(id: string, on: boolean) {
  const rest = libraryIds.value.filter((entry) => entry !== id);
  libraryIds.value = on ? [...rest, id] : rest;
}

function tagProblem(tag: string, existing: readonly string[]): string | null {
  const key = checkTag(tag, existing);
  return key === null ? null : t(key, { max: maxBlockedTags });
}

function keywordProblem(keyword: string, existing: readonly string[]): string | null {
  const key = checkKeyword(keyword, existing);
  return key === null ? null : t(key, { max: maxBlockedKeywords });
}

function serverMessage(error: ApiError): string {
  if (error.code === "conflict") {
    return t("accessMatrix.templates.nameTaken");
  }
  if (error.code === "invalid_request") {
    return t("accessMatrix.templates.invalid");
  }
  return t(errorMessageKey(error));
}

async function save() {
  submitted.value = true;
  serverError.value = null;
  if (templateNameProblem(name.value, store.templates, source?.id ?? null) !== null) {
    root.value?.querySelector<HTMLElement>("[aria-invalid='true']")?.focus();
    return;
  }
  const order = libraryChoices.value.map((library) => library.id);
  const input = templateInput({
    name: name.value,
    libraryIds: order.filter((id) => libraryIds.value.includes(id)),
    ceiling: ceiling.value,
    unrated: unrated.value,
    tags: tags.value,
    keywords: keywords.value,
  });
  saving.value = true;
  try {
    const template = source === null ? await store.create(input) : await store.update(source.id, input);
    emit("saved", template);
  } catch (error: unknown) {
    serverError.value = error instanceof ApiError ? error : networkError(error);
  } finally {
    saving.value = false;
  }
}

onMounted(() => {
  root.value?.querySelector<HTMLElement>("h3")?.focus();
});
</script>

<template>
  <section ref="root" class="jl-template-form" :aria-labelledby="titleId">
    <h3 :id="titleId" tabindex="-1">{{ source === null ? t("accessMatrix.templates.createTitle") : t("accessMatrix.templates.editTitle") }}</h3>
    <UiTextField
      v-model="name"
      :label="t('accessMatrix.templates.name')"
      :hint="t('accessMatrix.templates.nameHint')"
      :error="nameError"
      required
      autocomplete="off"
    />
    <fieldset class="jl-template-form__libraries">
      <legend>{{ t("accessMatrix.templates.libraries") }}</legend>
      <p class="jl-template-form__muted">{{ t("accessMatrix.templates.librariesHint") }}</p>
      <UiCheckbox
        v-for="library in libraryChoices"
        :key="library.id"
        :model-value="libraryIds.includes(library.id)"
        :label="library.name"
        @update:model-value="setLibrary(library.id, $event)"
      />
    </fieldset>
    <div class="jl-template-form__fields">
      <UiSelectField v-model="ceiling" :label="t('users.content.ceiling')" :options="ceilingOptions" />
      <UiSelectField v-model="unratedModel" :label="t('users.content.unrated')" :options="unratedOptions" />
    </div>
    <TermListEditor
      v-model="tags"
      :title="t('users.content.tags')"
      :impact="t('users.content.tagsImpact', { max: maxBlockedTags })"
      :empty-text="t('users.content.noTags')"
      :input-label="t('users.content.newTag')"
      :add-label="t('users.content.addTag')"
      :remove-text="t('users.content.remove')"
      :remove-label="(tag) => t('users.content.removeTag', { tag })"
      :check="tagProblem"
    />
    <TermListEditor
      v-model="keywords"
      :title="t('users.content.keywords')"
      :impact="t('users.content.keywordsImpact', { max: maxBlockedKeywords })"
      :empty-text="t('users.content.noKeywords')"
      :input-label="t('users.content.newKeyword')"
      :add-label="t('users.content.addKeyword')"
      :remove-text="t('users.content.remove')"
      :remove-label="(keyword) => t('users.content.removeKeyword', { keyword })"
      :check="keywordProblem"
    />
    <UiAlert v-if="serverError" tone="danger">
      <p>{{ serverMessage(serverError) }}</p>
      <p v-if="serverError.traceId">{{ t("errors.traceId", { id: serverError.traceId }) }}</p>
    </UiAlert>
    <p class="jl-template-form__muted">{{ t("accessMatrix.templates.saveNote") }}</p>
    <div class="jl-template-form__actions">
      <UiButton :busy="saving" @click="save">{{ source === null ? t("accessMatrix.templates.submitCreate") : t("accessMatrix.templates.submitUpdate") }}</UiButton>
      <UiButton variant="secondary" :disabled="saving" @click="emit('cancel')">{{ t("common.cancel") }}</UiButton>
    </div>
  </section>
</template>

<style scoped>
.jl-template-form {
  display: grid;
  gap: var(--jl-space-4);
  padding: var(--jl-space-4);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
}

.jl-template-form h3 {
  margin: 0;
  font-size: var(--jl-font-size-md);
}

.jl-template-form__libraries {
  display: flex;
  flex-wrap: wrap;
  gap: 0 var(--jl-space-4);
  margin: 0;
  padding: 0;
  border: 0;
}

.jl-template-form__libraries legend {
  margin-bottom: var(--jl-space-2);
  padding: 0;
  font-weight: 600;
}

.jl-template-form__libraries > p {
  flex-basis: 100%;
}

.jl-template-form__fields {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 16rem), 1fr));
  gap: var(--jl-space-4);
}

.jl-template-form__muted {
  margin: 0;
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-template-form__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
}
</style>
