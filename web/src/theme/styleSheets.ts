// Applies generated CSS as constructed stylesheets (document.adoptedStyleSheets).
// The CSSOM is not governed by the CSP style-src directive, so plugin theme
// tokens and administrator CSS work under style-src 'self' without
// 'unsafe-inline' and without <style> elements. Adopted sheets come after
// the bundled stylesheets in the cascade, in the order of `layers`.
// Every caller passes text it has already sanitized.

/** Sheets in cascade order: later layers win. */
export const styleLayers = ["plugin-tokens", "site-tokens", "custom-css"] as const;
export type StyleLayer = (typeof styleLayers)[number];

export interface StyleTarget {
  adoptedStyleSheets: CSSStyleSheet[];
}

const sheets = new WeakMap<StyleTarget, Map<StyleLayer, CSSStyleSheet>>();

function supported(target: unknown): target is StyleTarget {
  return (
    typeof target === "object" &&
    target !== null &&
    "adoptedStyleSheets" in target &&
    typeof CSSStyleSheet === "function" &&
    typeof CSSStyleSheet.prototype.replaceSync === "function"
  );
}

/**
 * Replaces one layer's CSS; an empty text removes the layer. Returns false
 * when the browser lacks constructable stylesheets (nothing is applied: an
 * inline <style> fallback would be blocked by the CSP anyway).
 */
export function applyStyleLayer(layer: StyleLayer, css: string, target: unknown = globalThis.document): boolean {
  if (!supported(target)) {
    return false;
  }
  let owned = sheets.get(target);
  if (owned === undefined) {
    owned = new Map();
    sheets.set(target, owned);
  }
  const ours = new Set(owned.values());
  const others = target.adoptedStyleSheets.filter((sheet) => !ours.has(sheet));
  if (css === "") {
    owned.delete(layer);
  } else {
    const sheet = owned.get(layer) ?? new CSSStyleSheet();
    sheet.replaceSync(css);
    owned.set(layer, sheet);
  }
  const ordered = styleLayers.map((name) => owned.get(name)).filter((sheet): sheet is CSSStyleSheet => sheet !== undefined);
  target.adoptedStyleSheets = [...others, ...ordered];
  return true;
}
