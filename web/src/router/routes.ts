import type { RouteRecordRaw } from "vue-router";

declare module "vue-router" {
  interface RouteMeta {
    /** Route is reachable without a session (login, error pages). */
    public?: boolean;
    /** Route needs an administrator; the server re-checks every request. */
    admin?: boolean;
  }
}

// All views are lazy-loaded (G31.3, G35.3). No route renders or starts
// media playback (G27.1); scripts/check-no-playback.mjs guards this file.
export const routes: RouteRecordRaw[] = [
  { path: "/", redirect: { name: "libraries" } },
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
  },
  {
    path: "/account",
    name: "account",
    component: () => import("@/features/account/AccountView.vue"),
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
