<script setup lang="ts">
import { onMounted, shallowRef, useTemplateRef } from "vue";
import { useI18n } from "vue-i18n";
import UiButton from "@/components/ui/UiButton.vue";
import type { RevealedSecret } from "@/stores/webhooks";

// The signing secret is returned by create and rotate only. It is shown
// once, here; "I have saved it" removes it from memory for good.
const props = defineProps<{ secret: RevealedSecret }>();
const emit = defineEmits<{ done: [] }>();
const { t } = useI18n();
const copyState = shallowRef<"idle" | "copied" | "failed">("idle");
const root = useTemplateRef<HTMLElement>("root");

onMounted(() => {
  root.value?.querySelector<HTMLElement>("h2")?.focus();
});

async function copy() {
  try {
    await navigator.clipboard.writeText(props.secret.secret);
    copyState.value = "copied";
  } catch {
    copyState.value = "failed";
  }
}
</script>

<template>
  <section ref="root" class="jl-secret" aria-labelledby="webhook-secret-title">
    <h2 id="webhook-secret-title" tabindex="-1">{{ t("webhooks.secret.title", { name: secret.name }) }}</h2>
    <p>{{ t("webhooks.secret.body") }}</p>
    <p class="jl-secret__value" data-secret>
      <code id="webhook-secret-value">{{ secret.secret }}</code>
    </p>
    <div class="jl-secret__actions">
      <UiButton variant="secondary" aria-describedby="webhook-secret-value" @click="copy">{{ t("webhooks.secret.copy") }}</UiButton>
      <UiButton @click="emit('done')">{{ t("webhooks.secret.done") }}</UiButton>
    </div>
    <p class="jl-secret__status" role="status">
      <template v-if="copyState === 'copied'">{{ t("webhooks.secret.copied") }}</template>
      <template v-else-if="copyState === 'failed'">{{ t("webhooks.secret.copyFailed") }}</template>
    </p>
  </section>
</template>

<style scoped>
.jl-secret {
  display: grid;
  gap: var(--jl-space-3);
  padding: var(--jl-space-6);
  border: 2px solid var(--jl-color-danger);
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-danger-bg);
}

.jl-secret h2 {
  margin: 0;
  font-size: var(--jl-font-size-lg);
  color: var(--jl-color-danger);
}

.jl-secret p {
  margin: 0;
}

.jl-secret__value {
  padding: var(--jl-space-3);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-sm);
  background: var(--jl-color-surface);
}

.jl-secret__value code {
  font-family: ui-monospace, monospace;
  overflow-wrap: anywhere;
  user-select: all;
}

.jl-secret__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
}

.jl-secret__status {
  min-height: 1.5em;
  font-size: var(--jl-font-size-sm);
}
</style>
