<script setup lang="ts">
defineProps<{ title: string }>();
defineSlots<{
  default?(): unknown;
  action?(): unknown;
}>();
</script>

<template>
  <div class="jl-empty" role="status">
    <span class="jl-empty__icon" aria-hidden="true" />
    <p class="jl-empty__title">{{ title }}</p>
    <div v-if="$slots.default" class="jl-empty__body"><slot /></div>
    <div v-if="$slots.action" class="jl-empty__action"><slot name="action" /></div>
  </div>
</template>

<style scoped>
.jl-empty {
  display: grid;
  justify-items: center;
  gap: var(--jl-space-2);
  padding: var(--jl-space-12) var(--jl-space-4);
  text-align: center;
  border: 1px dashed var(--jl-color-border);
  border-radius: var(--jl-radius-md);
}

.jl-empty__icon {
  width: var(--jl-space-12);
  height: var(--jl-space-12);
  border-radius: 50%;
  background: var(--jl-color-badge-bg);
}

.jl-empty__title {
  margin: 0;
  font-size: var(--jl-font-size-lg);
  font-weight: 600;
}

.jl-empty__body {
  color: var(--jl-color-text-muted);
}

.jl-empty__body :deep(p) {
  margin: 0;
}
</style>
