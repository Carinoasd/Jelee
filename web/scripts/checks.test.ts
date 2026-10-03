// @vitest-environment node
import { cpSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it } from "vitest";
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
