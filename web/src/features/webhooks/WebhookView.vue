<script setup lang="ts">
import { computed, onBeforeUnmount, shallowRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import type { ApiError } from "@/api/errors";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiEmptyState from "@/components/ui/UiEmptyState.vue";
import UiErrorState from "@/components/ui/UiErrorState.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { formatDateTime } from "@/i18n/format";
import { useToastStore } from "@/stores/toasts";
import { useWebhookDetailStore } from "@/stores/webhooksDetail";
import { useWebhooksStore } from "@/stores/webhooks";
import { defaultGraceSeconds, type DeliveryState, type Webhook, type WebhookDelivery, type WebhookInput } from "./api";
import { asApiError, graceChoices, outcomeKeys, stateKeys } from "./labels";
import WebhookForm from "./WebhookForm.vue";
import WebhookSecretNotice from "./WebhookSecretNotice.vue";
import WebhookTestOutcome from "./WebhookTestOutcome.vue";
import "./webhooks.css";

const props = defineProps<{ webhookId: string }>();
const { t, locale } = useI18n();
const list = useWebhooksStore();
const detail = useWebhookDetailStore();
const toasts = useToastStore();
const saving = shallowRef(false);
const formError = shallowRef<ApiError | null>(null);
/** Bumped to rebuild the settings form from the saved state. */
const formKey = shallowRef(0);
const grace = shallowRef(String(defaultGraceSeconds));
const expanded = shallowRef<ReadonlySet<string>>(new Set());

watch(
  () => props.webhookId,
  (id) => {
    expanded.value = new Set();
    formError.value = null;
    void detail.open(id);
    // The event catalog comes with the endpoint list.
    void list.ensureLoaded();
  },
  { immediate: true },
);

onBeforeUnmount(() => {
  list.dismissSecret();
});

const secret = computed(() => (list.secret?.webhookId === props.webhookId ? list.secret : null));
/** Falls back to the endpoint's own events so a missing catalog never drops them. */
function catalog(webhook: Webhook) {
  return list.events.length > 0 ? list.events : webhook.events;
}

const graceOptions = computed(() => graceChoices.map((choice) => ({ value: String(choice.seconds), label: t(choice.key) })));
const filterOptions = computed(() => [
  { value: "", label: t("webhooks.deliveries.stateAll") },
  ...(Object.keys(stateKeys) as DeliveryState[]).map((value) => ({ value, label: t(stateKeys[value]) })),
]);
const filter = computed({
  get: () => detail.filter,
  set: (value: string) => {
    expanded.value = new Set();
    const next = (Object.keys(stateKeys) as DeliveryState[]).find((state) => state === value) ?? "";
    void detail.setFilter(next);
  },
});

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

async function save(input: WebhookInput) {
  saving.value = true;
  formError.value = null;
  try {
    detail.replace(await list.update(props.webhookId, input));
    formKey.value++;
    toasts.push("webhooks.form.saved", "success");
  } catch (error: unknown) {
    formError.value = asApiError(error);
  } finally {
    saving.value = false;
  }
}

function cancelEdit() {
  formError.value = null;
  formKey.value++;
}

function rotate() {
  const seconds = graceChoices.find((choice) => String(choice.seconds) === grace.value)?.seconds ?? defaultGraceSeconds;
  return run(async () => {
    detail.replace(await list.rotate(props.webhookId, seconds));
  });
}

function toggleDetail(delivery: WebhookDelivery) {
  const next = new Set(expanded.value);
  if (next.has(delivery.id)) {
    next.delete(delivery.id);
  } else {
    next.add(delivery.id);
    void detail.loadDetail(delivery.id);
  }
  expanded.value = next;
}

function lastResult(delivery: WebhookDelivery): string {
  if (delivery.lastOutcome === undefined) {
    return "—";
  }
  const outcome = t(outcomeKeys[delivery.lastOutcome]);
  return delivery.lastStatus === undefined ? outcome : `${outcome} (${delivery.lastStatus})`;
}
</script>

<template>
  <section class="jl-webhook" aria-labelledby="webhook-title">
    <RouterLink :to="{ name: 'admin-webhooks' }" class="jl-webhook__back">{{ t("webhooks.detail.back") }}</RouterLink>
    <h1 id="webhook-title" tabindex="-1">
      {{ detail.webhook ? t("webhooks.detail.title", { name: detail.webhook.name }) : t("webhooks.detail.fallbackTitle") }}
    </h1>

    <WebhookSecretNotice v-if="secret" :secret="secret" @done="list.dismissSecret" />

    <RequestStatus :state="detail.state" @retry="detail.reload">
      <template #loading>
        <UiSkeleton shape="block" />
      </template>
      <template #default>
        <template v-if="detail.webhook">
          <div class="jl-wh-card">
            <p class="jl-wh-muted">
              <span class="jl-wh-code">{{ detail.webhook.url }}</span>
              <br />
              {{ t("webhooks.detail.counts", { pending: detail.webhook.pending, dead: detail.webhook.dead }) }}
              <template v-if="detail.webhook.previousSecretUntil">
                <br />{{ t("webhooks.detail.previousSecret", { date: formatDateTime(detail.webhook.previousSecretUntil, locale) }) }}
              </template>
            </p>
            <WebhookForm
              :key="formKey"
              :webhook="detail.webhook"
              :events="catalog(detail.webhook)"
              :busy="saving"
              :error="formError"
              @submit="save"
              @cancel="cancelEdit"
            />
          </div>

          <section class="jl-wh-card" aria-labelledby="webhook-tools-title">
            <h2 id="webhook-tools-title">{{ t("webhooks.detail.tools") }}</h2>
            <div class="jl-wh-actions">
              <UiButton variant="secondary" :busy="list.pending.has(webhookId)" @click="run(() => list.test(webhookId))">
                {{ t("webhooks.test.send") }}
              </UiButton>
            </div>
            <div role="status">
              <WebhookTestOutcome :result="list.testResults.get(webhookId)" />
            </div>
            <h3>{{ t("webhooks.rotate.title") }}</h3>
            <p class="jl-wh-muted">{{ t("webhooks.rotate.hint") }}</p>
            <div class="jl-wh-actions jl-webhook__rotate">
              <UiSelectField v-model="grace" :label="t('webhooks.rotate.grace')" :options="graceOptions" />
              <UiConfirmButton
                :label="t('webhooks.rotate.action')"
                :confirm-label="t('webhooks.rotate.confirm')"
                :prompt="t('webhooks.rotate.prompt')"
                :busy="list.pending.has(webhookId)"
                @confirm="rotate"
              />
            </div>
          </section>
        </template>
      </template>
    </RequestStatus>

    <section class="jl-wh-card" aria-labelledby="webhook-deliveries-title">
      <div class="jl-wh-head">
        <h2 id="webhook-deliveries-title">{{ t("webhooks.deliveries.title") }}</h2>
        <UiSelectField v-model="filter" :label="t('webhooks.deliveries.filter')" :options="filterOptions" />
      </div>
      <RequestStatus :state="detail.deliveriesState" @retry="detail.reloadDeliveries">
        <template #loading>
          <UiSkeleton v-for="n in 3" :key="n" shape="block" />
        </template>
        <template #empty>
          <UiEmptyState :title="t('webhooks.deliveries.empty')" />
        </template>
        <template #default>
          <div class="jl-wh-table-wrap">
            <table class="jl-wh-table">
              <caption class="jl-visually-hidden">{{ t("webhooks.deliveries.title") }}</caption>
              <thead>
                <tr>
                  <th scope="col">{{ t("webhooks.deliveries.occurredAt") }}</th>
                  <th scope="col">{{ t("webhooks.deliveries.event") }}</th>
                  <th scope="col">{{ t("webhooks.deliveries.state") }}</th>
                  <th scope="col" class="jl-wh-num">{{ t("webhooks.deliveries.attempts") }}</th>
                  <th scope="col">{{ t("webhooks.deliveries.lastResult") }}</th>
                  <th scope="col">{{ t("webhooks.deliveries.nextAttempt") }}</th>
                  <th scope="col">{{ t("common.actions") }}</th>
                </tr>
              </thead>
              <tbody>
                <template v-for="delivery in detail.deliveries" :key="delivery.id">
                  <tr>
                    <th :id="`delivery-${delivery.id}`" scope="row">
                      {{ formatDateTime(delivery.occurredAt, locale) }}
                      <br />
                      <span class="jl-wh-code jl-wh-muted">{{ delivery.eventId }}</span>
                    </th>
                    <td class="jl-wh-code">{{ delivery.eventType }}</td>
                    <td>
                      <UiBadge :tone="delivery.state === 'dead' ? 'neutral' : 'accent'">{{ t(stateKeys[delivery.state]) }}</UiBadge>
                    </td>
                    <td class="jl-wh-num">
                      {{ delivery.attempts }}
                      <span v-if="delivery.replays > 0" class="jl-wh-muted"><br />{{ t("webhooks.deliveries.resends", { count: delivery.replays }) }}</span>
                    </td>
                    <td>
                      {{ lastResult(delivery) }}
                      <span v-if="delivery.lastAttemptAt" class="jl-wh-muted"><br />{{ formatDateTime(delivery.lastAttemptAt, locale) }}</span>
                    </td>
                    <td>{{ delivery.nextAttemptAt ? formatDateTime(delivery.nextAttemptAt, locale) : "—" }}</td>
                    <td>
                      <div class="jl-wh-actions">
                        <UiButton
                          variant="ghost"
                          :aria-expanded="expanded.has(delivery.id) ? 'true' : 'false'"
                          :aria-controls="`delivery-detail-${delivery.id}`"
                          :aria-describedby="`delivery-${delivery.id}`"
                          @click="toggleDetail(delivery)"
                        >
                          {{ t("webhooks.deliveries.history") }}
                        </UiButton>
                        <UiConfirmButton
                          v-if="delivery.state !== 'pending'"
                          variant="secondary"
                          :label="t('webhooks.deliveries.resend')"
                          :confirm-label="t('webhooks.deliveries.resendConfirm')"
                          :prompt="t('webhooks.deliveries.resendPrompt', { eventId: delivery.eventId })"
                          :busy="detail.replaying.has(delivery.id)"
                          @confirm="run(() => detail.replay(delivery.id), 'webhooks.deliveries.resent')"
                        />
                      </div>
                    </td>
                  </tr>
                  <tr v-if="expanded.has(delivery.id)" :id="`delivery-detail-${delivery.id}`">
                    <td colspan="7">
                      <RequestStatus :state="detail.detailState(delivery.id)" @retry="detail.loadDetail(delivery.id)">
                        <template #default="{ data: info }">
                          <p v-if="info.history.length === 0" class="jl-wh-muted">{{ t("webhooks.deliveries.noHistory") }}</p>
                          <table v-else class="jl-wh-table">
                            <caption>{{ t("webhooks.deliveries.historyCaption", { eventId: info.eventId }) }}</caption>
                            <thead>
                              <tr>
                                <th scope="col">{{ t("webhooks.deliveries.round") }}</th>
                                <th scope="col">{{ t("webhooks.deliveries.startedAt") }}</th>
                                <th scope="col">{{ t("webhooks.deliveries.outcome") }}</th>
                                <th scope="col">{{ t("webhooks.deliveries.status") }}</th>
                                <th scope="col">{{ t("webhooks.deliveries.nextAttempt") }}</th>
                              </tr>
                            </thead>
                            <tbody>
                              <tr v-for="attempt in info.history" :key="`${attempt.round}-${attempt.attempt}`">
                                <th scope="row">{{ t("webhooks.deliveries.roundValue", { round: attempt.round, attempt: attempt.attempt }) }}</th>
                                <td>{{ formatDateTime(attempt.startedAt, locale) }}</td>
                                <td>{{ t(outcomeKeys[attempt.outcome]) }}</td>
                                <td>{{ attempt.statusCode ?? "—" }}</td>
                                <td>{{ attempt.nextAttemptAt ? formatDateTime(attempt.nextAttemptAt, locale) : "—" }}</td>
                              </tr>
                            </tbody>
                          </table>
                        </template>
                      </RequestStatus>
                    </td>
                  </tr>
                </template>
              </tbody>
            </table>
          </div>
          <UiErrorState v-if="detail.moreState.status === 'error'" :error="detail.moreState.error" @retry="detail.loadMore" />
          <UiButton v-if="detail.nextCursor !== ''" variant="secondary" :busy="detail.moreState.status === 'loading'" @click="detail.loadMore">
            {{ t("common.loadMore") }}
          </UiButton>
        </template>
      </RequestStatus>
    </section>
  </section>
</template>

<style scoped>
.jl-webhook {
  display: grid;
  gap: var(--jl-space-4);
}

.jl-webhook h1 {
  margin: 0;
}

.jl-webhook__back {
  justify-self: start;
}

.jl-webhook__rotate {
  align-items: end;
}
</style>
