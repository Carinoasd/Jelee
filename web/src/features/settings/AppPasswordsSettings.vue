<script setup lang="ts">
import { computed, nextTick, onMounted, shallowRef, useTemplateRef } from "vue";
import { useApi } from "@/api";
import { ApiError, networkError } from "@/api/errors";
import { useRequest } from "@/api/requestState";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { useTwoFactorI18n } from "@/i18n/twoFactor";
import { useToastStore } from "@/stores/toasts";
import AppPasswordTable from "./AppPasswordTable.vue";
import { copyText } from "./clipboard";
import {
  appPasswordNameMaxBytes,
  appPasswordNameProblem,
  createAppPassword,
  listAppPasswords,
  revokeAppPassword,
  type AppPassword,
  type NewAppPassword,
} from "./twoFactorApi";

// Application passwords of the signed-in account (G07.8): for native apps
// and compatibility clients, which cannot ask for a second factor. A new
// password is shown once, then only its name and dates remain.
const props = defineProps<{ userId: string }>();
const { client } = useApi();
const { t } = useTwoFactorI18n();
const toasts = useToastStore();
const root = useTemplateRef<HTMLElement>("root");

const list = useRequest(() => listAppPasswords(client, props.userId));
onMounted(() => {
  void list.run();
});

const name = shallowRef("");
const submitted = shallowRef(false);
const creating = shallowRef(false);
const createError = shallowRef<string | null>(null);
const revealed = shallowRef<NewAppPassword | null>(null);
const copyState = shallowRef<"idle" | "copied" | "failed">("idle");
const revoking = shallowRef<string | null>(null);

const nameError = computed(() => {
  if (!submitted.value) {
    return null;
  }
  const problem = appPasswordNameProblem(name.value.trim());
  if (problem === "required") {
    return t("twoFactor.appPasswords.nameRequired");
  }
  return problem === "invalid" ? t("twoFactor.appPasswords.nameInvalid") : null;
});

function failureKey(error: unknown): string {
  return errorMessageKey(error instanceof ApiError ? error : networkError(error));
}

async function create() {
  submitted.value = true;
  createError.value = null;
  if (nameError.value !== null || creating.value) {
    return;
  }
  creating.value = true;
  try {
    revealed.value = await createAppPassword(client, name.value.trim());
    copyState.value = "idle";
    name.value = "";
    submitted.value = false;
    void list.run();
    await nextTick();
    root.value?.querySelector<HTMLElement>("#app-password-secret-title")?.focus();
  } catch (error: unknown) {
    // The only conflict here is the limit of 20 per account.
    createError.value = error instanceof ApiError && error.code === "conflict" ? "twoFactor.appPasswords.limit" : failureKey(error);
  } finally {
    creating.value = false;
  }
}

async function copy() {
  if (revealed.value !== null) {
    copyState.value = (await copyText(revealed.value.password)) ? "copied" : "failed";
  }
}

async function dismiss() {
  revealed.value = null;
  await nextTick();
  root.value?.querySelector<HTMLElement>("#settings-app-passwords")?.focus();
}

async function revoke(password: AppPassword) {
  revoking.value = password.id;
  try {
    await revokeAppPassword(client, props.userId, password.id);
    toasts.push("twoFactor.appPasswords.revoked", "success");
    await list.run();
  } catch (error: unknown) {
    toasts.push(failureKey(error), "danger");
  } finally {
    revoking.value = null;
  }
}
</script>

<template>
  <section ref="root" class="jl-settings__card jl-apppws" aria-labelledby="settings-app-passwords">
    <h2 id="settings-app-passwords" tabindex="-1">{{ t("twoFactor.appPasswords.title") }}</h2>
    <p class="jl-apppws__hint">{{ t("twoFactor.appPasswords.intro") }}</p>

    <section v-if="revealed" class="jl-apppws__secret" aria-labelledby="app-password-secret-title">
      <h3 id="app-password-secret-title" tabindex="-1">{{ t("twoFactor.appPasswords.secretTitle", { name: revealed.appPassword.name }) }}</h3>
      <p>{{ t("twoFactor.appPasswords.secretBody") }}</p>
      <p class="jl-apppws__value">
        <code id="app-password-value" data-app-password>{{ revealed.password }}</code>
      </p>
      <div class="jl-apppws__actions">
        <UiButton variant="secondary" aria-describedby="app-password-value" @click="copy">{{ t("twoFactor.appPasswords.copy") }}</UiButton>
        <UiButton @click="dismiss">{{ t("twoFactor.appPasswords.done") }}</UiButton>
      </div>
      <p class="jl-apppws__status" role="status">
        <template v-if="copyState === 'copied'">{{ t("twoFactor.copied") }}</template>
        <template v-else-if="copyState === 'failed'">{{ t("twoFactor.copyFailed") }}</template>
      </p>
    </section>

    <RequestStatus :state="list.state.value" @retry="list.run">
      <template #default="{ data }">
        <AppPasswordTable :passwords="data" :busy-id="revoking" @revoke="revoke" />
      </template>
    </RequestStatus>

    <form class="jl-apppws__form" novalidate @submit.prevent="create">
      <UiAlert v-if="createError" tone="danger">{{ t(createError) }}</UiAlert>
      <UiTextField
        v-model="name"
        :label="t('twoFactor.appPasswords.name')"
        :hint="t('twoFactor.appPasswords.nameHint')"
        autocomplete="off"
        required
        :maxlength="appPasswordNameMaxBytes"
        :error="nameError"
      />
      <div>
        <UiButton type="submit" variant="secondary" :busy="creating">{{ t("twoFactor.appPasswords.create") }}</UiButton>
      </div>
    </form>
  </section>
</template>

<style scoped>
.jl-apppws__hint {
  margin: 0;
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-apppws__form {
  display: grid;
  gap: var(--jl-space-3);
}

.jl-apppws__secret {
  display: grid;
  gap: var(--jl-space-3);
  padding: var(--jl-space-4);
  border: 2px solid var(--jl-color-danger);
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-danger-bg);
}

.jl-apppws__secret h3 {
  margin: 0;
  font-size: var(--jl-font-size-md);
  color: var(--jl-color-danger);
}

.jl-apppws__secret p {
  margin: 0;
}

.jl-apppws__value {
  padding: var(--jl-space-3);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-sm);
  background: var(--jl-color-surface);
}

.jl-apppws__value code {
  font-family: ui-monospace, monospace;
  overflow-wrap: anywhere;
  user-select: all;
}

.jl-apppws__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
}

.jl-apppws__status {
  min-height: 1.5em;
  font-size: var(--jl-font-size-sm);
}
</style>
