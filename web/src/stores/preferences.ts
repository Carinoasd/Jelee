import { defineStore } from "pinia";
import { shallowRef, watch } from "vue";
import { useApi } from "@/api";
import { getPreferences, savePreferences, type UserPreferences } from "@/features/settings/api";
import { useAuthStore } from "./auth";

export const themes = ["system", "light", "dark"] as const;
export type Theme = (typeof themes)[number];
export type Density = UserPreferences["density"];

export function isTheme(value: unknown): value is Theme {
  return typeof value === "string" && (themes as readonly string[]).includes(value);
}

/** Sets data-theme on <html>; "system" removes it so prefers-color-scheme decides. */
export function applyTheme(theme: Theme, root: HTMLElement = document.documentElement): void {
  if (theme === "system") {
    root.removeAttribute("data-theme");
  } else {
    root.dataset.theme = theme;
  }
}

/**
 * Interface preferences (G33.3). A signed-in user's choice is stored on the
 * server (GET/PUT /api/v1/users/me/preferences) and loaded whenever a user
 * signs in or a session resumes, so it follows the account across reloads and
 * devices. Signed out, the choice lives only in this tab's memory: browser
 * storage is reserved by policy (G35.1). Signing out keeps the current theme.
 */
export const usePreferencesStore = defineStore("preferences", () => {
  const { client } = useApi();
  const auth = useAuthStore();
  const theme = shallowRef<Theme>("system");
  const density = shallowRef<Density>("comfortable");
  // Bumped by every change, so a response that a later change or another
  // user overtook is dropped instead of reverting the newer choice.
  let generation = 0;

  function apply(next: UserPreferences): void {
    theme.value = next.theme;
    density.value = next.density;
    applyTheme(next.theme);
  }

  /** Loads the signed-in user's stored preferences; a failure keeps the current ones. */
  async function load(): Promise<void> {
    if (auth.user === null) {
      return;
    }
    const current = ++generation;
    try {
      const stored = await getPreferences(client);
      if (current === generation) {
        apply(stored);
      }
    } catch {
      // Presentation only: keep what the tab shows now.
    }
  }

  /**
   * Applies a theme at once and, when signed in, stores it. Rejects with the
   * API error when saving failed; the theme stays applied in this tab.
   */
  async function setTheme(next: Theme): Promise<void> {
    const current = ++generation;
    theme.value = next;
    applyTheme(next);
    if (auth.user === null) {
      return;
    }
    const saved = await savePreferences(client, { theme: next, density: density.value });
    if (current === generation) {
      apply(saved);
    }
  }

  watch(
    () => auth.user?.id,
    (id, previous) => {
      if (id !== undefined && id !== previous) {
        void load();
      }
    },
    { immediate: true },
  );

  return { theme, density, load, setTheme };
});
