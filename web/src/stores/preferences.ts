import { defineStore } from "pinia";
import { computed, shallowRef, watch } from "vue";
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
import { readPersisted, removePersisted } from "./persist";
import { useSiteAppearanceStore } from "./siteAppearance";

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

/** Display choices as applied: every member resolved (G33.2). */
export interface DisplayPreferences {
  /** A preset beyond plain light and dark; null follows the stored theme. */
  readonly palette: PaletteId | null;
  readonly posterRatio: PosterRatio;
  readonly posterSize: PosterSize;
  readonly listDensity: ListDensity;
  readonly view: ItemView;
}

/** One layer of display choices (the user's or the site default); null members are unset. */
export type DisplayChoices = { readonly [K in keyof DisplayPreferences]: DisplayPreferences[K] | null };

/** The built-in values, the last layer. */
export const defaultDisplay: DisplayPreferences = { palette: null, posterRatio: "portrait", posterSize: "medium", listDensity: "comfortable", view: "poster" };
export const noDisplayChoices: DisplayChoices = { palette: null, posterRatio: null, posterSize: null, listDensity: null, view: null };

const pick = <T extends string>(value: unknown, allowed: readonly T[]): T | null =>
  typeof value === "string" && (allowed as readonly string[]).includes(value) ? (value as T) : null;

/** Reads one layer from untrusted input (server or storage); unknown values are unset. */
export function normalizeChoices(raw: unknown): DisplayChoices {
  const record = typeof raw === "object" && raw !== null ? (raw as Record<string, unknown>) : {};
  return {
    palette: pick<PaletteId>(record.palette, extraPaletteIds),
    posterRatio: pick(record.posterRatio, posterRatioIds),
    posterSize: pick(record.posterSize, posterSizeIds),
    listDensity: pick(record.listDensity, listDensityIds),
    view: pick(record.view, itemViews),
  };
}

/**
 * The applied display choices, member by member: the user's choice, then the
 * site default, then the built-in value. The palette is the user's only: it
 * goes with the theme, and the server already answers a user who never saved
 * preferences with the site default theme and its preset.
 */
export function resolveDisplay(user: DisplayChoices, site: DisplayChoices | null): DisplayPreferences {
  const fallback = site ?? noDisplayChoices;
  return {
    palette: user.palette,
    posterRatio: user.posterRatio ?? fallback.posterRatio ?? defaultDisplay.posterRatio,
    posterSize: user.posterSize ?? fallback.posterSize ?? defaultDisplay.posterSize,
    listDensity: user.listDensity ?? fallback.listDensity ?? defaultDisplay.listDensity,
    view: user.view ?? fallback.view ?? defaultDisplay.view,
  };
}

/** Reads display choices from untrusted storage; unknown values fall back to the defaults. */
export function normalizeDisplay(raw: unknown): DisplayPreferences {
  return resolveDisplay(normalizeChoices(raw), null);
}

type ServerDisplay = NonNullable<UserPreferences["display"]>;

