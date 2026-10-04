<script setup lang="ts">
import { computed, useId } from "vue";

const props = defineProps<{
  label: string;
  type?: "text" | "password";
  autocomplete?: string;
  required?: boolean;
  maxlength?: number;
  /** Help text shown under the field and linked with aria-describedby. */
  hint?: string;
  /** Validation message; marks the field invalid for assistive technology. */
  error?: string | null;
}>();
const model = defineModel<string>({ required: true });
const id = useId();
const hintId = `${id}-hint`;
const errorId = `${id}-error`;
const describedBy = computed(() => {
  const ids = [props.hint ? hintId : "", props.error ? errorId : ""].filter((value) => value !== "");
  return ids.length > 0 ? ids.join(" ") : undefined;
});
</script>

<template>
  <div class="jl-field">
    <label class="jl-field__label" :for="id">{{ label }}</label>
    <input
      :id="id"
      v-model="model"
      class="jl-field__input"
      :class="{ 'jl-field__input--invalid': error }"
      :type="type ?? 'text'"
      :autocomplete="autocomplete"
      :required="required"
      :aria-required="required ? 'true' : undefined"
      :aria-invalid="error ? 'true' : undefined"
      :aria-describedby="describedBy"
      :maxlength="maxlength"
    />
    <p v-if="hint" :id="hintId" class="jl-field__hint">{{ hint }}</p>
    <p v-if="error" :id="errorId" class="jl-field__error">{{ error }}</p>
  </div>
</template>

<style scoped>
.jl-field {
  display: grid;
  gap: var(--jl-space-1);
}

.jl-field__label {
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}

.jl-field__input {
  min-height: var(--jl-touch-target);
  padding: var(--jl-space-2) var(--jl-space-3);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-sm);
  background: var(--jl-color-surface);
  color: var(--jl-color-text);
  font: inherit;
}

.jl-field__input--invalid {
  border-color: var(--jl-color-danger);
}

.jl-field__hint,
.jl-field__error {
  margin: 0;
  font-size: var(--jl-font-size-sm);
}

.jl-field__hint {
  color: var(--jl-color-text-muted);
}

.jl-field__error {
  color: var(--jl-color-danger);
}
</style>
