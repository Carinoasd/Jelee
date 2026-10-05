<script setup lang="ts">
import { computed, shallowRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import UiButton from "@/components/ui/UiButton.vue";
import UiErrorState from "@/components/ui/UiErrorState.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import type { SelectOption } from "@/components/ui/types";
import { useAccessPolicyStore } from "@/stores/accessPolicy";
import { useUserAdminContentStore } from "@/stores/userAdminContent";
import type { ContentAccessView } from "./api";
import { useAdminFeedback } from "./feedback";
import { ceilingText, checkTag, contentAccessBody, contentAccessOf, maxBlockedTags, unratedChoice, type UnratedChoice } from "./form";
import { unratedKey } from "./labels";

const props = defineProps<{ access: ContentAccessView }>();
const { t } = useI18n();
const store = useUserAdminContentStore();
const policy = useAccessPolicyStore();
const feedback = useAdminFeedback();

const ceiling = shallowRef("");
const unrated = shallowRef<UnratedChoice>("policy");
const tags = shallowRef<readonly string[]>([]);
const newTag = shallowRef("");
const tagProblem = shallowRef<string | null>(null);

watch(
  () => props.access,
  (access) => {
    ceiling.value = ceilingText(access.parentalRatingMax);
    unrated.value = unratedChoice(access.blockUnrated);
    tags.value = access.blockedTags;
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

const body = computed(() => contentAccessBody(ceiling.value, unrated.value, tags.value));
const changed = computed(() => JSON.stringify(body.value) !== JSON.stringify(contentAccessOf(props.access)));

function addTag() {
  tagProblem.value = checkTag(newTag.value, tags.value);
  if (tagProblem.value !== null) {
    return;
  }
  tags.value = [...tags.value, newTag.value.trim()];
  newTag.value = "";
}

function removeTag(tag: string) {
  tags.value = tags.value.filter((entry) => entry !== tag);
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

    <div class="jl-tags">
      <h3 id="user-tags-title">{{ t("users.content.tags") }}</h3>
      <p class="jl-card__muted">{{ t("users.content.tagsImpact", { max: maxBlockedTags }) }}</p>
      <ul v-if="tags.length > 0" class="jl-tags__list" aria-labelledby="user-tags-title">
        <li v-for="tag in tags" :key="tag" class="jl-tags__tag">
          <span>{{ tag }}</span>
          <UiButton variant="ghost" :aria-label="t('users.content.removeTag', { tag })" @click="removeTag(tag)">
            {{ t("users.content.remove") }}
          </UiButton>
        </li>
      </ul>
      <p v-else class="jl-card__muted">{{ t("users.content.noTags") }}</p>
      <form class="jl-tags__add" novalidate @submit.prevent="addTag">
        <UiTextField v-model="newTag" :label="t('users.content.newTag')" :error="tagProblem ? t(tagProblem, { max: maxBlockedTags }) : null" autocomplete="off" />
        <UiButton type="submit" variant="secondary">{{ t("users.content.addTag") }}</UiButton>
      </form>
    </div>

    <p class="jl-card__muted">{{ t("users.content.saveNote") }}</p>
    <div class="jl-card__actions">
      <UiButton :disabled="!changed" :busy="store.saving" @click="save">{{ t("common.save") }}</UiButton>
    </div>
  </section>
</template>

<style scoped src="./sections.css"></style>
<style scoped>
.jl-tags {
  display: grid;
  gap: var(--jl-space-2);
}

.jl-tags h3 {
  margin: 0;
  font-size: var(--jl-font-size-md);
}

.jl-tags__list {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.jl-tags__tag {
  display: inline-flex;
  align-items: center;
  gap: var(--jl-space-1);
  padding-left: var(--jl-space-3);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-pill);
  background: var(--jl-color-badge-bg);
}

.jl-tags__add {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: var(--jl-space-2);
}

.jl-tags__add > :first-child {
  flex: 1 1 16rem;
}
</style>
