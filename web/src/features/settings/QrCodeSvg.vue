<script setup lang="ts">
import { computed } from "vue";
import { encodeQr, qrPath } from "./qr";

// The QR code as inline SVG (no image URL, no style attribute), dark modules
// on white with the 4-module quiet zone, independent of the color theme so
// that cameras read it. Data beyond version 40 renders nothing; the caller
// shows the text form as well.
const props = defineProps<{ value: string; label: string }>();
const quietZone = 4;
const code = computed(() => {
  try {
    return encodeQr(props.value);
  } catch {
    return null;
  }
});
const extent = computed(() => (code.value === null ? 0 : code.value.size + quietZone * 2));
const path = computed(() => (code.value === null ? "" : qrPath(code.value, quietZone)));
</script>

<template>
  <svg
    v-if="code"
    class="jl-qr"
    xmlns="http://www.w3.org/2000/svg"
    :viewBox="`0 0 ${extent} ${extent}`"
    role="img"
    :aria-label="label"
    shape-rendering="crispEdges"
    :data-version="code.version"
  >
    <rect x="0" y="0" :width="extent" :height="extent" fill="#ffffff" />
    <path :d="path" fill="#000000" />
  </svg>
</template>

<style scoped>
.jl-qr {
  display: block;
  width: min(100%, 240px);
  height: auto;
  border-radius: var(--jl-radius-sm);
}
</style>
