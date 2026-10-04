import { defineStore } from "pinia";
import { shallowRef, watch } from "vue";
import type { UserLayout } from "@/features/site/api";
import { useAuthStore } from "./auth";
import { readPersisted, writePersisted } from "./persist";
import { usePreferencesStore } from "./preferences";
import { useSiteAppearanceStore } from "./siteAppearance";

// Layout customization (G33.5): order and visibility of the home page blocks
// and the item page panels, with built-in presets and up to ten named
// presets of the user's own. A signed-in user's layout is part of the
// server preferences (layout member of /api/v1/users/me/preferences), so it
// follows the account to other devices; until the user customizes it, the
// administrator's site default layout applies (GET /api/v1/site/appearance),
// else the built-in standard one. Every change is also cached per account in
// this browser, which is what applies while signed out or when the server
// cannot be read. Nothing is written until the user changes something.

export const homeBlockIds = ["welcome", "libraries", "latest"] as const;
export type HomeBlockId = (typeof homeBlockIds)[number];
export const detailPanelIds = ["overview", "genres", "externalIds", "nfo", "files", "pluginPanels", "pluginTabs"] as const;
export type DetailPanelId = (typeof detailPanelIds)[number];

export interface LayoutEntry<T extends string = string> {
  readonly id: T;
  readonly visible: boolean;
}

export interface Layout {
  readonly home: readonly LayoutEntry<HomeBlockId>[];
  readonly detail: readonly LayoutEntry<DetailPanelId>[];
}

export type LayoutArea = keyof Layout;

export interface CustomPreset {
  readonly id: string;
  readonly name: string;
  readonly layout: Layout;
}

export const builtInPresetIds = ["standard", "focused", "metadata"] as const;
export type BuiltInPresetId = (typeof builtInPresetIds)[number];
export const customPresetLimit = 10;
export const presetNameMaxLength = 40;

const all = <T extends string>(ids: readonly T[], hidden: readonly T[] = []): LayoutEntry<T>[] =>
  ids.map((id) => ({ id, visible: !hidden.includes(id) }));

export const builtInPresets: Readonly<Record<BuiltInPresetId, Layout>> = {
  standard: { home: all(homeBlockIds), detail: all(detailPanelIds) },
  // Straight to the content: libraries first, no greeting, fewer panels.
  focused: {
    home: all<HomeBlockId>(["libraries", "latest", "welcome"], ["welcome"]),
    detail: all<DetailPanelId>(["overview", "files", "pluginTabs", "genres", "externalIds", "nfo", "pluginPanels"], ["externalIds", "nfo"]),
  },
  // Metadata review: identifiers and NFO state before the overview.
  metadata: {
    home: all<HomeBlockId>(["latest", "libraries", "welcome"]),
    detail: all<DetailPanelId>(["externalIds", "nfo", "genres", "overview", "pluginPanels", "pluginTabs", "files"]),
  },
};

/** Moves one entry; out-of-range indexes leave the list unchanged. */
export function moveEntry<T>(list: readonly T[], from: number, to: number): T[] {
  const copy = [...list];
  if (from < 0 || from >= copy.length || to < 0 || to >= copy.length || from === to) {
    return copy;
  }
  const [entry] = copy.splice(from, 1);
  if (entry !== undefined) {
    copy.splice(to, 0, entry);
  }
  return copy;
}

function normalizeArea<T extends string>(raw: unknown, ids: readonly T[]): LayoutEntry<T>[] {
  const entries: LayoutEntry<T>[] = [];
  if (Array.isArray(raw)) {
    for (const entry of raw as unknown[]) {
      if (typeof entry !== "object" || entry === null || !("id" in entry) || !("visible" in entry)) {
        continue;
      }
      const id = ids.find((candidate) => candidate === entry.id);
      if (id !== undefined && typeof entry.visible === "boolean" && !entries.some((known) => known.id === id)) {
        entries.push({ id, visible: entry.visible });
      }
    }
  }
  // Blocks added by a newer version appear, visible, at the end.
  return [...entries, ...ids.filter((id) => !entries.some((entry) => entry.id === id)).map((id) => ({ id, visible: true }))];
}

/** Repairs an untrusted layout: unknown blocks dropped, missing ones appended. */
export function normalizeLayout(raw: unknown): Layout {
  const record = typeof raw === "object" && raw !== null ? (raw as Record<string, unknown>) : {};
  return { home: normalizeArea(record.home, homeBlockIds), detail: normalizeArea(record.detail, detailPanelIds) };
}

function sameLayout(a: Layout, b: Layout): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

interface Stored {
  readonly layout: Layout;
  readonly presets: readonly CustomPreset[];
}

const storageKey = "page-layout";

/** Repairs untrusted presets: invalid entries dropped, at most ten kept. */
function normalizePresets(raw: unknown): CustomPreset[] {
  const presets: CustomPreset[] = [];
  if (Array.isArray(raw)) {
    for (const entry of (raw as unknown[]).slice(0, customPresetLimit)) {
      if (typeof entry !== "object" || entry === null) {
        continue;
      }
      const preset = entry as Record<string, unknown>;
      const name = typeof preset.name === "string" ? preset.name.trim().slice(0, presetNameMaxLength).trim() : "";
      if (typeof preset.id === "string" && /^custom-\d{1,6}$/.test(preset.id) && name !== "" && !presets.some((known) => known.id === preset.id)) {
        presets.push({ id: preset.id, name, layout: normalizeLayout(preset.layout) });
      }
    }
  }
  return presets;
}

