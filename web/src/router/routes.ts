import type { RouteRecordRaw } from "vue-router";
import { loadNamespace, type LazyNamespace } from "@/i18n";

declare module "vue-router" {
  interface RouteMeta {
    /** Route is reachable without a session (login, error pages). */
    public?: boolean;
    /** Route needs an administrator; the server re-checks every request. */
    admin?: boolean;
    /**
     * Route is open to share guests (the restricted sessions of a share
     * link); the guard sends guests from every other route to their page.
     */
    guest?: boolean;
  }
}

/** A lazy view whose catalog namespace is fetched together with its chunk. */
function lazyView<T>(namespace: LazyNamespace, load: () => Promise<T>): () => Promise<T> {
  return async () => {
    const [view] = await Promise.all([load(), loadNamespace(namespace)]);
    return view;
  };
}

// All views are lazy-loaded (G31.3, G35.3). No route renders or starts
// media playback (G27.1); scripts/check-no-playback.mjs guards this file.
export const routes: RouteRecordRaw[] = [
  {
    path: "/",
    name: "home",
    component: () => import("@/features/home/HomeView.vue"),
  },
  {
    path: "/login",
    name: "login",
    component: () => import("@/features/auth/LoginView.vue"),
    meta: { public: true },
  },
  {
    path: "/setup",
    name: "setup",
    component: () => import("@/features/setup/SetupView.vue"),
    meta: { public: true },
  },
  {
    // Share links: /share#<token>. The token stays in the fragment, which
    // the browser never sends to the server; the view removes it at once.
    path: "/share",
    name: "share-redeem",
    component: lazyView("shares", () => import("@/features/shares/ShareRedeemView.vue")),
    meta: { public: true },
  },
  {
    path: "/shared",
    name: "shared",
    component: lazyView("shares", () => import("@/features/shares/GuestShareView.vue")),
    meta: { guest: true },
  },
  {
    path: "/libraries",
    name: "libraries",
    component: () => import("@/features/libraries/LibrariesView.vue"),
  },
  {
    path: "/libraries/:libraryId",
    name: "library",
    component: () => import("@/features/items/LibraryItemsView.vue"),
    props: true,
  },
  {
    path: "/items/:itemId",
    name: "item",
    component: () => import("@/features/items/ItemDetailView.vue"),
    props: true,
    meta: { guest: true },
  },
  {
    path: "/account",
    name: "account",
    component: () => import("@/features/account/AccountView.vue"),
  },
  {
    path: "/search",
    name: "search",
    component: () => import("@/features/search/SearchView.vue"),
  },
  {
    path: "/stats",
    name: "stats",
    component: () => import("@/features/stats/MyStatsView.vue"),
  },
  {
    path: "/settings",
    name: "settings",
    component: () => import("@/features/settings/SettingsView.vue"),
  },
  {
    // Administration. meta is merged into every child, so the guard sends
    // non-administrators to 403 for all of them; the server re-checks.
    path: "/admin",
    component: () => import("@/features/admin/AdminLayout.vue"),
    meta: { admin: true },
    children: [
      { path: "", name: "admin", redirect: { name: "admin-users" } },
      {
        path: "users",
        name: "admin-users",
        component: () => import("@/features/users/AdminUsersView.vue"),
      },
      {
        path: "users/:userId",
        name: "admin-user",
        component: () => import("@/features/users/AdminUserView.vue"),
        props: true,
      },
      {
        path: "access",
        name: "admin-access",
        component: () => import("@/features/access/AccessPolicyView.vue"),
      },
      {
        path: "access/network",
        name: "admin-access-network",
        component: lazyView("networkRules", () => import("@/features/access/NetworkRulesView.vue")),
      },
      {
        path: "shares",
        name: "admin-shares",
        component: lazyView("shares", () => import("@/features/shares/SharesAdminView.vue")),
      },
      {
        path: "clients",
        name: "admin-clients",
        component: lazyView("clients", () => import("@/features/clients/ClientControlView.vue")),
      },
      {
        path: "webhooks",
        name: "admin-webhooks",
        component: () => import("@/features/webhooks/WebhooksView.vue"),
      },
      {
        path: "webhooks/:webhookId",
        name: "admin-webhook",
        component: () => import("@/features/webhooks/WebhookView.vue"),
        props: true,
      },
      {
        path: "stats",
        name: "admin-stats",
        component: () => import("@/features/stats/AdminStatsView.vue"),
      },
      {
        path: "plugins",
        name: "admin-plugins",
        component: () => import("@/features/admin/PluginsAdminView.vue"),
      },
      {
        path: "appearance",
        name: "admin-appearance",
        component: () => import("@/features/admin/AppearanceAdminView.vue"),
      },
    ],
  },
  {
    path: "/forbidden",
    name: "forbidden",
    component: () => import("@/features/errors/ForbiddenView.vue"),
    meta: { public: true },
  },
  {
    path: "/:pathMatch(.*)*",
    name: "not-found",
    component: () => import("@/features/errors/NotFoundView.vue"),
    meta: { public: true },
  },
];
