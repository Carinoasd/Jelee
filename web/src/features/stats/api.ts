import type { ApiClient } from "@/api/client";
import { call, callNoContent } from "@/api/call";
import { downloadAttachment } from "@/api/download";
import type { components } from "@/api/schema";

export type WatchStatsReport = components["schemas"]["WatchStatsReport"];
export type WatchPeriod = WatchStatsReport["period"];

export const watchPeriods = ["day", "week", "month", "year"] as const satisfies readonly WatchPeriod[];
/** Top list sizes offered; the server accepts 1..100. */
export const topSizes = [10, 25, 50] as const;

/**
 * Days shown for each grouping: 30 days, 12 weeks, 12 months, 5 years. The
 * server accepts at most 400 days by day and 3660 days otherwise.
 */
export const periodDays: Readonly<Record<WatchPeriod, number>> = { day: 30, week: 84, month: 365, year: 1826 };

export interface WatchStatsQuery {
  readonly period: WatchPeriod;
  readonly top: number;
  readonly from: string;
  readonly to: string;
}

export function isWatchPeriod(value: unknown): value is WatchPeriod {
  return typeof value === "string" && (watchPeriods as readonly string[]).includes(value);
}

function isoDate(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

/** The date range ending today (local calendar) that a grouping shows. */
export function rangeFor(period: WatchPeriod, today: Date = new Date()): { from: string; to: string } {
  const start = new Date(today.getFullYear(), today.getMonth(), today.getDate() - (periodDays[period] - 1));
  return { from: isoDate(start), to: isoDate(today) };
}

/** True when nothing was counted in the range. */
export function isEmptyReport(report: WatchStatsReport): boolean {
  return report.totals.sessions === 0 && report.totals.effectiveSeconds === 0 && report.periods.length === 0;
}

/** The caller's own statistics (G23.3); visible items only (G48.3). */
export async function readMyWatchStats(client: ApiClient, query: WatchStatsQuery): Promise<WatchStatsReport> {
  const body = await call(client.GET("/api/v1/users/me/watch-stats", { params: { query } }));
  return body.data;
}

/** Statistics of every user, with the top users (administrators only). */
export async function readAllWatchStats(client: ApiClient, query: WatchStatsQuery): Promise<WatchStatsReport> {
  const body = await call(client.GET("/api/v1/watch-stats", { params: { query } }));
  return body.data;
}

/**
 * Deletes the caller's whole viewing history: progress, finished marks,
 * counts and statistics (G23.4). Cannot be undone.
 */
export async function clearMyHistory(client: ApiClient): Promise<void> {
  await callNoContent(client.DELETE("/api/v1/users/me/playback-history", {}));
}

export type ExportFormat = "csv" | "ndjson";

/**
 * Downloads the daily roll-up of a range (administrators only) through the
 * API client: the server checks the role, audits the export and refuses
 * ranges over its row limit (409 stats_export_limit), which is thrown as an
 * ApiError instead of being saved as the file.
 */
export async function downloadExport(client: ApiClient, range: { from: string; to: string }, format: ExportFormat): Promise<void> {
  await downloadAttachment(
    client.GET("/api/v1/watch-stats/export", { params: { query: { from: range.from, to: range.to, format } }, parseAs: "blob" }),
    `watch-stats-${range.from}_${range.to}.${format}`,
  );
}
