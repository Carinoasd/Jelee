<script setup lang="ts">
import { computed, onMounted, reactive, shallowRef, useId, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import type { ApiError } from "@/api/errors";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { toInput, type Webhook, type WebhookEvent, type WebhookInput } from "./api";

// Create (webhook null) or replace an endpoint. Header values are write-only:
// on edit the stored headers are kept unless "replace headers" is chosen.
const props = defineProps<{
  webhook: Webhook | null;
  events: readonly WebhookEvent[];
  busy: boolean;
  error: ApiError | null;
  /** Heading level inside the page. */
  level?: "h2" | "h3";
}>();
const emit = defineEmits<{ submit: [input: WebhookInput]; cancel: [] }>();
const { t } = useI18n();
const root = useTemplateRef<HTMLElement>("root");
const titleId = useId();
const maxHeaders = 16;

interface HeaderRow {
  id: number;
  name: string;
  value: string;
}

const source = props.webhook;
const form = reactive({
  name: source?.name ?? "",
  url: source?.url ?? "https://",
  enabled: source?.enabled ?? true,
  timeout: String(source?.timeoutSeconds ?? 10),
  replaceHeaders: source === null,
});
const selected = shallowRef<ReadonlySet<WebhookEvent>>(new Set(source?.events ?? []));
const headers = reactive<HeaderRow[]>([]);
let nextRow = 1;
const submitted = shallowRef(false);

function toggle(event: WebhookEvent, on: boolean) {
  const next = new Set(selected.value);
  if (on) {
    next.add(event);
  } else {
    next.delete(event);
  }
  selected.value = next;
}

function addHeader() {
  headers.push({ id: nextRow++, name: "", value: "" });
}

function removeHeader(id: number) {
  const index = headers.findIndex((row) => row.id === id);
  if (index >= 0) {
    headers.splice(index, 1);
  }
}

const timeoutSeconds = computed(() => {
  const value = form.timeout.trim();
  const n = /^\d+$/.test(value) ? Number(value) : NaN;
  return n >= 1 && n <= 30 ? n : null;
});

const errors = computed(() => {
  const names = headers.map((row) => row.name.trim().toLowerCase());
  return {
    name: form.name.trim() === "" ? t("webhooks.form.nameRequired") : null,
    url: /^https:\/\/[^\s/?#]+/i.test(form.url.trim()) ? null : t("webhooks.form.urlInvalid"),
    timeout: timeoutSeconds.value === null ? t("webhooks.form.timeoutInvalid") : null,
    headers: form.replaceHeaders && names.some((name, index) => name === "" || names.indexOf(name) !== index) ? t("webhooks.form.headerInvalid") : null,
  };
});
const valid = computed(() => Object.values(errors.value).every((message) => message === null));
const shown = (message: string | null) => (submitted.value ? message : null);

function input(): WebhookInput {
  const base = source === null ? null : toInput(source);
  const body: WebhookInput = {
    ...(base ?? {}),
    name: form.name.trim(),
    url: form.url.trim(),
    enabled: form.enabled,
    // Catalog order; an empty list subscribes to every event.
    events: props.events.filter((event) => selected.value.has(event)),
    timeoutSeconds: timeoutSeconds.value ?? 10,
  };
  if (form.replaceHeaders) {
    body.headers = Object.fromEntries(headers.map((row) => [row.name.trim(), row.value]));
  }
  return body;
}

function submit() {
  submitted.value = true;
  if (!valid.value) {
    root.value?.querySelector<HTMLElement>("[aria-invalid='true']")?.focus();
    return;
  }
  emit("submit", input());
}

onMounted(() => {
  if (source === null) {
    root.value?.querySelector<HTMLInputElement>("input")?.focus();
  }
});
</script>

<template>
  <form ref="root" class="jl-wh-form" :aria-labelledby="titleId" novalidate @submit.prevent="submit">
    <component :is="level ?? 'h2'" :id="titleId">
      {{ webhook === null ? t("webhooks.form.createTitle") : t("webhooks.form.editTitle") }}
    </component>
    <div class="jl-wh-grid">
      <UiTextField v-model="form.name" :label="t('webhooks.form.name')" :maxlength="128" required :error="shown(errors.name)" />
      <UiTextField
        v-model="form.url"
        type="url"
        inputmode="url"
        :label="t('webhooks.form.url')"
        :hint="t('webhooks.form.urlHint')"
        :maxlength="2048"
        required
        :error="shown(errors.url)"
      />
      <UiTextField
        v-model="form.timeout"
        inputmode="numeric"
        :label="t('webhooks.form.timeout')"
        :hint="t('webhooks.form.timeoutHint')"
        :error="shown(errors.timeout)"
      />
    </div>
    <UiCheckbox v-model="form.enabled" :label="t('webhooks.form.enabled')" />

    <fieldset class="jl-wh-fieldset">
      <legend>{{ t("webhooks.form.events") }}</legend>
      <p class="jl-wh-muted">{{ t("webhooks.form.eventsHint") }}</p>
      <div class="jl-wh-events">
        <UiCheckbox
          v-for="event in events"
          :key="event"
          :label="event"
          :model-value="selected.has(event)"
          @update:model-value="(on: boolean) => toggle(event, on)"
        />
      </div>
    </fieldset>

    <fieldset class="jl-wh-fieldset">
      <legend>{{ t("webhooks.form.headers") }}</legend>
      <p class="jl-wh-muted">{{ t("webhooks.form.headersHint") }}</p>
      <template v-if="webhook !== null">
        <p class="jl-wh-muted">
          {{
            webhook.headerNames.length > 0
              ? t("webhooks.form.existingHeaders", { names: webhook.headerNames.join(", ") })
              : t("webhooks.form.noHeaders")
          }}
        </p>
        <UiCheckbox v-model="form.replaceHeaders" :label="t('webhooks.form.replaceHeaders')" :hint="t('webhooks.form.replaceHeadersHint')" />
      </template>
      <template v-if="form.replaceHeaders">
        <div v-for="row in headers" :key="row.id" class="jl-wh-header">
          <UiTextField v-model="row.name" :label="t('webhooks.form.headerName')" :maxlength="256" autocomplete="off" />
          <UiTextField v-model="row.value" type="password" :label="t('webhooks.form.headerValue')" :maxlength="1024" autocomplete="off" />
          <UiButton variant="ghost" :aria-label="t('webhooks.form.removeHeader', { name: row.name || '?' })" @click="removeHeader(row.id)">
            {{ t("webhooks.form.remove") }}
          </UiButton>
        </div>
        <p v-if="shown(errors.headers)" class="jl-wh-error" role="alert">{{ errors.headers }}</p>
        <div>
          <UiButton variant="secondary" :disabled="headers.length >= maxHeaders" @click="addHeader">{{ t("webhooks.form.addHeader") }}</UiButton>
        </div>
      </template>
    </fieldset>

    <UiAlert v-if="error" tone="danger">
      <p>{{ error.code === "invalid_request" ? t("webhooks.form.invalid") : t(errorMessageKey(error)) }}</p>
      <p v-if="error.traceId" class="jl-wh-code">{{ t("errors.traceId", { id: error.traceId }) }}</p>
    </UiAlert>
    <div class="jl-wh-actions">
      <UiButton type="submit" :busy="busy">{{ webhook === null ? t("webhooks.form.submitCreate") : t("webhooks.form.submitUpdate") }}</UiButton>
      <UiButton variant="secondary" :disabled="busy" @click="emit('cancel')">{{ t("common.cancel") }}</UiButton>
    </div>
  </form>
</template>

<style scoped>
.jl-wh-form h2,
.jl-wh-form h3 {
  margin: 0;
  font-size: var(--jl-font-size-lg);
}

.jl-wh-events {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(12rem, 1fr));
  gap: 0 var(--jl-space-4);
  font-family: ui-monospace, monospace;
  font-size: var(--jl-font-size-sm);
}

.jl-wh-header {
  display: grid;
  grid-template-columns: 1fr 1fr auto;
  align-items: end;
  gap: var(--jl-space-2);
}

.jl-wh-error {
  margin: 0;
  color: var(--jl-color-danger);
  font-size: var(--jl-font-size-sm);
}

@media (max-width: 40rem) {
  .jl-wh-header {
    grid-template-columns: 1fr;
  }
}
</style>
