<script setup lang="ts">
import { computed, nextTick, onMounted, shallowRef, useTemplateRef } from "vue";
import { useApi } from "@/api";
import { ApiError, networkError } from "@/api/errors";
import { useRequest } from "@/api/requestState";
import RequestStatus from "@/components/ui/RequestStatus.vue";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiBadge from "@/components/ui/UiBadge.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { factorProblem, proofOf } from "@/features/auth/secondFactor";
import SecondFactorFields from "@/features/auth/SecondFactorFields.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { formatDateTime } from "@/i18n/format";
import { useTwoFactorI18n } from "@/i18n/twoFactor";
import { useToastStore } from "@/stores/toasts";
import { copyText } from "./clipboard";
import QrCodeSvg from "./QrCodeSvg.vue";
import RecoveryCodesPanel from "./RecoveryCodesPanel.vue";
import {
  confirmTwoFactor,
  disableTwoFactor,
  enrollTwoFactor,
  getTwoFactorStatus,
  regenerateRecoveryCodes,
  type TwoFactorEnrollment,
} from "./twoFactorApi";

// Two-step verification of the signed-in account (G07.8): status, setup
// with QR code and confirmation, one-time recovery codes, new recovery codes
// and turning it off.
const props = defineProps<{ userId: string; account: string }>();
const { client } = useApi();
const { t, locale } = useTwoFactorI18n();
const toasts = useToastStore();
const root = useTemplateRef<HTMLElement>("root");

const status = useRequest(() => getTwoFactorStatus(client, props.userId));
onMounted(() => {
  void status.run();
});

/** Recovery codes to show once; justEnabled adds the notes about other sessions. */
const codes = shallowRef<readonly string[] | null>(null);
const justEnabled = shallowRef(false);
const enrollment = shallowRef<TwoFactorEnrollment | null>(null);
const busy = shallowRef<"enroll" | "confirm" | "regenerate" | "disable" | null>(null);
const copyState = shallowRef<"idle" | "copied" | "failed">("idle");

/** Low enough to suggest new codes. */
const lowRecoveryCodes = 3;

function failureKey(error: unknown): string {
  return errorMessageKey(error instanceof ApiError ? error : networkError(error));
}

async function focusIn(selector: string) {
  await nextTick();
  root.value?.querySelector<HTMLElement>(selector)?.focus();
}

/** The setup key in groups of four, easier to type by hand. */
const groupedSecret = computed(() => enrollment.value?.secret.replace(/(.{4})(?=.)/g, "$1 ") ?? "");

// Setup.
const startError = shallowRef<string | null>(null);
const setupCode = shallowRef("");
const setupError = shallowRef<string | null>(null);

async function start() {
  startError.value = null;
  busy.value = "enroll";
  try {
    enrollment.value = await enrollTwoFactor(client);
    setupCode.value = "";
    setupError.value = null;
    copyState.value = "idle";
    await focusIn("#two-factor-setup-title");
  } catch (error: unknown) {
    startError.value = failureKey(error);
  } finally {
    busy.value = null;
  }
}

async function copy(text: string) {
  copyState.value = (await copyText(text)) ? "copied" : "failed";
}

async function cancelSetup() {
  enrollment.value = null;
  setupCode.value = "";
  await focusIn("#settings-two-factor");
}

async function confirm() {
  setupError.value = factorProblem(setupCode.value, false);
  if (setupError.value !== null) {
    return;
  }
  busy.value = "confirm";
  try {
    codes.value = await confirmTwoFactor(client, setupCode.value);
    justEnabled.value = true;
    enrollment.value = null;
    void status.run();
  } catch (error: unknown) {
    setupError.value = failureKey(error);
  } finally {
    setupCode.value = "";
    busy.value = null;
  }
}

async function closeCodes() {
  codes.value = null;
  justEnabled.value = false;
  await focusIn("#settings-two-factor");
}

// New recovery codes.
const regenerateCode = shallowRef("");
const regenerateError = shallowRef<string | null>(null);

async function regenerate() {
  regenerateError.value = factorProblem(regenerateCode.value, false);
  if (regenerateError.value !== null) {
    return;
  }
  busy.value = "regenerate";
  try {
    codes.value = await regenerateRecoveryCodes(client, regenerateCode.value);
    justEnabled.value = false;
    void status.run();
  } catch (error: unknown) {
    regenerateError.value = failureKey(error);
  } finally {
    regenerateCode.value = "";
    busy.value = null;
  }
}

