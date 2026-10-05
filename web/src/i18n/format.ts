/** Formats an RFC 3339 timestamp for the UI locale; invalid input yields "". */
export function formatDateTime(value: string | null | undefined, locale: string): string {
  if (value === null || value === undefined) {
    return "";
  }
  const time = Date.parse(value);
  if (Number.isNaN(time)) {
    return "";
  }
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(time);
}

/** Formats a calendar date (YYYY-MM-DD, read as a local day) for the locale. */
export function formatDate(value: string, locale: string, style: "day" | "month" | "year" = "day"): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (match === null) {
    return "";
  }
  const date = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]));
  const options: Intl.DateTimeFormatOptions =
    style === "year" ? { year: "numeric" } : style === "month" ? { year: "numeric", month: "short" } : { dateStyle: "medium" };
  return new Intl.DateTimeFormat(locale, options).format(date);
}

/** Formats an amount of time: minutes under an hour, hours with one decimal above. */
export function formatDuration(seconds: number, locale: string): string {
  const safe = Number.isFinite(seconds) && seconds > 0 ? seconds : 0;
  if (safe < 3600) {
    return new Intl.NumberFormat(locale, { style: "unit", unit: "minute", unitDisplay: "short", maximumFractionDigits: 0 }).format(
      Math.round(safe / 60),
    );
  }
  return new Intl.NumberFormat(locale, { style: "unit", unit: "hour", unitDisplay: "short", maximumFractionDigits: 1 }).format(safe / 3600);
}

/** Formats a ratio between 0 and 1 as a percentage. */
export function formatPercent(ratio: number, locale: string): string {
  const safe = Number.isFinite(ratio) ? Math.min(Math.max(ratio, 0), 1) : 0;
  return new Intl.NumberFormat(locale, { style: "percent", maximumFractionDigits: 0 }).format(safe);
}

export function formatNumber(value: number, locale: string): string {
  return new Intl.NumberFormat(locale).format(value);
}
