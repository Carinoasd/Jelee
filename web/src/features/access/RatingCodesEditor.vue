<script setup lang="ts">
import { computed, nextTick, onMounted, shallowRef, useId, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import UiButton from "@/components/ui/UiButton.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import { useAdminFeedback } from "@/features/users/feedback";
import { useAccessPolicyStore } from "@/stores/accessPolicy";
import { maxRatingCodes, ratingLevelMax, ratingProblems, ratingRow, ratingRowsOf, ratingsBody, type RatingRow } from "./ratings";

// Edits the rating code table (G48.7). The whole table is replaced at once,
// after a confirmation, because every account's ceiling is read through it.
const emit = defineEmits<{ done: [] }>();
const { t } = useI18n();
const store = useAccessPolicyStore();
const feedback = useAdminFeedback();
const root = useTemplateRef<HTMLElement>("root");
const titleId = useId();

const rows = shallowRef<RatingRow[]>(ratingRowsOf(store.levels));
const problems = computed(() => ratingProblems(rows.value));
const levelOptions = Array.from({ length: ratingLevelMax + 1 }, (_, level) => level);

function edit(key: number, change: Partial<Omit<RatingRow, "key">>) {
  rows.value = rows.value.map((row) => (row.key === key ? { ...row, ...change } : row));
}

async function add() {
  const row = ratingRow();
  rows.value = [...rows.value, row];
  await nextTick();
  root.value?.querySelector<HTMLInputElement>(`[data-row="${row.key}"] input`)?.focus();
}

async function remove(key: number) {
  rows.value = rows.value.filter((row) => row.key !== key);
  await nextTick();
  root.value?.querySelector<HTMLElement>("[data-add-code] button")?.focus();
}

function problem(row: RatingRow): string | null {
  const key = problems.value.get(row.key);
  return key === undefined ? null : t(key);
}

async function save() {
  if (problems.value.size > 0) {
    return;
  }
  try {
    await store.saveRatings(ratingsBody(rows.value));
    await feedback.done("done", "contentRules.ratings.saved");
    emit("done");
  } catch (error: unknown) {
    feedback.failed(error);
  }
}

onMounted(() => {
  root.value?.querySelector<HTMLElement>("h3")?.focus();
});
</script>

<template>
  <div ref="root" class="jl-ratings-edit" role="group" :aria-labelledby="titleId">
    <h3 :id="titleId" tabindex="-1">{{ t("contentRules.ratings.editTitle") }}</h3>
    <p class="jl-ratings-edit__muted">{{ t("contentRules.ratings.editIntro", { max: maxRatingCodes }) }}</p>
    <div class="jl-ratings-edit__wrap">
      <table class="jl-ratings-edit__table">
        <caption class="jl-visually-hidden">{{ t("contentRules.ratings.editTitle") }}</caption>
        <thead>
          <tr>
            <th scope="col">{{ t("contentRules.ratings.code") }}</th>
            <th scope="col">{{ t("access.ratings.level") }}</th>
            <th scope="col">{{ t("common.actions") }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(row, index) in rows" :key="row.key" :data-row="row.key">
            <td>
              <input
                class="jl-ratings-edit__input"
                :value="row.code"
                :aria-label="t('contentRules.ratings.codeLabel', { n: index + 1 })"
                :aria-invalid="problem(row) ? 'true' : undefined"
                :aria-describedby="problem(row) ? `rating-problem-${row.key}` : undefined"
                autocomplete="off"
                maxlength="40"
                @input="edit(row.key, { code: ($event.target as HTMLInputElement).value })"
              />
              <p v-if="problem(row)" :id="`rating-problem-${row.key}`" class="jl-ratings-edit__error">{{ problem(row) }}</p>
            </td>
            <td>
              <select
                class="jl-ratings-edit__input"
                :value="row.level"
                :aria-label="t('contentRules.ratings.levelLabel', { n: index + 1 })"
                @change="edit(row.key, { level: ($event.target as HTMLSelectElement).value })"
              >
                <option v-for="level in levelOptions" :key="level" :value="String(level)">{{ t("access.ratings.age", { level }) }}</option>
              </select>
            </td>
            <td>
              <UiButton variant="ghost" :aria-label="t('contentRules.ratings.removeLabel', { n: index + 1 })" @click="remove(row.key)">
                {{ t("users.content.remove") }}
              </UiButton>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-if="rows.length === 0" class="jl-ratings-edit__muted">{{ t("contentRules.ratings.emptyWarning") }}</p>
    <div class="jl-ratings-edit__actions">
      <span data-add-code>
        <UiButton variant="secondary" :disabled="rows.length >= maxRatingCodes" @click="add">{{ t("contentRules.ratings.add") }}</UiButton>
      </span>
      <UiConfirmButton
        variant="secondary"
        :label="t('contentRules.ratings.save')"
        :confirm-label="t('contentRules.ratings.confirm')"
        :prompt="t('contentRules.ratings.impact')"
        :busy="store.savingRatings"
        :disabled="problems.size > 0"
        @confirm="save"
      />
      <UiButton variant="ghost" :disabled="store.savingRatings" @click="emit('done')">{{ t("common.cancel") }}</UiButton>
    </div>
  </div>
</template>

<style scoped>
.jl-ratings-edit {
  display: grid;
  gap: var(--jl-space-3);
  min-width: 0;
  padding: var(--jl-space-4);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
}

.jl-ratings-edit h3 {
  margin: 0;
  font-size: var(--jl-font-size-md);
}

.jl-ratings-edit__muted {
  margin: 0;
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-ratings-edit__wrap {
  max-height: 28rem;
  overflow: auto;
}

.jl-ratings-edit__table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--jl-font-size-sm);
}

.jl-ratings-edit__table th,
.jl-ratings-edit__table td {
  padding: var(--jl-space-1) var(--jl-space-2);
  border-bottom: 1px solid var(--jl-color-border);
  text-align: start;
  vertical-align: top;
}

.jl-ratings-edit__input {
  width: 100%;
  min-width: 6rem;
  min-height: var(--jl-touch-target);
  padding: 0 var(--jl-space-2);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-sm);
  background: var(--jl-color-surface);
  color: var(--jl-color-text);
  font: inherit;
}

.jl-ratings-edit__input[aria-invalid="true"] {
  border-color: var(--jl-color-danger);
}

.jl-ratings-edit__error {
  margin: var(--jl-space-1) 0 0;
  color: var(--jl-color-danger);
}

.jl-ratings-edit__actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-2);
}
</style>
