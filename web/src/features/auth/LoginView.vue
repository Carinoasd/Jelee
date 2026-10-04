<script setup lang="ts">
import { nextTick, shallowRef, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";
import { ApiError } from "@/api/errors";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { safeRedirect } from "@/router/redirect";
import { useAuthStore } from "@/stores/auth";

const { t } = useI18n();
const auth = useAuthStore();
const route = useRoute();
const router = useRouter();

const name = shallowRef("");
const password = shallowRef("");
const busy = shallowRef(false);
const missing = shallowRef(false);
const alertBox = useTemplateRef<HTMLElement>("alert");
const failure = shallowRef<string | null>(route.query.reason === "expired" ? "errors.sessionExpired" : null);

/** Login-specific reading of an error code; everything else uses the shared table. */
function loginFailureKey(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return "errors.generic";
  }
  return error.code === "authentication_required" ? "auth.failed" : errorMessageKey(error);
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
    await auth.login(name.value, password.value);
    await router.replace(safeRedirect(route.query.redirect));
  } catch (error: unknown) {
    failure.value = loginFailureKey(error);
    await nextTick();
    alertBox.value?.focus();
  } finally {
    password.value = "";
    busy.value = false;
  }
}
</script>

<template>
  <section class="jl-login" aria-labelledby="login-title">
    <h1 id="login-title" tabindex="-1">{{ t("auth.title") }}</h1>
    <form class="jl-login__form" novalidate @submit.prevent="submit">
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

.jl-login__alert:focus {
  outline: none;
}

.jl-login__note {
  margin-bottom: 0;
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}
</style>
