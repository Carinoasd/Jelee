<script setup lang="ts">
import { computed, shallowRef, watch } from "vue";
import { posterUrl } from "./api";

// Poster artwork with a lettered placeholder when the item has no image or
// it fails to load. Decorative (alt=""): the title is always shown as text.
const props = withDefaults(defineProps<{ itemId: string; title: string; width?: number; eager?: boolean }>(), {
  width: 300,
  eager: false,
});
const failed = shallowRef(false);
watch(
  () => props.itemId,
  () => {
    failed.value = false;
  },
);
const initial = computed(() => Array.from(props.title.trim())[0]?.toLocaleUpperCase() ?? "");
</script>

<template>
  <span class="jl-poster">
    <img
      v-if="!failed"
      class="jl-poster__image"
      :src="posterUrl(itemId, width)"
      alt=""
      :loading="eager ? 'eager' : 'lazy'"
      decoding="async"
      @error="failed = true"
    />
    <span v-else class="jl-poster__fallback" aria-hidden="true">{{ initial }}</span>
  </span>
</template>

<style scoped>
.jl-poster {
  display: block;
  aspect-ratio: var(--jl-poster-ratio);
  overflow: hidden;
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-skeleton);
}

.jl-poster__image {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.jl-poster__fallback {
  display: grid;
  place-items: center;
  height: 100%;
  font-size: var(--jl-font-size-xl);
  font-weight: 700;
  color: var(--jl-color-text-muted);
}
</style>
