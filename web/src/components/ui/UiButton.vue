<script setup lang="ts">
withDefaults(
  defineProps<{
    variant?: "primary" | "secondary";
    type?: "button" | "submit";
    disabled?: boolean;
    busy?: boolean;
  }>(),
  { variant: "primary", type: "button", disabled: false, busy: false },
);
</script>

<template>
  <button
    :type="type"
    class="jl-button"
    :class="`jl-button--${variant}`"
    :disabled="disabled || busy"
    :aria-busy="busy"
  >
    <slot />
  </button>
</template>

<style scoped>
.jl-button {
  min-height: var(--jl-touch-target);
  padding: var(--jl-space-2) var(--jl-space-4);
  border: 1px solid transparent;
  border-radius: var(--jl-radius-md);
  font: inherit;
  cursor: pointer;
  transition: background-color var(--jl-motion-duration) var(--jl-motion-easing);
}

.jl-button--primary {
  background: var(--jl-color-primary);
  color: var(--jl-color-primary-text);
}

.jl-button--secondary {
  background: transparent;
  border-color: var(--jl-color-border);
  color: var(--jl-color-text);
}

.jl-button:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}
</style>
