<script setup lang="ts">
import { computed, nextTick, onMounted, shallowRef, useTemplateRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ApiError, networkError } from "@/api/errors";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiErrorState from "@/components/ui/UiErrorState.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import type { SelectOption } from "@/components/ui/types";
import { useAccessMatrixStore } from "@/stores/accessMatrix";
import { useToastStore } from "@/stores/toasts";
import AccessChangeDialog from "./AccessChangeDialog.vue";
import AccessTabs from "./AccessTabs.vue";
import AccessTemplatesSection from "./AccessTemplatesSection.vue";
import { maxBulkUsers, type AccessChangePreview, type AccessTemplate, type GrantOperation, type MatrixUser } from "./api";
import { allOn, cellChanged, grantDiffs, grantOperations, grantsOf, withCell, withCells, type Grants } from "./matrix";

// User × library grant matrix (G48.7). Ticks are edited locally; a change
// is first previewed by the server (who would see or lose how many items)
// and written only after the confirmation, as one audited bulk change.
const { t } = useI18n();
const store = useAccessMatrixStore();
const toasts = useToastStore();

const draft = shallowRef<Grants>(new Map());
const selected = shallowRef<ReadonlySet<string>>(new Set());
const templateId = shallowRef("");

type PendingChange =
  | { readonly kind: "grants"; readonly operations: readonly GrantOperation[] }
  | { readonly kind: "template"; readonly template: AccessTemplate; readonly userIds: readonly string[] };
const pending = shallowRef<PendingChange | null>(null);
const preview = shallowRef<AccessChangePreview | null>(null);
/** Failure of the last preview (shown in the toolbar) or apply (shown in the dialog). */
const changeError = shallowRef<ApiError | null>(null);
const toolbar = useTemplateRef<HTMLElement>("toolbar");

const users = computed<readonly MatrixUser[]>(() => store.matrix?.users ?? []);
const libraries = computed(() => store.matrix?.libraries ?? []);
const libraryIds = computed(() => libraries.value.map((library) => library.libraryId));
const libraryNames = computed(() => new Map(libraries.value.map((library) => [library.libraryId, library.name])));
const server = computed<Grants>(() => (store.matrix === null ? new Map() : grantsOf(store.matrix)));

watch(
  () => store.matrix,
  (matrix) => {
    draft.value = matrix === null ? new Map() : grantsOf(matrix);
    const ids = new Set(matrix?.users.map((user) => user.id) ?? []);
    selected.value = new Set([...selected.value].filter((id) => ids.has(id)));
  },
  { immediate: true },
);

onMounted(() => {
  void store.loadMatrix();
  void store.loadTemplates();
});

const diffs = computed(() => grantDiffs(server.value, draft.value, libraryIds.value));
const changedCells = computed(() => diffs.value.reduce((sum, diff) => sum + diff.added.length + diff.removed.length, 0));
const tooMany = computed(() => diffs.value.length > maxBulkUsers);
const locked = computed(() => pending.value !== null || store.applying);

function displayName(user: MatrixUser): string {
  return user.displayName !== undefined && user.displayName !== "" ? user.displayName : user.name;
}

function granted(userId: string, libraryId: string): boolean {
  return draft.value.get(userId)?.has(libraryId) ?? false;
}

function setCell(userId: string, libraryId: string, event: Event) {
  draft.value = withCell(draft.value, userId, libraryId, (event.target as HTMLInputElement).checked);
}

function rowFull(userId: string): boolean {
  return allOn(draft.value, [userId], libraryIds.value);
}

function toggleRow(userId: string) {
  draft.value = withCells(draft.value, [userId], libraryIds.value, !rowFull(userId));
}

const userIds = computed(() => users.value.map((user) => user.id));

function columnFull(libraryId: string): boolean {
  return allOn(draft.value, userIds.value, [libraryId]);
}

function toggleColumn(libraryId: string) {
  draft.value = withCells(draft.value, userIds.value, [libraryId], !columnFull(libraryId));
}

function discard() {
  draft.value = server.value;
}

const allSelected = computed(() => users.value.length > 0 && users.value.every((user) => selected.value.has(user.id)));
const someSelected = computed(() => selected.value.size > 0 && !allSelected.value);

