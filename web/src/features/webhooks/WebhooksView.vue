<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, shallowRef, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import type { ApiError } from "@/api/errors";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { useToastStore } from "@/stores/toasts";
import { useWebhooksStore } from "@/stores/webhooks";
import type { Webhook, WebhookInput } from "./api";
import { asApiError } from "./labels";
import WebhookForm from "./WebhookForm.vue";
import WebhookSecretNotice from "./WebhookSecretNotice.vue";
import WebhookTestOutcome from "./WebhookTestOutcome.vue";
import "./webhooks.css";

const { t } = useI18n();
const store = useWebhooksStore();
const toasts = useToastStore();
const creating = shallowRef(false);
const saving = shallowRef(false);
const formError = shallowRef<ApiError | null>(null);
const addButton = useTemplateRef<HTMLElement>("addButton");

onMounted(() => {
  void store.load();
});

// The one-time secret never outlives the page that revealed it.
onBeforeUnmount(() => {
  store.dismissSecret();
});

async function closeForm() {
  creating.value = false;
  formError.value = null;
  await nextTick();
  addButton.value?.querySelector("button")?.focus();
}

async function create(input: WebhookInput) {
  saving.value = true;
  formError.value = null;
  try {
    await store.create(input);
    creating.value = false;
    toasts.push("webhooks.created", "success", { name: input.name });
  } catch (error: unknown) {
    formError.value = asApiError(error);
  } finally {
    saving.value = false;
  }
}

async function run(work: () => Promise<unknown>, success?: string, params: Record<string, string> = {}) {
  try {
    await work();
    if (success !== undefined) {
      toasts.push(success, "success", params);
    }
  } catch (error: unknown) {
    toasts.push(errorMessageKey(asApiError(error)), "danger");
  }
}

function toggle(webhook: Webhook) {
  return run(() => store.setEnabled(webhook, !webhook.enabled), webhook.enabled ? "webhooks.disabled" : "webhooks.enabled", {
    name: webhook.name,
  });
}
</script>

<template>
  <section class="jl-webhooks" aria-labelledby="webhooks-title">
    <h1 id="webhooks-title" tabindex="-1">{{ t("webhooks.title") }}</h1>
    <p class="jl-wh-muted">{{ t("webhooks.intro") }}</p>

    <WebhookSecretNotice v-if="store.secret" :secret="store.secret" @done="store.dismissSecret" />

    <div class="jl-wh-head">
      <span ref="addButton">
        <UiButton v-if="!creating" @click="creating = true">{{ t("webhooks.add") }}</UiButton>
      </span>
    </div>
    <div v-if="creating" class="jl-wh-card">
      <WebhookForm :webhook="null" :events="store.events" :busy="saving" :error="formError" @submit="create" @cancel="closeForm" />
    </div>

    <RequestStatus :state="store.state" @retry="store.load">
      <template #loading>
        <UiSkeleton v-for="n in 3" :key="n" shape="block" />
      </template>
      <template #empty>
        <UiEmptyState :title="t('webhooks.empty')">
          <p>{{ t("webhooks.emptyHint") }}</p>
        </UiEmptyState>
      </template>
      <template #default>
        <div class="jl-wh-table-wrap">
          <table class="jl-wh-table">
            <caption class="jl-visually-hidden">{{ t("webhooks.title") }}</caption>
            <thead>
              <tr>
                <th scope="col">{{ t("webhooks.columns.name") }}</th>
                <th scope="col">{{ t("webhooks.columns.status") }}</th>
                <th scope="col">{{ t("webhooks.columns.events") }}</th>
                <th scope="col" class="jl-wh-num">{{ t("webhooks.columns.pending") }}</th>
                <th scope="col" class="jl-wh-num">{{ t("webhooks.columns.dead") }}</th>
                <th scope="col">{{ t("common.actions") }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="webhook in store.webhooks" :key="webhook.id">
                <th :id="`webhook-${webhook.id}`" scope="row">
                  <RouterLink :to="{ name: 'admin-webhook', params: { webhookId: webhook.id } }">{{ webhook.name }}</RouterLink>
                  <br />
                  <span class="jl-wh-code jl-wh-muted">{{ webhook.url }}</span>
                </th>
                <td>
                  <UiBadge :tone="webhook.enabled ? 'accent' : 'neutral'">
                    {{ webhook.enabled ? t("webhooks.on") : t("webhooks.off") }}
                  </UiBadge>
                </td>
                <td>
                  <span v-if="webhook.events.length === 0">{{ t("webhooks.allEvents") }}</span>
                  <span v-else class="jl-wh-code">{{ webhook.events.join(", ") }}</span>
                </td>
                <td class="jl-wh-num">{{ webhook.pending }}</td>
                <td class="jl-wh-num">{{ webhook.dead }}</td>
                <td>
                  <div class="jl-wh-actions">
                    <UiButton
                      variant="secondary"
                      :busy="store.pending.has(webhook.id)"
                      :aria-describedby="`webhook-${webhook.id}`"
                      @click="toggle(webhook)"
                    >
                      {{ webhook.enabled ? t("webhooks.disable") : t("webhooks.enable") }}
                    </UiButton>
                    <UiButton
                      variant="ghost"
                      :busy="store.pending.has(webhook.id)"
                      :aria-describedby="`webhook-${webhook.id}`"
                      @click="run(() => store.test(webhook.id))"
                    >
                      {{ t("webhooks.test.send") }}
                    </UiButton>
                    <UiConfirmButton
                      :label="t('common.delete')"
                      :confirm-label="t('webhooks.deleteConfirm')"
                      :prompt="t('webhooks.deletePrompt', { name: webhook.name })"
                      :busy="store.pending.has(webhook.id)"
                      @confirm="run(() => store.remove(webhook.id), 'webhooks.deleted', { name: webhook.name })"
                    />
                  </div>
                  <div role="status">
                    <WebhookTestOutcome :result="store.testResults.get(webhook.id)" />
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

<style scoped>
.jl-webhooks {
  display: grid;
  gap: var(--jl-space-4);
}

.jl-webhooks h1 {
  margin: 0;
}
</style>
