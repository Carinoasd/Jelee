<script setup lang="ts">
import { useI18n } from "vue-i18n";
import { useRouter } from "vue-router";
import LocaleSwitcher from "@/components/LocaleSwitcher.vue";
import UiButton from "@/components/ui/UiButton.vue";
import { useAuthStore } from "@/stores/auth";
import { useLibrariesStore } from "@/stores/libraries";

const { t } = useI18n();
const auth = useAuthStore();
const libraries = useLibrariesStore();
const router = useRouter();

async function signOut() {
  await auth.logout().catch(() => undefined);
  libraries.reset();
  await router.replace({ name: "login" });
}
</script>

<template>
  <header class="jl-header">
    <span class="jl-header__brand">{{ t("common.appName") }}</span>
    <nav v-if="auth.isAuthenticated" :aria-label="t('common.mainNavigation')" class="jl-header__nav">
      <RouterLink :to="{ name: 'libraries' }">{{ t("libraries.title") }}</RouterLink>
    </nav>
    <span class="jl-header__spacer" />
    <LocaleSwitcher />
    <template v-if="auth.user">
      <span class="jl-header__user">{{ t("common.signedInAs", { name: auth.user.displayName || auth.user.name }) }}</span>
      <UiButton variant="secondary" @click="signOut">{{ t("common.logout") }}</UiButton>
    </template>
  </header>
  <main class="jl-main">
    <RouterView />
  </main>
</template>

<style scoped>
.jl-header {
  position: sticky;
  top: 0;
  z-index: var(--jl-z-header);
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-4);
  padding: var(--jl-space-2) var(--jl-space-4);
  background: var(--jl-color-surface);
  border-bottom: 1px solid var(--jl-color-border);
}

.jl-header__brand {
  font-weight: 700;
  font-size: var(--jl-font-size-lg);
}

.jl-header__spacer {
  flex: 1;
}

.jl-header__user {
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-main {
  max-width: 1200px;
  margin: 0 auto;
  padding: var(--jl-space-6) var(--jl-space-4);
}
</style>
