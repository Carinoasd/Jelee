<script setup lang="ts">
import { onMounted, useId, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import type { ApiError } from "@/api/errors";
import UiButton from "@/components/ui/UiButton.vue";
import UiErrorState from "@/components/ui/UiErrorState.vue";
import type { AccessChangePreview } from "./api";

// Confirmation of a previewed access change (G48.7): what the server reports
// the change would do, account by account. Nothing is written until the
// apply button is pressed; Escape or Cancel backs out.
const props = defineProps<{
  title: string;
  preview: AccessChangePreview;
  libraryNames: ReadonlyMap<string, string>;
  busy: boolean;
  error: ApiError | null;
}>();
const emit = defineEmits<{ confirm: []; cancel: [] }>();
const { t } = useI18n();
const titleId = useId();
const summaryId = useId();
const root = useTemplateRef<HTMLElement>("root");

function names(ids: readonly string[]): string {
  return ids.length === 0 ? "—" : ids.map((id) => props.libraryNames.get(id) ?? id).join(", ");
}

onMounted(() => {
  root.value?.querySelector<HTMLElement>("h2")?.focus();
});
</script>

<template>
  <div ref="root" class="jl-change" role="dialog" :aria-labelledby="titleId" :aria-describedby="summaryId" @keydown.esc.stop="emit('cancel')">
    <h2 :id="titleId" tabindex="-1">{{ title }}</h2>
    <dl :id="summaryId" class="jl-change__summary">
      <div>
        <dt>{{ t("accessMatrix.preview.users") }}</dt>
        <dd data-count="users">{{ preview.users }}</dd>
      </div>
      <div>
        <dt>{{ t("accessMatrix.preview.items") }}</dt>
        <dd data-count="items">{{ preview.items }}</dd>
      </div>
      <div>
        <dt>{{ t("accessMatrix.preview.shown") }}</dt>
        <dd data-count="shown">+{{ preview.shown }}</dd>
      </div>
      <div>
        <dt>{{ t("accessMatrix.preview.hidden") }}</dt>
        <dd data-count="hidden">−{{ preview.hidden }}</dd>
      </div>
    </dl>
    <p class="jl-change__muted">{{ t("accessMatrix.preview.countsNote") }}</p>

    <p v-if="preview.changes.length === 0" class="jl-change__muted">{{ t("accessMatrix.preview.noEffect") }}</p>
    <div v-else class="jl-change__wrap">
      <table class="jl-change__table">
        <caption>{{ t("accessMatrix.preview.perUser") }}</caption>
        <thead>
          <tr>
            <th scope="col">{{ t("accessMatrix.preview.account") }}</th>
            <th scope="col">{{ t("accessMatrix.preview.added") }}</th>
            <th scope="col">{{ t("accessMatrix.preview.removed") }}</th>
            <th scope="col">{{ t("accessMatrix.preview.restrictions") }}</th>
            <th scope="col">{{ t("accessMatrix.preview.shownShort") }}</th>
            <th scope="col">{{ t("accessMatrix.preview.hiddenShort") }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="change in preview.changes" :key="change.userId">
            <th scope="row">{{ change.name }}</th>
            <td>{{ names(change.addedLibraryIds) }}</td>
            <td>{{ names(change.removedLibraryIds) }}</td>
            <td>{{ change.restrictionsChanged ? t("accessMatrix.preview.restrictionsReplaced") : "—" }}</td>
            <td>+{{ change.shown }}</td>
            <td>−{{ change.hidden }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <UiErrorState v-if="error" :error="error" :retryable="false" />
    <p class="jl-change__impact">{{ t("accessMatrix.preview.impact") }}</p>
    <div class="jl-change__actions">
      <UiButton data-apply :busy="busy" :disabled="preview.users === 0" @click="emit('confirm')">{{ t("accessMatrix.preview.apply") }}</UiButton>
      <UiButton variant="secondary" :disabled="busy" @click="emit('cancel')">{{ t("common.cancel") }}</UiButton>
    </div>
  </div>
</template>

<style scoped>
.jl-change {
  display: grid;
  gap: var(--jl-space-3);
  min-width: 0;
  padding: var(--jl-space-4);
  border: 2px solid var(--jl-color-primary);
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-surface);
}

.jl-change h2 {
  margin: 0;
  font-size: var(--jl-font-size-lg);
}

.jl-change__summary {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 10rem), 1fr));
  gap: var(--jl-space-3);
  margin: 0;
}

.jl-change__summary dt {
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-change__summary dd {
  margin: 0;
  font-size: var(--jl-font-size-lg);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.jl-change__muted {
  margin: 0;
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-change__impact {
  margin: 0;
  font-size: var(--jl-font-size-sm);
}

.jl-change__wrap {
  max-height: 24rem;
  overflow: auto;
}

.jl-change__table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--jl-font-size-sm);
}

.jl-change__table caption {
  text-align: start;
  font-weight: 600;
  padding-bottom: var(--jl-space-2);
}

.jl-change__table th,
.jl-change__table td {
  padding: var(--jl-space-2) var(--jl-space-3);
  border-bottom: 1px solid var(--jl-color-border);
  text-align: start;
  overflow-wrap: anywhere;
}

.jl-change__table thead th {
  color: var(--jl-color-text-muted);
}

.jl-change__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
}
</style>
