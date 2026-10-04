// The key pages of G34.6, shared by the visual and the end-to-end specs.
import type { FakeApiOptions } from "./api";
import { detailItem, ids } from "./data";

export interface VisualPage {
  name: string;
  path: string;
  api?: FakeApiOptions;
}

export const visualPages: VisualPage[] = [
  { name: "login", path: "/login", api: { signedIn: false } },
  { name: "setup", path: "/setup", api: { signedIn: false, setupRequired: true } },
  { name: "libraries", path: "/libraries" },
  { name: "library-posters", path: "/libraries/" + ids.movies },
  { name: "library-list", path: "/libraries/" + ids.movies + "?view=list" },
  { name: "item", path: "/items/" + detailItem.id },
  { name: "search", path: "/search?q=a" },
  { name: "stats", path: "/stats" },
  { name: "settings", path: "/settings" },
  { name: "admin-users", path: "/admin/users" },
  { name: "admin-access", path: "/admin/access" },
  { name: "admin-clients", path: "/admin/clients" },
  { name: "admin-webhooks", path: "/admin/webhooks" },
  { name: "admin-appearance", path: "/admin/appearance" },
  { name: "admin-plugins", path: "/admin/plugins" },
  { name: "devmode-banner", path: "/libraries", api: { devMode: true } },
];
