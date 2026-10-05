<script setup lang="ts">
import { nextTick, onMounted, shallowRef, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { useAdminFeedback } from "@/features/users/feedback";
import { useNetworkRulesStore } from "@/stores/networkRules";
import { useToastStore } from "@/stores/toasts";
import AccessTabs from "./AccessTabs.vue";
import type { NetworkRule } from "./api";
import NetworkRuleForm from "./NetworkRuleForm.vue";
import { networkKeys, sessionKindKeys } from "./networkLabels";

// Network rules of the libraries (G48.6). The server applies them to every
// catalog read through the unified storage filter; this page only edits them.
const { t } = useI18n();
const store = useNetworkRulesStore();
const toasts = useToastStore();
const feedback = useAdminFeedback();
/** The rule being edited, "new" while creating, null when the form is closed. */
const editing = shallowRef<NetworkRule | "new" | null>(null);
const addButton = useTemplateRef<HTMLElement>("addButton");

onMounted(() => {
  void store.load();
});

async function closeForm() {
  editing.value = null;
  await nextTick();
  addButton.value?.querySelector("button")?.focus();
}

async function saved(created: boolean) {
  toasts.push(created ? "networkRules.created" : "networkRules.updated", "success");
  await closeForm();
}

async function remove(rule: NetworkRule) {
  try {
    await store.remove(rule.id);
    toasts.push("networkRules.deleted", "success");
  } catch (error: unknown) {
    feedback.failed(error);
  }
}

function kinds(rule: NetworkRule): string {
  return rule.clientKinds.length === 0 ? t("networkRules.anyClient") : rule.clientKinds.map((kind) => t(sessionKindKeys[kind])).join(", ");
}
</script>

<template>
  <section class="jl-netrules" aria-labelledby="netrules-title">
    <h1 id="netrules-title" tabindex="-1">{{ t("access.title") }}</h1>
    <AccessTabs />

    <section class="jl-card" aria-labelledby="netrules-list-title">
      <div class="jl-netrules__head">
        <h2 id="netrules-list-title">{{ t("networkRules.title") }}</h2>
        <span ref="addButton">
          <UiButton v-if="editing === null" @click="editing = 'new'">{{ t("networkRules.add") }}</UiButton>
        </span>
      </div>
      <p class="jl-card__muted">{{ t("networkRules.intro") }}</p>
      <p class="jl-card__impact">{{ t("networkRules.adminsNote") }}</p>

      <NetworkRuleForm
        v-if="editing !== null"
        :key="editing === 'new' ? 'new' : editing.id"
        :rule="editing === 'new' ? null : editing"
        @saved="saved(editing === 'new')"
        @cancel="closeForm"
      />

      <RequestStatus :state="store.state" @retry="store.load">
        <template #loading>
          <div aria-hidden="true">
            <UiSkeleton v-for="n in 3" :key="n" class="jl-skeleton-row" />
          </div>
        </template>
        <template #empty>
          <UiEmptyState :title="t('networkRules.empty')">
            <p>{{ t("networkRules.emptyHint") }}</p>
          </UiEmptyState>
        </template>
        <template #default>
          <div class="jl-table-wrap">
            <table class="jl-table">
              <caption class="jl-visually-hidden">{{ t("networkRules.title") }}</caption>
              <thead>
                <tr>
                  <th scope="col">{{ t("networkRules.column.library") }}</th>
                  <th scope="col">{{ t("networkRules.column.network") }}</th>
                  <th scope="col">{{ t("networkRules.column.addresses") }}</th>
                  <th scope="col">{{ t("networkRules.column.clients") }}</th>
                  <th scope="col">{{ t("networkRules.column.enabled") }}</th>
                  <th scope="col">{{ t("common.actions") }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="rule in store.rules" :key="rule.id">
                  <th :id="`netrule-${rule.id}`" scope="row">
                    {{ rule.libraryName }}
                    <UiBadge v-if="rule.includeAdmins" tone="accent">{{ t("networkRules.includesAdmins") }}</UiBadge>
                    <span v-if="rule.note" class="jl-card__muted"><br />{{ rule.note }}</span>
                  </th>
                  <td>{{ t(networkKeys[rule.network]) }}</td>
                  <td class="jl-netrules__cidrs">{{ rule.cidrs.length === 0 ? t("networkRules.anyAddress") : rule.cidrs.join(", ") }}</td>
                  <td>{{ kinds(rule) }}</td>
                  <td>{{ rule.enabled ? t("common.yes") : t("common.no") }}</td>
                  <td>
                    <div class="jl-card__actions">
                      <UiButton variant="ghost" :aria-describedby="`netrule-${rule.id}`" @click="editing = rule">{{ t("common.edit") }}</UiButton>
                      <UiConfirmButton
                        :label="t('common.delete')"
                        :confirm-label="t('networkRules.deleteConfirm')"
                        :prompt="t('networkRules.deletePrompt')"
                        :busy="store.pending.has(rule.id)"
                        :describedby="`netrule-${rule.id}`"
                        @confirm="remove(rule)"
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
  </section>
</template>

<style scoped src="../users/sections.css"></style>
<style scoped>
.jl-netrules {
  display: grid;
  gap: var(--jl-space-6);
}

.jl-netrules h1 {
  margin: 0;
}

.jl-netrules__head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--jl-space-3);
}

.jl-netrules__head h2 {
  margin: 0;
  font-size: var(--jl-font-size-lg);
}

.jl-netrules__cidrs {
  font-family: ui-monospace, monospace;
}
</style>
