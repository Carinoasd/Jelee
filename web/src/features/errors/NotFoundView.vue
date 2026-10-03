<script setup lang="ts">
import { useI18n } from "vue-i18n";

const { t } = useI18n();
</script>

<template>
  <section aria-labelledby="not-found-title">
    <h1 id="not-found-title">{{ t("errors.notFoundTitle") }}</h1>
    <p>{{ t("errors.notFoundBody") }}</p>
    <RouterLink :to="{ name: 'libraries' }">{{ t("errors.backHome") }}</RouterLink>
  </section>
</template>