/** The server's display member: null when no member is set. */
export function toServerDisplay(choices: DisplayChoices): ServerDisplay | null {
  if (Object.values(choices).every((value) => value === null)) {
    return null;
  }
  // normalizeChoices only ever yields the presets beyond light and dark.
  return { ...choices, palette: choices.palette as ServerDisplay["palette"] };
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
 * density, the page layout and the display choices (preset, poster ratio and
 * size, list density, default view); every replacement carries all members,
 * so a theme change keeps the layout and a layout change keeps the theme.
 * `theme` keeps the preset's scheme so older clients still follow it; the
 * display member's palette wins when both are present. Each display member
 * the user did not choose follows the site default display (GET
 * /api/v1/site/appearance), then the built-in value, so a later site change
 * still reaches it (resolveDisplay). Display choices an earlier version kept
 * per account in this browser are sent to the server the first time the
 * account loads without any, and the local copy is removed once that succeeds.
 */
export const usePreferencesStore = defineStore("preferences", () => {
  const { client } = useApi();
  const auth = useAuthStore();
  const theme = shallowRef<Theme>("system");
  const density = shallowRef<Density>("comfortable");
  const site = useSiteAppearanceStore();
  /** The account's own display choices; unset members follow the site default. */
  const choices = shallowRef<DisplayChoices>(noDisplayChoices);
  const siteDisplay = computed(() => (site.view?.defaultDisplay == null ? null : normalizeChoices(site.view.defaultDisplay)));
  const display = computed(() => resolveDisplay(choices.value, siteDisplay.value));
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
    // An older server answers without the member: nothing chosen.
    choices.value = normalizeChoices(next.display);
    theme.value = choices.value.palette ?? next.theme;
    density.value = next.density;
    layout.value = next.layout ?? null;
    applyAll();
  }

  function documentOf(storedTheme: ServerTheme, storedDensity: Density, storedLayout: UserLayout | null): UserPreferences {
    return { theme: storedTheme, density: storedDensity, layout: storedLayout, display: toServerDisplay(choices.value) };
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
        if (stored.display == null) {
          await migrateLocalDisplay(user.id, current);
        } else {
          removePersisted(displayKey, user.id);
        }
      }
    } catch {
      // Presentation only: keep what the tab shows now.
    }
  }

  /**
   * Sends display choices an earlier version kept in this browser to the
   * account, then drops the local copy. A preset only counts while the stored
   * theme still names its scheme, as before. A failure keeps the copy for the
   * next load.
   */
  async function migrateLocalDisplay(userId: string, from: number): Promise<void> {
    const legacy = readPersisted(displayKey, userId);
    if (typeof legacy !== "object" || legacy === null) {
      return;
    }
    const local = normalizeChoices(legacy);
    const palette = local.palette !== null && palettes[local.palette].scheme === serverTheme(theme.value) ? local.palette : null;
    choices.value = { ...local, palette };
    theme.value = palette ?? theme.value;
    applyAll();
    try {
      const saved = await savePreferences(client, documentOf(serverTheme(theme.value), density.value, layout.value));
      removePersisted(displayKey, userId);
      if (from === generation) {
        apply(saved);
      }
    } catch {
      // Presentation only: the local copy stays for the next load.
    }
  }

  async function store(next: Pick<UserPreferences, "theme" | "density">): Promise<void> {
    const current = ++generation;
    if (auth.user === null) {
      return;
    }
    const saved = await savePreferences(client, documentOf(next.theme, next.density, layout.value));
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
    const palette = next === "system" || next === "light" || next === "dark" ? null : next;
    choices.value = { ...choices.value, palette };
    applyAll();
    await store({ theme: serverTheme(next), density: density.value });
  }

  /** Applies and stores the density like setTheme. */
  async function setDensity(next: Density): Promise<void> {
    density.value = next;
    applyAll();
    await store({ theme: serverTheme(theme.value), density: next });
  }

  /** Applies display choices at once and stores them like setTheme; the other members keep following the site default. */
  async function setDisplay(next: Partial<Omit<DisplayPreferences, "palette">>): Promise<void> {
    choices.value = { ...choices.value, ...next };
    applyAll();
    await store({ theme: serverTheme(theme.value), density: density.value });
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
    const saved = await savePreferences(client, documentOf(serverTheme(theme.value), density.value, next));
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
          // Signing in as another account starts from the defaults until its own are read.
          choices.value = noDisplayChoices;
          applyAll();
        }
      }
      if (id !== undefined && id !== previous) {
        void load();
      }
    },
    { immediate: true },
  );

  // The site default arrives on its own schedule (stores/siteAppearance.ts).
  watch(siteDisplay, applyAll);

  return { theme, density, display, choices, layout, loadedFor, load, setTheme, setDensity, setDisplay, saveLayout };
});
