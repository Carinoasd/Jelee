<script setup lang="ts">
import { useI18n } from "vue-i18n";

// Links between the pages of the content access section.
const { t } = useI18n();
</script>

<template>
  <nav class="jl-access-tabs" :aria-label="t('access.tabs.label')">
    <RouterLink :to="{ name: 'admin-access' }" exact-active-class="jl-access-tabs--current">{{ t("access.tabs.policy") }}</RouterLink>
    <RouterLink :to="{ name: 'admin-access-network' }" exact-active-class="jl-access-tabs--current">{{ t("access.tabs.network") }}</RouterLink>
    <RouterLink :to="{ name: 'admin-access-matrix' }" exact-active-class="jl-access-tabs--current">{{ t("access.tabs.matrix") }}</RouterLink>
  </nav>
</template>

<style scoped>
.jl-access-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-2);
}

.jl-access-tabs a {
  display: inline-flex;
  align-items: center;
  min-height: var(--jl-touch-target);
  padding: 0 var(--jl-space-3);
  border: 1px solid var(--jl-color-border);
  border-radius: var(--jl-radius-md);
  color: var(--jl-color-text);
  text-decoration: none;
}

.jl-access-tabs a.jl-access-tabs--current {
  border-color: var(--jl-color-primary);
  color: var(--jl-color-primary);
  font-weight: 600;
}
</style>
