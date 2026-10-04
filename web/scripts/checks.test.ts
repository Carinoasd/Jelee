// @vitest-environment node
import { spawnSync } from "node:child_process";
import { cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it } from "vitest";
import { budgetKeys, checkBudget, compareBudget, initialAssets, measureBundle, parseBudget } from "./check-bundle-budget.mjs";
import { checkCatalogs, parseStrictJSON, placeholders, scriptMixing } from "./check-i18n.mjs";
import { checkWorkspace, lockfileViolations, manifestViolations, textViolations } from "./check-no-playback.mjs";

const webRoot = fileURLToPath(new URL("..", import.meta.url));
const temporary: string[] = [];

function scratch(): string {
  const directory = mkdtempSync(join(tmpdir(), "jelee-web-check-"));
  temporary.push(directory);
  return directory;
}

afterEach(() => {
  for (const directory of temporary.splice(0)) {
    rmSync(directory, { recursive: true, force: true });
  }
});

describe("no-playback gate", () => {
  it("rejects player packages in manifests and anywhere in the lockfile", () => {
    expect(manifestViolations({ dependencies: { "hls.js": "1.0.0", vue: "3" } })).toEqual(["dependencies: hls.js"]);
    expect(manifestViolations({ devDependencies: { "@videojs/http-streaming": "1" } })).toHaveLength(1);
    expect(
      lockfileViolations({
        packages: {
          "": { name: "root" },
          "node_modules/some-lib/node_modules/shaka-player": { version: "4" },
          "node_modules/other": { dependencies: { "mpegts.js": "^1" } },
        },
      }),
    ).toHaveLength(2);
  });

  it.each([
    "<video src=x>",
    "<AUDIO controls>",
    "new MediaSource()",
    "el.requestPictureInPicture()",
    "navigator.mediaSession.metadata = 1",
    'document.createElement("video")',
    "const p: HTMLVideoElement = x",
    '{ path: "/items/:id/play" }',
    'client.GET("/api/v1/sources/{id}/stream")',
    'client.GET("/api/v1/sources/{id}/subtitles/{trackId}")',
    'client.HEAD("/api/v1/sources/{id}/audio/{trackId}")',
  ])("flags %s", (text) => {
    expect(textViolations(text)).not.toHaveLength(0);
  });

  it("accepts ordinary browsing code", () => {
    expect(textViolations('{ path: "/libraries", name: "libraries" } <img src="/images/x">')).toEqual([]);
  });

  it("passes on the real workspace and fails once a <video> is added", () => {
    const repo = scratch();
    cpSync(join(webRoot, "src"), join(repo, "web", "src"), { recursive: true });
    cpSync(join(webRoot, "index.html"), join(repo, "web", "index.html"));
    cpSync(join(webRoot, "package.json"), join(repo, "web", "package.json"));
    expect(checkWorkspace(repo).problems).toEqual([]);
    writeFileSync(join(repo, "web", "src", "Bad.vue"), "<template><video /></template>\n");
    expect(checkWorkspace(repo).problems).toEqual(["web/src/Bad.vue: <video> element"]);
    expect(checkWorkspace(repo, { requireDist: true }).problems).toContain("web/dist: missing; run the build first");
  });
});

describe("i18n gate", () => {
  it("rejects duplicate keys and accepts strict JSON", () => {
    expect(parseStrictJSON('{"a": {"b": "x", "c": [1, "]"]}}')).toEqual({ a: { b: "x", c: [1, "]"] } });
    expect(() => {
      parseStrictJSON('{"a": "x", "a": "y"}');
    }).toThrow(/duplicate key/);
    expect(() => {
      parseStrictJSON('{"a": "x",}');
    }).toThrow();
  });

  it("compares placeholders and detects mixed Chinese scripts", () => {
    expect(placeholders("Hi {name}, {0}")).toBe(placeholders("{0} {name} です"));
    expect(placeholders("{name}")).not.toBe(placeholders("{user}"));
    expect(scriptMixing("zh-TW", "auth.title", "登录")).not.toBeNull();
    expect(scriptMixing("zh-CN", "auth.title", "登錄")).not.toBeNull();
    expect(scriptMixing("zh-TW", "common.localeNames.zhCN", "简体中文")).toBeNull();
  });

  function catalogCopy(): string {
    const root = scratch();
    cpSync(join(webRoot, "src"), join(root, "src"), { recursive: true });
    return root;
  }

  it("passes on the real catalogs", () => {
    expect(checkCatalogs(webRoot).problems).toEqual([]);
  });

  it("reports missing, unused and referenced-but-missing keys", () => {
    const root = catalogCopy();
    writeFileSync(join(root, "src", "i18n", "ja-JP", "auth.json"), JSON.stringify({ auth: { title: "x" } }));
    writeFileSync(join(root, "src", "extra.ts"), 'export const k = "auth.doesNotExist";\n');
    const problems = checkCatalogs(root).problems;
    expect(problems).toContain("ja-JP: missing key auth.name");
    expect(problems).toContain("source references missing key auth.doesNotExist");
  });

  it("accepts a flat reserved core.json in every locale and exempts it from usage", () => {
    const root = catalogCopy();
    for (const locale of ["zh-CN", "zh-TW", "ja-JP", "en-US"]) {
      writeFileSync(join(root, "src", "i18n", locale, "core.json"), JSON.stringify({ Favorites: "x {0}" }));
    }
    expect(checkCatalogs(root).problems).toEqual([]);
  });

  it("rejects a fifth locale", () => {
    const root = catalogCopy();
    mkdirSync(join(root, "src", "i18n", "fr-FR"));
    expect(checkCatalogs(root).problems[0]).toMatch(/exactly/);
  });
});

