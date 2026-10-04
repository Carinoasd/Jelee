<script setup lang="ts">
import { computed, useId } from "vue";
import type { SelectOption } from "./types";

const props = defineProps<{
  label: string;
  options: readonly SelectOption[];
  hint?: string;
  disabled?: boolean;
}>();
const model = defineModel<string>({ required: true });
const id = useId();
const hintId = `${id}-hint`;
const describedBy = computed(() => (props.hint ? hintId : undefined));
</script>

<template>
  <div class="jl-select">
    <label class="jl-select__label" :for="id">{{ label }}</label>
    <select :id="id" v-model="model" class="jl-select__input" :disabled="disabled" :aria-describedby="describedBy">
      <option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option>
    </select>
    <p v-if="hint" :id="hintId" class="jl-select__hint">{{ hint }}</p>
  </div>
</template>

<style scoped>
.jl-select {
  display: grid;
  gap: var(--jl-space-1);
}

.jl-select__label {
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}

.jl-select__input {
  min-height: var(--jl-touch-target);
  padding: 0 var(--jl-space-2);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-sm);
  background: var(--jl-color-surface);
  color: var(--jl-color-text);
  font: inherit;
}

.jl-select__hint {
  margin: 0;
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}
</style>
