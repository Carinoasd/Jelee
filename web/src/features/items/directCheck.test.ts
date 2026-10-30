import { flushPromises, type VueWrapper } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "@/api/schema";
import en from "@/i18n/en-US/versions.json";
import { mountView, unmountAll } from "@/test/mountView";
import { apiError, createRouteFetch, data, expectNoPlaybackMarkup, regularUser, type RouteFetch } from "@/test/routeFetch";
import { emptyCustomForm, parseTokens, toCapabilities, trackRows } from "./directCheck";

type MediaSourceInfo = components["schemas"]["MediaSourceInfo"];

const text = en.versions.check;
const itemId = "30000000-0000-4000-8000-000000000001";
const sourceId = "40000000-0000-4000-8000-000000000001";
const externalId = "70000000-0000-4000-8000-000000000001";

const source: MediaSourceInfo = {
  id: sourceId,
  primary: true,
  container: "mp4",
  contentType: "video/mp4",
  probed: true,
  layout: "moov_at_end",
  layoutHints: ["faststart_recommended"],
  version: { displayName: "1080p", qualityScore: 1 },
  videoTracks: [],
  audioTracks: [{ index: 1, codec: "dts", language: "eng", default: true, forced: false, atmos: false, commentary: false, visualImpaired: false }],
  subtitleTracks: [{ index: 2, codec: "hdmv_pgs_subtitle", format: "pgs", language: "jpn", default: false, forced: false, sdh: false }],
  externalTracks: [{ id: externalId, kind: "subtitle", format: "srt", language: "zh", forced: false, sdh: false, default: false, commentary: false, sizeBytes: 5, probeState: "pending" }],
};

const decision: components["schemas"]["PlaybackDecision"] = {
  sourceId,
  directPlay: false,
  code: "direct_play_unsupported",
  reasons: ["container_unsupported", "audio_codec_unsupported"],
  tracks: [
    { kind: "audio", index: 1, external: false, supported: false, code: "direct_play_unsupported", reason: "audio_codec_unsupported" },
    { kind: "subtitle", index: 2, external: false, supported: false, code: "direct_play_unsupported", reason: "subtitle_format_unsupported" },
    { kind: "subtitle", id: externalId, external: true, supported: false, code: "direct_play_unsupported", reason: "track_not_probed" },
  ],
};

function routes(): RouteFetch {
  return createRouteFetch()
    .on("GET", "/api/v1/libraries", () => data({ libraries: [], pagination: { nextCursor: "", limit: 50 } }))
    .on("GET", "/api/v1/items/:id/details", () =>
      data({ id: itemId, libraryId: itemId, kind: "Movie", title: "Heat", genres: [], externalIds: [], nfo: { status: "unread", fields: [] } }),
    )
    .on("GET", "/api/v1/items/:id/sources", () => data({ itemId, sources: [source] }))
    .on("GET", "/api/v1/items/:id/track-preferences", () => data({ itemId, user: null, item: null, versions: [] }))
    .on("POST", "/api/v1/items/:id/sources/check", () => data({ itemId, directPlayable: false, decisions: [decision], sources: [source], selection: { reason: "none_playable", preferences: [], excluded: [] } }));
}

async function open(fetch: RouteFetch): Promise<VueWrapper> {
  const { wrapper } = await mountView("/items/" + itemId, { fetch: fetch.fetch, user: regularUser });
  for (let i = 0; i < 3; i++) {
    await flushPromises();
    await vi.dynamicImportSettled();
  }
  await flushPromises();
  return wrapper;
}

afterEach(unmountAll);

describe("direct play check", () => {
  it("explains each version, layout and track for a preset without offering playback", async () => {
    const fetch = routes();
    const wrapper = await open(fetch);
    expect(wrapper.text()).toContain(text.title);
    await wrapper.find("[data-testid=direct-check] button").trigger("click");
    await flushPromises();
    const request = fetch.calls("POST", `/api/v1/items/${itemId}/sources/check`);
    expect(request).toHaveLength(1);
    expect(request[0]!.body).toMatchObject({ containers: ["mp4", "webm"], videoCodecs: ["h264"], audioCodecs: ["aac"] });

    const panel = wrapper.find("[data-testid=direct-check]");
    expect(panel.text()).toContain(text.notDirect);
    expect(panel.find("[data-testid=check-reasons]").text()).toContain(text.reasonContainer);
    expect(panel.find("[data-testid=check-reasons]").text()).toContain(text.reasonAudio);
    expect(panel.find("[data-testid=check-layout]").text()).toContain(text.layoutMoovAtEnd);
    expect(panel.find("[data-testid=check-hint]").exists()).toBe(true);
    const tracks = panel.find("[data-testid=check-tracks]");
    expect(tracks.findAll("li")).toHaveLength(3);
    expect(tracks.text()).toContain(text.trackReasonAudio);
    expect(tracks.text()).toContain(text.trackReasonSubtitle);
    expect(tracks.find("[data-delivery=unprobed]").text()).toContain(text.deliveryUnprobed);
    expectNoPlaybackMarkup(wrapper.html());
  });

  it("validates a custom declaration before asking and localizes a refusal", async () => {
    const fetch = routes();
    fetch.on("POST", "/api/v1/items/:id/sources/check", () => apiError(409, "transcode_disabled"));
    const wrapper = await open(fetch);
    await wrapper.find("[data-testid=direct-check] select").setValue("custom");
    const fields = wrapper.findAll("[data-testid=direct-check] input");
    await fields[0]!.setValue("mp4 bad/token");
    await wrapper.find("[data-testid=direct-check] button").trigger("click");
    expect(wrapper.text()).toContain(text.invalid);
    expect(fetch.calls("POST", `/api/v1/items/${itemId}/sources/check`)).toHaveLength(0);

    await fields[0]!.setValue("mp4, mkv");
    await fields[4]!.setValue("20");
    await wrapper.find("[data-testid=direct-check] button").trigger("click");
    await flushPromises();
    expect(fetch.calls("POST", `/api/v1/items/${itemId}/sources/check`)[0]!.body).toEqual({
      containers: ["mp4", "mkv"],
      videoCodecs: [],
      audioCodecs: [],
      subtitleFormats: [],
      maxBitrate: 20_000_000,
    });
  });
});

describe("direct check helpers", () => {
  it("parses token lists and the bit rate", () => {
    expect(parseTokens("mp4, mkv  mp4")).toEqual(["mp4", "mkv"]);
    expect(parseTokens("a/b")).toBeNull();
    expect(parseTokens("x".repeat(33))).toBeNull();
    expect(toCapabilities({ ...emptyCustomForm(), maxMbit: "abc" })).toBeNull();
    expect(toCapabilities({ ...emptyCustomForm(), maxMbit: "-1" })).toBeNull();
    expect(toCapabilities(emptyCustomForm())).toEqual({ containers: [], videoCodecs: [], audioCodecs: [], subtitleFormats: [] });
  });

  it("marks a failed external probe and keeps unreadable tracks unconverted", () => {
    const failed = { ...source, externalTracks: [{ ...source.externalTracks[0]!, probeState: "failed" as const }] };
    const rows = trackRows(failed, decision);
    expect(rows.map((row) => row.delivery)).toEqual(["unreadable", "unreadable", "failed"]);
    expect(trackRows(source, undefined).map((row) => row.delivery)).toEqual(["embedded", "embedded", "unprobed"]);
  });
});
