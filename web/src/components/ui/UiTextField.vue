<script setup lang="ts">
import { useId } from "vue";

defineProps<{
  label: string;
  type?: "text" | "password";
  autocomplete?: string;
  required?: boolean;
  maxlength?: number;
}>();
const model = defineModel<string>({ required: true });
const id = useId();
</script>

<template>
  <div class="jl-field">
    <label class="jl-field__label" :for="id">{{ label }}</label>
    <input
      :id="id"
      v-model="model"
      class="jl-field__input"
      :type="type ?? 'text'"
      :autocomplete="autocomplete"
      :required="required"
      :maxlength="maxlength"
    />
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
</style>
