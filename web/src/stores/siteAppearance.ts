import { defineStore } from "pinia";
import { shallowRef, watch } from "vue";
import { useApi } from "@/api";
import { getSiteAppearance, type SiteAppearance } from "@/features/site/api";
import { useAuthStore } from "./auth";

/**
 * The effective site appearance (G33.2–G33.5) for the signed-in user: the
 * administrator's default layout, token overrides and sanitized custom CSS.
 * Null while signed out or when the server cannot be read; callers then keep
 * their browser-local behavior. It holds no CSS logic, so the main bundle
 * does not carry the sanitizer (theme/customCss.ts applies the CSS).
 */
export const useSiteAppearanceStore = defineStore("siteAppearance", () => {
  const { client } = useApi();
  const auth = useAuthStore();
  const view = shallowRef<SiteAppearance | null>(null);
  /** Bumped when this tab replaced the plugin document (import), so the plugin host reloads it. */
  const pluginsRevision = shallowRef(0);
  // Bumped by every load and replacement, so a slow response for an
  // earlier user or an older state never overwrites a newer one.
  let generation = 0;

  async function load(): Promise<void> {
    const current = ++generation;
    if (auth.user === null) {
      view.value = null;
      return;
    }
    try {
      const next = await getSiteAppearance(client);
      if (current === generation) {
        view.value = next;
      }
    } catch {
      if (current === generation) {
        view.value = null;
      }
    }
  }

  /** Replaces the view after this tab saved a new appearance. */
  function replace(next: SiteAppearance | null): void {
    generation++;
    view.value = next;
  }

  watch(
    () => auth.user?.id,
    () => {
      void load();
    },
    { immediate: true },
  );

  function pluginsChanged(): void {
    pluginsRevision.value++;
  }

  return { view, pluginsRevision, load, replace, pluginsChanged };
});