// Turning it off.
const disablePassword = shallowRef("");
const disableFactor = shallowRef("");
const disableRecovery = shallowRef(false);
const disableSubmitted = shallowRef(false);
const disableError = shallowRef<string | null>(null);
const disablePasswordError = computed(() =>
  disableSubmitted.value && disablePassword.value === "" ? t("twoFactor.settings.passwordRequired") : null,
);
const disableFactorError = computed(() => {
  const key = disableSubmitted.value ? factorProblem(disableFactor.value, disableRecovery.value) : null;
  return key === null ? null : t(key);
});

async function disable() {
  disableSubmitted.value = true;
  disableError.value = null;
  if (disablePasswordError.value !== null || disableFactorError.value !== null) {
    return;
  }
  busy.value = "disable";
  try {
    await disableTwoFactor(client, disablePassword.value, proofOf(disableFactor.value, disableRecovery.value));
  } catch (error: unknown) {
    disableError.value = failureKey(error);
    return;
  } finally {
    disablePassword.value = "";
    disableFactor.value = "";
    busy.value = null;
  }
  disableSubmitted.value = false;
  disableRecovery.value = false;
  toasts.push("twoFactor.settings.disabled", "success");
  await status.run();
  await focusIn("#settings-two-factor");
}
</script>

<template>
  <section ref="root" class="jl-settings__card jl-twofactor" aria-labelledby="settings-two-factor">
    <h2 id="settings-two-factor" tabindex="-1">{{ t("twoFactor.settings.title") }}</h2>
    <p class="jl-twofactor__hint">{{ t("twoFactor.settings.intro") }}</p>

    <template v-if="codes">
      <UiAlert v-if="justEnabled" tone="success">
        <p>
          <strong>{{ t("twoFactor.settings.enabledTitle") }}</strong>
        </p>
        <p>{{ t("twoFactor.settings.othersSignedOut") }}</p>
        <p>{{ t("twoFactor.settings.useAppPasswords") }}</p>
      </UiAlert>
      <RecoveryCodesPanel :codes="codes" :account="account" @done="closeCodes" />
    </template>

    <RequestStatus v-else :state="status.state.value" @retry="status.run">
      <template #default="{ data }">
        <p class="jl-twofactor__status">
          <span>{{ t("twoFactor.settings.status") }}</span>
          <UiBadge :tone="data.enabled ? 'accent' : 'neutral'">{{ data.enabled ? t("twoFactor.on") : t("twoFactor.off") }}</UiBadge>
          <span v-if="data.enabled && data.enabledAt">{{ t("twoFactor.settings.enabledSince", { date: formatDateTime(data.enabledAt, locale) }) }}</span>
        </p>
        <UiAlert v-if="!data.available">{{ t("twoFactor.settings.unavailable") }}</UiAlert>

        <template v-if="data.enabled">
          <p class="jl-twofactor__hint">{{ t("twoFactor.settings.remaining", { count: data.recoveryCodesRemaining }) }}</p>
          <UiAlert v-if="data.recoveryCodesRemaining <= lowRecoveryCodes">{{ t("twoFactor.settings.remainingLow") }}</UiAlert>

          <form class="jl-twofactor__form" aria-labelledby="two-factor-regenerate-title" novalidate @submit.prevent="regenerate">
            <h3 id="two-factor-regenerate-title">{{ t("twoFactor.settings.regenerateTitle") }}</h3>
            <p class="jl-twofactor__hint">{{ t("twoFactor.settings.regenerateHint") }}</p>
            <UiTextField
              v-model="regenerateCode"
              :label="t('twoFactor.code.label')"
              :hint="t('twoFactor.code.hint')"
              inputmode="numeric"
              autocomplete="one-time-code"
              required
              :maxlength="12"
              :error="regenerateError ? t(regenerateError) : null"
            />
            <div>
              <UiButton type="submit" variant="secondary" :busy="busy === 'regenerate'">{{ t("twoFactor.settings.regenerate") }}</UiButton>
            </div>
          </form>

          <form class="jl-twofactor__form" aria-labelledby="two-factor-disable-title" novalidate @submit.prevent="disable">
            <h3 id="two-factor-disable-title">{{ t("twoFactor.settings.disableTitle") }}</h3>
            <p class="jl-twofactor__hint">{{ t("twoFactor.settings.disableHint") }}</p>
            <UiAlert v-if="disableError" tone="danger">{{ t(disableError) }}</UiAlert>
            <UiTextField
              v-model="disablePassword"
              type="password"
              autocomplete="current-password"
              required
              :label="t('twoFactor.settings.password')"
              :error="disablePasswordError"
            />
            <SecondFactorFields v-model="disableFactor" v-model:recovery="disableRecovery" :error="disableFactorError" />
            <div>
              <UiButton type="submit" variant="danger" :busy="busy === 'disable'">{{ t("twoFactor.settings.disable") }}</UiButton>
            </div>
          </form>
        </template>

        <template v-else-if="data.available && enrollment === null">
          <UiAlert v-if="startError" tone="danger">{{ t(startError) }}</UiAlert>
          <div>
            <UiButton :busy="busy === 'enroll'" @click="start">{{ t("twoFactor.settings.start") }}</UiButton>
          </div>
        </template>

        <form v-else-if="enrollment" class="jl-twofactor__form" aria-labelledby="two-factor-setup-title" novalidate @submit.prevent="confirm">
          <h3 id="two-factor-setup-title" tabindex="-1">{{ t("twoFactor.settings.setupTitle") }}</h3>
          <p class="jl-twofactor__hint">{{ t("twoFactor.settings.scan") }}</p>
          <QrCodeSvg :value="enrollment.uri" :label="t('twoFactor.settings.qrLabel')" />
          <dl class="jl-twofactor__secret">
            <dt>{{ t("twoFactor.settings.secret") }}</dt>
            <dd>
              <code id="two-factor-secret" data-secret>{{ groupedSecret }}</code>
              <UiButton variant="secondary" aria-describedby="two-factor-secret" @click="copy(enrollment.secret)">{{ t("twoFactor.settings.copySecret") }}</UiButton>
            </dd>
            <dt>{{ t("twoFactor.settings.uri") }}</dt>
            <dd>
              <code id="two-factor-uri" data-uri>{{ enrollment.uri }}</code>
              <UiButton variant="secondary" aria-describedby="two-factor-uri" @click="copy(enrollment.uri)">{{ t("twoFactor.settings.copyUri") }}</UiButton>
            </dd>
          </dl>
          <p class="jl-twofactor__status-line" role="status">
            <template v-if="copyState === 'copied'">{{ t("twoFactor.copied") }}</template>
            <template v-else-if="copyState === 'failed'">{{ t("twoFactor.copyFailed") }}</template>
          </p>
          <p class="jl-twofactor__hint">{{ t("twoFactor.settings.secretNote") }}</p>
          <UiTextField
            v-model="setupCode"
            :label="t('twoFactor.code.label')"
            :hint="t('twoFactor.code.hint')"
            inputmode="numeric"
            autocomplete="one-time-code"
            required
            :maxlength="12"
            :error="setupError ? t(setupError) : null"
          />
          <div class="jl-twofactor__actions">
            <UiButton type="submit" :busy="busy === 'confirm'">{{ t("twoFactor.settings.confirm") }}</UiButton>
            <UiButton variant="secondary" :disabled="busy === 'confirm'" @click="cancelSetup">{{ t("common.cancel") }}</UiButton>
          </div>
        </form>
      </template>
    </RequestStatus>
  </section>
</template>

<style scoped>
.jl-twofactor > :deep(.jl-request) {
  display: grid;
  gap: var(--jl-space-4);
}

.jl-twofactor h3 {
  margin: 0;
  font-size: var(--jl-font-size-md);
}

.jl-twofactor__hint,
.jl-twofactor__status-line {
  margin: 0;
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-twofactor__status-line {
  min-height: 1.5em;
}

.jl-twofactor__status {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-2);
  margin: 0;
}

.jl-twofactor__form {
  display: grid;
  gap: var(--jl-space-3);
  padding-top: var(--jl-space-4);
  border-top: 1px solid var(--jl-color-border);
}

.jl-twofactor__secret {
  display: grid;
  gap: var(--jl-space-2);
  margin: 0;
}

.jl-twofactor__secret dt {
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}

.jl-twofactor__secret dd {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-2);
  margin: 0;
}

.jl-twofactor__secret code {
  flex: 1 1 16rem;
  padding: var(--jl-space-2) var(--jl-space-3);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-sm);
  background: var(--jl-color-surface);
  font-family: ui-monospace, monospace;
  overflow-wrap: anywhere;
  user-select: all;
}

.jl-twofactor__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
}
</style>
