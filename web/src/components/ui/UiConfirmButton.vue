<script setup lang="ts">
import { nextTick, shallowRef, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import UiButton from "./UiButton.vue";

// Two-step confirmation for dangerous actions: the first press only reveals
// the prompt and the confirm button (which receives focus); Escape or Cancel
// backs out and returns focus to the trigger. Only the second press emits.
withDefaults(
  defineProps<{
    /** Text of the first button. */
    label: string;
    /** Text of the confirming button. */
    confirmLabel: string;
    /** What will happen; announced when the confirmation opens. */
    prompt?: string;
    busy?: boolean;
    disabled?: boolean;
    variant?: "danger" | "secondary";
    /** ID of the element naming the target, e.g. the row the action applies to. */
    describedby?: string;
  }>(),
  { prompt: "", busy: false, disabled: false, variant: "danger", describedby: undefined },
);
const emit = defineEmits<{ confirm: [] }>();
const { t } = useI18n();
const confirming = shallowRef(false);
const root = useTemplateRef<HTMLElement>("root");

async function open() {
  confirming.value = true;
  await nextTick();
  root.value?.querySelector<HTMLButtonElement>("[data-confirm]")?.focus();
}

async function cancel() {
  confirming.value = false;
  await nextTick();
  root.value?.querySelector<HTMLButtonElement>("[data-trigger]")?.focus();
}

function confirm() {
  emit("confirm");
  confirming.value = false;
}

defineExpose({ cancel, confirming });
</script>

<template>
  <span ref="root" class="jl-confirm" @keydown.esc.stop="cancel">
    <UiButton
      v-if="!confirming"
      data-trigger
      :variant="variant"
      :busy="busy"
      :disabled="disabled"
      :aria-describedby="describedby"
      @click="open"
    >
      {{ label }}
    </UiButton>
    <template v-else>
      <span v-if="prompt" class="jl-confirm__prompt" role="alert">{{ prompt }}</span>
      <UiButton data-confirm variant="danger" :busy="busy" :aria-describedby="describedby" @click="confirm">{{ confirmLabel }}</UiButton>
      <UiButton variant="secondary" :disabled="busy" @click="cancel">{{ t("common.cancel") }}</UiButton>
    </template>
  </span>
</template>

<style scoped>
.jl-confirm {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-2);
}

.jl-confirm__prompt {
  flex-basis: 100%;
  color: var(--jl-color-danger);
  font-size: var(--jl-font-size-sm);
}
</style>
