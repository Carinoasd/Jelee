<script setup lang="ts">
import { useI18n } from "vue-i18n";

const { t } = useI18n();
</script>

<template>
  <section aria-labelledby="forbidden-title">
    <h1 id="forbidden-title">{{ t("errors.forbiddenTitle") }}</h1>
    <p>{{ t("errors.forbiddenBody") }}</p>
    <RouterLink :to="{ name: 'libraries' }">{{ t("errors.backHome") }}</RouterLink>
  </section>
</template>
