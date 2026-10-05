import { defineStore } from "pinia";
import { shallowRef, watch } from "vue";
import { useApi } from "@/api";
import { getPreferences, savePreferences, type UserPreferences } from "@/features/settings/api";
import type { UserLayout } from "@/features/site/api";
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
 * Interface preferences (G33.3, G33.5). A signed-in user's choice is stored on
 * the server (GET/PUT /api/v1/users/me/preferences) and loaded whenever a user
 * signs in or a session resumes, so it follows the account across reloads and
 * devices. A user who never saved a theme reads the site's default theme.
 * Signed out, the choice lives only in this tab's memory: browser storage is
 * reserved by policy (G35.1). Signing out keeps the current theme.
 *
 * The page layout (stores/layout.ts) is part of the same document: every
 * replacement carries all fields, so a theme change keeps the layout and a
 * layout change keeps the theme.
 */
export const usePreferencesStore = defineStore("preferences", () => {
  const { client } = useApi();
  const auth = useAuthStore();
  const theme = shallowRef<Theme>("system");
  const density = shallowRef<Density>("comfortable");
  /** The account's stored layout; null until customized or while unknown. */
  const layout = shallowRef<UserLayout | null>(null);
  /** ID of the user whose stored preferences were read; null until then. */
  const loadedFor = shallowRef<string | null>(null);
  // Bumped by every change, so a response that a later change or another
  // user overtook is dropped instead of reverting the newer choice.
  let generation = 0;

  function apply(next: UserPreferences): void {
    theme.value = next.theme;
    density.value = next.density;
    // An older server answers without the member: nothing stored.
    layout.value = next.layout ?? null;
    applyTheme(next.theme);
  }

  /** Loads the signed-in user's stored preferences; a failure keeps the current ones. */
  async function load(): Promise<void> {
    const user = auth.user;
    if (user === null) {
      return;
    }
    const current = ++generation;
    try {
      const stored = await getPreferences(client);
      if (current === generation) {
        apply(stored);
        loadedFor.value = user.id;
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
    const saved = await savePreferences(client, { theme: next, density: density.value, layout: layout.value });
    if (current === generation) {
      apply(saved);
    }
  }

  /**
   * Stores the account's layout; returns false without a request while
   * signed out or before the stored preferences were read (saving then
   * would replace a theme this tab never saw). Rejects with the API error.
   */
  async function saveLayout(next: UserLayout | null): Promise<boolean> {
    const user = auth.user;
    if (user === null || loadedFor.value !== user.id) {
      return false;
    }
    const current = ++generation;
    layout.value = next;
    const saved = await savePreferences(client, { theme: theme.value, density: density.value, layout: next });
    if (current === generation) {
      apply(saved);
    }
    return true;
  }

  watch(
    () => auth.user?.id,
    (id, previous) => {
      if (id !== previous) {
        layout.value = null;
        loadedFor.value = null;
      }
      if (id !== undefined && id !== previous) {
        void load();
      }
    },
    { immediate: true },
  );

  return { theme, density, layout, loadedFor, load, setTheme, saveLayout };
});