function selectAll(event: Event) {
  selected.value = (event.target as HTMLInputElement).checked ? new Set(userIds.value) : new Set();
}

function select(userId: string, event: Event) {
  const next = new Set(selected.value);
  if ((event.target as HTMLInputElement).checked) {
    next.add(userId);
  } else {
    next.delete(userId);
  }
  selected.value = next;
}

const templateOptions = computed<SelectOption[]>(() => [
  { value: "", label: t("accessMatrix.apply.choose") },
  ...store.templates.map((template) => ({ value: template.id, label: template.name })),
]);
const chosenTemplate = computed(() => store.templates.find((template) => template.id === templateId.value) ?? null);
const templateBlocked = computed(() => {
  if (diffs.value.length > 0) {
    return "accessMatrix.apply.unsaved";
  }
  if (selected.value.size === 0) {
    return "accessMatrix.apply.noneSelected";
  }
  if (selected.value.size > maxBulkUsers) {
    return "accessMatrix.apply.tooMany";
  }
  return null;
});

const dialogTitle = computed(() => {
  const change = pending.value;
  if (change === null) {
    return "";
  }
  return change.kind === "grants"
    ? t("accessMatrix.preview.titleGrants")
    : t("accessMatrix.preview.titleTemplate", { template: change.template.name, n: change.userIds.length });
});

async function run(change: PendingChange, apply: boolean): Promise<AccessChangePreview> {
  return change.kind === "grants" ? store.changeGrants(change.operations, !apply) : store.changeByTemplate(change.template.id, change.userIds, !apply);
}

async function startPreview(change: PendingChange) {
  changeError.value = null;
  try {
    preview.value = await run(change, false);
    pending.value = change;
  } catch (error: unknown) {
    changeError.value = error instanceof ApiError ? error : networkError(error);
  }
}

function previewGrants() {
  void startPreview({ kind: "grants", operations: grantOperations(diffs.value) });
}

function previewTemplate() {
  const template = chosenTemplate.value;
  if (template === null || templateBlocked.value !== null) {
    return;
  }
  // Matrix row order, whatever order the rows were selected in.
  void startPreview({ kind: "template", template, userIds: userIds.value.filter((id) => selected.value.has(id)) });
}

async function closeDialog() {
  pending.value = null;
  preview.value = null;
  changeError.value = null;
  await nextTick();
  toolbar.value?.querySelector<HTMLButtonElement>("button:not([disabled])")?.focus();
}

async function confirm() {
  const change = pending.value;
  if (change === null) {
    return;
  }
  changeError.value = null;
  try {
    const result = await run(change, true);
    toasts.push("accessMatrix.applied", "success", { users: result.users, shown: result.shown, hidden: result.hidden });
    if (change.kind === "template") {
      selected.value = new Set();
    }
    pending.value = null;
    preview.value = null;
    await store.loadMatrix();
    await nextTick();
    document.getElementById("access-matrix-title")?.focus();
  } catch (error: unknown) {
    changeError.value = error instanceof ApiError ? error : networkError(error);
  }
}
</script>

