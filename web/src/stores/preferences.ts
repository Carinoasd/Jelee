import { defineStore } from "pinia";
import { shallowRef, watch } from "vue";
import { useApi } from "@/api";
import { getPreferences, savePreferences, type UserPreferences } from "@/features/settings/api";
import { itemViews, type ItemView } from "@/features/items/labels";
import type { UserLayout } from "@/features/site/api";
import {
  densities,
  extraPaletteIds,
  listDensities,
  palettes,
  posterRatios,
  posterSizes,
  type ColorScheme,
  type Density,
  type ListDensity,
  type PaletteId,
  type PosterRatio,
  type PosterSize,
} from "@/theme/tokens";
import { useAuthStore } from "./auth";
import { readPersisted, writePersisted } from "./persist";

/** Theme choices: the system scheme or one of the presets (theme/tokens.ts). */
export const themes = ["system", "light", "dark", ...extraPaletteIds] as const satisfies readonly ("system" | PaletteId)[];
export type Theme = (typeof themes)[number];
export type { Density, ListDensity, PosterRatio, PosterSize };
export type ServerTheme = UserPreferences["theme"];
/** Themes the server stores (also the site default the administrator picks). */
export const serverThemes = ["system", "light", "dark"] as const satisfies readonly ServerTheme[];
export const posterRatioIds = Object.keys(posterRatios) as PosterRatio[];
export const posterSizeIds = Object.keys(posterSizes) as PosterSize[];
export const listDensityIds = Object.keys(listDensities) as ListDensity[];
export { densities };

export function isTheme(value: unknown): value is Theme {
  return typeof value === "string" && (themes as readonly string[]).includes(value);
}

/** The scheme a theme choice resolves to; null follows the system. */
export function themeScheme(theme: Theme): ColorScheme | null {
  return theme === "system" ? null : palettes[theme].scheme;
}

/** What the server stores for a theme: presets keep their scheme. */
export function serverTheme(theme: Theme): ServerTheme {
  return themeScheme(theme) ?? "system";
}

/** Display choices the server's preferences document has no member for yet. */
export interface DisplayPreferences {
  /** A preset beyond plain light and dark, kept only while the server's theme matches its scheme. */
  readonly palette: PaletteId | null;
  readonly posterRatio: PosterRatio;
  readonly posterSize: PosterSize;
  readonly listDensity: ListDensity;
  readonly view: ItemView;
}

export const defaultDisplay: DisplayPreferences = { palette: null, posterRatio: "portrait", posterSize: "medium", listDensity: "comfortable", view: "poster" };

const pick = <T extends string>(value: unknown, allowed: readonly T[], fallback: T): T =>
  typeof value === "string" && (allowed as readonly string[]).includes(value) ? (value as T) : fallback;

/** Reads display choices from untrusted storage; unknown values fall back to the defaults. */
export function normalizeDisplay(raw: unknown): DisplayPreferences {
  const record = typeof raw === "object" && raw !== null ? (raw as Record<string, unknown>) : {};
  const palette = pick<PaletteId | "">(record.palette, extraPaletteIds, "");
  return {
    palette: palette === "" ? null : palette,
    posterRatio: pick(record.posterRatio, posterRatioIds, defaultDisplay.posterRatio),
    posterSize: pick(record.posterSize, posterSizeIds, defaultDisplay.posterSize),
    listDensity: pick(record.listDensity, listDensityIds, defaultDisplay.listDensity),
    view: pick(record.view, itemViews, defaultDisplay.view),
  };
}

function setAttribute(root: HTMLElement, name: string, value: string | null): void {
  if (value === null) {
    root.removeAttribute(name);
  } else {
    root.setAttribute(name, value);
  }
}

/**
 * Sets data-theme on <html> ("system" removes it so prefers-color-scheme
 * decides) and data-palette for presets beyond light and dark.
 */
export function applyTheme(theme: Theme, root: HTMLElement = document.documentElement): void {
  setAttribute(root, "data-theme", themeScheme(theme));
  setAttribute(root, "data-palette", theme === "system" || theme === "light" || theme === "dark" ? null : theme);
}

