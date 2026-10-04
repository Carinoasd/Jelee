<script setup lang="ts">
import { computed, nextTick, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";
import LocaleSwitcher from "@/components/LocaleSwitcher.vue";
import UiButton from "@/components/ui/UiButton.vue";
import UiToastRegion from "@/components/ui/UiToastRegion.vue";
import DevModeBanner from "@/features/devmode/DevModeBanner.vue";
import SearchBox from "@/features/search/SearchBox.vue";
import { useLocaleSync } from "@/i18n/useLocaleSync";
import { useAuthStore } from "@/stores/auth";
import { usePreferencesStore } from "@/stores/preferences";
import { useToastStore } from "@/stores/toasts";

const { t } = useI18n();
const auth = useAuthStore();
const toastStore = useToastStore();
const router = useRouter();
const route = useRoute();
const inAdmin = computed(() => route.meta.admin === true);
const { applyUserLocale } = useLocaleSync();
// Loads the signed-in user's stored theme whenever a user becomes known.
usePreferencesStore();

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
    <RouterLink class="jl-header__brand" :to="{ name: auth.isGuest ? 'shared' : 'home' }">{{ t("common.appName") }}</RouterLink>
    <!-- A share guest may only browse its share (auth.isGuest, decided by the
         account name), so it gets none of the account, settings, search or
         administration entries; the server refuses those routes anyway. -->
    <nav v-if="auth.isGuest" :aria-label="t('common.mainNavigation')" class="jl-header__nav" data-testid="guest-nav">
      <RouterLink :to="{ name: 'shared' }">{{ t("common.sharedWithMe") }}</RouterLink>
    </nav>
    <nav v-else-if="auth.isAuthenticated" :aria-label="t('common.mainNavigation')" class="jl-header__nav">
      <RouterLink :to="{ name: 'home' }">{{ t("layout.home.title") }}</RouterLink>
      <RouterLink :to="{ name: 'libraries' }">{{ t("libraries.title") }}</RouterLink>
      <RouterLink :to="{ name: 'stats' }">{{ t("common.stats") }}</RouterLink>
      <RouterLink :to="{ name: 'account' }">{{ t("account.title") }}</RouterLink>
      <RouterLink :to="{ name: 'settings' }">{{ t("common.settings") }}</RouterLink>
      <RouterLink
        v-if="auth.isAdmin"
        :to="{ name: 'admin-users' }"
        :class="{ 'router-link-active': inAdmin }"
        data-testid="admin-link"
      >
        {{ t("common.admin") }}
      </RouterLink>
    </nav>
    <span class="jl-header__spacer" />
    <SearchBox v-if="auth.isAuthenticated && !auth.isGuest" class="jl-header__search" />
    <LocaleSwitcher />
    <template v-if="auth.user">
      <span v-if="auth.isGuest" class="jl-header__user">{{ t("common.guestSession") }}</span>
      <span v-else class="jl-header__user">{{ t("common.signedInAs", { name: auth.user.displayName || auth.user.name }) }}</span>
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
  flex-wrap: wrap;
  gap: var(--jl-space-1);
}

.jl-header__search {
  flex: 0 1 18rem;
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
