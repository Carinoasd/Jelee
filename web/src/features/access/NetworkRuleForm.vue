<script setup lang="ts">
import { computed, onMounted, reactive, shallowRef, useId, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import { ApiError, networkError } from "@/api/errors";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { useLibrariesStore } from "@/stores/libraries";
import { useNetworkRulesStore } from "@/stores/networkRules";
import { maxCidrLength, maxRuleCidrs, parseCidrs, type NetworkKind, type NetworkRule, type NetworkRuleInput, type SessionKind } from "./api";
import { networkKeys, sessionKindKeys } from "./networkLabels";

// Creates or edits one network rule. The address field takes one address or
// prefix per line; the server validates and masks them.
const props = defineProps<{ rule: NetworkRule | null }>();
const emit = defineEmits<{ saved: [rule: NetworkRule]; cancel: [] }>();
const { t } = useI18n();
const store = useNetworkRulesStore();
const libraries = useLibrariesStore();
const root = useTemplateRef<HTMLElement>("root");
const cidrsId = useId();

const source = props.rule;
interface FormState {
  libraryId: string;
  network: NetworkKind;
  cidrs: string;
  clientKinds: SessionKind[];
  includeAdmins: boolean;
  enabled: boolean;
  note: string;
}

const form = reactive<FormState>({
  libraryId: source?.libraryId ?? "",
  network: source?.network ?? "any",
  cidrs: (source?.cidrs ?? []).join("\n"),
  clientKinds: [...(source?.clientKinds ?? [])],
  includeAdmins: source?.includeAdmins ?? false,
  enabled: source?.enabled ?? true,
  note: source?.note ?? "",
});
const submitted = shallowRef(false);
const saving = shallowRef(false);
const serverError = shallowRef<ApiError | null>(null);

const libraryOptions = computed(() => {
  const options = libraries.libraries.map((library) => ({ value: library.id, label: library.name }));
  // A rule's library missing from the list (still loading, or unlisted)
  // keeps its stored name instead of falling back to the first option.
  if (source !== null && !options.some((option) => option.value === source.libraryId)) {
    options.unshift({ value: source.libraryId, label: source.libraryName });
  }
  return [{ value: "", label: t("networkRules.form.libraryNone") }, ...options];
});
const networkOptions = computed(() => (Object.keys(networkKeys) as NetworkKind[]).map((value) => ({ value, label: t(networkKeys[value]) })));

const cidrs = computed(() => parseCidrs(form.cidrs));
const errors = computed(() => {
  const tooLong = cidrs.value.find((value) => value.length > maxCidrLength);
  return {
    libraryId: form.libraryId === "" ? t("networkRules.form.libraryRequired") : null,
    cidrs:
      cidrs.value.length > maxRuleCidrs
        ? t("networkRules.form.tooMany", { max: maxRuleCidrs })
        : tooLong !== undefined
          ? t("networkRules.form.tooLong", { max: maxCidrLength })
          : null,
  };
});
const valid = computed(() => Object.values(errors.value).every((message) => message === null));
const shown = (message: string | null) => (submitted.value ? message : null);

function toggleKind(kind: SessionKind, on: boolean) {
  form.clientKinds = on ? [...form.clientKinds.filter((entry) => entry !== kind), kind] : form.clientKinds.filter((entry) => entry !== kind);
}

function input(): NetworkRuleInput {
  return {
    libraryId: form.libraryId,
    network: form.network,
    cidrs: cidrs.value,
    // Server order, whatever order the boxes were ticked in.
    clientKinds: (Object.keys(sessionKindKeys) as SessionKind[]).filter((kind) => form.clientKinds.includes(kind)),
    includeAdmins: form.includeAdmins,
    enabled: form.enabled,
    note: form.note,
  };
}

function serverMessage(error: ApiError): string {
  if (error.code === "invalid_request") {
    return t("networkRules.form.invalid");
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
    serverError.value = error instanceof ApiError ? error : networkError(error);
  } finally {
    saving.value = false;
  }
}

onMounted(() => {
  root.value?.querySelector<HTMLElement>("h2")?.focus();
  void libraries.ensureAll();
});
</script>

<template>
  <form ref="root" class="jl-card jl-netrule-form" aria-labelledby="netrule-form-title" novalidate @submit.prevent="submit">
    <h2 id="netrule-form-title" tabindex="-1">
      {{ rule === null ? t("networkRules.form.createTitle") : t("networkRules.form.editTitle") }}
    </h2>
    <div class="jl-card__fields">
      <div class="jl-netrule-form__field">
        <UiSelectField v-model="form.libraryId" :label="t('networkRules.form.library')" :options="libraryOptions" />
        <p v-if="shown(errors.libraryId)" class="jl-netrule-form__error" role="alert">{{ errors.libraryId }}</p>
      </div>
      <UiSelectField v-model="form.network" :label="t('networkRules.form.network')" :options="networkOptions" :hint="t('networkRules.form.networkHint')" />
      <UiTextField v-model="form.note" :label="t('networkRules.form.note')" :maxlength="2048" />
    </div>
    <div class="jl-netrule-form__field">
      <label class="jl-netrule-form__label" :for="cidrsId">{{ t("networkRules.form.cidrs") }}</label>
      <textarea
        :id="cidrsId"
        v-model="form.cidrs"
        class="jl-netrule-form__textarea"
        rows="4"
        spellcheck="false"
        autocapitalize="off"
        :aria-invalid="shown(errors.cidrs) ? 'true' : undefined"
        :aria-describedby="`${cidrsId}-hint${shown(errors.cidrs) ? ` ${cidrsId}-error` : ''}`"
      />
      <p :id="`${cidrsId}-hint`" class="jl-netrule-form__hint">{{ t("networkRules.form.cidrsHint", { max: maxRuleCidrs }) }}</p>
      <p v-if="shown(errors.cidrs)" :id="`${cidrsId}-error`" class="jl-netrule-form__error">{{ errors.cidrs }}</p>
    </div>
    <fieldset class="jl-netrule-form__kinds">
      <legend>{{ t("networkRules.form.clientKinds") }}</legend>
      <p class="jl-netrule-form__hint">{{ t("networkRules.form.clientKindsHint") }}</p>
      <UiCheckbox
        v-for="(key, kind) in sessionKindKeys"
        :key="kind"
        :model-value="form.clientKinds.includes(kind)"
        :label="t(key)"
        @update:model-value="toggleKind(kind, $event)"
      />
    </fieldset>
    <UiCheckbox v-model="form.includeAdmins" :label="t('networkRules.form.includeAdmins')" :hint="t('networkRules.form.includeAdminsHint')" />
    <UiCheckbox v-model="form.enabled" :label="t('networkRules.form.enabled')" />
    <UiAlert v-if="serverError" tone="danger">
      <p>{{ serverMessage(serverError) }}</p>
      <p v-if="serverError.traceId">{{ t("errors.traceId", { id: serverError.traceId }) }}</p>
    </UiAlert>
    <div class="jl-card__actions">
      <UiButton type="submit" :busy="saving">
        {{ rule === null ? t("networkRules.form.submitCreate") : t("networkRules.form.submitUpdate") }}
      </UiButton>
      <UiButton variant="secondary" :disabled="saving" @click="emit('cancel')">{{ t("common.cancel") }}</UiButton>
    </div>
  </form>
</template>

<style scoped src="../users/sections.css"></style>
<style scoped>
.jl-netrule-form h2 {
  margin: 0;
  font-size: var(--jl-font-size-lg);
}

.jl-netrule-form__field {
  display: grid;
  gap: var(--jl-space-1);
}

.jl-netrule-form__label,
.jl-netrule-form__kinds legend {
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}

.jl-netrule-form__textarea {
  padding: var(--jl-space-2) var(--jl-space-3);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-sm);
  background: var(--jl-color-surface);
  color: var(--jl-color-text);
  font-family: ui-monospace, monospace;
  resize: vertical;
}

.jl-netrule-form__textarea[aria-invalid="true"] {
  border-color: var(--jl-color-danger);
}

.jl-netrule-form__hint {
  margin: 0;
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-netrule-form__error {
  margin: 0;
  color: var(--jl-color-danger);
  font-size: var(--jl-font-size-sm);
}

.jl-netrule-form__kinds {
  display: grid;
  gap: var(--jl-space-2);
  margin: 0;
  padding: var(--jl-space-3) var(--jl-space-4);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-sm);
}
</style>
