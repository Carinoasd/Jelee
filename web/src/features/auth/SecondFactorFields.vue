<script setup lang="ts">
import { nextTick, useTemplateRef } from "vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiTextField from "@/components/ui/UiTextField.vue";
import { useTwoFactorI18n } from "@/i18n/twoFactor";

// The second factor as one field: an authenticator code (numeric keyboard,
// one-time-code autofill) or, after switching, a recovery code. Switching
// clears the entry and moves focus to the new field.
defineProps<{ error?: string | null }>();
const value = defineModel<string>({ required: true });
const recovery = defineModel<boolean>("recovery", { required: true });
const { t } = useTwoFactorI18n();
const root = useTemplateRef<HTMLElement>("root");

async function toggle() {
  recovery.value = !recovery.value;
  value.value = "";
  await nextTick();
  root.value?.querySelector("input")?.focus();
}

defineExpose({
  focus: () => root.value?.querySelector("input")?.focus(),
});
</script>

<template>
  <div ref="root" class="jl-factor">
    <UiTextField
      v-if="!recovery"
      v-model="value"
      :label="t('twoFactor.code.label')"
      :hint="t('twoFactor.code.hint')"
      inputmode="numeric"
      autocomplete="one-time-code"
      required
      :maxlength="12"
      :error="error"
    />
    <UiTextField
      v-else
      v-model="value"
      :label="t('twoFactor.code.recoveryLabel')"
      :hint="t('twoFactor.code.recoveryHint')"
      autocomplete="off"
      required
      :maxlength="64"
      :error="error"
    />
    <div>
      <UiButton variant="ghost" @click="toggle">{{ recovery ? t("twoFactor.code.useApp") : t("twoFactor.code.useRecovery") }}</UiButton>
    </div>
  </div>
</template>

<style scoped>
.jl-factor {
  display: grid;
  gap: var(--jl-space-2);
}
</style>
