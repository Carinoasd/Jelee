import type { ClientCapabilities, MediaSourceInfo, PlaybackDecision } from "./api";

/** Capability presets of common clients; "custom" is typed in by the user. */
export const presetIds = ["browser", "android", "apple", "custom"] as const;
export type PresetId = (typeof presetIds)[number];

/** Declarations are what a client can decode, never a conversion request. */
export const presets: Readonly<Record<Exclude<PresetId, "custom">, ClientCapabilities>> = {
  browser: { containers: ["mp4", "webm"], videoCodecs: ["h264"], audioCodecs: ["aac"], subtitleFormats: ["webvtt", "srt"] },
  android: {
    containers: ["mp4", "mkv", "webm", "mpegts"],
    videoCodecs: ["h264", "hevc", "vp9", "av1"],
    audioCodecs: ["aac", "ac3", "eac3", "mp3", "opus", "flac"],
    subtitleFormats: ["srt", "ass", "ssa", "webvtt", "ttml"],
  },
  apple: { containers: ["mp4", "mov"], videoCodecs: ["h264", "hevc"], audioCodecs: ["aac", "ac3", "eac3", "alac"], subtitleFormats: ["webvtt", "mov_text"] },
};

export interface CustomForm {
  containers: string;
  videoCodecs: string;
  audioCodecs: string;
  subtitleFormats: string;
  /** Highest sustained bit rate in Mbit/s; empty declares none. */
  maxMbit: string;
}

export const emptyCustomForm = (): CustomForm => ({ containers: "", videoCodecs: "", audioCodecs: "", subtitleFormats: "", maxMbit: "" });

const tokenPattern = /^[A-Za-z0-9._-]{1,32}$/;
const maxTokens = 32;
const maxBitrate = 1_000_000_000_000;

/** Splits a list typed with commas or spaces; null when a token is not valid. */
export function parseTokens(text: string): string[] | null {
  const tokens = [...new Set(text.split(/[\s,]+/).filter((token) => token !== ""))];
  return tokens.length <= maxTokens && tokens.every((token) => tokenPattern.test(token)) ? tokens : null;
}

/** The custom form as a declaration; null when any field is invalid. */
export function toCapabilities(form: CustomForm): ClientCapabilities | null {
  const containers = parseTokens(form.containers);
  const videoCodecs = parseTokens(form.videoCodecs);
  const audioCodecs = parseTokens(form.audioCodecs);
  const subtitleFormats = parseTokens(form.subtitleFormats);
  if (!containers || !videoCodecs || !audioCodecs || !subtitleFormats) {
    return null;
  }
  const result: ClientCapabilities = { containers, videoCodecs, audioCodecs, subtitleFormats };
  const mbit = form.maxMbit.trim();
  if (mbit !== "") {
    const bps = Math.round(Number(mbit) * 1_000_000);
    if (!Number.isFinite(bps) || bps < 0 || bps > maxBitrate) {
      return null;
    }
    result.maxBitrate = bps;
  }
  return result;
}

/** How one track would reach a client that can read it: nothing is converted. */
export type TrackDelivery = "embedded" | "separate" | "unreadable" | "unprobed" | "failed";

export interface TrackRow {
  readonly key: string;
  readonly kind: "audio" | "subtitle";
  readonly external: boolean;
  readonly language: string | undefined;
  readonly codec: string;
  readonly title: string | undefined;
  readonly delivery: TrackDelivery;
  /** Server reason code of an unreadable track. */
  readonly reason: string | undefined;
  /** Charset of an external text subtitle, as reported. */
  readonly charset: string | undefined;
}

/** Joins a source's tracks with the verdict the server gave each of them. */
export function trackRows(source: MediaSourceInfo, decision: PlaybackDecision | undefined): TrackRow[] {
  const embedded = (kind: "audio" | "subtitle", index: number) => decision?.tracks.find((track) => !track.external && track.kind === kind && track.index === index);
  const rows: TrackRow[] = [];
  for (const track of source.audioTracks) {
    const verdict = embedded("audio", track.index);
    rows.push({
      key: `a${track.index}`,
      kind: "audio",
      external: false,
      language: track.language,
      codec: track.codec ?? "",
      title: undefined,
      delivery: verdict === undefined || verdict.supported ? "embedded" : "unreadable",
      reason: verdict?.reason,
      charset: undefined,
    });
  }
  for (const track of source.subtitleTracks) {
    const verdict = embedded("subtitle", track.index);
    rows.push({
      key: `s${track.index}`,
      kind: "subtitle",
      external: false,
      language: track.language,
      codec: track.format ?? track.codec ?? "",
      title: undefined,
      delivery: verdict === undefined || verdict.supported ? "embedded" : "unreadable",
      reason: verdict?.reason,
      charset: undefined,
    });
  }
  for (const track of source.externalTracks) {
    const verdict = decision?.tracks.find((candidate) => candidate.external && candidate.id === track.id);
    let delivery: TrackDelivery = "separate";
    if (track.probeState === "failed") {
      delivery = "failed";
    } else if (verdict?.reason === "track_not_probed" || track.probeState === "pending") {
      delivery = "unprobed";
    } else if (verdict !== undefined && !verdict.supported) {
      delivery = "unreadable";
    }
    rows.push({
      key: "x" + track.id,
      kind: track.kind,
      external: true,
      language: track.language,
      codec: track.codec ?? track.format,
      title: track.title,
      delivery,
      reason: verdict?.reason,
      charset: track.charsetState === "undetermined" ? "undetermined" : track.charset,
    });
  }
  return rows;
}
