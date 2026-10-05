import { flushPromises, type VueWrapper } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "@/api/schema";
import en from "@/i18n/en-US/versions.json";
import { useToastStore } from "@/stores/toasts";
import { mountView, unmountAll } from "@/test/mountView";
import { adminUser, apiError, createRouteFetch, data, expectNoPlaybackMarkup, regularUser, type RouteFetch } from "@/test/routeFetch";
import { emptyTrackForm, fromTrackForm, toTrackForm } from "./versions";

type MediaSourceInfo = components["schemas"]["MediaSourceInfo"];
type VersionOverview = components["schemas"]["VersionOverview"];

const text = en.versions;
const itemId = "30000000-0000-4000-8000-000000000001";
const low = "40000000-0000-4000-8000-000000000001";
const high = "40000000-0000-4000-8000-000000000002";
const operationId = "50000000-0000-4000-8000-000000000001";
const exclusionId = "60000000-0000-4000-8000-000000000001";

function source(id: string, name: string, primary: boolean): MediaSourceInfo {
  return {
    id,
    primary,
    container: "mkv",
    contentType: "video/x-matroska",
    probed: true,
    version: { displayName: name, qualityScore: 1 },
    videoTracks: [],
    audioTracks: [{ index: 1, codec: "eac3", language: "eng", default: true, forced: false, atmos: false }],
    subtitleTracks: [{ index: 2, codec: "subrip", format: "srt", language: "jpn", default: false, forced: false }],
    externalTracks: [{ id: "70000000-0000-4000-8000-000000000001", kind: "audio", format: "ac3", language: "en", commentary: true, forced: false, sdh: false, default: false, sizeBytes: 1 }],
  };
}

const overview: VersionOverview = {
  itemId,
  exclusions: [{ id: exclusionId, itemId, fileName: "Film.Trailer.mkv", createdAt: "2026-10-01T00:00:00Z" }],
  operations: [
    { id: operationId, libraryId: itemId, kind: "split", itemId, sourceIds: [high], createdAt: "2026-10-02T00:00:00Z", undoUntil: "2026-11-01T00:00:00Z", undoable: true },
    { id: exclusionId, libraryId: itemId, kind: "merge", itemId, sourceIds: [], createdAt: "2026-10-01T00:00:00Z", undoUntil: "2026-10-31T00:00:00Z", undoable: false, undoBlocked: "later_operation" },
  ],
};

function routes(): RouteFetch {
  return createRouteFetch()
    .on("GET", "/api/v1/libraries", () => data({ libraries: [], pagination: { nextCursor: "", limit: 50 } }))
    .on("GET", "/api/v1/items/:id/details", () =>
      data({ id: itemId, libraryId: itemId, kind: "Movie", title: "Heat", genres: [], externalIds: [], nfo: { status: "unread", fields: [] } }),
    )
    .on("GET", "/api/v1/items/:id/sources", () => data({ itemId, sources: [source(low, "1080p", true), source(high, "2160p", false)] }))
    .on("GET", "/api/v1/items/:id/versions", () => data(overview))
    .on("GET", "/api/v1/items/:id/track-preferences", () => data({ itemId, user: null, item: { audioLanguage: "ja", subtitleMode: "off" }, versions: [] }))
    .on("PUT", "/api/v1/items/:id/track-preferences", ({ body }) => data({ itemId, user: null, item: null, versions: [{ sourceId: high, preference: body }] }))
    .on("POST", "/api/v1/items/:id/versions/split", () => data({ ...overview.operations[0] }, 201))
    .on("POST", "/api/v1/items/:id/versions/merge", () => apiError(409, "version_identity_conflict"))
    .on("PUT", "/api/v1/items/:id/versions/primary", () => data({ ...overview.operations[0], kind: "primary" }))
    .on("DELETE", "/api/v1/items/:id/versions/exclusions/:exclusion", () => data({ ...overview.operations[0], kind: "unexclude" }))
    .on("POST", "/api/v1/version-operations/:id/undo", () => data({ ...overview.operations[0], undoable: false }));
}

async function open(fetch: RouteFetch, admin = true): Promise<VueWrapper> {
  const { wrapper } = await mountView("/items/" + itemId, { fetch: fetch.fetch, user: admin ? adminUser : regularUser });
  // The panels are lazy chunks: wait for their imports, then their reads.
  for (let i = 0; i < 3; i++) {
    await flushPromises();
    await vi.dynamicImportSettled();
  }
  await flushPromises();
  return wrapper;
}

async function confirmIn(wrapper: VueWrapper, rowId: string, label: string) {
  const row = wrapper.find("#" + rowId);
  const trigger = row.findAll("button").find((button) => button.text() === label);
  if (!trigger) {
    throw new Error("no button " + label + " in " + rowId);
  }
  await trigger.trigger("click");
  const confirm = row.find("[data-confirm]");
  await confirm.trigger("click");
  await flushPromises();
}

afterEach(unmountAll);

