<script setup lang="ts">
import { computed, shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import type { SelectOption } from "@/components/ui/types";
import { kindLabelKey } from "@/features/items/labels";
import { formatDateTime } from "@/i18n/format";
import { useUserAdminContentStore } from "@/stores/userAdminContent";
import type { CatalogItem, ContentAccessView, ItemAccessEffect, ItemAccessRule } from "./api";
import { useAdminFeedback } from "./feedback";
import { effectKey } from "./labels";

const props = defineProps<{ access: ContentAccessView }>();
const { t, locale } = useI18n();
const store = useUserAdminContentStore();
const feedback = useAdminFeedback();
const query = shallowRef("");
const effect = shallowRef<ItemAccessEffect>("hide");

const effectOptions = computed<SelectOption[]>(() =>
  (["hide", "allow"] as const).map((value) => ({ value, label: t(effectKey[value]) })),
);
const effectModel = computed({
  get: () => effect.value,
  set: (value: string) => {
    if (value === "hide" || value === "allow") {
      effect.value = value;
    }
  },
});

function ruleOf(itemId: string): ItemAccessRule | undefined {
  return props.access.rules.find((rule) => rule.itemId === itemId);
}

function kindLabel(kind: string): string {
  return kind in kindLabelKey ? t(kindLabelKey[kind as CatalogItem["kind"]]) : kind;
}

async function search() {
  await store.search(query.value);
}

async function add(item: CatalogItem) {
  try {
    await store.setRule(item.id, effect.value);
    await feedback.done("done", "users.rules.added", { title: item.title });
  } catch (error: unknown) {
    feedback.failed(error);
  }
}

async function remove(rule: ItemAccessRule) {
  try {
    await store.removeRule(rule.itemId);
    await feedback.done("done", "users.rules.removed", { title: rule.title });
  } catch (error: unknown) {
    feedback.failed(error);
  }
}
</script>

<template>
  <section class="jl-card" aria-labelledby="user-rules-title">
    <h2 id="user-rules-title">{{ t("users.rules.title") }}</h2>
    <div class="jl-card__impact">
      <p>{{ t("users.rules.impact") }}</p>
      <ul>
        <li>{{ t("users.rules.hideImpact") }}</li>
        <li>{{ t("users.rules.allowImpact") }}</li>
        <li>{{ t("users.rules.nearestWins") }}</li>
      </ul>
    </div>

    <div v-if="access.rules.length > 0" class="jl-table-wrap">
      <table class="jl-table">
        <caption class="jl-visually-hidden">{{ t("users.rules.caption") }}</caption>
        <thead>
          <tr>
            <th scope="col">{{ t("users.rules.item") }}</th>
            <th scope="col">{{ t("users.rules.kind") }}</th>
            <th scope="col">{{ t("users.rules.effect") }}</th>
            <th scope="col">{{ t("users.rules.createdAt") }}</th>
            <th scope="col">{{ t("common.actions") }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="rule in access.rules" :key="rule.itemId">
            <th scope="row">{{ rule.title }}</th>
            <td>{{ kindLabel(rule.kind) }}</td>
            <td>
              <UiBadge :tone="rule.effect === 'allow' ? 'accent' : 'neutral'">{{ t(effectKey[rule.effect]) }}</UiBadge>
            </td>
            <td>{{ formatDateTime(rule.createdAt, locale) }}</td>
            <td>
              <UiConfirmButton
                :label="t('common.delete')"
                :confirm-label="t('users.rules.removeConfirm')"
                :prompt="t('users.rules.removePrompt', { title: rule.title })"
                :busy="store.pendingRules.has(rule.itemId)"
                @confirm="remove(rule)"
              />
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-else class="jl-card__muted">{{ t("users.rules.none") }}</p>

    <section class="jl-rules__add" aria-labelledby="user-rule-add-title">
      <h3 id="user-rule-add-title">{{ t("users.rules.addTitle") }}</h3>
      <form class="jl-rules__search" role="search" novalidate @submit.prevent="search">
        <UiTextField v-model="query" type="search" :label="t('users.rules.searchLabel')" :hint="t('users.rules.searchHint')" autocomplete="off" />
        <UiButton type="submit" variant="secondary">{{ t("common.search") }}</UiButton>
      </form>
      <UiSelectField v-model="effectModel" :label="t('users.rules.newEffect')" :options="effectOptions" />
      <RequestStatus :state="store.searchState" @retry="search">
        <template #default="{ data }">
          <ul class="jl-rules__results">
            <li v-for="item in data" :key="item.id" class="jl-rules__result">
              <span :id="`result-${item.id}`" class="jl-rules__result-title">
                {{ item.title }}
                <span class="jl-card__muted">{{ kindLabel(item.kind) }}{{ item.productionYear ? ` · ${item.productionYear}` : "" }}</span>
              </span>
              <UiBadge v-if="ruleOf(item.id)">{{ t(effectKey[ruleOf(item.id)?.effect ?? "hide"]) }}</UiBadge>
              <UiButton
                variant="secondary"
                :busy="store.pendingRules.has(item.id)"
                :aria-describedby="`result-${item.id}`"
                @click="add(item)"
              >
                {{ ruleOf(item.id) ? t("users.rules.replace") : t("users.rules.add") }}
              </UiButton>
            </li>
          </ul>
        </template>
        <template #empty>
          <UiEmptyState :title="t('users.rules.noResults')" />
        </template>
      </RequestStatus>
    </section>
  </section>
</template>

<style scoped src="./sections.css"></style>
<style scoped>
.jl-rules__add {
  display: grid;
  gap: var(--jl-space-3);
  padding-top: var(--jl-space-4);
  border-top: 1px solid var(--jl-color-border);
}

.jl-rules__add h3 {
  margin: 0;
  font-size: var(--jl-font-size-md);
}

.jl-rules__search {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: var(--jl-space-2);
}

.jl-rules__search > :first-child {
  flex: 1 1 16rem;
}

.jl-rules__results {
  display: grid;
  gap: var(--jl-space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.jl-rules__result {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-2);
  padding: var(--jl-space-2) var(--jl-space-3);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
}

.jl-rules__result-title {
  display: grid;
  flex: 1 1 12rem;
  min-width: 0;
  overflow-wrap: anywhere;
}
</style>
