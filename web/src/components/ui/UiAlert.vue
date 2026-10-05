<script setup lang="ts">
withDefaults(defineProps<{ tone?: "info" | "danger" | "success" }>(), { tone: "info" });
</script>

<template>
  <div class="jl-alert" :class="`jl-alert--${tone}`" :role="tone === 'danger' ? 'alert' : 'status'">
    <slot />
  </div>
</template>

<style scoped>
.jl-alert {
  padding: var(--jl-space-3) var(--jl-space-4);
  border-radius: var(--jl-radius-md);
  border: 1px solid var(--jl-color-border);
}

.jl-alert > :deep(:first-child) {
  margin-top: 0;
}

.jl-alert > :deep(:last-child) {
  margin-bottom: 0;
}

.jl-alert--info {
  background: var(--jl-color-info-bg);
}

.jl-alert--danger {
  border-color: var(--jl-color-danger);
  background: var(--jl-color-danger-bg);
  color: var(--jl-color-danger);
}

.jl-alert--success {
  border-color: var(--jl-color-success);
  background: var(--jl-color-success-bg);
  color: var(--jl-color-success);
}
</style>