describe("bundle budget gate (G35.4)", () => {
  // Deterministic, poorly compressible content of a given length.
  function noise(length: number): string {
    let seed = 7;
    let text = "";
    while (text.length < length) {
      seed = (seed * 48271) % 2147483647;
      text += String.fromCharCode(33 + (seed % 90));
    }
    return text;
  }

  function fakeDist(entryLength: number): string {
    const dist = scratch();
    mkdirSync(join(dist, "assets"));
    writeFileSync(
      join(dist, "index.html"),
      '<!doctype html><html><head><script type="module" crossorigin src="/assets/index-a.js"></script>' +
        '<link rel="modulepreload" crossorigin href="/assets/vendor-b.js"><link rel="stylesheet" crossorigin href="/assets/index-c.css">' +
        '</head><body><div id="app"></div></body></html>',
    );
    writeFileSync(join(dist, "assets", "index-a.js"), noise(entryLength));
    writeFileSync(join(dist, "assets", "vendor-b.js"), noise(2000));
    writeFileSync(join(dist, "assets", "index-c.css"), "a{color:red}".repeat(50));
    // Lazy chunks are not part of the initial download.
    writeFileSync(join(dist, "assets", "Lazy-d.js"), noise(500_000));
    return dist;
  }

  function budgetFile(budget: Record<string, unknown>): string {
    const path = join(scratch(), "budget.json");
    writeFileSync(path, JSON.stringify(budget));
    return path;
  }

  it("finds the entry, its preloads and the initial stylesheets", () => {
    expect(initialAssets('<script type="module" src="/a.js"></script><script src="/legacy.js"></script><link rel="modulepreload" href="/b.js"><link rel="stylesheet" href="/c.css"><link rel="icon" href="/i.png">')).toEqual({
      entries: ["/a.js"],
      preloads: ["/b.js"],
      styles: ["/c.css"],
    });
  });

  it("measures gzip sizes of the initial download only", () => {
    const measured = measureBundle(fakeDist(10_000));
    const metrics = measured.metrics as Record<string, number>;
    const files = measured.files as { href: string }[];
    expect(files.map((file) => file.href)).toEqual(["/assets/index-a.js", "/assets/vendor-b.js", "/assets/index-c.css"]);
    expect(metrics.entryJsGzipBytes).toBeGreaterThan(5_000);
    expect(metrics.initialJsGzipBytes).toBeGreaterThan(metrics.entryJsGzipBytes!);
    expect(metrics.initialJsGzipBytes).toBeLessThan(20_000);
    expect(metrics.initialCssGzipBytes).toBeLessThan(200);
  });

  it("passes within the budget and fails once the main bundle grows past it", () => {
    const budget = budgetFile({ note: "test", entryJsGzipBytes: 12_000, initialJsGzipBytes: 15_000, initialCssGzipBytes: 500 });
    expect(checkBudget(fakeDist(10_000), budget).problems).toEqual([]);
    const over = checkBudget(fakeDist(20_000), budget).problems;
    expect(over).toHaveLength(2);
    expect(over[0]).toMatch(/^entryJsGzipBytes: \d+ B gzip exceeds the budget of 12000 B by \d+ B$/);
    expect(over[1]).toMatch(/^initialJsGzipBytes: /);
  });

  it("exits non-zero from the command line when over budget", () => {
    const script = fileURLToPath(new URL("./check-bundle-budget.mjs", import.meta.url));
    const budget = budgetFile({ entryJsGzipBytes: 12_000, initialJsGzipBytes: 15_000, initialCssGzipBytes: 500 });
    const ok = spawnSync(process.execPath, [script, "--dist", fakeDist(10_000), "--budget", budget], { encoding: "utf8" });
    expect(ok.status).toBe(0);
    expect(ok.stdout).toContain("entryJsGzipBytes");
    const failed = spawnSync(process.execPath, [script, "--dist", fakeDist(20_000), "--budget", budget], { encoding: "utf8" });
    expect(failed.status).toBe(1);
    expect(failed.stderr).toContain("bundle budget gate failed (G35.4)");
    const missing = spawnSync(process.execPath, [script, "--dist", scratch(), "--budget", budget], { encoding: "utf8" });
    expect(missing.status).toBe(1);
    expect(missing.stderr).toContain("index.html missing");
  });

  it("rejects malformed budgets and missing referenced files", () => {
    expect(() => parseBudget({ entryJsGzipBytes: 1, initialJsGzipBytes: 1 })).toThrow(/initialCssGzipBytes/);
    expect(() => parseBudget({ entryJsGzipBytes: -1, initialJsGzipBytes: 1, initialCssGzipBytes: 1 })).toThrow(/positive integer/);
    expect(() => parseBudget({ entryJsGzipBytes: 1, initialJsGzipBytes: 1, initialCssGzipBytes: 1, extra: 1 })).toThrow(/unknown field/);
    const dist = fakeDist(100);
    rmSync(join(dist, "assets", "vendor-b.js"));
    expect(() => measureBundle(dist)).toThrow(/vendor-b\.js: referenced by index\.html but missing/);
    expect(compareBudget({ entryJsGzipBytes: 1, initialJsGzipBytes: 1, initialCssGzipBytes: 1 }, { entryJsGzipBytes: 1, initialJsGzipBytes: 1, initialCssGzipBytes: 1 })).toEqual([]);
  });

  it("keeps the repository budget well-formed", () => {
    const budget = parseBudget(JSON.parse(readFileSync(join(webRoot, "bundle-budget.json"), "utf8")));
    expect(Object.keys(budget).sort()).toEqual([...budgetKeys].sort());
  });
});
