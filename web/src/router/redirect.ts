/**
 * Accepts only same-origin, absolute in-app paths as post-login targets, so a
 * crafted ?redirect= can never send the user to another origin.
 */
export function safeRedirect(value: unknown, fallback = "/libraries"): string {
  if (typeof value !== "string" || value.length === 0 || value.length > 2048) {
    return fallback;
  }
  if (!value.startsWith("/") || value.startsWith("//") || value.includes("\\")) {
    return fallback;
  }
  // Reject control characters, which browsers strip before parsing URLs.
  for (const char of value) {
    const code = char.charCodeAt(0);
    if (code < 0x20 || code === 0x7f) {
      return fallback;
    }
  }
  if (value === "/login" || value.startsWith("/login?") || value.startsWith("/login#")) {
    return fallback;
  }
  return value;
}
