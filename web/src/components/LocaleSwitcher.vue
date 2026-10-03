<script setup lang="ts">
import { computed, useId } from "vue";
import { useI18n } from "vue-i18n";
import { isLocale, supportedLocales, type Locale } from "@/i18n/locales";
import { useLocaleSync } from "@/i18n/useLocaleSync";

const { t } = useI18n();
const { locale, setLocale } = useLocaleSync();
const id = useId();
const names: Readonly<Record<Locale, string>> = {
  "zh-CN": "common.localeNames.zhCN",
  "zh-TW": "common.localeNames.zhTW",
  "en-US": "common.localeNames.enUS",
  "ja-JP": "common.localeNames.jaJP",
};
const options = supportedLocales.map((code) => ({ code, key: names[code] }));
const selected = computed({
  get: () => locale.value,
  set: (value: string) => {
    if (isLocale(value)) {
      setLocale(value);
    }
  },
});
</script>

<template>
  <span class="jl-locale">
    <label :for="id" class="jl-visually-hidden">{{ t("common.language") }}</label>
    <select :id="id" v-model="selected" class="jl-locale__select">
      <option v-for="option in options" :key="option.code" :value="option.code">{{ t(option.key) }}</option>
    </select>
  </span>
</template>

<style scoped>
.jl-locale__select {
  min-height: var(--jl-touch-target);
  padding: 0 var(--jl-space-2);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-sm);
  background: var(--jl-color-surface);
  color: var(--jl-color-text);
  font: inherit;
}
</style>
