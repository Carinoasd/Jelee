<script setup lang="ts">
import { computed, shallowRef, useId, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useRouter } from "vue-router";
import { ApiError, networkError } from "@/api/errors";
import UiAlert from "@/components/ui/UiAlert.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { errorMessageKey } from "@/features/errors/messages";
import { isLocale, supportedLocales, type Locale } from "@/i18n/locales";
import { useLocaleSync } from "@/i18n/useLocaleSync";
import { useAuthStore } from "@/stores/auth";
import { themes, usePreferencesStore, type Theme } from "@/stores/preferences";
import { useSettingsStore } from "@/stores/settings";
import { useToastStore } from "@/stores/toasts";
import { passwordMaxBytes, passwordMinBytes, utf8Length } from "./api";

const { t } = useI18n();
const router = useRouter();
const auth = useAuthStore();
const settings = useSettingsStore();
const preferences = usePreferencesStore();
const toasts = useToastStore();
const { locale, setLocale } = useLocaleSync();
const themeName = useId();

const localeKey: Readonly<Record<Locale, string>> = {
  "zh-CN": "common.localeNames.zhCN",
  "zh-TW": "common.localeNames.zhTW",
  "ja-JP": "common.localeNames.jaJP",
  "en-US": "common.localeNames.enUS",
};
const themeKey: Readonly<Record<Theme, string>> = {
  system: "settings.theme.system",
  light: "settings.theme.light",
  dark: "settings.theme.dark",
};
const localeOptions = computed(() => supportedLocales.map((value) => ({ value, label: t(localeKey[value]) })));

function failure(error: unknown): ApiError {
  return error instanceof ApiError ? error : networkError(error);
}

// The language applies at once and is saved with the profile, so it also
// follows the account to other devices (the server resets omitted fields,
// hence the whole profile is sent).
const language = computed({
  get: () => locale.value,
  set: (value: string) => {
    if (isLocale(value) && value !== locale.value) {
      setLocale(value);
      void saveLanguage(value);
    }
  },
});

async function saveLanguage(value: Locale) {
  const user = auth.user;
  if (user === null) {
    return;
  }
  try {
    await settings.saveProfile({ displayName: user.displayName, hidden: user.hidden, locale: value });
    toasts.push("settings.language.saved", "success");
  } catch (error: unknown) {
    toasts.push(errorMessageKey(failure(error)), "danger");
  }
}

// The theme applies at once; signed in, it is also stored with the account.
async function saveTheme(value: Theme) {
  try {
    await preferences.setTheme(value);
    if (auth.user !== null) {
      toasts.push("settings.theme.saved", "success");
    }
  } catch (error: unknown) {
    toasts.push(errorMessageKey(failure(error)), "danger");
  }
}

// Profile form, refilled whenever the account changes.
const profileName = shallowRef("");
const hidden = shallowRef(false);
watch(
  () => auth.user,
  (user) => {
    profileName.value = user?.displayName ?? "";
    hidden.value = user?.hidden ?? false;
  },
  { immediate: true },
);

async function saveProfile() {
  const user = auth.user;
  if (user === null) {
    return;
  }
  try {
    await settings.saveProfile({ displayName: profileName.value.trim(), hidden: hidden.value, locale: user.locale });
    toasts.push("settings.profile.saved", "success");
  } catch (error: unknown) {
    toasts.push(errorMessageKey(failure(error)), "danger");
  }
}

// Password change with client-side checks mirroring the server's rules.
const oldPassword = shallowRef("");
const newPassword = shallowRef("");
const confirmPassword = shallowRef("");
const submitted = shallowRef(false);
const passwordError = shallowRef<string | null>(null);

const oldError = computed(() => (submitted.value && oldPassword.value === "" ? t("settings.password.required") : null));
const newError = computed(() => {
  if (!submitted.value) {
    return null;
  }
  const bytes = utf8Length(newPassword.value);
  if (bytes < passwordMinBytes) {
    return t("settings.password.tooShort", { min: passwordMinBytes });
  }
  return bytes > passwordMaxBytes ? t("settings.password.tooLong", { max: passwordMaxBytes }) : null;
});
const confirmError = computed(() =>
  submitted.value && confirmPassword.value !== newPassword.value ? t("settings.password.mismatch") : null,
);

