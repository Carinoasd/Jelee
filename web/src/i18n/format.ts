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
