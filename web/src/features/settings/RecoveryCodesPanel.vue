<script setup lang="ts">
import { onMounted, shallowRef, useTemplateRef } from "vue";
import { saveBlob } from "@/api/download";
import UiButton from "@/components/ui/UiButton.vue";
import UiCheckbox from "@/components/ui/UiCheckbox.vue";
import { useTwoFactorI18n } from "@/i18n/twoFactor";
import { copyText } from "./clipboard";

// Recovery codes are returned once (enable, regenerate). They stay on screen
// until the user confirms having saved them; closing drops them for good.
const props = defineProps<{ codes: readonly string[]; account: string }>();
const emit = defineEmits<{ done: [] }>();
const { t } = useTwoFactorI18n();
const saved = shallowRef(false);
const copyState = shallowRef<"idle" | "copied" | "failed">("idle");
const root = useTemplateRef<HTMLElement>("root");

onMounted(() => {
  root.value?.querySelector<HTMLElement>("h3")?.focus();
});

function fileText(): string {
  return [t("twoFactor.recovery.fileHeading", { name: props.account }), t("twoFactor.recovery.fileNote"), "", ...props.codes, ""].join("\n");
}

function download() {
  saveBlob(new Blob([fileText()], { type: "text/plain;charset=utf-8" }), "jelee-recovery-codes.txt");
}

async function copy() {
  copyState.value = (await copyText(props.codes.join("\n"))) ? "copied" : "failed";
}
</script>

<template>
  <section ref="root" class="jl-codes" aria-labelledby="two-factor-codes-title">
    <h3 id="two-factor-codes-title" tabindex="-1">{{ t("twoFactor.recovery.title") }}</h3>
    <p>{{ t("twoFactor.recovery.body") }}</p>
    <ol class="jl-codes__list" :aria-label="t('twoFactor.recovery.listLabel')" data-recovery-codes>
      <li v-for="code in codes" :key="code"><code>{{ code }}</code></li>
    </ol>
    <div class="jl-codes__actions">
      <UiButton variant="secondary" @click="download">{{ t("twoFactor.recovery.download") }}</UiButton>
      <UiButton variant="secondary" @click="copy">{{ t("twoFactor.recovery.copy") }}</UiButton>
    </div>
    <p class="jl-codes__status" role="status">
      <template v-if="copyState === 'copied'">{{ t("twoFactor.copied") }}</template>
      <template v-else-if="copyState === 'failed'">{{ t("twoFactor.copyFailed") }}</template>
    </p>
    <UiCheckbox v-model="saved" :label="t('twoFactor.recovery.saved')" />
    <div>
      <UiButton :disabled="!saved" @click="emit('done')">{{ t("twoFactor.recovery.done") }}</UiButton>
    </div>
  </section>
</template>

<style scoped>
.jl-codes {
  display: grid;
  gap: var(--jl-space-3);
  padding: var(--jl-space-4);
  border: 2px solid var(--jl-color-danger);
  border-radius: var(--jl-radius-md);
  background: var(--jl-color-danger-bg);
}

.jl-codes h3 {
  margin: 0;
  font-size: var(--jl-font-size-md);
  color: var(--jl-color-danger);
}

.jl-codes p {
  margin: 0;
}

.jl-codes__list {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(12rem, 1fr));
  gap: var(--jl-space-1) var(--jl-space-4);
  margin: 0;
  padding: var(--jl-space-3) var(--jl-space-3) var(--jl-space-3) var(--jl-space-8);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-sm);
  background: var(--jl-color-surface);
}

.jl-codes__list code {
  font-family: ui-monospace, monospace;
  user-select: all;
}

.jl-codes__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
}

.jl-codes__status {
  min-height: 1.5em;
  font-size: var(--jl-font-size-sm);
}
</style>
