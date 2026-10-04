// Administrator CSS sanitizer (G33.4). The input is parsed into rules and
// declarations and re-serialized from what passed, so the applied text never
// contains anything the checks did not see. Refused outright: markup ("<",
// so no </style> or <script>), backslash escapes (they can spell blocked
// words), control characters, unterminated comments and unbalanced blocks.
// Dropped with an issue: @import and unknown at-rules, expression(),
// javascript:/vbscript: URLs, behavior/-moz-binding, image-set()/src()/
// element(), attr() used as a URL, and every url() that is not a same-origin
// path. External fonts are refused unless enabled and the https host is on
// the administrator's allowlist (and the CSP allows it, see
// docs/frontend-adr.md). The result is applied as a constructed stylesheet
// (theme/styleSheets.ts), which works under style-src 'self'.

export type CssIssueCode =
  | "too_long"
  | "markup"
  | "escape"
  | "control_char"
  | "unterminated_comment"
  | "unbalanced"
  | "too_deep"
  | "import_blocked"
  | "at_rule_blocked"
  | "invalid_selector"
  | "invalid_declaration"
  | "nesting_unsupported"
  | "expression"
  | "script_url"
  | "binding"
  | "blocked_function"
  | "external_url"
  | "external_font";

export interface CssIssue {
  readonly code: CssIssueCode;
  /** Short excerpt of the offending text, for the administrator. */
  readonly excerpt: string;
}

export interface CssPolicy {
  /** Allow @font-face sources from allowlisted https hosts. Off by default. */
  readonly allowExternalFonts: boolean;
  /** Exact host names fonts may come from when allowed. */
  readonly fontHosts: readonly string[];
}

export interface SanitizedCss {
  /** CSS safe to apply; empty when the input was refused. */
  readonly css: string;
  readonly issues: readonly CssIssue[];
  /** The whole input was refused (structural problem). */
  readonly rejected: boolean;
}

export const customCssMaxLength = 64 * 1024;
export const fontHostLimit = 10;
const maxDepth = 3;

export const defaultCssPolicy: CssPolicy = { allowExternalFonts: false, fontHosts: [] };

const hostPattern = /^(?=.{1,253}$)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$/;

/**
 * Normalizes an allowlist entry; null when it is not a plain DNS host name.
 * IP addresses (a numeric last label) are refused like on the server, which
 * places the entries in the CSP font-src directive.
 */
export function normalizeFontHost(value: string): string | null {
  const host = value.trim().toLowerCase();
  return hostPattern.test(host) && !/\.\d+$/.test(host) ? host : null;
}

function excerpt(text: string): string {
  const flat = text.replace(/\s+/g, " ").trim();
  return flat.length > 80 ? flat.slice(0, 77) + "..." : flat;
}

interface Entry {
  readonly prelude: string;
  /** Text between the braces, or null for a statement ending in ";". */
  readonly body: string | null;
}

class Unbalanced extends Error {}

/** Splits text into top-level statements and blocks, respecting strings and parentheses. */
function split(text: string): Entry[] {
  const entries: Entry[] = [];
  let buffer = "";
  let i = 0;
  let parens = 0;
  while (i < text.length) {
    const char = text[i] ?? "";
    if (char === '"' || char === "'") {
      const end = text.indexOf(char, i + 1);
      const newline = text.slice(i + 1, end < 0 ? undefined : end).search(/[\n\r\f]/);
      if (end < 0 || newline >= 0) {
        throw new Unbalanced();
      }
      buffer += text.slice(i, end + 1);
      i = end + 1;
      continue;
    }
    if (char === "(") {
      parens++;
    } else if (char === ")") {
      parens--;
      if (parens < 0) {
        throw new Unbalanced();
      }
    } else if (parens === 0 && char === ";") {
      entries.push({ prelude: buffer, body: null });
      buffer = "";
      i++;
      continue;
    } else if (parens === 0 && char === "{") {
      const end = matchingBrace(text, i);
      entries.push({ prelude: buffer, body: text.slice(i + 1, end) });
      buffer = "";
      i = end + 1;
      continue;
    } else if (char === "}" || char === "{") {
      throw new Unbalanced();
    }
    buffer += char;
    i++;
  }
  if (parens !== 0) {
    throw new Unbalanced();
  }
  if (buffer.trim() !== "") {
    entries.push({ prelude: buffer, body: null });
  }
  return entries;
}

function matchingBrace(text: string, open: number): number {
  let depth = 0;
  for (let i = open; i < text.length; i++) {
    const char = text[i];
    if (char === '"' || char === "'") {
      const end = text.indexOf(char, i + 1);
      if (end < 0) {
        throw new Unbalanced();
      }
      i = end;
    } else if (char === "{") {
      depth++;
    } else if (char === "}") {
      depth--;
      if (depth === 0) {
        return i;
      }
    }
  }
  throw new Unbalanced();
}

