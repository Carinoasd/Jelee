<script setup lang="ts">
withDefaults(defineProps<{ tone?: "info" | "danger" }>(), { tone: "info" });
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

.jl-alert--danger {
  border-color: var(--jl-color-danger);
  background: var(--jl-color-danger-bg);
  color: var(--jl-color-danger);
}
</style>
