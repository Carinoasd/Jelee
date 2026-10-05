<script setup lang="ts">
// Placeholder with the final element's footprint, so content replacing it
// does not shift the layout (G34.4). Hidden from assistive technology; the
// surrounding region announces loading through role="status".
withDefaults(defineProps<{ shape?: "text" | "title" | "block" | "poster"; width?: string }>(), {
  shape: "text",
  width: "100%",
});
</script>

<template>
  <span class="jl-skeleton" :class="`jl-skeleton--${shape}`" :style="{ width }" aria-hidden="true" />
</template>

<style scoped>
.jl-skeleton {
  display: block;
  border-radius: var(--jl-radius-sm);
  background: linear-gradient(
    90deg,
    var(--jl-color-skeleton) 25%,
    var(--jl-color-skeleton-highlight) 50%,
    var(--jl-color-skeleton) 75%
  );
  background-size: 200% 100%;
  animation: jl-skeleton-shimmer 1.4s ease-in-out infinite;
}

.jl-skeleton--text {
  height: 1em;
  margin: 0.25em 0;
}

.jl-skeleton--title {
  height: 1.5em;
  margin: 0.25em 0;
}

.jl-skeleton--block {
  height: 4em;
}

.jl-skeleton--poster {
  aspect-ratio: var(--jl-poster-ratio);
  border-radius: var(--jl-radius-md);
}

@keyframes jl-skeleton-shimmer {
  from {
    background-position: 100% 0;
  }
  to {
    background-position: -100% 0;
  }
}

@media (prefers-reduced-motion: reduce) {
  .jl-skeleton {
    animation: none;
    background: var(--jl-color-skeleton);
  }
}
</style>
