<script setup lang="ts">
import { computed, reactive, shallowRef, watch } from "vue";
import { useI18n } from "vue-i18n";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import UiConfirmButton from "@/components/ui/UiConfirmButton.vue";
import UiSelectField from "@/components/ui/UiSelectField.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import type { SelectOption } from "@/components/ui/types";
import { useUserAdminStore } from "@/stores/userAdmin";
import type { User, UserLocale } from "./api";
import { useAdminFeedback } from "./feedback";
import { isUserLocale, localeNameKey, nameMaxBytes, settingsOf, userLocales, utf8Length } from "./form";

const props = defineProps<{ user: User }>();
const { t } = useI18n();
const store = useUserAdminStore();
const feedback = useAdminFeedback();

interface SettingsForm {
  name: string;
  profileName: string;
  locale: UserLocale;
  admin: boolean;
  hidden: boolean;
}

const form = reactive<SettingsForm>({ name: "", profileName: "", locale: "en-US", admin: false, hidden: false });
const nameProblem = shallowRef<string | null>(null);
const profileProblem = shallowRef<string | null>(null);

watch(
  () => props.user,
  (user) => {
    Object.assign(form, {
      name: user.name,
      profileName: user.displayName,
      locale: user.locale,
      admin: user.admin,
      hidden: user.hidden,
    });
  },
  { immediate: true },
);

const localeOptions = computed<SelectOption[]>(() => userLocales.map((value) => ({ value, label: t(localeNameKey[value]) })));
const localeModel = computed({
  get: () => form.locale,
  set: (value: string) => {
    if (isUserLocale(value)) {
      form.locale = value;
    }
  },
});
const roleChanged = computed(() => form.admin !== props.user.admin);

function check(): boolean {
  const name = form.name.trim();
  nameProblem.value = name === "" ? "users.form.nameRequired" : utf8Length(name) > nameMaxBytes ? "users.form.tooLong" : null;
  profileProblem.value = utf8Length(form.profileName.trim()) > nameMaxBytes ? "users.form.tooLong" : null;
  return nameProblem.value === null && profileProblem.value === null;
}

async function save() {
  if (!check()) {
    return;
  }
  try {
    const outcome = await store.saveSettings({
      ...settingsOf(props.user),
      name: form.name.trim(),
      displayName: form.profileName.trim(),
      locale: form.locale,
      admin: form.admin,
      hidden: form.hidden,
    });
    await feedback.done(outcome, "users.settings.saved");
  } catch (error: unknown) {
    feedback.failed(error);
  }
}

/** Enter in a field submits; a role change still needs the explicit confirmation. */
function submit() {
  if (!roleChanged.value) {
    void save();
  }
}

async function setDisabled(disabled: boolean) {
  try {
    const outcome = await store.saveSettings({ ...settingsOf(props.user), disabled }, "status");
    await feedback.done(outcome, disabled ? "users.status.disabledDone" : "users.status.enabledDone", { name: props.user.name });
  } catch (error: unknown) {
    feedback.failed(error);
  }
}
</script>

<template>
  <section class="jl-card" aria-labelledby="user-settings-title">
    <h2 id="user-settings-title">{{ t("users.settings.title") }}</h2>
    <form class="jl-settings" novalidate @submit.prevent="submit">
      <div class="jl-card__fields">
        <UiTextField
          v-model="form.name"
          :label="t('users.form.name')"
          :hint="t('users.form.nameHint')"
          :error="nameProblem ? t(nameProblem) : null"
          autocomplete="off"
          required
        />
        <UiTextField
          v-model="form.profileName"
          :label="t('users.form.profileName')"
          :error="profileProblem ? t(profileProblem) : null"
          autocomplete="off"
        />
        <UiSelectField v-model="localeModel" :label="t('users.form.locale')" :options="localeOptions" />
      </div>
      <UiCheckbox v-model="form.admin" :label="t('users.form.admin')" :hint="t('users.form.adminHint')" />
      <UiCheckbox v-model="form.hidden" :label="t('users.form.hidden')" :hint="t('users.form.hiddenHint')" />
      <div class="jl-card__impact">
        <p>{{ t("users.settings.impact") }}</p>
        <p v-if="roleChanged">{{ t("users.settings.roleImpact") }}</p>
      </div>
      <div class="jl-card__actions">
        <UiConfirmButton
          v-if="roleChanged"
          variant="secondary"
          :label="t('common.save')"
          :confirm-label="t('users.settings.confirmRole')"
          :prompt="t('users.settings.roleImpact')"
          :busy="store.busy === 'settings'"
          @confirm="save"
        />
        <UiButton v-else type="submit" :busy="store.busy === 'settings'">{{ t("common.save") }}</UiButton>
      </div>
    </form>

    <section class="jl-settings__status" aria-labelledby="user-status-title">
      <h3 id="user-status-title">{{ t("users.status.title") }}</h3>
      <p>{{ user.disabled ? t("users.status.isDisabled") : t("users.status.isActive") }}</p>
      <p class="jl-card__impact">{{ user.disabled ? t("users.status.enableImpact") : t("users.status.disableImpact") }}</p>
      <div class="jl-card__actions">
        <UiButton v-if="user.disabled" variant="secondary" :busy="store.busy === 'status'" @click="setDisabled(false)">
          {{ t("users.status.enable") }}
        </UiButton>
        <UiConfirmButton
          v-else
          :label="t('users.status.disable')"
          :confirm-label="t('users.status.disableConfirm')"
          :prompt="t('users.status.disableImpact')"
          :busy="store.busy === 'status'"
          @confirm="setDisabled(true)"
        />
      </div>
    </section>
  </section>
</template>

<style scoped src="./sections.css"></style>
<style scoped>
.jl-settings {
  display: grid;
  gap: var(--jl-space-4);
}

.jl-settings__status {
  display: grid;
  gap: var(--jl-space-3);
  padding-top: var(--jl-space-4);
  border-top: 1px solid var(--jl-color-border);
}

.jl-settings__status h3,
.jl-settings__status p {
  margin: 0;
}
</style>
