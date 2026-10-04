<script setup lang="ts">
import { computed, onMounted, reactive, shallowRef, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import type { ApiError } from "@/api/errors";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { useClientRulesStore } from "@/stores/clientsRules";
import { isObserving, type ClientRule, type ClientRuleInput, type RuleAction, type RuleDimension, type RuleIntent, type RuleMatch } from "./api";
import { asApiError, dimensionKeys, intentKeys, matchKeys, ruleActionKeys } from "./labels";

// Creates or edits one rule. The form covers the global scope without a time
// window; when an existing rule has a scope or window, both are sent back
// unchanged so saving here never widens or drops them.
const props = defineProps<{ rule: ClientRule | null }>();
const emit = defineEmits<{ saved: [rule: ClientRule]; cancel: [] }>();
const { t } = useI18n();
const store = useClientRulesStore();
const root = useTemplateRef<HTMLElement>("root");

interface FormState {
  dimension: string;
  header: string;
  match: string;
  pattern: string;
  action: string;
  intent: string;
  priority: string;
  caseFold: boolean;
  enabled: boolean;
  note: string;
  requests: string;
  periodSeconds: string;
}

const source = props.rule;
const form = reactive<FormState>({
  dimension: source?.dimension ?? "user_agent",
  header: source?.header ?? "",
  match: source?.match ?? "exact",
  pattern: source?.pattern ?? "",
  action: source?.action ?? "observe",
  intent: source?.intent ?? "deny",
  priority: String(source?.priority ?? 0),
  caseFold: source?.caseFold ?? false,
  enabled: source?.enabled ?? true,
  note: source?.note ?? "",
  requests: String(source?.rateLimit?.requests ?? 60),
  periodSeconds: String(source?.rateLimit?.periodSeconds ?? 60),
});
const submitted = shallowRef(false);
const saving = shallowRef(false);
const serverError = shallowRef<ApiError | null>(null);
const keepsScope = source !== null && (source.scopeKind !== "global" || source.window !== undefined);

function options<K extends string>(keys: Readonly<Record<K, string>>) {
  return (Object.keys(keys) as K[]).map((value) => ({ value, label: t(keys[value]) }));
}

const dimensionOptions = computed(() => options(dimensionKeys));
const matchOptions = computed(() => options(matchKeys));
const actionOptions = computed(() => options(ruleActionKeys));
const intentOptions = computed(() => options(intentKeys));

const observing = computed(() => isObserving({ action: form.action as RuleAction }));
const limited = computed(() => form.action === "rate_limit" || (observing.value && form.intent === "rate_limit"));

function integer(value: string, min: number, max: number): number | null {
  if (!/^-?\d+$/.test(value.trim())) {
    return null;
  }
  const n = Number(value.trim());
  return Number.isSafeInteger(n) && n >= min && n <= max ? n : null;
}

const errors = computed(() => {
  const range = (min: number, max: number) => t("clients.ruleForm.integer", { min, max });
  return {
    header: form.dimension === "header" && form.header.trim() === "" ? t("clients.ruleForm.required") : null,
    pattern: form.match !== "absent" && form.pattern === "" ? t("clients.ruleForm.required") : null,
    priority: integer(form.priority, -1000000, 1000000) === null ? range(-1000000, 1000000) : null,
    requests: limited.value && integer(form.requests, 1, 1000000) === null ? range(1, 1000000) : null,
    periodSeconds: limited.value && integer(form.periodSeconds, 1, 86400) === null ? range(1, 86400) : null,
  };
});
const valid = computed(() => Object.values(errors.value).every((message) => message === null));
const shown = (message: string | null) => (submitted.value ? message : null);

function input(): ClientRuleInput {
  const body: ClientRuleInput = {
    dimension: form.dimension as RuleDimension,
    match: form.match as RuleMatch,
    pattern: form.match === "absent" ? "" : form.pattern,
    action: form.action as RuleAction,
    priority: integer(form.priority, -1000000, 1000000) ?? 0,
    caseFold: form.caseFold,
    enabled: form.enabled,
    note: form.note,
    scopeKind: source?.scopeKind ?? "global",
    scopeValues: source?.scopeValues ?? [],
  };
  if (form.dimension === "header") {
    body.header = form.header.trim();
  }
  if (observing.value) {
    body.intent = form.intent as RuleIntent;
  }
  if (limited.value) {
    body.rateLimit = { requests: integer(form.requests, 1, 1000000) ?? 1, periodSeconds: integer(form.periodSeconds, 1, 86400) ?? 1 };
  }
  if (source?.window !== undefined) {
    body.window = source.window;
  }
  return body;
}

function serverMessage(error: ApiError): string {
  if (error.code === "invalid_request") {
    return t("clients.ruleForm.invalid");
  }
  if (error.code === "conflict") {
    return t("clients.ruleForm.limit");
  }
  return t(errorMessageKey(error));
}

async function submit() {
  submitted.value = true;
  serverError.value = null;
  if (!valid.value) {
    root.value?.querySelector<HTMLElement>("[aria-invalid='true']")?.focus();
    return;
  }
  saving.value = true;
  try {
    const rule = source === null ? await store.create(input()) : await store.update(source.id, input());
    emit("saved", rule);
  } catch (error: unknown) {
    serverError.value = asApiError(error);
  } finally {
    saving.value = false;
  }
}

onMounted(() => {
  root.value?.querySelector<HTMLElement>("h3")?.focus();
});
</script>

<template>
  <form ref="root" class="jl-cc-card jl-cc-form" aria-labelledby="clients-rule-form-title" novalidate @submit.prevent="submit">
    <h3 id="clients-rule-form-title" tabindex="-1">
      {{ rule === null ? t("clients.ruleForm.createTitle") : t("clients.ruleForm.editTitle") }}
    </h3>
    <p class="jl-cc-muted">{{ keepsScope ? t("clients.ruleForm.scopeKept") : t("clients.ruleForm.scopeGlobal") }}</p>
    <div class="jl-cc-grid">
      <UiSelectField v-model="form.dimension" :label="t('clients.ruleForm.dimension')" :options="dimensionOptions" :hint="t('clients.ruleForm.dimensionHint')" />
      <UiTextField
        v-if="form.dimension === 'header'"
        v-model="form.header"
        :label="t('clients.ruleForm.header')"
        :maxlength="256"
        required
        :error="shown(errors.header)"
      />
      <UiSelectField v-model="form.match" :label="t('clients.ruleForm.match')" :options="matchOptions" :hint="t('clients.ruleForm.matchHint')" />
      <UiTextField
        v-if="form.match !== 'absent'"
        v-model="form.pattern"
        :label="t('clients.ruleForm.pattern')"
        :maxlength="1024"
        required
        :error="shown(errors.pattern)"
      />
      <UiSelectField v-model="form.action" :label="t('clients.ruleForm.action')" :options="actionOptions" :hint="t('clients.ruleForm.actionHint')" />
      <UiSelectField
        v-if="observing"
        v-model="form.intent"
        :label="t('clients.ruleForm.intent')"
        :options="intentOptions"
        :hint="t('clients.ruleForm.intentHint')"
      />
      <template v-if="limited">
        <UiTextField v-model="form.requests" :label="t('clients.ruleForm.rateRequests')" inputmode="numeric" required :error="shown(errors.requests)" />
        <UiTextField
          v-model="form.periodSeconds"
          :label="t('clients.ruleForm.ratePeriod')"
          inputmode="numeric"
          required
          :error="shown(errors.periodSeconds)"
        />
      </template>
      <UiTextField
        v-model="form.priority"
        :label="t('clients.ruleForm.priority')"
        inputmode="numeric"
        :hint="t('clients.ruleForm.priorityHint')"
        :error="shown(errors.priority)"
      />
      <UiTextField v-model="form.note" :label="t('clients.ruleForm.note')" :maxlength="2048" />
    </div>
    <UiCheckbox v-model="form.caseFold" :label="t('clients.ruleForm.caseFold')" />
    <UiCheckbox v-model="form.enabled" :label="t('clients.ruleForm.enabled')" />
    <UiAlert v-if="serverError" tone="danger">
      <p>{{ serverMessage(serverError) }}</p>
      <p v-if="serverError.traceId" class="jl-cc-code">{{ t("errors.traceId", { id: serverError.traceId }) }}</p>
    </UiAlert>
    <div class="jl-cc-actions">
      <UiButton type="submit" :busy="saving">
        {{ rule === null ? t("clients.ruleForm.submitCreate") : t("clients.ruleForm.submitUpdate") }}
      </UiButton>
      <UiButton variant="secondary" :disabled="saving" @click="emit('cancel')">{{ t("common.cancel") }}</UiButton>
    </div>
  </form>
</template>
