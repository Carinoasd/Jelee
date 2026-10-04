import { defineStore } from "pinia";
import { shallowRef, watch } from "vue";
import { useAuthStore } from "./auth";
import { readPersisted, writePersisted } from "./persist";

// Layout customization (G33.5): order and visibility of the home page blocks
// and the item page panels, with built-in presets and up to ten named
// presets of the user's own. The server's preferences schema has no layout
// field yet, so layouts are kept per account in this browser (see
// docs/frontend-adr.md); nothing is written until the user changes something.

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

function readStored(scope: string): Stored {
  const raw = readPersisted(storageKey, scope);
  const record = typeof raw === "object" && raw !== null ? (raw as Record<string, unknown>) : {};
  const presets: CustomPreset[] = [];
  if (Array.isArray(record.presets)) {
    for (const entry of (record.presets as unknown[]).slice(0, customPresetLimit)) {
      if (typeof entry !== "object" || entry === null) {
        continue;
      }
      const preset = entry as Record<string, unknown>;
      if (typeof preset.id === "string" && /^custom-\d{1,6}$/.test(preset.id) && typeof preset.name === "string" && preset.name.trim() !== "") {
        presets.push({ id: preset.id, name: preset.name.slice(0, presetNameMaxLength), layout: normalizeLayout(preset.layout) });
      }
    }
  }
  return { layout: normalizeLayout(record.layout ?? builtInPresets.standard), presets };
}

export const useLayoutStore = defineStore("layout", () => {
  const auth = useAuthStore();
  const layout = shallowRef<Layout>(builtInPresets.standard);
  const presets = shallowRef<readonly CustomPreset[]>([]);
  // One layout per account in this browser; "guest" before sign-in.
  let scope = "guest";

  function load(): void {
    scope = auth.user?.id ?? "guest";
    const stored = readStored(scope);
    layout.value = stored.layout;
    presets.value = stored.presets;
  }

  function persist(): void {
    writePersisted(storageKey, { layout: layout.value, presets: presets.value }, scope);
  }

  watch(() => auth.user?.id, load, { immediate: true });

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

  function reset(): void {
    update(builtInPresets.standard);
  }

  return { layout, presets, activePreset, move, setVisible, applyPreset, savePreset, deletePreset, visibleIds, reset };
});