const selectorPattern = /^[A-Za-z0-9_\s.#:,>+~*[\]="'^$|()&%-]+$/;
const propertyPattern = /^(?:--[A-Za-z0-9_-]{1,64}|-?[a-z][a-z0-9-]{0,63})$/;
const keyframeSelector = /^(?:from|to|\d{1,3}(?:\.\d+)?%)(?:\s*,\s*(?:from|to|\d{1,3}(?:\.\d+)?%))*$/i;
const identPattern = /^-?[A-Za-z_][A-Za-z0-9_-]*$/;
const conditionPattern = /^[A-Za-z0-9_\s.,:()<>=/-]*$/;
const sameOriginPath = /^\/(?!\/)[A-Za-z0-9._~%/-]*(?:\?[A-Za-z0-9._~%=&-]*)?$/;
const fragmentRef = /^#[A-Za-z][A-Za-z0-9_-]*$/;
const blockedFunctions = /(?:^|[^A-Za-z0-9_-])(?:-webkit-image-set|image-set|src|element|-moz-element|paint)\s*\(/i;

interface Context {
  readonly policy: CssPolicy;
  readonly issues: CssIssue[];
}

function report(context: Context, code: CssIssueCode, text: string): void {
  context.issues.push({ code, excerpt: excerpt(text) });
}

/** Returns the issue code that blocks a declaration value, or null. */
function valueIssue(value: string, fontFace: boolean, policy: CssPolicy): CssIssueCode | null {
  const lower = value.toLowerCase();
  if (/expression\s*\(/.test(lower)) {
    return "expression";
  }
  if (/(?:javascript|vbscript|livescript)\s*:/.test(lower)) {
    return "script_url";
  }
  if (blockedFunctions.test(lower) || /attr\s*\([^)]*\burl\b/.test(lower)) {
    return "blocked_function";
  }
  const urls = [...value.matchAll(/url\s*\(\s*(?:"([^"]*)"|'([^']*)'|([^"'()\s]*))\s*\)/gi)];
  if (urls.length !== (lower.match(/url\s*\(/g) ?? []).length) {
    return "external_url";
  }
  for (const match of urls) {
    const target = (match[1] ?? match[2] ?? match[3] ?? "").trim();
    if (sameOriginPath.test(target) || fragmentRef.test(target)) {
      continue;
    }
    if (fontFace) {
      if (policy.allowExternalFonts && allowedFontUrl(target, policy.fontHosts)) {
        continue;
      }
      return "external_font";
    }
    return "external_url";
  }
  return null;
}

function allowedFontUrl(target: string, hosts: readonly string[]): boolean {
  let url: URL;
  try {
    url = new URL(target);
  } catch {
    return false;
  }
  return url.protocol === "https:" && url.username === "" && url.password === "" && url.port === "" && hosts.includes(url.hostname.toLowerCase());
}

function declarations(body: string, context: Context, fontFace: boolean): string[] {
  const kept: string[] = [];
  for (const entry of split(body)) {
    if (entry.body !== null) {
      report(context, "nesting_unsupported", entry.prelude);
      continue;
    }
    const text = entry.prelude.trim();
    if (text === "") {
      continue;
    }
    const colon = text.indexOf(":");
    const property = colon < 0 ? "" : text.slice(0, colon).trim().toLowerCase();
    const value = colon < 0 ? "" : text.slice(colon + 1).trim().replace(/\s+/g, " ");
    if (!propertyPattern.test(property) || value === "" || value.length > 2048) {
      report(context, "invalid_declaration", text);
      continue;
    }
    if (property === "behavior" || property.endsWith("-binding")) {
      report(context, "binding", text);
      continue;
    }
    const issue = valueIssue(value, fontFace, context.policy);
    if (issue !== null) {
      report(context, issue, text);
      continue;
    }
    kept.push(property + ":" + value);
  }
  return kept;
}

function rule(prelude: string, body: string, context: Context, fontFace = false): string | null {
  const kept = declarations(body, context, fontFace);
  return kept.length === 0 ? null : prelude + "{" + kept.join(";") + "}";
}

function stylesheet(text: string, context: Context, depth: number): string[] {
  if (depth > maxDepth) {
    report(context, "too_deep", text);
    return [];
  }
  const output: string[] = [];
  for (const entry of split(text)) {
    const prelude = entry.prelude.trim().replace(/\s+/g, " ");
    if (prelude === "" && entry.body === null) {
      continue;
    }
    if (prelude.startsWith("@")) {
      const name = /^@([A-Za-z-]+)/.exec(prelude)?.[1]?.toLowerCase() ?? "";
      const condition = prelude.slice(name.length + 1).trim();
      if (name === "import") {
        report(context, "import_blocked", prelude);
        continue;
      }
      if (["media", "supports", "container", "layer"].includes(name) && conditionPattern.test(condition) && !/url\s*\(/i.test(condition)) {
        if (entry.body === null) {
          if (name === "layer" && /^[A-Za-z0-9_\s,.-]+$/.test(condition)) {
            output.push("@layer " + condition + ";");
          } else {
            report(context, "at_rule_blocked", prelude);
          }
          continue;
        }
        const inner = stylesheet(entry.body, context, depth + 1);
        if (inner.length > 0) {
          output.push("@" + name + (condition === "" ? "" : " " + condition) + "{" + inner.join("") + "}");
        }
        continue;
      }
      if (name === "font-face" && condition === "" && entry.body !== null) {
        const face = rule("@font-face", entry.body, context, true);
        if (face !== null) {
          output.push(face);
        }
        continue;
      }
      if ((name === "keyframes" || name === "-webkit-keyframes") && identPattern.test(condition) && entry.body !== null) {
        const frames: string[] = [];
        for (const frame of split(entry.body)) {
          const selector = frame.prelude.trim();
          if (frame.body === null || !keyframeSelector.test(selector)) {
            report(context, "invalid_selector", selector);
            continue;
          }
          const kept = rule(selector, frame.body, context);
          if (kept !== null) {
            frames.push(kept);
          }
        }
        output.push("@" + name + " " + condition + "{" + frames.join("") + "}");
        continue;
      }
      report(context, "at_rule_blocked", prelude);
      continue;
    }
    if (entry.body === null || prelude === "" || prelude.length > 1024 || !selectorPattern.test(prelude)) {
      report(context, "invalid_selector", prelude === "" ? (entry.body ?? "") : prelude);
      continue;
    }
    const kept = rule(prelude, entry.body, context);
    if (kept !== null) {
      output.push(kept);
    }
  }
  return output;
}

/** Sanitizes administrator CSS; never throws. */
export function sanitizeCustomCss(input: string, policy: CssPolicy = defaultCssPolicy): SanitizedCss {
  const refuse = (code: CssIssueCode, text: string): SanitizedCss => ({ css: "", issues: [{ code, excerpt: excerpt(text) }], rejected: true });
  if (input.length > customCssMaxLength) {
    return refuse("too_long", input.slice(0, 80));
  }
  const markup = input.indexOf("<");
  if (markup >= 0) {
    return refuse("markup", input.slice(markup, markup + 80));
  }
  const escape = input.indexOf("\\");
  if (escape >= 0) {
    return refuse("escape", input.slice(escape, escape + 80));
  }
  // Tabs and line breaks are whitespace; every other control character
  // (including NUL) and the Unicode line separators are refused.
  // eslint-disable-next-line no-control-regex -- the pattern exists to find control characters
  const control = input.search(/[\u0000-\u0008\u000B\u000E-\u001F\u007F\u2028\u2029]/);
  if (control >= 0) {
    return refuse("control_char", input.slice(Math.max(0, control - 20), control + 20));
  }
  // Comments go first: "expr/**/ession(" must not survive as two halves.
  const withoutComments = input.replace(/\/\*[\s\S]*?\*\//g, " ");
  const open = withoutComments.indexOf("/*");
  if (open >= 0) {
    return refuse("unterminated_comment", withoutComments.slice(open, open + 80));
  }
  const context: Context = { policy, issues: [] };
  try {
    const css = stylesheet(withoutComments, context, 0).join("\n");
    return { css, issues: context.issues, rejected: false };
  } catch (error: unknown) {
    if (error instanceof Unbalanced) {
      return refuse("unbalanced", input.slice(0, 80));
    }
    throw error;
  }
}

/**
 * Checks one design token value (plugin theme.token). Returns the value or
 * null: no url(), no blocks or statements, no quotes except in font lists.
 */
export function sanitizeTokenValue(value: string, allowQuotes = false): string | null {
  const text = value.trim();
  if (text === "" || text.length > 256 || /[<>{};\\@]/.test(text) || (!allowQuotes && /["']/.test(text))) {
    return null;
  }
  // eslint-disable-next-line no-control-regex -- the pattern exists to find control characters
  if (/url\s*\(/i.test(text) || /[\u0000-\u001F\u007F\u2028\u2029]/.test(text)) {
    return null;
  }
  return valueIssue(text, false, defaultCssPolicy) === null ? text : null;
}

/**
 * Builds a token override sheet: light values on :root, dark values for the
 * dark scheme (system or chosen). Unknown names and unsafe values are
 * dropped; an empty result means nothing to apply.
 */
export function tokenStyleSheet(
  light: Readonly<Record<string, unknown>>,
  dark: Readonly<Record<string, unknown>>,
  names: ReadonlySet<string>,
): string {
  const declarations = (tokens: Readonly<Record<string, unknown>>) =>
    Object.entries(tokens).flatMap(([name, value]) => {
      const safe = typeof value === "string" && names.has(name) ? sanitizeTokenValue(value, name === "font-family") : null;
      return safe === null ? [] : ["--jl-" + name + ":" + safe];
    });
  const lightRules = declarations(light);
  const darkRules = declarations(dark);
  if (lightRules.length === 0 && darkRules.length === 0) {
    return "";
  }
  const darkBlock = darkRules.join(";");
  return `:root{${lightRules.join(";")}}\n@media (prefers-color-scheme: dark){:root:not([data-theme="light"]){${darkBlock}}}\n:root[data-theme="dark"]{${darkBlock}}`;
}
