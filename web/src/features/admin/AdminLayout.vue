<script setup lang="ts">
import { useI18n } from "vue-i18n";

// Shell of the administration pages: a section navigation and the child
// view. Reaching it already required an administrator (route meta.admin);
// every request below is authorized again by the server (G35.2).
const { t } = useI18n();
const sections = [
  { name: "admin-users", key: "admin.nav.users" },
  { name: "admin-access", key: "admin.nav.access" },
  { name: "admin-clients", key: "admin.nav.clients" },
  { name: "admin-webhooks", key: "admin.nav.webhooks" },
  { name: "admin-stats", key: "admin.nav.stats" },
  { name: "admin-plugins", key: "admin.nav.plugins" },
  { name: "admin-appearance", key: "admin.nav.appearance" },
] as const;
</script>

<template>
  <div class="jl-admin">
    <nav class="jl-admin__nav" :aria-label="t('admin.navigation')">
      <RouterLink v-for="section in sections" :key="section.name" :to="{ name: section.name }">{{ t(section.key) }}</RouterLink>
    </nav>
    <div class="jl-admin__content">
      <RouterView />
    </div>
  </div>
</template>

<style scoped>
.jl-admin {
  display: grid;
  gap: var(--jl-space-6);
}

.jl-admin__nav {
  display: flex;
  flex-wrap: wrap;
  gap: var(--jl-space-1);
  border-bottom: 1px solid var(--jl-color-border);
}

.jl-admin__nav a {
  display: inline-flex;
  align-items: center;
  min-height: var(--jl-touch-target);
  padding: 0 var(--jl-space-3);
  color: var(--jl-color-text);
  text-decoration: none;
}

.jl-admin__nav a.router-link-active {
  color: var(--jl-color-primary);
  font-weight: 600;
  box-shadow: inset 0 -2px 0 var(--jl-color-primary);
}

.jl-admin__content {
  min-width: 0;
}
</style>
