<script setup lang="ts">
withDefaults(defineProps<{ tone?: "neutral" | "accent" | "danger" }>(), { tone: "neutral" });
</script>

<template>
  <span class="jl-badge" :class="`jl-badge--${tone}`"><slot /></span>
</template>

<style scoped>
.jl-badge {
  display: inline-block;
  padding: 0 var(--jl-space-2);
  border-radius: var(--jl-radius-pill);
  font-size: var(--jl-font-size-sm);
  line-height: 1.6;
  white-space: nowrap;
}

.jl-badge--neutral {
  background: var(--jl-color-badge-bg);
  color: var(--jl-color-text);
}

.jl-badge--accent {
  background: var(--jl-color-info-bg);
  color: var(--jl-color-primary);
  border: 1px solid var(--jl-color-primary);
}

.jl-badge--danger {
  background: var(--jl-color-danger-bg);
  color: var(--jl-color-danger);
  border: 1px solid var(--jl-color-danger);
}
</style>