async function changePassword() {
  submitted.value = true;
  passwordError.value = null;
  if (oldError.value || newError.value || confirmError.value) {
    return;
  }
  try {
    await settings.changePassword(oldPassword.value, newPassword.value);
  } catch (error: unknown) {
    // A wrong current password is 400 invalid_password; the session stays.
    passwordError.value = t(errorMessageKey(failure(error)));
    return;
  } finally {
    oldPassword.value = "";
  }
  newPassword.value = "";
  confirmPassword.value = "";
  toasts.push("settings.password.changed", "success");
  await router.replace({ name: "login" });
}
</script>

<template>
  <section class="jl-settings" aria-labelledby="settings-title">
    <h1 id="settings-title" tabindex="-1">{{ t("settings.title") }}</h1>

    <section class="jl-settings__card" aria-labelledby="settings-appearance">
      <h2 id="settings-appearance">{{ t("settings.appearance") }}</h2>
      <UiSelectField v-model="language" :label="t('settings.language.label')" :options="localeOptions" :hint="t('settings.language.hint')" />
      <fieldset class="jl-settings__themes">
        <legend>{{ t("settings.theme.label") }}</legend>
        <label v-for="option in themes" :key="option" class="jl-settings__theme">
          <input
            type="radio"
            :name="themeName"
            :value="option"
            :checked="preferences.theme === option"
            @change="saveTheme(option)"
          />
          {{ t(themeKey[option]) }}
        </label>
        <p class="jl-settings__hint">{{ t("settings.theme.hint") }}</p>
      </fieldset>
    </section>

    <form class="jl-settings__card" aria-labelledby="settings-profile" novalidate @submit.prevent="saveProfile">
      <h2 id="settings-profile">{{ t("settings.profile.heading") }}</h2>
      <UiTextField v-model="profileName" :label="t('settings.profile.name')" :hint="t('settings.profile.nameHint')" :maxlength="64" />
      <UiCheckbox v-model="hidden" :label="t('settings.profile.hidden')" :hint="t('settings.profile.hiddenHint')" />
      <div>
        <UiButton type="submit" :busy="settings.savingProfile">{{ t("common.save") }}</UiButton>
      </div>
    </form>

    <form class="jl-settings__card" aria-labelledby="settings-password" novalidate @submit.prevent="changePassword">
      <h2 id="settings-password">{{ t("settings.password.heading") }}</h2>
      <p class="jl-settings__hint">{{ t("settings.password.hint") }}</p>
      <UiAlert v-if="passwordError" tone="danger">{{ passwordError }}</UiAlert>
      <UiTextField
        v-model="oldPassword"
        type="password"
        autocomplete="current-password"
        required
        :label="t('settings.password.current')"
        :error="oldError"
      />
      <UiTextField
        v-model="newPassword"
        type="password"
        autocomplete="new-password"
        required
        :label="t('settings.password.new')"
        :hint="t('settings.password.rule', { min: passwordMinBytes })"
        :error="newError"
      />
      <UiTextField
        v-model="confirmPassword"
        type="password"
        autocomplete="new-password"
        required
        :label="t('settings.password.confirm')"
        :error="confirmError"
      />
      <div>
        <UiButton type="submit" :busy="settings.changingPassword">{{ t("settings.password.submit") }}</UiButton>
      </div>
    </form>
  </section>
</template>

<style scoped>
.jl-settings {
  display: grid;
  gap: var(--jl-space-6);
  max-width: 720px;
}

.jl-settings h1 {
  margin: 0;
}

.jl-settings__card {
  display: grid;
  gap: var(--jl-space-4);
  padding: var(--jl-space-6);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-surface);
}

.jl-settings__card h2 {
  margin: 0;
  font-size: var(--jl-font-size-lg);
}

.jl-settings__themes {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2) var(--jl-space-6);
  margin: 0;
  padding: 0;
  border: none;
}

.jl-settings__themes legend {
  margin-bottom: var(--jl-space-1);
  padding: 0;
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}

.jl-settings__theme {
  display: inline-flex;
  align-items: center;
  gap: var(--jl-space-2);
  min-height: var(--jl-touch-target);
  cursor: pointer;
}

.jl-settings__theme input {
  width: 20px;
  height: 20px;
  margin: 0;
  accent-color: var(--jl-color-primary);
}

.jl-settings__hint {
  flex-basis: 100%;
  margin: 0;
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}
</style>
