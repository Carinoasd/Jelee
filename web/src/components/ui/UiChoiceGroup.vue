<script setup lang="ts" generic="V extends string">
import { useId } from "vue";

// A set of native radio buttons drawn as tiles (arrow keys move between
// them, as with any radio group). The optional "preview" slot draws a
// picture of the option above its name.
defineProps<{ legend: string; options: readonly { readonly value: V; readonly label: string }[] }>();
const model = defineModel<V>({ required: true });
defineSlots<{ preview?(props: { value: V }): unknown }>();
const name = useId();
</script>

<template>
  <fieldset class="jl-choices">
    <legend class="jl-choices__legend">{{ legend }}</legend>
    <label v-for="option in options" :key="option.value" class="jl-choices__option">
      <input v-model="model" class="jl-choices__input" type="radio" :name="name" :value="option.value" />
      <slot name="preview" :value="option.value" />
      <span>{{ option.label }}</span>
    </label>
  </fieldset>
</template>

<style scoped>
.jl-choices {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
  margin: 0;
  padding: 0;
  border: none;
}

.jl-choices__legend {
  margin-bottom: var(--jl-space-2);
  padding: 0;
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}

.jl-choices__option {
  position: relative;
  display: grid;
  justify-items: center;
  align-content: center;
  gap: var(--jl-space-2);
  min-width: 5.5rem;
  min-height: var(--jl-touch-target);
  padding: var(--jl-space-2) var(--jl-space-3);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-surface);
  color: var(--jl-color-text);
  cursor: pointer;
}

.jl-choices__option:has(:checked) {
  border-color: var(--jl-color-primary);
  box-shadow: inset 0 0 0 1px var(--jl-color-primary);
  color: var(--jl-color-primary);
  font-weight: 600;
}

.jl-choices__option:has(:focus-visible) {
  outline: 2px solid var(--jl-color-focus);
  outline-offset: 2px;
}

/* The radio stays in the accessibility tree and takes focus; the tile shows its state. */
.jl-choices__input {
  position: absolute;
  inset: 0;
  margin: 0;
  opacity: 0;
  cursor: pointer;
}
</style>
