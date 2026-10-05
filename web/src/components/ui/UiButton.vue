<script setup lang="ts">
// Native <button> keeps keyboard activation and focus behavior; "pressed"
// turns it into a toggle button for screen readers (aria-pressed).
withDefaults(
  defineProps<{
    variant?: "primary" | "secondary" | "danger" | "ghost";
    type?: "button" | "submit";
    disabled?: boolean;
    busy?: boolean;
    pressed?: boolean | undefined;
  }>(),
  { variant: "primary", type: "button", disabled: false, busy: false, pressed: undefined },
);
</script>

<template>
  <button
    :type="type"
    class="jl-button"
    :class="`jl-button--${variant}`"
    :disabled="disabled || busy"
    :aria-busy="busy"
    :aria-pressed="pressed"
  >
    <span v-if="busy" class="jl-button__spinner" aria-hidden="true" />
    <slot />
  </button>
</template>

<style scoped>
.jl-button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: var(--jl-space-2);
  min-height: var(--jl-touch-target);
  padding: var(--jl-space-2) var(--jl-space-4);
  border: 1px solid transparent;
  border-radius: var(--jl-radius-md);
  font: inherit;
  cursor: pointer;
  transition:
    background-color var(--jl-motion-duration) var(--jl-motion-easing),
    border-color var(--jl-motion-duration) var(--jl-motion-easing);
}

.jl-button--primary {
  background: var(--jl-color-primary);
  color: var(--jl-color-primary-text);
}

.jl-button--secondary {
  background: var(--jl-color-surface);
  border-color: var(--jl-color-border);
  color: var(--jl-color-text);
}

.jl-button--danger {
  background: var(--jl-color-surface);
  border-color: var(--jl-color-danger);
  color: var(--jl-color-danger);
}

.jl-button--ghost {
  background: transparent;
  color: var(--jl-color-text);
}

.jl-button--secondary:hover:not(:disabled),
.jl-button--ghost:hover:not(:disabled) {
  border-color: var(--jl-color-text-muted);
}

.jl-button--danger:hover:not(:disabled) {
  background: var(--jl-color-danger-bg);
}

.jl-button[aria-pressed="true"] {
  background: var(--jl-color-primary);
  border-color: var(--jl-color-primary);
  color: var(--jl-color-primary-text);
}

.jl-button:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}

.jl-button__spinner {
  width: 1em;
  height: 1em;
  border: 2px solid currentcolor;
  border-right-color: transparent;
  border-radius: 50%;
  animation: jl-spin 800ms linear infinite;
}

@keyframes jl-spin {
  to {
    transform: rotate(360deg);
  }
}

@media (prefers-reduced-motion: reduce) {
  .jl-button__spinner {
    animation: none;
    border-right-color: currentcolor;
    opacity: 0.6;
  }
}
</style>