<template>
  <section class="jl-matrix-page" aria-labelledby="access-matrix-page-title">
    <h1 id="access-matrix-page-title" tabindex="-1">{{ t("access.title") }}</h1>
    <AccessTabs />

    <section class="jl-card" aria-labelledby="access-matrix-title">
      <h2 id="access-matrix-title" tabindex="-1">{{ t("accessMatrix.title") }}</h2>
      <p class="jl-card__muted">{{ t("accessMatrix.intro") }}</p>
      <p class="jl-card__impact">{{ t("accessMatrix.adminsNote") }}</p>

      <RequestStatus :state="store.matrixState" @retry="store.loadMatrix">
        <template #loading>
          <div aria-hidden="true">
            <UiSkeleton v-for="n in 5" :key="n" class="jl-skeleton-row" />
          </div>
        </template>
        <template #empty>
          <UiEmptyState :title="t('accessMatrix.empty')">
            <p>{{ t("accessMatrix.emptyHint") }}</p>
          </UiEmptyState>
        </template>
        <template #default>
          <div class="jl-matrix">
            <UiAlert v-if="store.matrix?.truncated">
              <p>{{ t("accessMatrix.truncated", { n: users.length }) }}</p>
            </UiAlert>

            <div ref="toolbar" class="jl-matrix__toolbar">
              <p class="jl-matrix__status" role="status">
                {{ diffs.length === 0 ? t("accessMatrix.noChanges") : t("accessMatrix.pending", { users: diffs.length, cells: changedCells }) }}
              </p>
              <p v-if="tooMany" class="jl-matrix__warning" role="alert">{{ t("accessMatrix.tooMany", { n: diffs.length, max: maxBulkUsers }) }}</p>
              <div class="jl-card__actions">
                <UiButton :disabled="diffs.length === 0 || tooMany || locked" :busy="store.applying && pending === null" @click="previewGrants">
                  {{ t("accessMatrix.previewChanges") }}
                </UiButton>
                <UiButton variant="secondary" :disabled="diffs.length === 0 || locked" @click="discard">{{ t("accessMatrix.discard") }}</UiButton>
              </div>

              <div class="jl-matrix__apply">
                <UiSelectField v-model="templateId" :label="t('accessMatrix.apply.template')" :options="templateOptions" :disabled="locked" />
                <UiButton
                  variant="secondary"
                  :disabled="chosenTemplate === null || templateBlocked !== null || locked"
                  aria-describedby="access-matrix-apply-hint"
                  @click="previewTemplate"
                >
                  {{ t("accessMatrix.apply.preview") }}
                </UiButton>
              </div>
              <p id="access-matrix-apply-hint" class="jl-card__muted">
                {{ t("accessMatrix.apply.selected", { n: selected.size }) }}
                <template v-if="templateBlocked !== null"> {{ t(templateBlocked, { max: maxBulkUsers }) }}</template>
                <template v-else> {{ t("accessMatrix.apply.hint") }}</template>
              </p>
              <UiErrorState v-if="changeError && pending === null" :error="changeError" :retryable="false" />
            </div>

            <AccessChangeDialog
              v-if="pending !== null && preview !== null"
              :title="dialogTitle"
              :preview="preview"
              :library-names="libraryNames"
              :busy="store.applying"
              :error="changeError"
              @confirm="confirm"
              @cancel="closeDialog"
            />

            <div class="jl-table-wrap jl-matrix__wrap" role="region" aria-labelledby="access-matrix-caption" tabindex="0">
              <table class="jl-table jl-matrix__table">
                <caption id="access-matrix-caption" class="jl-visually-hidden">{{ t("accessMatrix.caption") }}</caption>
                <thead>
                  <tr>
                    <th scope="col" class="jl-matrix__select">
                      <input
                        type="checkbox"
                        :checked="allSelected"
                        :indeterminate="someSelected"
                        :disabled="locked"
                        :aria-label="t('accessMatrix.selectAll')"
                        @change="selectAll"
                      />
                    </th>
                    <th scope="col">{{ t("accessMatrix.user") }}</th>
                    <th v-for="library in libraries" :key="library.libraryId" scope="col" class="jl-matrix__library">
                      <span :id="`access-matrix-lib-${library.libraryId}`">{{ library.name }}</span>
                      <UiButton
                        variant="ghost"
                        :disabled="locked"
                        :aria-label="columnFull(library.libraryId) ? t('accessMatrix.clearColumn', { library: library.name }) : t('accessMatrix.fillColumn', { library: library.name })"
                        @click="toggleColumn(library.libraryId)"
                      >
                        {{ columnFull(library.libraryId) ? t("accessMatrix.none") : t("accessMatrix.all") }}
                      </UiButton>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="user in users" :key="user.id" :class="{ 'jl-matrix__row--selected': selected.has(user.id) }">
                    <td class="jl-matrix__select">
                      <input
                        type="checkbox"
                        :checked="selected.has(user.id)"
                        :disabled="locked"
                        :aria-label="t('accessMatrix.selectUser', { user: displayName(user) })"
                        @change="select(user.id, $event)"
                      />
                    </td>
                    <th :id="`access-matrix-user-${user.id}`" scope="row" class="jl-matrix__user">
                      <span class="jl-matrix__name">{{ displayName(user) }}</span>
                      <span v-if="displayName(user) !== user.name" class="jl-matrix__login">{{ user.name }}</span>
                      <UiBadge v-if="user.admin" tone="accent">{{ t("accessMatrix.adminBadge") }}</UiBadge>
                      <UiBadge v-if="user.disabled">{{ t("accessMatrix.disabledBadge") }}</UiBadge>
                      <UiButton
                        variant="ghost"
                        :disabled="locked || libraries.length === 0"
                        :aria-label="rowFull(user.id) ? t('accessMatrix.clearRow', { user: displayName(user) }) : t('accessMatrix.fillRow', { user: displayName(user) })"
                        @click="toggleRow(user.id)"
                      >
                        {{ rowFull(user.id) ? t("accessMatrix.none") : t("accessMatrix.all") }}
                      </UiButton>
                    </th>
                    <td
                      v-for="library in libraries"
                      :key="library.libraryId"
                      class="jl-matrix__cell"
                      :class="{ 'jl-matrix__cell--changed': cellChanged(server, draft, user.id, library.libraryId) }"
                    >
                      <input
                        type="checkbox"
                        :checked="granted(user.id, library.libraryId)"
                        :disabled="locked"
                        :aria-label="t('accessMatrix.cell', { user: displayName(user), library: library.name })"
                        @change="setCell(user.id, library.libraryId, $event)"
                      />
                      <span v-if="cellChanged(server, draft, user.id, library.libraryId)" class="jl-matrix__mark">
                        <span aria-hidden="true">●</span>
                        <span class="jl-visually-hidden">{{ t("accessMatrix.changed") }}</span>
                      </span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </template>
      </RequestStatus>
    </section>

    <AccessTemplatesSection :libraries="libraries" />
  </section>
