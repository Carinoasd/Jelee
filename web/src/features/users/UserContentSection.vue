<script setup lang="ts">
import { computed, shallowRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import UiButton from "@/components/ui/UiButton.vue";
import UiErrorState from "@/components/ui/UiErrorState.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import type { SelectOption } from "@/components/ui/types";
import { useAccessPolicyStore } from "@/stores/accessPolicy";
import { useUserAdminContentStore } from "@/stores/userAdminContent";
import type { ContentAccessView } from "./api";
import { useAdminFeedback } from "./feedback";
import {
  ceilingText,
  checkKeyword,
  checkTag,
  contentAccessBody,
  contentAccessOf,
  maxBlockedKeywords,
  maxBlockedTags,
  unratedChoice,
  type UnratedChoice,
} from "./form";
import { unratedKey } from "./labels";
import TermListEditor from "./TermListEditor.vue";

const props = defineProps<{ access: ContentAccessView }>();
const { t } = useI18n();
const store = useUserAdminContentStore();
const policy = useAccessPolicyStore();
const feedback = useAdminFeedback();

const ceiling = shallowRef("");
const unrated = shallowRef<UnratedChoice>("policy");
const tags = shallowRef<readonly string[]>([]);
const keywords = shallowRef<readonly string[]>([]);

// Only a change of these settings resets the form: saving the time windows
// or an item rule replaces the view without touching unsaved edits here.
watch(
  () => JSON.stringify(contentAccessOf(props.access)),
  () => {
    const access = props.access;
    ceiling.value = ceilingText(access.parentalRatingMax);
    unrated.value = unratedChoice(access.blockUnrated);
    tags.value = access.blockedTags;
    keywords.value = access.blockedKeywords;
  },
  { immediate: true },
);

void policy.ensureRatings();

const ceilingOptions = computed<SelectOption[]>(() => {
  const options: SelectOption[] = [{ value: "", label: t("users.content.noCeiling") }];
  for (const level of policy.levels) {
    options.push({
      value: String(level.level),
      label: t("users.content.ceilingOption", { level: level.level, codes: level.codes.join(", ") }),
    });
  }
  const saved = ceilingText(props.access.parentalRatingMax);
  if (saved !== "" && !options.some((option) => option.value === saved)) {
    options.push({ value: saved, label: t("users.content.ceilingAge", { level: saved }) });
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

const body = computed(() => contentAccessBody(ceiling.value, unrated.value, tags.value, keywords.value));
const changed = computed(() => JSON.stringify(body.value) !== JSON.stringify(contentAccessOf(props.access)));

function tagProblem(tag: string, existing: readonly string[]): string | null {
  const key = checkTag(tag, existing);
  return key === null ? null : t(key, { max: maxBlockedTags });
}

function keywordProblem(keyword: string, existing: readonly string[]): string | null {
  const key = checkKeyword(keyword, existing);
  return key === null ? null : t(key, { max: maxBlockedKeywords });
}

async function save() {
  try {
    await store.save(body.value);
    await feedback.done("done", "users.content.saved");
  } catch (error: unknown) {
    feedback.failed(error);
  }
}
</script>

<template>
  <section class="jl-card" aria-labelledby="user-content-title">
    <h2 id="user-content-title">{{ t("users.content.title") }}</h2>
    <div class="jl-card__impact">
      <p>{{ t("users.content.impact") }}</p>
      <p>{{ t("users.content.noPreview") }}</p>
    </div>

    <div class="jl-card__fields">
      <UiSelectField v-model="ceiling" :label="t('users.content.ceiling')" :options="ceilingOptions" :hint="t('users.content.ceilingImpact')" />
      <UiSelectField v-model="unratedModel" :label="t('users.content.unrated')" :options="unratedOptions" :hint="t('users.content.unratedImpact')" />
    </div>
    <p v-if="ceiling === ''" class="jl-card__muted">{{ t("users.content.unratedInactive") }}</p>
    <UiErrorState v-if="policy.ratingsState.status === 'error'" :error="policy.ratingsState.error" @retry="policy.loadRatings" />

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

    <p class="jl-card__muted">{{ t("users.content.saveNote") }}</p>
    <div class="jl-card__actions">
      <UiButton :disabled="!changed" :busy="store.saving" @click="save">{{ t("common.save") }}</UiButton>
    </div>
  </section>
</template>

<style scoped src="./sections.css"></style>
