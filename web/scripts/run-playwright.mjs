#!/usr/bin/env node
// Runs the Playwright test runner against the manifest-pinned Chrome Headless
// Shell in .tools/playwright (G30.6). PLAYWRIGHT_BROWSERS_PATH is always set
// here, before Playwright loads, so it never reads, downloads to or garbage
// collects the user's global browser cache.
//
// Usage: node scripts/run-playwright.mjs [playwright test arguments]
import { spawnSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const repo = fileURLToPath(new URL("../..", import.meta.url));

/**
 * @typedef {{ browsersPath: string; installPath: string; executable: string; sha256: string; url: string }} BrowserSpec
 * @typedef {{ name: string; version: string; status?: string; platforms: Record<string, unknown> }} ManifestTool
 */

/**
 * Returns the manifest entry and the platform key for this host.
 * @param {{ tools: ManifestTool[] }} manifest
 * @param {string} [platform]
 * @param {string} [arch]
 * @returns {{ tool: ManifestTool; spec: BrowserSpec; target: string }}
 */
export function playwrightBrowser(manifest, platform = process.platform, arch = process.arch) {
  const tool = manifest.tools.find((entry) => entry.name === "playwright");
  if (tool === undefined || tool.status !== "active") {
    throw new Error("tools/manifest.json has no active playwright entry");
  }
  const target = (platform === "win32" ? "windows" : platform) + "-" + ({ x64: "amd64", arm64: "arm64" }[arch] ?? arch);
  const spec = /** @type {BrowserSpec | undefined} */ (tool.platforms[target]);
  if (spec === undefined || platform !== "linux") {
    // The end-to-end and visual gates run on Linux only (docs/toolchain.md).
    throw new Error("no pinned Playwright browser for " + target + "; run the web end-to-end tests on Linux");
  }
  return { tool, spec, target };
}

function main(argv) {
  const manifest = JSON.parse(readFileSync(join(repo, "tools/manifest.json"), "utf8"));
  const { spec } = playwrightBrowser(manifest);
  const browsers = resolve(repo, ".tools", spec.browsersPath);
  if (!existsSync(resolve(repo, ".tools", spec.installPath, spec.executable))) {
    console.error("Playwright browser missing in " + browsers + "; run make bootstrap-playwright");
    return 1;
  }
  const cli = createRequire(import.meta.url).resolve("@playwright/test/cli");
  const env = { ...process.env, PLAYWRIGHT_BROWSERS_PATH: browsers, PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD: "1", PLAYWRIGHT_SKIP_BROWSER_GC: "1" };
  const result = spawnSync(process.execPath, [cli, "test", ...argv], { stdio: "inherit", env, cwd: fileURLToPath(new URL("..", import.meta.url)) });
  return result.status ?? 1;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    process.exitCode = main(process.argv.slice(2));
  } catch (error) {
    console.error(error instanceof Error ? error.message : error);
    process.exitCode = 1;
  }
}