</template>

<style scoped src="../users/sections.css"></style>
<style scoped>
.jl-matrix-page {
  display: grid;
  gap: var(--jl-space-6);
  min-width: 0;
}

.jl-matrix-page > * {
  min-width: 0;
}

.jl-matrix-page h1 {
  margin: 0;
}

.jl-card > p {
  margin: 0;
}

.jl-matrix {
  display: grid;
  gap: var(--jl-space-4);
  min-width: 0;
}

.jl-matrix__toolbar {
  display: grid;
  gap: var(--jl-space-3);
}

.jl-matrix__status {
  margin: 0;
  font-weight: 600;
}

.jl-matrix__warning {
  margin: 0;
  color: var(--jl-color-danger);
  font-size: var(--jl-font-size-sm);
}

.jl-matrix__apply {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: var(--jl-space-2);
}

.jl-matrix__apply > :first-child {
  flex: 0 1 20rem;
}

.jl-matrix__wrap {
  max-width: 100%;
}

.jl-matrix__wrap:focus-visible {
  outline: 2px solid var(--jl-color-primary);
  outline-offset: 2px;
}

.jl-matrix__table {
  width: auto;
  min-width: 100%;
}

.jl-matrix__table input[type="checkbox"] {
  width: 20px;
  height: 20px;
  margin: 0;
  accent-color: var(--jl-color-primary);
}

.jl-matrix__select {
  width: 1%;
}

.jl-matrix__library {
  min-width: 7rem;
  text-align: center;
}

.jl-matrix__library > span {
  display: block;
}

.jl-matrix__user {
  min-width: 12rem;
}

.jl-matrix__name {
  font-weight: 600;
}

.jl-matrix__login {
  display: block;
  color: var(--jl-color-text-muted);
  font-weight: 400;
}

.jl-matrix__cell {
  text-align: center;
  white-space: nowrap;
}

.jl-matrix__cell--changed {
  background: var(--jl-color-info-bg);
  box-shadow: inset 0 0 0 2px var(--jl-color-primary);
}

.jl-matrix__mark {
  margin-inline-start: var(--jl-space-1);
  color: var(--jl-color-primary);
  font-size: var(--jl-font-size-sm);
}

.jl-matrix__row--selected > * {
  background: var(--jl-color-badge-bg);
}
</style>
