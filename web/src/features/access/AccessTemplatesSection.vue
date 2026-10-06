<script setup lang="ts">
import { computed, nextTick, shallowRef, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { useAdminFeedback } from "@/features/users/feedback";
import { unratedChoice } from "@/features/users/form";
import { unratedKey } from "@/features/users/labels";
import { useAccessMatrixStore } from "@/stores/accessMatrix";
import { useToastStore } from "@/stores/toasts";
import AccessTemplateForm from "./AccessTemplateForm.vue";
import { maxTemplates, type AccessTemplate, type LibraryGrant } from "./api";

// Access templates (G48.7): named sets of library grants and restrictions
// that the matrix applies to the selected accounts.
const props = defineProps<{ libraries: readonly LibraryGrant[] }>();
const { t } = useI18n();
const store = useAccessMatrixStore();
const toasts = useToastStore();
const feedback = useAdminFeedback();
/** The template being edited, "new" while creating, null when the form is closed. */
const editing = shallowRef<AccessTemplate | "new" | null>(null);
const addButton = useTemplateRef<HTMLElement>("addButton");

const libraryNames = computed(() => new Map(props.libraries.map((library) => [library.libraryId, library.name])));

function libraryText(template: AccessTemplate): string {
  if (template.libraryIds.length === 0) {
    return t("accessMatrix.templates.noLibraries");
  }
  return template.libraryIds.map((id) => libraryNames.value.get(id) ?? id).join(", ");
}

function ceiling(template: AccessTemplate): string {
  return template.parentalRatingMax === undefined ? t("users.content.noCeiling") : t("users.content.ceilingAge", { level: template.parentalRatingMax });
}

async function closeForm() {
  editing.value = null;
  await nextTick();
  addButton.value?.querySelector("button")?.focus();
}

async function saved(created: boolean) {
  toasts.push(created ? "accessMatrix.templates.created" : "accessMatrix.templates.updated", "success");
  await closeForm();
}

async function remove(template: AccessTemplate) {
  try {
    await store.remove(template.id);
    toasts.push("accessMatrix.templates.deleted", "success");
  } catch (error: unknown) {
    feedback.failed(error);
  }
}
</script>

<template>
  <section class="jl-card" aria-labelledby="access-templates-title">
    <div class="jl-templates__head">
      <h2 id="access-templates-title">{{ t("accessMatrix.templates.title") }}</h2>
      <span ref="addButton">
        <UiButton v-if="editing === null" :disabled="store.templates.length >= maxTemplates" @click="editing = 'new'">
          {{ t("accessMatrix.templates.add") }}
        </UiButton>
      </span>
    </div>
    <p class="jl-card__muted">{{ t("accessMatrix.templates.intro") }}</p>

    <AccessTemplateForm
      v-if="editing !== null"
      :key="editing === 'new' ? 'new' : editing.id"
      :template="editing === 'new' ? null : editing"
      :libraries="libraries"
      @saved="saved(editing === 'new')"
      @cancel="closeForm"
    />

    <RequestStatus :state="store.templatesState" @retry="store.loadTemplates">
      <template #loading>
        <div aria-hidden="true">
          <UiSkeleton v-for="n in 2" :key="n" class="jl-skeleton-row" />
        </div>
      </template>
      <template #empty>
        <UiEmptyState :title="t('accessMatrix.templates.empty')">
          <p>{{ t("accessMatrix.templates.emptyHint") }}</p>
        </UiEmptyState>
      </template>
      <template #default>
        <div class="jl-table-wrap">
          <table class="jl-table">
            <caption class="jl-visually-hidden">{{ t("accessMatrix.templates.title") }}</caption>
            <thead>
              <tr>
                <th scope="col">{{ t("accessMatrix.templates.name") }}</th>
                <th scope="col">{{ t("accessMatrix.templates.libraries") }}</th>
                <th scope="col">{{ t("users.content.ceiling") }}</th>
                <th scope="col">{{ t("users.content.unrated") }}</th>
                <th scope="col">{{ t("accessMatrix.templates.blocks") }}</th>
                <th scope="col">{{ t("common.actions") }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="template in store.templates" :key="template.id">
                <th :id="`access-template-${template.id}`" scope="row">{{ template.name }}</th>
                <td>{{ libraryText(template) }}</td>
                <td>{{ ceiling(template) }}</td>
                <td>{{ t(unratedKey[unratedChoice(template.blockUnrated)]) }}</td>
                <td>{{ t("accessMatrix.templates.blockCounts", { tags: template.blockedTags.length, keywords: template.blockedKeywords.length }) }}</td>
                <td>
                  <div class="jl-card__actions">
                    <UiButton variant="ghost" :aria-describedby="`access-template-${template.id}`" @click="editing = template">{{ t("common.edit") }}</UiButton>
                    <UiConfirmButton
                      :label="t('common.delete')"
                      :confirm-label="t('accessMatrix.templates.deleteConfirm')"
                      :prompt="t('accessMatrix.templates.deletePrompt')"
                      :busy="store.pending.has(template.id)"
                      :describedby="`access-template-${template.id}`"
                      @confirm="remove(template)"
                    />
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </RequestStatus>
  </section>
</template>

<style scoped src="../users/sections.css"></style>
<style scoped>
.jl-card {
  min-width: 0;
}

.jl-templates__head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--jl-space-3);
}

.jl-templates__head h2 {
  margin: 0;
  font-size: var(--jl-font-size-lg);
}
</style>
