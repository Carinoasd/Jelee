<script setup lang="ts">
import { computed, useId } from "vue";

const props = defineProps<{ label: string; hint?: string; disabled?: boolean }>();
const model = defineModel<boolean>({ required: true });
const id = useId();
const hintId = `${id}-hint`;
const describedBy = computed(() => (props.hint ? hintId : undefined));
</script>

<template>
  <div class="jl-checkbox">
    <input :id="id" v-model="model" type="checkbox" class="jl-checkbox__input" :disabled="disabled" :aria-describedby="describedBy" />
    <label class="jl-checkbox__label" :for="id">{{ label }}</label>
    <p v-if="hint" :id="hintId" class="jl-checkbox__hint">{{ hint }}</p>
  </div>
</template>

<style scoped>
.jl-checkbox {
  display: grid;
  grid-template-columns: auto 1fr;
  align-items: center;
  column-gap: var(--jl-space-2);
  min-height: var(--jl-touch-target);
}

.jl-checkbox__input {
  width: 20px;
  height: 20px;
  margin: 0;
  accent-color: var(--jl-color-primary);
}

.jl-checkbox__hint {
  grid-column: 2;
  margin: 0;
  font-size: var(--jl-font-size-sm);
  color: var(--jl-color-text-muted);
}
</style>