describe("item versions panel", () => {
  it("needs a second press for every decision and reports server refusals", async () => {
    const fetch = routes();
    const wrapper = await open(fetch);
    expect(wrapper.text()).toContain(text.panel.title);
    expect(wrapper.text()).toContain(text.panel.main);
    expect(wrapper.text()).toContain("Film.Trailer.mkv");
    expect(wrapper.text()).toContain(text.panel.blockedLater);
    expectNoPlaybackMarkup(wrapper.html());

    // One press only opens the confirmation.
    const splitButton = wrapper.find("#version-" + high).findAll("button").find((button) => button.text() === text.panel.split);
    await splitButton!.trigger("click");
    expect(fetch.calls("POST", `/api/v1/items/${itemId}/versions/split`)).toHaveLength(0);
    await wrapper.find("#version-" + high).find("[data-confirm]").trigger("click");
    await flushPromises();
    expect(fetch.calls("POST", `/api/v1/items/${itemId}/versions/split`)[0]!.body).toEqual({ sourceId: high, exclude: true });

    await confirmIn(wrapper, "version-" + high, text.panel.makeMain);
    expect(fetch.calls("PUT", `/api/v1/items/${itemId}/versions/primary`)[0]!.body).toEqual({ sourceId: high });
    await confirmIn(wrapper, "version-" + low, text.panel.clearMain);
    expect(fetch.calls("PUT", `/api/v1/items/${itemId}/versions/primary`)[1]!.body).toEqual({ sourceId: null });
    await confirmIn(wrapper, "exclusion-" + exclusionId, text.panel.lift);
    expect(fetch.calls("DELETE", `/api/v1/items/${itemId}/versions/exclusions/${exclusionId}`)).toHaveLength(1);
    await confirmIn(wrapper, "operation-" + operationId, text.panel.undo);
    expect(fetch.calls("POST", `/api/v1/version-operations/${operationId}/undo`)).toHaveLength(1);

    // Merge: the ID is checked before the request; a refusal is localized.
    const field = wrapper.find(".jl-versions__merge input");
    await field.setValue("not-an-id");
    expect(wrapper.text()).toContain(text.panel.mergeInvalid);
    await field.setValue("30000000-0000-4000-8000-000000000002");
    const merge = wrapper.find(".jl-versions__merge").findAll("button").find((button) => button.text() === text.panel.merge);
    await merge!.trigger("click");
    await wrapper.find(".jl-versions__merge [data-confirm]").trigger("click");
    await flushPromises();
    expect(fetch.calls("POST", `/api/v1/items/${itemId}/versions/merge`)[0]!.body).toEqual({ sourceItemId: "30000000-0000-4000-8000-000000000002" });
    expect(useToastStore().toasts.map((toast) => toast.key)).toContain("versions.errors.identity");
  });

  it("is not shown to other users, who still set their track preferences", async () => {
    const fetch = routes();
    const wrapper = await open(fetch, false);
    expect(wrapper.text()).not.toContain(text.panel.title);
    expect(fetch.calls("GET", `/api/v1/items/${itemId}/versions`)).toHaveLength(0);
    expect(wrapper.text()).toContain(text.tracks.title);
  });
});

describe("track preferences panel", () => {
  it("edits the item level and one version with named tracks", async () => {
    const fetch = routes();
    const wrapper = await open(fetch, false);
    const panel = wrapper.find(".jl-tracks");
    const inputs = panel.findAll("input");
    expect((inputs[0]!.element as HTMLInputElement).value).toBe("ja");
    // Named tracks only appear for one version.
    expect(panel.text()).not.toContain(text.tracks.track);
    await panel.find("select").setValue(high);
    expect(panel.text()).toContain(text.tracks.track);
    const selects = panel.findAll("select");
    await selects[2]!.setValue("x:70000000-0000-4000-8000-000000000001");
    await selects[3]!.setValue("always");
    await panel.findAll("input")[1]!.setValue("zh-TW");
    await panel.find("form").trigger("submit");
    await flushPromises();
    expect(fetch.calls("PUT", `/api/v1/items/${itemId}/track-preferences`)[0]!.body).toEqual({
      sourceId: high,
      audioLanguage: null,
      audioCommentary: null,
      audioTrack: "x:70000000-0000-4000-8000-000000000001",
      subtitleMode: "always",
      subtitleLanguage: "zh-TW",
      subtitleSdh: null,
      subtitleTrack: null,
    });
    expect(useToastStore().toasts.map((toast) => toast.key)).toContain("versions.tracks.saved");
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("maps form values to inheriting and explicit members", () => {
    const form = toTrackForm({ audioCommentary: false, subtitleSdh: true, subtitleMode: "forced", audioTrack: "e:1" });
    expect(form.audioCommentary).toBe("no");
    expect(form.subtitleSdh).toBe("yes");
    expect(fromTrackForm(form, false).audioTrack).toBeNull();
    expect(fromTrackForm(form, true).audioTrack).toBe("e:1");
    expect(fromTrackForm(emptyTrackForm(), true)).toEqual({
      audioLanguage: null,
      audioCommentary: null,
      audioTrack: null,
      subtitleMode: null,
      subtitleLanguage: null,
      subtitleSdh: null,
      subtitleTrack: null,
    });
    expect(toTrackForm({ subtitleMode: "sometimes" as never }).subtitleMode).toBe("");
  });
});