/** The browser cache of one account's layout; null when nothing is stored. */
function readStored(scope: string): Stored | null {
  const raw = readPersisted(storageKey, scope);
  if (typeof raw !== "object" || raw === null) {
    return null;
  }
  const record = raw as Record<string, unknown>;
  return { layout: normalizeLayout(record.layout ?? builtInPresets.standard), presets: normalizePresets(record.presets) };
}

/** The server form of a layout (UserLayout). */
export function toUserLayout(layout: Layout, presets: readonly CustomPreset[]): UserLayout {
  const area = <T extends string>(entries: readonly LayoutEntry<T>[]) => entries.map((entry) => ({ id: entry.id, visible: entry.visible }));
  return {
    current: { home: area(layout.home), detail: area(layout.detail) },
    presets: presets.map((preset) => ({ id: preset.id, name: preset.name, layout: { home: area(preset.layout.home), detail: area(preset.layout.detail) } })),
  };
}

export const useLayoutStore = defineStore("layout", () => {
  const auth = useAuthStore();
  const preferences = usePreferencesStore();
  const site = useSiteAppearanceStore();
  const layout = shallowRef<Layout>(builtInPresets.standard);
  const presets = shallowRef<readonly CustomPreset[]>([]);
  /** Where the current layout comes from. */
  const source = shallowRef<"server" | "browser" | "site" | "standard">("standard");
  // One cached layout per account in this browser; "guest" before sign-in.
  let scope = "guest";

  /** The administrator's default layout, or the built-in standard one. */
  function defaultLayout(): Layout {
    const fallback = site.view?.defaultLayout;
    return fallback === null || fallback === undefined ? builtInPresets.standard : normalizeLayout(fallback);
  }

  function load(): void {
    scope = auth.user?.id ?? "guest";
    const server = auth.user !== null && preferences.loadedFor === auth.user.id ? preferences.layout : null;
    if (server !== null && typeof server === "object") {
      layout.value = normalizeLayout(server.current);
      presets.value = normalizePresets(server.presets);
      source.value = "server";
      return;
    }
    const stored = readStored(scope);
    if (stored !== null) {
      layout.value = stored.layout;
      presets.value = stored.presets;
      source.value = "browser";
      return;
    }
    layout.value = defaultLayout();
    presets.value = [];
    source.value = site.view?.defaultLayout ? "site" : "standard";
  }

  function persist(): void {
    writePersisted(storageKey, { layout: layout.value, presets: presets.value }, scope);
    source.value = "browser";
    // The account copy; while signed out or before the preferences were
    // read the browser copy is all there is. A failed save keeps it too.
    preferences.saveLayout(toUserLayout(layout.value, presets.value)).then(
      (saved) => {
        if (saved) {
          source.value = "server";
        }
      },
      () => undefined,
    );
  }

  watch(() => [auth.user?.id, preferences.loadedFor, preferences.layout, site.view?.defaultLayout], load, { immediate: true });

  /** Preset the current layout equals, or null after individual changes. */
  function activePreset(): string | null {
    for (const id of builtInPresetIds) {
      if (sameLayout(layout.value, builtInPresets[id])) {
        return id;
      }
    }
    return presets.value.find((preset) => sameLayout(layout.value, preset.layout))?.id ?? null;
  }

  function update(next: Layout): void {
    layout.value = next;
    persist();
  }

  function move(area: LayoutArea, from: number, to: number): void {
    const current = layout.value;
    update(area === "home" ? { ...current, home: moveEntry(current.home, from, to) } : { ...current, detail: moveEntry(current.detail, from, to) });
  }

  function setVisible(area: LayoutArea, id: string, visible: boolean): void {
    const current = layout.value;
    const toggle = <T extends string>(entries: readonly LayoutEntry<T>[]) => entries.map((entry) => (entry.id === id ? { ...entry, visible } : entry));
    update(area === "home" ? { ...current, home: toggle(current.home) } : { ...current, detail: toggle(current.detail) });
  }

  function applyPreset(id: string): boolean {
    const builtIn = builtInPresetIds.find((candidate) => candidate === id);
    const preset = builtIn !== undefined ? builtInPresets[builtIn] : presets.value.find((entry) => entry.id === id)?.layout;
    if (preset === undefined) {
      return false;
    }
    update(preset);
    return true;
  }

  /** Saves the current layout under a name; returns the new preset's ID or null when full or unnamed. */
  function savePreset(name: string): string | null {
    const trimmed = name.trim().slice(0, presetNameMaxLength);
    if (trimmed === "" || presets.value.length >= customPresetLimit) {
      return null;
    }
    const used = new Set(presets.value.map((preset) => preset.id));
    let n = 1;
    while (used.has(`custom-${n}`)) {
      n++;
    }
    const id = `custom-${n}`;
    presets.value = [...presets.value, { id, name: trimmed, layout: layout.value }];
    persist();
    return id;
  }

  function deletePreset(id: string): void {
    presets.value = presets.value.filter((preset) => preset.id !== id);
    persist();
  }

  function visibleIds<T extends LayoutArea>(area: T): Layout[T][number]["id"][] {
    return layout.value[area].filter((entry) => entry.visible).map((entry) => entry.id);
  }

  /** Restores the site default layout (else the standard one); presets stay. */
  function reset(): void {
    update(defaultLayout());
  }

  return { layout, presets, source, activePreset, move, setVisible, applyPreset, savePreset, deletePreset, visibleIds, reset };
});
