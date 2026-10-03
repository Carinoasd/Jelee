<script setup lang="ts">
import { shallowRef } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";
import { ApiError } from "@/api/errors";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { useLocaleSync } from "@/i18n/useLocaleSync";
import { safeRedirect } from "@/router/redirect";
import { useAuthStore } from "@/stores/auth";

const { t } = useI18n();
const auth = useAuthStore();
const route = useRoute();
const router = useRouter();
const { applyUserLocale } = useLocaleSync();

const name = shallowRef("");
const password = shallowRef("");
const busy = shallowRef(false);
const failure = shallowRef<string | null>(route.query.reason === "expired" ? "errors.sessionExpired" : null);

async function submit() {
  if (busy.value) {
    return;
  }
  busy.value = true;
  failure.value = null;
  try {
    const user = await auth.login(name.value, password.value);
    applyUserLocale(user.locale);
    await router.replace(safeRedirect(route.query.redirect));
  } catch (error: unknown) {
    if (!(error instanceof ApiError)) {
      failure.value = "errors.generic";
    } else {
      failure.value = error.status === 401 ? "auth.failed" : errorMessageKey(error);
    }
  } finally {
    password.value = "";
    busy.value = false;
  }
}
</script>

<template>
  <section class="jl-login" aria-labelledby="login-title">
    <h1 id="login-title">{{ t("auth.title") }}</h1>
    <form class="jl-login__form" novalidate @submit.prevent="submit">
      <UiTextField v-model="name" :label="t('auth.name')" autocomplete="username" required :maxlength="128" />
      <UiTextField
        v-model="password"
        :label="t('auth.password')"
        type="password"
        autocomplete="current-password"
        required
        :maxlength="1024"
      />
      <UiAlert v-if="failure" tone="danger">{{ t(failure) }}</UiAlert>
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

.jl-login__form {
  display: grid;
  gap: var(--jl-space-4);
}

.jl-login__note {
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}
</style>