/** Sets the density, poster and list attributes the token stylesheet reads; defaults remove them. */
export function applyDisplay(density: Density, display: DisplayPreferences, root: HTMLElement = document.documentElement): void {
  setAttribute(root, "data-density", density === "comfortable" ? null : density);
  setAttribute(root, "data-poster", display.posterRatio === defaultDisplay.posterRatio ? null : display.posterRatio);
  setAttribute(root, "data-poster-size", display.posterSize === defaultDisplay.posterSize ? null : display.posterSize);
  setAttribute(root, "data-list-density", display.listDensity === defaultDisplay.listDensity ? null : display.listDensity);
}

const displayKey = "display-preferences";

/**
 * Interface preferences (G33.2, G33.3, G33.5). A signed-in user's choice is
 * stored on the server (GET/PUT /api/v1/users/me/preferences) and loaded
 * whenever a user signs in or a session resumes, so it follows the account
 * across reloads and devices. A user who never saved a theme reads the site's
 * default theme. Signed out, the choice lives only in this tab's memory.
 * Signing out keeps the current theme.
 *
 * The server document holds the theme scheme (system, light or dark), the
 * density and the page layout; every replacement carries all fields, so a
 * theme change keeps the layout and a layout change keeps the theme. The
 * other display choices (a preset beyond light and dark, poster ratio and
 * size, list density, default view) have no member there yet: they are kept
 * per account in this browser (stores/persist.ts) until the server document
 * grows one. A preset is only applied while the server's theme still names
 * its scheme, so a theme changed on another device wins.
 */
export const usePreferencesStore = defineStore("preferences", () => {
  const { client } = useApi();
  const auth = useAuthStore();
  const theme = shallowRef<Theme>("system");
  const density = shallowRef<Density>("comfortable");
  const display = shallowRef<DisplayPreferences>(defaultDisplay);
  /** The account's stored layout; null until customized or while unknown. */
  const layout = shallowRef<UserLayout | null>(null);
  /** ID of the user whose stored preferences were read; null until then. */
  const loadedFor = shallowRef<string | null>(null);
  // Bumped by every change, so a response that a later change or another
  // user overtook is dropped instead of reverting the newer choice.
  let generation = 0;

  function applyAll(): void {
    applyTheme(theme.value);
    applyDisplay(density.value, display.value);
  }

  function apply(next: UserPreferences): void {
    const palette = display.value.palette;
    theme.value = palette !== null && palettes[palette].scheme === next.theme ? palette : next.theme;
    density.value = next.density;
    // An older server answers without the member: nothing stored.
    layout.value = next.layout ?? null;
    applyAll();
  }

  function setDisplayState(next: DisplayPreferences): void {
    display.value = next;
    const user = auth.user;
    if (user !== null) {
      writePersisted(displayKey, next, user.id);
    }
    applyAll();
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

  async function store(next: Pick<UserPreferences, "theme" | "density">): Promise<void> {
    const current = ++generation;
    if (auth.user === null) {
      return;
    }
    const saved = await savePreferences(client, { ...next, layout: layout.value });
    if (current === generation) {
      apply(saved);
    }
  }

  /**
   * Applies a theme at once and, when signed in, stores it. Rejects with the
   * API error when saving failed; the theme stays applied in this tab.
   */
  async function setTheme(next: Theme): Promise<void> {
    theme.value = next;
    setDisplayState({ ...display.value, palette: next === "system" || next === "light" || next === "dark" ? null : next });
    await store({ theme: serverTheme(next), density: density.value });
  }

  /** Applies and stores the density like setTheme. */
  async function setDensity(next: Density): Promise<void> {
    density.value = next;
    applyAll();
    await store({ theme: serverTheme(theme.value), density: next });
  }

  /** Applies display choices at once; kept per account in this browser. */
  function setDisplay(next: Partial<Omit<DisplayPreferences, "palette">>): void {
    setDisplayState({ ...display.value, ...next });
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
    const saved = await savePreferences(client, { theme: serverTheme(theme.value), density: density.value, layout: next });
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
        if (id !== undefined) {
          display.value = normalizeDisplay(readPersisted(displayKey, id));
          applyAll();
        }
      }
      if (id !== undefined && id !== previous) {
        void load();
      }
    },
    { immediate: true },
  );

  return { theme, density, display, layout, loadedFor, load, setTheme, setDensity, setDisplay, saveLayout };
});
