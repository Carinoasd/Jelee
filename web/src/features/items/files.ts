import type { MediaSourceInfo } from "./api";

/** Formats a duration in microseconds as h:mm:ss (or m:ss under an hour). */
export function formatDuration(micros: number | undefined): string {
  if (micros === undefined || !Number.isFinite(micros) || micros < 0) {
    return "";
  }
  const total = Math.round(micros / 1_000_000);
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = String(total % 60).padStart(2, "0");
  return hours > 0 ? `${hours}:${String(minutes).padStart(2, "0")}:${seconds}` : `${minutes}:${seconds}`;
}

const byteUnits = ["byte", "kilobyte", "megabyte", "gigabyte", "terabyte"] as const;

/** Formats a byte count with binary steps in the UI locale. */
export function formatBytes(bytes: number | undefined, locale: string): string {
  if (bytes === undefined || !Number.isFinite(bytes) || bytes < 0) {
    return "";
  }
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < byteUnits.length - 1) {
    value /= 1024;
    unit++;
  }
  return new Intl.NumberFormat(locale, { style: "unit", unit: byteUnits[unit], unitDisplay: "short", maximumFractionDigits: unit === 0 ? 0 : 1 }).format(value);
}

/** Formats bits per second as Mbit/s (kbit/s below one megabit). */
export function formatBitrate(bps: number | undefined, locale: string): string {
  if (bps === undefined || !Number.isFinite(bps) || bps <= 0) {
    return "";
  }
  const mega = bps >= 1_000_000;
  return new Intl.NumberFormat(locale, {
    style: "unit",
    unit: mega ? "megabit-per-second" : "kilobit-per-second",
    unitDisplay: "short",
    maximumFractionDigits: 1,
  }).format(bps / (mega ? 1_000_000 : 1_000));
}

/** Width×height of the primary video track, else of the first one. */
export function resolutionOf(source: MediaSourceInfo): string {
  const video = source.videoTracks.find((track) => track.primary) ?? source.videoTracks[0];
  return video?.width !== undefined && video.height !== undefined ? `${video.width}×${video.height}` : "";
}

/** Distinct codec names in track order. */
export function codecsOf(tracks: readonly { codec?: string }[]): string {
  return [...new Set(tracks.map((track) => track.codec).filter((codec): codec is string => codec !== undefined && codec !== ""))].join(", ");
}
