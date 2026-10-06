// Restricted time windows of one account (G48.7): pure helpers between the
// edit form and the request body. The server validates everything again,
// including whether it knows the time zone.
import type { components } from "@/api/schema";

export type AccessWindow = components["schemas"]["AccessWindow"];

/** Limits the server enforces. */
export const maxWindows = 20;
export const timeZoneMaxBytes = 64;
/** Weekday numbers, 0 is Sunday. */
export const weekdays = [0, 1, 2, 3, 4, 5, 6] as const;

/** Inside the window: hide every item, or apply a rating ceiling. */
export type WindowMode = "hide" | "ceiling";

export interface WindowDraft {
  /** Local key for list rendering; never sent. */
  readonly key: number;
  weekdays: number[];
  start: string;
  end: string;
  timeZone: string;
  mode: WindowMode;
  /** Ceiling select value; "" until one is chosen. */
  ratingMax: string;
}

let nextKey = 0;

/** The browser's IANA time zone, or UTC when it reports none usable. */
export function browserTimeZone(): string {
  try {
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    return typeof zone === "string" && zone !== "" && zone !== "Local" ? zone : "UTC";
  } catch {
    return "UTC";
  }
}

export function windowDraftOf(window: AccessWindow): WindowDraft {
  return {
    key: ++nextKey,
    weekdays: [...(window.weekdays ?? [])].sort((a, b) => a - b),
    start: window.start,
    end: window.end,
    timeZone: window.timeZone,
    mode: window.ratingMax === undefined ? "hide" : "ceiling",
    ratingMax: window.ratingMax === undefined ? "" : String(window.ratingMax),
  };
}

/** A new window: every day, 22:00 to 06:00 in the browser's zone, everything hidden. */
export function newWindowDraft(timeZone: string = browserTimeZone()): WindowDraft {
  return { key: ++nextKey, weekdays: [], start: "22:00", end: "06:00", timeZone, mode: "hide", ratingMax: "" };
}

/** Minutes after midnight of an HH:MM time, or null; 24:00 only as an end. */
export function clockMinutes(value: string, end: boolean): number | null {
  const match = /^(\d\d):(\d\d)$/.exec(value);
  if (match === null) {
    return null;
  }
  const hours = Number(match[1]);
  const minutes = Number(match[2]);
  if (minutes > 59 || hours > 24 || (hours === 24 && (minutes !== 0 || !end))) {
    return null;
  }
  return hours * 60 + minutes;
}

/** Catalog keys of the problems of one window, by field. */
export interface WindowProblems {
  start: string | null;
  end: string | null;
  timeZone: string | null;
  ratingMax: string | null;
}

export function windowProblems(draft: WindowDraft): WindowProblems {
  const start = clockMinutes(draft.start.trim(), false);
  const end = clockMinutes(draft.end.trim(), true);
  const zone = draft.timeZone.trim();
  return {
    start: start === null ? "contentRules.windows.badStart" : null,
    end: end === null ? "contentRules.windows.badEnd" : start !== null && start === end ? "contentRules.windows.sameTimes" : null,
    timeZone:
      zone === "" || zone === "Local" || new TextEncoder().encode(zone).length > timeZoneMaxBytes ? "contentRules.windows.badZone" : null,
    ratingMax: draft.mode === "ceiling" && draft.ratingMax === "" ? "contentRules.windows.ceilingRequired" : null,
  };
}

export function windowValid(draft: WindowDraft): boolean {
  return Object.values(windowProblems(draft)).every((problem) => problem === null);
}

/** One window as the request sends it: days sorted, an empty day list and a hide-all ceiling omitted. */
export function windowBody(draft: WindowDraft): AccessWindow {
  const days = [...new Set(draft.weekdays)].sort((a, b) => a - b);
  return {
    ...(days.length > 0 && days.length < 7 ? { weekdays: days } : {}),
    start: draft.start.trim(),
    end: draft.end.trim(),
    timeZone: draft.timeZone.trim(),
    ...(draft.mode === "ceiling" && draft.ratingMax !== "" ? { ratingMax: Number(draft.ratingMax) } : {}),
  };
}

export function windowsBody(drafts: readonly WindowDraft[]): AccessWindow[] {
  return drafts.map(windowBody);
}

/** The stored windows in the same normalized shape, to detect unsaved changes. */
export function windowsOf(windows: readonly AccessWindow[]): AccessWindow[] {
  return windowsBody(windows.map(windowDraftOf));
}
