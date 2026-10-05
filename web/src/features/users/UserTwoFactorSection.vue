<script setup lang="ts">
import { onMounted, shallowRef } from "vue";
import { useApi } from "@/api";
import { useRequest } from "@/api/requestState";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiSkeleton from "@/components/ui/UiSkeleton.vue";
import AppPasswordTable from "@/features/settings/AppPasswordTable.vue";
import {
  getTwoFactorStatus,
  listAppPasswords,
  resetTwoFactor,
  revokeAppPassword,
  type AppPassword,
} from "@/features/settings/twoFactorApi";
import { formatDateTime } from "@/i18n/format";
import { useTwoFactorI18n } from "@/i18n/twoFactor";
import type { User } from "./api";
import { useAdminFeedback } from "./feedback";

// Two-step verification of one account (G07.8): its state, a reset for a
// user who lost the authenticator and the recovery codes, and its
// application passwords with revoke.
const props = defineProps<{ user: User }>();
const { client } = useApi();
const { t, locale } = useTwoFactorI18n();
const feedback = useAdminFeedback();
const status = useRequest(() => getTwoFactorStatus(client, props.user.id));
const passwords = useRequest(() => listAppPasswords(client, props.user.id));
const busy = shallowRef<string | null>(null);

onMounted(() => {
  void status.run();
  void passwords.run();
});

async function reset() {
  busy.value = "reset";
  try {
    await resetTwoFactor(client, props.user.id);
    await feedback.done("done", "twoFactor.admin.resetDone");
    await status.run();
  } catch (error: unknown) {
    feedback.failed(error);
  } finally {
    busy.value = null;
  }
}

async function revoke(password: AppPassword) {
  busy.value = password.id;
  try {
    await revokeAppPassword(client, props.user.id, password.id);
    await feedback.done("done", "twoFactor.appPasswords.revoked");
    await passwords.run();
  } catch (error: unknown) {
    feedback.failed(error);
  } finally {
    busy.value = null;
  }
}
</script>

<template>
  <section class="jl-card" aria-labelledby="user-two-factor-title">
    <h2 id="user-two-factor-title">{{ t("twoFactor.admin.title") }}</h2>
    <RequestStatus :state="status.state.value" @retry="status.run">
      <template #loading>
        <UiSkeleton shape="title" />
      </template>
      <template #default="{ data }">
        <div class="jl-card__actions">
          <UiBadge :tone="data.enabled ? 'accent' : 'neutral'">{{ data.enabled ? t("twoFactor.on") : t("twoFactor.off") }}</UiBadge>
          <span v-if="data.enabled && data.enabledAt" class="jl-card__muted">
            {{ t("twoFactor.settings.enabledSince", { date: formatDateTime(data.enabledAt, locale) }) }}
          </span>
          <span v-if="data.enabled" class="jl-card__muted">{{ t("twoFactor.settings.remaining", { count: data.recoveryCodesRemaining }) }}</span>
        </div>
        <p v-if="data.pending && !data.enabled" class="jl-card__muted">{{ t("twoFactor.admin.pending") }}</p>
        <template v-if="data.enabled || data.pending">
          <p class="jl-card__impact">{{ t("twoFactor.admin.resetImpact") }}</p>
          <div class="jl-card__actions">
            <UiConfirmButton
              :label="t('twoFactor.admin.reset')"
              :confirm-label="t('twoFactor.admin.resetConfirm')"
              :prompt="t('twoFactor.admin.resetImpact')"
              :busy="busy === 'reset'"
              @confirm="reset"
            />
          </div>
        </template>
      </template>
    </RequestStatus>
    <h3 class="jl-card__subtitle">{{ t("twoFactor.appPasswords.title") }}</h3>
    <RequestStatus :state="passwords.state.value" @retry="passwords.run">
      <template #loading>
        <UiSkeleton shape="block" />
      </template>
      <template #default="{ data }">
        <AppPasswordTable :passwords="data" :busy-id="busy" @revoke="revoke" />
      </template>
    </RequestStatus>
  </section>
</template>

<style scoped src="./sections.css"></style>
<style scoped>
.jl-card__subtitle {
  margin: 0;
  font-size: var(--jl-font-size-md);
}
</style>
