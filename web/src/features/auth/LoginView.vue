<script setup lang="ts">
import { nextTick, shallowRef, useTemplateRef } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useApi } from "@/api";
import { ApiError } from "@/api/errors";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { useTwoFactorI18n } from "@/i18n/twoFactor";
import { safeRedirect } from "@/router/redirect";
import { useAuthStore } from "@/stores/auth";
import type { SecondFactorChallenge } from "./api";
import { completeSecondFactor, factorProblem, proofOf } from "./secondFactor";
import SecondFactorFields from "./SecondFactorFields.vue";

// The global catalogs plus the lazily merged second-step messages.
const { t } = useTwoFactorI18n();
const { client } = useApi();
const auth = useAuthStore();
const route = useRoute();
const router = useRouter();

const name = shallowRef("");
const password = shallowRef("");
const busy = shallowRef(false);
const missing = shallowRef(false);
const alertBox = useTemplateRef<HTMLElement>("alert");
const heading = useTemplateRef<HTMLElement>("heading");
const factorFields = useTemplateRef<InstanceType<typeof SecondFactorFields>>("factorFields");
const failure = shallowRef<string | null>(route.query.reason === "expired" ? "errors.sessionExpired" : null);

// Second step (G07.8): the verified password returned a challenge instead of
// a session. It lives only in this component; leaving the page drops it.
const challenge = shallowRef<SecondFactorChallenge | null>(null);
const factor = shallowRef("");
const recovery = shallowRef(false);
const factorError = shallowRef<string | null>(null);

/** Login-specific reading of an error code; everything else uses the shared table. */
function loginFailureKey(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return "errors.generic";
  }
  return error.code === "authentication_required" ? "auth.failed" : errorMessageKey(error);
}

async function showFailure(key: string) {
  failure.value = key;
  await nextTick();
  alertBox.value?.focus();
}

async function submit() {
  if (busy.value) {
    return;
  }
  missing.value = name.value.trim() === "" || password.value === "";
  if (missing.value) {
    failure.value = null;
    return;
  }
  busy.value = true;
  failure.value = null;
  try {
    const result = await auth.login(name.value, password.value);
    if ("secondFactorRequired" in result) {
      challenge.value = result;
      factor.value = "";
      recovery.value = false;
      factorError.value = null;
      await nextTick();
      factorFields.value?.focus();
      return;
    }
    await router.replace(safeRedirect(route.query.redirect));
  } catch (error: unknown) {
    await showFailure(loginFailureKey(error));
  } finally {
    password.value = "";
    busy.value = false;
  }
}

async function backToPassword(key: string | null) {
  challenge.value = null;
  factor.value = "";
  factorError.value = null;
  if (key !== null) {
    await showFailure(key);
  } else {
    failure.value = null;
    await nextTick();
    heading.value?.focus();
  }
}

async function verify() {
  const current = challenge.value;
  if (busy.value || current === null) {
    return;
  }
  factorError.value = factorProblem(factor.value, recovery.value);
  if (factorError.value !== null) {
    failure.value = null;
    return;
  }
  busy.value = true;
  failure.value = null;
  try {
    auth.establish(await completeSecondFactor(client, current.challenge, proofOf(factor.value, recovery.value)));
    await router.replace(safeRedirect(route.query.redirect));
  } catch (error: unknown) {
    const code = error instanceof ApiError ? error.code : null;
    if (code === "login_challenge_invalid") {
      // Expired, spent or out of attempts: the password step starts over.
      await backToPassword("twoFactor.errors.challengeInvalid");
    } else if (code === "authentication_required") {
      // For example locked by the failed attempts.
      await backToPassword("twoFactor.login.refused");
    } else {
      // A wrong code (or a rate limit) keeps the step; the challenge stays usable.
      await showFailure(error instanceof ApiError ? errorMessageKey(error) : "errors.generic");
    }
  } finally {
    factor.value = "";
    busy.value = false;
  }
}
</script>

<template>
  <section class="jl-login" aria-labelledby="login-title">
    <h1 id="login-title" ref="heading" tabindex="-1">{{ challenge ? t("twoFactor.login.title") : t("auth.title") }}</h1>
    <form v-if="challenge === null" class="jl-login__form" novalidate @submit.prevent="submit">
      <UiTextField
        v-model="name"
        :label="t('auth.name')"
        autocomplete="username"
        required
        :maxlength="128"
        :error="missing && name.trim() === '' ? t('auth.nameRequired') : null"
      />
      <UiTextField
        v-model="password"
        :label="t('auth.password')"
        type="password"
        autocomplete="current-password"
        required
        :maxlength="1024"
        :error="missing && password === '' ? t('auth.passwordRequired') : null"
      />
      <div v-if="failure" ref="alert" tabindex="-1" class="jl-login__alert">
        <UiAlert tone="danger">{{ t(failure) }}</UiAlert>
      </div>
      <UiButton type="submit" :busy="busy">{{ busy ? t("auth.submitting") : t("auth.submit") }}</UiButton>
    </form>
    <form v-else class="jl-login__form" novalidate data-second-factor @submit.prevent="verify">
      <p class="jl-login__intro">{{ recovery ? t("twoFactor.login.introRecovery") : t("twoFactor.login.intro") }}</p>
      <SecondFactorFields ref="factorFields" v-model="factor" v-model:recovery="recovery" :error="factorError ? t(factorError) : null" />
      <div v-if="failure" ref="alert" tabindex="-1" class="jl-login__alert">
        <UiAlert tone="danger">{{ t(failure) }}</UiAlert>
      </div>
      <UiButton type="submit" :busy="busy">{{ busy ? t("twoFactor.login.submitting") : t("twoFactor.login.submit") }}</UiButton>
      <UiButton variant="secondary" :disabled="busy" @click="backToPassword(null)">{{ t("twoFactor.login.back") }}</UiButton>
    </form>
    <p class="jl-login__note">{{ t("auth.sessionNote") }}</p>
  </section>
</template>

<style scoped>
.jl-login {
  max-width: 400px;
  margin: var(--jl-space-8) auto;
  padding: var(--jl-space-6);
  background: var(--jl-color-surface);
  border-radius: var(--jl-radius-md);
  box-shadow: var(--jl-shadow-1);
}

.jl-login h1 {
  margin-top: 0;
  font-size: var(--jl-font-size-xl);
}

.jl-login__form {
  display: grid;
  gap: var(--jl-space-4);
}

.jl-login__intro {
  margin: 0;
}

.jl-login__alert:focus {
  outline: none;
}

.jl-login__note {
  margin-bottom: 0;
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}
</style>
