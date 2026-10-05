<script setup lang="ts">
import { computed, onMounted, shallowRef } from "vue";
import { useRouter } from "vue-router";
import { useApi } from "@/api";
import { ApiError, networkError } from "@/api/errors";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { factorProblem, proofOf } from "@/features/auth/secondFactor";
import SecondFactorFields from "@/features/auth/SecondFactorFields.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { useTwoFactorI18n } from "@/i18n/twoFactor";
import { useAuthStore } from "@/stores/auth";
import { useToastStore } from "@/stores/toasts";
import { deleteMyAccount, downloadMyData } from "./dataRightsApi";
import { getTwoFactorStatus } from "./twoFactorApi";

// Personal data of the signed-in account (G07.7): download everything the
// server keeps about it, or delete the account for good. Deleting asks for
// the password (and the second factor when it is on), an explicit
// acknowledgement and a second press.
const props = defineProps<{ userId: string }>();
const { client } = useApi();
const { t } = useTwoFactorI18n();
const auth = useAuthStore();
const router = useRouter();
const toasts = useToastStore();

function failureKey(error: unknown): string {
  return errorMessageKey(error instanceof ApiError ? error : networkError(error));
}

// Whether the account has a second factor; unknown counts as off, and the
// server answers invalid_two_factor_code if it was on after all.
const factorOn = shallowRef(false);
onMounted(async () => {
  try {
    factorOn.value = (await getTwoFactorStatus(client, props.userId)).enabled;
  } catch {
    factorOn.value = false;
  }
});

const exporting = shallowRef(false);
const exportError = shallowRef<string | null>(null);

async function exportData() {
  exportError.value = null;
  exporting.value = true;
  try {
    await downloadMyData(client, props.userId);
    toasts.push("settings.dataRights.exported", "success");
  } catch (error: unknown) {
    exportError.value = failureKey(error);
  } finally {
    exporting.value = false;
  }
}

const password = shallowRef("");
const factor = shallowRef("");
const recovery = shallowRef(false);
const acknowledged = shallowRef(false);
const submitted = shallowRef(false);
const deleting = shallowRef(false);
const deleteError = shallowRef<string | null>(null);

const passwordError = computed(() => (submitted.value && password.value === "" ? t("settings.dataRights.passwordRequired") : null));
const factorError = computed(() => {
  const key = submitted.value && factorOn.value ? factorProblem(factor.value, recovery.value) : null;
  return key === null ? null : t(key);
});
const acknowledgeError = computed(() => (submitted.value && !acknowledged.value ? t("settings.dataRights.acknowledgeRequired") : null));

async function deleteAccount() {
  submitted.value = true;
  deleteError.value = null;
  if (passwordError.value !== null || factorError.value !== null || acknowledgeError.value !== null) {
    return;
  }
  deleting.value = true;
  try {
    await deleteMyAccount(client, password.value, factorOn.value ? proofOf(factor.value, recovery.value) : null);
  } catch (error: unknown) {
    deleteError.value = failureKey(error);
    return;
  } finally {
    password.value = "";
    factor.value = "";
    deleting.value = false;
  }
  // The session ended with the account.
  auth.expire();
  toasts.push("settings.dataRights.deleted", "success");
  await router.replace({ name: "login" });
}
</script>

<template>
  <section class="jl-settings__card jl-datarights" aria-labelledby="settings-data-rights">
    <h2 id="settings-data-rights">{{ t("settings.dataRights.title") }}</h2>

    <div class="jl-datarights__part">
      <h3>{{ t("settings.dataRights.exportTitle") }}</h3>
      <p class="jl-datarights__hint">{{ t("settings.dataRights.exportHint") }}</p>
      <UiAlert v-if="exportError" tone="danger">{{ t(exportError) }}</UiAlert>
      <div>
        <UiButton variant="secondary" :busy="exporting" @click="exportData">{{ t("settings.dataRights.export") }}</UiButton>
      </div>
    </div>

    <form class="jl-datarights__part" aria-labelledby="data-rights-delete-title" novalidate @submit.prevent>
      <h3 id="data-rights-delete-title">{{ t("settings.dataRights.deleteTitle") }}</h3>
      <UiAlert tone="danger">{{ t("settings.dataRights.deleteWarning") }}</UiAlert>
      <p class="jl-datarights__hint">{{ t("settings.dataRights.deleteHint") }}</p>
      <UiAlert v-if="deleteError" tone="danger">{{ t(deleteError) }}</UiAlert>
      <UiTextField
        v-model="password"
        type="password"
        autocomplete="current-password"
        required
        :label="t('settings.dataRights.password')"
        :error="passwordError"
      />
      <SecondFactorFields v-if="factorOn" v-model="factor" v-model:recovery="recovery" :error="factorError" />
      <UiCheckbox v-model="acknowledged" :label="t('settings.dataRights.acknowledge')" />
      <UiAlert v-if="acknowledgeError" tone="danger">{{ acknowledgeError }}</UiAlert>
      <div>
        <UiConfirmButton
          :label="t('settings.dataRights.delete')"
          :confirm-label="t('settings.dataRights.confirmDelete')"
          :prompt="t('settings.dataRights.confirmPrompt')"
          :busy="deleting"
          @confirm="deleteAccount"
        />
      </div>
    </form>
  </section>
</template>

<style scoped>
.jl-datarights h3 {
  margin: 0;
  font-size: var(--jl-font-size-md);
}

.jl-datarights__part {
  display: grid;
  gap: var(--jl-space-3);
}

.jl-datarights__part + .jl-datarights__part {
  padding-top: var(--jl-space-4);
  border-top: 1px solid var(--jl-color-border);
}

.jl-datarights__hint {
  margin: 0;
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}
</style>
