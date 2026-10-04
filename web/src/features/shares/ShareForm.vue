<script setup lang="ts">
import { computed, onMounted, reactive, shallowRef, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import { ApiError, networkError } from "@/api/errors";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { useLibrariesStore } from "@/stores/libraries";
import { useSharesStore } from "@/stores/shares";
import { shareMaxConcurrency, shareMaxLifetimeMs, shareMinLifetimeMs, type ShareGrant, type ShareInput } from "./api";
import { lifetimes, type Lifetime } from "./labels";

// Creates one share link. Web guests only ever browse; allowPlayback only
// lets native clients redeem a session that may direct play the originals.
const emit = defineEmits<{ created: [grant: ShareGrant]; cancel: [] }>();
const { t } = useI18n();
const store = useSharesStore();
const libraries = useLibrariesStore();
const root = useTemplateRef<HTMLElement>("root");

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

interface FormState {
  scope: "library" | "item";
  libraryId: string;
  itemId: string;
  lifetime: Lifetime;
  customExpiry: string;
  readOnly: boolean;
  allowNative: boolean;
  concurrency: string;
  note: string;
}

const form = reactive<FormState>({
  scope: "library",
  libraryId: "",
  itemId: "",
  lifetime: "week",
  customExpiry: "",
  readOnly: true,
  allowNative: false,
  concurrency: "1",
  note: "",
});
const submitted = shallowRef(false);
const saving = shallowRef(false);
const serverError = shallowRef<ApiError | null>(null);

const scopeOptions = computed(() => [
  { value: "library", label: t("shares.create.scopeLibrary") },
  { value: "item", label: t("shares.create.scopeItem") },
]);
const libraryOptions = computed(() => [
  { value: "", label: t("shares.create.libraryNone") },
  ...libraries.libraries.map((library) => ({ value: library.id, label: library.name })),
]);
const lifetimeOptions = computed(() => (Object.keys(lifetimes) as Lifetime[]).map((value) => ({ value, label: t(lifetimes[value].key) })));

/** The chosen expiry in milliseconds since the epoch, or null when invalid. */
function expiry(now: number): number | null {
  if (form.lifetime !== "custom") {
    return now + lifetimes[form.lifetime].ms;
  }
  // datetime-local is the browser's local time without a zone.
  const time = Date.parse(form.customExpiry);
  if (Number.isNaN(time) || time - now < shareMinLifetimeMs || time - now > shareMaxLifetimeMs) {
    return null;
  }
  return time;
}

function concurrency(): number | null {
  const value = form.concurrency.trim();
  if (!/^\d+$/.test(value)) {
    return null;
  }
  const n = Number(value);
  return n >= 1 && n <= shareMaxConcurrency ? n : null;
}

const errors = computed(() => ({
  libraryId: form.scope === "library" && form.libraryId === "" ? t("shares.create.invalidLibrary") : null,
  itemId: form.scope === "item" && !uuid.test(form.itemId.trim()) ? t("shares.create.invalidItem") : null,
  customExpiry: form.lifetime === "custom" && expiry(Date.now()) === null ? t("shares.create.invalidExpiry") : null,
  concurrency: form.allowNative && concurrency() === null ? t("shares.create.invalidConcurrency", { max: shareMaxConcurrency }) : null,
}));
const valid = computed(() => Object.values(errors.value).every((message) => message === null));
const shown = (message: string | null) => (submitted.value ? message : null);

function input(): ShareInput {
  const body: ShareInput = {
    expiresAt: new Date(expiry(Date.now()) ?? Date.now()).toISOString(),
    readOnly: form.readOnly,
    allowPlayback: form.allowNative,
  };
  if (form.scope === "library") {
    body.libraryId = form.libraryId;
  } else {
    body.itemId = form.itemId.trim().toLowerCase();
  }
  if (form.allowNative) {
    body.maxStreams = concurrency() ?? 1;
  }
  if (form.note.trim() !== "") {
    body.note = form.note.trim();
  }
  return body;
}

function serverMessage(error: ApiError): string {
  if (error.code === "invalid_request") {
    return t("shares.create.refused");
  }
  if (error.code === "not_found") {
    return t("shares.create.scopeMissing");
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
    emit("created", await store.create(input()));
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
  <form ref="root" class="jl-card jl-share-form" aria-labelledby="share-form-title" novalidate @submit.prevent="submit">
    <h2 id="share-form-title" tabindex="-1">{{ t("shares.create.title") }}</h2>
    <div class="jl-card__fields">
      <UiSelectField v-model="form.scope" :label="t('shares.create.scope')" :options="scopeOptions" />
      <div v-if="form.scope === 'library'" class="jl-share-form__select">
        <UiSelectField v-model="form.libraryId" :label="t('shares.create.library')" :options="libraryOptions" />
        <p v-if="shown(errors.libraryId)" class="jl-share-form__error" role="alert">{{ errors.libraryId }}</p>
      </div>
      <UiTextField
        v-else
        v-model="form.itemId"
        :label="t('shares.create.itemId')"
        :hint="t('shares.create.itemIdHint')"
        :maxlength="36"
        required
        :error="shown(errors.itemId)"
      />
      <UiSelectField v-model="form.lifetime" :label="t('shares.create.expires')" :options="lifetimeOptions" />
      <UiTextField
        v-if="form.lifetime === 'custom'"
        v-model="form.customExpiry"
        type="datetime-local"
        :label="t('shares.create.customExpiry')"
        :hint="t('shares.create.customExpiryHint')"
        required
        :error="shown(errors.customExpiry)"
      />
      <UiTextField v-model="form.note" :label="t('shares.create.note')" :maxlength="512" />
    </div>
    <UiCheckbox v-model="form.readOnly" :label="t('shares.create.readOnly')" :hint="t('shares.create.readOnlyHint')" />
    <UiCheckbox v-model="form.allowNative" :label="t('shares.create.allowNative')" :hint="t('shares.create.allowNativeHint')" />
    <div v-if="form.allowNative" class="jl-card__fields">
      <UiTextField
        v-model="form.concurrency"
        :label="t('shares.create.concurrency', { max: shareMaxConcurrency })"
        :hint="t('shares.create.concurrencyHint')"
        inputmode="numeric"
        required
        :error="shown(errors.concurrency)"
      />
    </div>
    <p class="jl-card__impact">{{ t("shares.create.webNotice") }}</p>
    <UiAlert v-if="serverError" tone="danger">
      <p>{{ serverMessage(serverError) }}</p>
      <p v-if="serverError.traceId">{{ t("errors.traceId", { id: serverError.traceId }) }}</p>
    </UiAlert>
    <div class="jl-card__actions">
      <UiButton type="submit" :busy="saving">{{ t("shares.create.submit") }}</UiButton>
      <UiButton variant="secondary" :disabled="saving" @click="emit('cancel')">{{ t("common.cancel") }}</UiButton>
    </div>
  </form>
</template>

<style scoped src="../users/sections.css"></style>
<style scoped>
.jl-share-form h2 {
  margin: 0;
  font-size: var(--jl-font-size-lg);
}

.jl-share-form__select {
  display: grid;
  gap: var(--jl-space-1);
}

.jl-share-form__error {
  margin: 0;
  color: var(--jl-color-danger);
  font-size: var(--jl-font-size-sm);
}
</style>
