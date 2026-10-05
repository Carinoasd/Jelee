import type { ApiClient } from "@/api/client";
import { call } from "@/api/call";
import type { components } from "@/api/schema";

// Version decisions (G20.3, G20.5) and track preferences (G16.5, G20.4).
// Preferences only choose which original track a native client starts with;
// the web client never plays anything (G27).

export type VersionOverview = components["schemas"]["VersionOverview"];
export type VersionOperation = components["schemas"]["VersionOperation"];
export type TrackPreference = components["schemas"]["TrackPreference"];
export type TrackPreferences = components["schemas"]["TrackPreferences"];

/** Administrator view of an item's versions: main version, exclusions, operations. */
export async function getVersionOverview(client: ApiClient, itemId: string): Promise<VersionOverview> {
  const body = await call(client.GET("/api/v1/items/{id}/versions", { params: { path: { id: itemId } } }));
  return body.data;
}

/** Moves one version into a new item; exclude keeps synchronisation from regrouping it. */
export async function splitVersion(client: ApiClient, itemId: string, sourceId: string, exclude: boolean): Promise<VersionOperation> {
  const body = await call(client.POST("/api/v1/items/{id}/versions/split", { params: { path: { id: itemId } }, body: { sourceId, exclude } }));
  return body.data;
}

/** Absorbs another item into this one. */
export async function mergeItem(client: ApiClient, itemId: string, sourceItemId: string): Promise<VersionOperation> {
  const body = await call(client.POST("/api/v1/items/{id}/versions/merge", { params: { path: { id: itemId } }, body: { sourceItemId } }));
  return body.data;
}

/** Chooses the main version; null clears it. */
export async function setMainVersion(client: ApiClient, itemId: string, sourceId: string | null): Promise<VersionOperation> {
  const body = await call(client.PUT("/api/v1/items/{id}/versions/primary", { params: { path: { id: itemId } }, body: { sourceId } }));
  return body.data;
}

/** Lets synchronisation group an excluded file into the item again. */
export async function liftExclusion(client: ApiClient, itemId: string, exclusionId: string): Promise<VersionOperation> {
  const body = await call(client.DELETE("/api/v1/items/{id}/versions/exclusions/{exclusionId}", { params: { path: { id: itemId, exclusionId } } }));
  return body.data;
}

/** Reverses one operation. */
export async function undoOperation(client: ApiClient, operationId: string): Promise<VersionOperation> {
  const body = await call(client.POST("/api/v1/version-operations/{id}/undo", { params: { path: { id: operationId } }, body: {} }));
  return body.data;
}

/** The caller's own preferences for one item. */
export async function getTrackPreferences(client: ApiClient, itemId: string): Promise<TrackPreferences> {
  const body = await call(client.GET("/api/v1/items/{id}/track-preferences", { params: { path: { id: itemId } } }));
  return body.data;
}

/** Replaces the item level (sourceId null) or one version level. */
export async function putTrackPreference(client: ApiClient, itemId: string, sourceId: string | null, preference: TrackPreference): Promise<TrackPreferences> {
  const body = await call(client.PUT("/api/v1/items/{id}/track-preferences", { params: { path: { id: itemId } }, body: { ...preference, sourceId } }));
  return body.data;
}

/** Subtitle modes in the order the form offers them. */
export const subtitleModes = ["auto", "always", "forced", "off"] as const;

/** Form state of one level; empty strings inherit. */
export interface TrackForm {
  audioLanguage: string;
  /** "" inherits, "yes" prefers commentary, "no" avoids it. */
  audioCommentary: "" | "yes" | "no";
  audioTrack: string;
  subtitleMode: "" | (typeof subtitleModes)[number];
  subtitleLanguage: string;
  subtitleSdh: "" | "yes" | "no";
  subtitleTrack: string;
}

export function emptyTrackForm(): TrackForm {
  return { audioLanguage: "", audioCommentary: "", audioTrack: "", subtitleMode: "", subtitleLanguage: "", subtitleSdh: "", subtitleTrack: "" };
}

const tri = (value: boolean | null | undefined): "" | "yes" | "no" => (value === true ? "yes" : value === false ? "no" : "");
const fromTri = (value: "" | "yes" | "no"): boolean | null => (value === "" ? null : value === "yes");
const text = (value: string): string | null => (value.trim() === "" ? null : value.trim());

/** Fills the form from a stored level; absent members inherit. */
export function toTrackForm(preference: TrackPreference | null | undefined): TrackForm {
  const form = emptyTrackForm();
  if (!preference) {
    return form;
  }
  const mode = preference.subtitleMode;
  return {
    audioLanguage: preference.audioLanguage ?? "",
    audioCommentary: tri(preference.audioCommentary),
    audioTrack: preference.audioTrack ?? "",
    subtitleMode: mode === "auto" || mode === "always" || mode === "forced" || mode === "off" ? mode : "",
    subtitleLanguage: preference.subtitleLanguage ?? "",
    subtitleSdh: tri(preference.subtitleSdh),
    subtitleTrack: preference.subtitleTrack ?? "",
  };
}

/** Builds the request body; named tracks only for a version level. */
export function fromTrackForm(form: TrackForm, version: boolean): TrackPreference {
  return {
    audioLanguage: text(form.audioLanguage),
    audioCommentary: fromTri(form.audioCommentary),
    audioTrack: version ? text(form.audioTrack) : null,
    subtitleMode: form.subtitleMode === "" ? null : form.subtitleMode,
    subtitleLanguage: text(form.subtitleLanguage),
    subtitleSdh: fromTri(form.subtitleSdh),
    subtitleTrack: version ? text(form.subtitleTrack) : null,
  };
}
