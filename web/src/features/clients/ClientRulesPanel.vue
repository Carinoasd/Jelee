<script setup lang="ts">
import { nextTick, onMounted, shallowRef, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { formatDateTime } from "@/i18n/format";
import { useClientRulesStore } from "@/stores/clientsRules";
import { useToastStore } from "@/stores/toasts";
import { isObserving, type ClientRule } from "./api";
import ClientRuleForm from "./ClientRuleForm.vue";
import { asApiError, dimensionKeys, intentKeys, matchKeys, ruleActionKeys } from "./labels";

const { t, locale } = useI18n();
const store = useClientRulesStore();
const toasts = useToastStore();
/** The rule being edited, "new" while creating, null when the form is closed. */
const editing = shallowRef<ClientRule | "new" | null>(null);
const addButton = useTemplateRef<HTMLElement>("addButton");

onMounted(() => {
  void store.load();
});

function intentLabel(rule: ClientRule): string {
  return rule.intent === undefined ? "" : t(intentKeys[rule.intent]);
}

async function closeForm() {
  editing.value = null;
  await nextTick();
  addButton.value?.querySelector("button")?.focus();
}

async function saved(created: boolean) {
  toasts.push(created ? "clients.rules.created" : "clients.rules.updated", "success");
  await closeForm();
}

async function act(work: () => Promise<unknown>, success: string) {
  try {
    await work();
    toasts.push(success, "success");
  } catch (error: unknown) {
    toasts.push(errorMessageKey(asApiError(error)), "danger");
  }
}
</script>

<template>
  <section class="jl-cc-card" aria-labelledby="clients-rules-title">
    <div class="jl-cc-head">
      <h2 id="clients-rules-title">{{ t("clients.rules.title") }}</h2>
      <span ref="addButton">
        <UiButton v-if="editing === null" @click="editing = 'new'">{{ t("clients.rules.add") }}</UiButton>
      </span>
    </div>
    <p class="jl-cc-muted">{{ t("clients.rules.hint") }}</p>

    <ClientRuleForm
      v-if="editing !== null"
      :key="editing === 'new' ? 'new' : editing.id"
      :rule="editing === 'new' ? null : editing"
      @saved="saved(editing === 'new')"
      @cancel="closeForm"
    />

    <RequestStatus :state="store.state" @retry="store.load">
      <template #loading>
        <UiSkeleton v-for="n in 3" :key="n" shape="block" />
      </template>
      <template #empty>
        <UiEmptyState :title="t('clients.rules.empty')">
          <p>{{ t("clients.rules.emptyHint") }}</p>
        </UiEmptyState>
      </template>
      <template #default>
        <div class="jl-cc-table-wrap">
          <table class="jl-cc-table">
            <caption class="jl-visually-hidden">{{ t("clients.rules.title") }}</caption>
            <thead>
              <tr>
                <th scope="col">{{ t("clients.rules.priority") }}</th>
                <th scope="col">{{ t("clients.rules.condition") }}</th>
                <th scope="col">{{ t("clients.rules.action") }}</th>
                <th scope="col">{{ t("clients.rules.enabled") }}</th>
                <th scope="col">{{ t("clients.rules.hits") }}</th>
                <th scope="col">{{ t("clients.rules.lastHit") }}</th>
                <th scope="col">{{ t("common.actions") }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="rule in store.rules" :key="rule.id">
                <td class="jl-cc-num">{{ rule.priority }}</td>
                <th :id="`rule-${rule.id}`" scope="row">
                  <span>{{ t(dimensionKeys[rule.dimension]) }}</span>
                  <span v-if="rule.header" class="jl-cc-code"> {{ t("clients.rules.header", { name: rule.header }) }}</span>
                  <br />
                  <span class="jl-cc-muted">{{ t(matchKeys[rule.match]) }}</span>
                  <span v-if="rule.match !== 'absent'" class="jl-cc-code"> {{ rule.pattern }}</span>
                  <UiBadge v-if="rule.caseFold">{{ t("clients.rules.caseFold") }}</UiBadge>
                  <UiBadge v-if="rule.scopeKind !== 'global' || rule.window">{{ t("clients.rules.scoped") }}</UiBadge>
                  <span v-if="rule.note" class="jl-cc-muted"><br />{{ rule.note }}</span>
                </th>
                <td>
                  <template v-if="isObserving(rule)">
                    <UiBadge tone="accent">{{ t(ruleActionKeys[rule.action]) }}</UiBadge>
                    <br />
                    <span class="jl-cc-muted">{{ t("clients.rules.intent", { action: intentLabel(rule) }) }}</span>
                  </template>
                  <template v-else>{{ t(ruleActionKeys[rule.action]) }}</template>
                  <span v-if="rule.libraries?.length" class="jl-cc-muted">
                    <br />{{ t("clients.rules.libraries", { count: rule.libraries.length }) }}
                  </span>
                  <span v-if="rule.rateLimit" class="jl-cc-muted">
                    <br />{{ t("clients.rules.rate", { requests: rule.rateLimit.requests, seconds: rule.rateLimit.periodSeconds }) }}
                  </span>
                </td>
                <td>{{ rule.enabled ? t("common.yes") : t("common.no") }}</td>
                <td class="jl-cc-num">{{ rule.hitCount.toLocaleString(locale) }}</td>
                <td>{{ rule.lastHitAt ? formatDateTime(rule.lastHitAt, locale) : t("clients.rules.never") }}</td>
                <td>
                  <div class="jl-cc-actions">
                    <UiConfirmButton
                      v-if="isObserving(rule)"
                      variant="secondary"
                      :label="t('clients.rules.enforce')"
                      :confirm-label="t('clients.rules.enforceConfirm')"
                      :prompt="t('clients.rules.enforcePrompt', { count: rule.hitCount.toLocaleString(locale), action: intentLabel(rule) })"
                      :busy="store.pending.has(rule.id)"
                      @confirm="act(() => store.enforce(rule.id), 'clients.rules.enforced')"
                    />
                    <UiButton
                      v-else
                      variant="secondary"
                      :busy="store.pending.has(rule.id)"
                      :aria-describedby="`rule-${rule.id}`"
                      @click="act(() => store.observe(rule.id), 'clients.rules.observed')"
                    >
                      {{ t("clients.rules.observe") }}
                    </UiButton>
                    <UiButton variant="ghost" :aria-describedby="`rule-${rule.id}`" @click="editing = rule">{{ t("common.edit") }}</UiButton>
                    <UiConfirmButton
                      :label="t('common.delete')"
                      :confirm-label="t('clients.rules.deleteConfirm')"
                      :prompt="t('clients.rules.deletePrompt')"
                      :busy="store.pending.has(rule.id)"
                      @confirm="act(() => store.remove(rule.id), 'clients.rules.deleted')"
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
