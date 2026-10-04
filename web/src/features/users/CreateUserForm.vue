<script setup lang="ts">
import { computed, reactive, shallowRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ApiError, networkError } from "@/api/errors";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import type { SelectOption } from "@/components/ui/types";
import { errorMessageKey } from "@/features/errors/messages";
import { useToastStore } from "@/stores/toasts";
import { useUsersStore } from "@/stores/users";
import {
  checkNewUser,
  hasProblems,
  isUserLocale,
  localeNameKey,
  newUserBody,
  passwordMaxBytes,
  passwordMinBytes,
  userLocales,
  type NewUserForm,
  type NewUserProblems,
} from "./form";

const { t, locale } = useI18n();
const store = useUsersStore();
const toasts = useToastStore();

function blankForm(): NewUserForm {
  return {
    name: "",
    profileName: "",
    password: "",
    locale: isUserLocale(locale.value) ? locale.value : "en-US",
    admin: false,
    hidden: false,
  };
}

const form = reactive<NewUserForm>(blankForm());
const problems = shallowRef<NewUserProblems>({ name: null, profileName: null, password: null });
const failure = shallowRef<ApiError | null>(null);
// One key per submission: a retry of the same, unchanged form reuses it so a
// lost response cannot create the account twice; any edit starts a new one.
const idempotencyKey = shallowRef<string | null>(null);

watch(form, () => {
  idempotencyKey.value = null;
});

const localeOptions = computed<SelectOption[]>(() =>
  userLocales.map((value) => ({ value, label: t(localeNameKey[value]) })),
);
const localeModel = computed({
  get: () => form.locale,
  set: (value: string) => {
    if (isUserLocale(value)) {
      form.locale = value;
    }
  },
});

function failureKey(error: ApiError): string {
  switch (error.code) {
    case "conflict":
      return "users.create.nameTaken";
    case "invalid_request":
      return "users.create.invalid";
    default:
      return errorMessageKey(error);
  }
}

async function submit() {
  problems.value = checkNewUser(form);
  if (hasProblems(problems.value)) {
    return;
  }
  failure.value = null;
  idempotencyKey.value ??= crypto.randomUUID();
  try {
    const user = await store.create(newUserBody(form), idempotencyKey.value);
    toasts.push("users.create.created", "success", { name: user.name });
    Object.assign(form, blankForm());
    // Assigning the form cleared the key through the watcher; be explicit.
    idempotencyKey.value = null;
  } catch (error: unknown) {
    failure.value = error instanceof ApiError ? error : networkError(error);
  }
}
</script>

<template>
  <section class="jl-card" aria-labelledby="create-user-title">
    <h2 id="create-user-title">{{ t("users.create.title") }}</h2>
    <form class="jl-create" novalidate @submit.prevent="submit">
      <div class="jl-card__fields">
        <UiTextField
          v-model="form.name"
          :label="t('users.form.name')"
          :hint="t('users.form.nameHint')"
          :error="problems.name ? t(problems.name) : null"
          autocomplete="off"
          required
        />
        <UiTextField
          v-model="form.profileName"
          :label="t('users.form.profileName')"
          :error="problems.profileName ? t(problems.profileName) : null"
          autocomplete="off"
        />
        <UiTextField
          v-model="form.password"
          type="password"
          :label="t('users.create.password')"
          :hint="t('users.create.passwordHint', { min: passwordMinBytes, max: passwordMaxBytes })"
          :error="problems.password ? t(problems.password, { min: passwordMinBytes, max: passwordMaxBytes }) : null"
          autocomplete="new-password"
          required
        />
        <UiSelectField v-model="localeModel" :label="t('users.form.locale')" :options="localeOptions" />
      </div>
      <UiCheckbox v-model="form.admin" :label="t('users.form.admin')" :hint="t('users.form.adminHint')" />
      <UiCheckbox v-model="form.hidden" :label="t('users.form.hidden')" :hint="t('users.form.hiddenHint')" />
      <UiAlert v-if="failure" tone="danger">
        <p>{{ t(failureKey(failure)) }}</p>
        <p v-if="failure.traceId" class="jl-create__trace">{{ t("errors.traceId", { id: failure.traceId }) }}</p>
      </UiAlert>
      <div class="jl-card__actions">
        <UiButton type="submit" :busy="store.creating">{{ t("users.create.submit") }}</UiButton>
      </div>
    </form>
  </section>
</template>

<style scoped src="./sections.css"></style>
<style scoped>
.jl-create {
  display: grid;
  gap: var(--jl-space-4);
}

.jl-create__trace {
  font-family: ui-monospace, monospace;
  font-size: var(--jl-font-size-sm);
}
</style>
