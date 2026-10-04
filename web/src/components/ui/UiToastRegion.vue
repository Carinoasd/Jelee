<script setup lang="ts">
import { useI18n } from "vue-i18n";

export interface ToastView {
  readonly id: number;
  readonly message: string;
  readonly tone: "info" | "success" | "danger";
}

// Live region for transient notices. Polite announcements for info/success;
// errors use role="alert". Each toast can be dismissed with the keyboard.
defineProps<{ toasts: readonly ToastView[] }>();
defineEmits<{ dismiss: [id: number] }>();
const { t } = useI18n();
</script>

<template>
  <section class="jl-toasts" :aria-label="t('common.notifications')">
    <div aria-live="polite" aria-atomic="false" class="jl-toasts__list">
      <div
        v-for="toast in toasts"
        :key="toast.id"
        class="jl-toast"
        :class="`jl-toast--${toast.tone}`"
        :role="toast.tone === 'danger' ? 'alert' : 'status'"
      >
        <p class="jl-toast__message">{{ toast.message }}</p>
        <button type="button" class="jl-toast__close" :aria-label="t('common.dismiss')" @click="$emit('dismiss', toast.id)">
          ×
        </button>
      </div>
    </div>
  </section>
</template>

<style scoped>
.jl-toasts {
  position: fixed;
  right: var(--jl-space-4);
  bottom: var(--jl-space-4);
  left: var(--jl-space-4);
  z-index: var(--jl-z-toast);
  display: flex;
  justify-content: flex-end;
  pointer-events: none;
}

.jl-toasts__list {
  display: grid;
  gap: var(--jl-space-2);
  width: min(100%, 420px);
}

.jl-toast {
  display: flex;
  align-items: flex-start;
  gap: var(--jl-space-2);
  padding: var(--jl-space-3) var(--jl-space-4);
  border: 1px solid var(--jl-color-border);
  border-left-width: 4px;
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-surface);
  color: var(--jl-color-text);
  box-shadow: var(--jl-shadow-2);
  pointer-events: auto;
  animation: jl-toast-in var(--jl-motion-duration) var(--jl-motion-easing);
}

.jl-toast--success {
  border-left-color: var(--jl-color-success);
}

.jl-toast--danger {
  border-left-color: var(--jl-color-danger);
}

.jl-toast--info {
  border-left-color: var(--jl-color-primary);
}

.jl-toast__message {
  flex: 1;
  margin: 0;
}

.jl-toast__close {
  min-width: var(--jl-touch-target);
  min-height: var(--jl-touch-target);
  margin: calc(-1 * var(--jl-space-3)) calc(-1 * var(--jl-space-3)) calc(-1 * var(--jl-space-3)) 0;
  border: none;
  background: transparent;
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-lg);
  cursor: pointer;
}

@keyframes jl-toast-in {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
}
</style>
