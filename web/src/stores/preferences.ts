import { defineStore } from "pinia";
import { shallowRef } from "vue";

export const themes = ["system", "light", "dark"] as const;
export type Theme = (typeof themes)[number];

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
 * Interface preferences of this browser tab. The server has no per-user UI
 * preference API yet (G33.3), and browser storage is reserved by policy, so
 * the theme choice lives in memory and falls back to the system setting after
 * a reload (see docs/frontend-adr.md).
 */
export const usePreferencesStore = defineStore("preferences", () => {
  const theme = shallowRef<Theme>("system");

  function setTheme(next: Theme): void {
    theme.value = next;
    applyTheme(next);
  }

  return { theme, setTheme };
});
