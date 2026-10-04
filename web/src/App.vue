<script setup lang="ts">
import { computed, nextTick, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useRouter } from "vue-router";
import LocaleSwitcher from "@/components/LocaleSwitcher.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiToastRegion from "@/components/ui/UiToastRegion.vue";
import DevModeBanner from "@/features/devmode/DevModeBanner.vue";
import { useLocaleSync } from "@/i18n/useLocaleSync";
import { useAuthStore } from "@/stores/auth";
import { useToastStore } from "@/stores/toasts";

const { t } = useI18n();
const auth = useAuthStore();
const toastStore = useToastStore();
const router = useRouter();
const { applyUserLocale } = useLocaleSync();

const toasts = computed(() =>
  toastStore.toasts.map((toast) => ({ id: toast.id, tone: toast.tone, message: t(toast.key, toast.params) })),
);

// The account's saved locale wins over the browser once a user is known
// (login or resumed session). Per-user caches reset themselves in their
// stores (see stores/userScoped.ts).
watch(
  () => auth.user?.id,
  (id) => {
    if (id !== undefined) {
      applyUserLocale(auth.user?.locale);
    }
  },
  { immediate: true },
);

// After each navigation, move focus to the new page's heading so keyboard
// and screen reader users start at the top of the new content.
router.afterEach((to, from) => {
  if (from.matched.length === 0 || to.path === from.path) {
    return;
  }
  void nextTick(() => {
    document.querySelector<HTMLElement>("#main h1")?.focus();
  });
});

async function signOut() {
  await auth.logout().catch(() => undefined);
  toastStore.push("auth.signedOut", "info");
  await router.replace({ name: "login" });
}
</script>

<template>
  <a class="jl-skip-link" href="#main">{{ t("common.skipToContent") }}</a>
  <DevModeBanner />
  <header class="jl-header">
    <RouterLink class="jl-header__brand" :to="{ name: 'libraries' }">{{ t("common.appName") }}</RouterLink>
    <nav v-if="auth.isAuthenticated" :aria-label="t('common.mainNavigation')" class="jl-header__nav">
      <RouterLink :to="{ name: 'libraries' }">{{ t("libraries.title") }}</RouterLink>
      <RouterLink :to="{ name: 'account' }">{{ t("account.title") }}</RouterLink>
    </nav>
    <span class="jl-header__spacer" />
    <LocaleSwitcher />
    <template v-if="auth.user">
      <span class="jl-header__user">{{ t("common.signedInAs", { name: auth.user.displayName || auth.user.name }) }}</span>
      <UiButton variant="secondary" @click="signOut">{{ t("common.logout") }}</UiButton>
    </template>
  </header>
  <main id="main" class="jl-main" tabindex="-1">
    <RouterView />
  </main>
  <UiToastRegion :toasts="toasts" @dismiss="toastStore.dismiss" />
</template>

<style scoped>
.jl-header {
  position: sticky;
  top: 0;
  z-index: var(--jl-z-header);
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--jl-space-2) var(--jl-space-4);
  padding: var(--jl-space-2) var(--jl-space-4);
  background: var(--jl-color-surface);
  border-bottom: 1px solid var(--jl-color-border);
}

.jl-header__brand {
  font-weight: 700;
  font-size: var(--jl-font-size-lg);
  color: var(--jl-color-text);
  text-decoration: none;
}

.jl-header__nav {
  display: flex;
  gap: var(--jl-space-1);
}

.jl-header__nav a {
  display: inline-flex;
  align-items: center;
  min-height: var(--jl-touch-target);
  padding: 0 var(--jl-space-3);
  border-radius: var(--jl-radius-md);
  color: var(--jl-color-text);
  text-decoration: none;
}

.jl-header__nav a:hover {
  background: var(--jl-color-badge-bg);
}

.jl-header__nav a[aria-current="page"],
.jl-header__nav a.router-link-active {
  color: var(--jl-color-primary);
  font-weight: 600;
  box-shadow: inset 0 -2px 0 var(--jl-color-primary);
}

.jl-header__spacer {
  flex: 1;
}

.jl-header__user {
  color: var(--jl-color-text-muted);
  font-size: var(--jl-font-size-sm);
}

.jl-main {
  max-width: var(--jl-content-max-width);
  margin: 0 auto;
  padding: var(--jl-space-6) var(--jl-space-4) var(--jl-space-12);
}

.jl-main:focus {
  outline: none;
}
</style>
