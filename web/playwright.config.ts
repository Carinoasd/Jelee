// End-to-end and visual regression tests (G27.4, G34.5, G34.6). Run through
// scripts/run-playwright.mjs (make web-e2e, web-visual, web-visual-update),
// which points Playwright at the manifest-pinned browser in .tools/playwright.
//
// The built client is served by `vite preview`; every API call is answered
// by e2e/fixtures/api.ts, so no Jelee server or database is involved.
// Baselines are only written by `make web-visual-update` (--update-snapshots);
// every other run, CI included, compares and never writes (G34.6: a visual
// change needs a person to accept it).
import { defineConfig, devices } from "@playwright/test";

const port = 4179;
const vite = "node ../node_modules/vite/bin/vite.js";
const outDir = "../.testdata/playwright/dist";

const desktop = { viewport: { width: 1280, height: 800 } };
const mobile = { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true };

export default defineConfig({
  testDir: "e2e",
  outputDir: "../.testdata/playwright/results",
  snapshotPathTemplate: "{testDir}/__screenshots__/{projectName}/{arg}{ext}",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: 0,
  // The machine is shared with database and soak tests; two workers keep
  // memory well under a gigabyte.
  workers: 2,
  reporter: process.env.CI ? [["list"], ["html", { outputFolder: "../.testdata/playwright/report", open: "never" }]] : "list",
  updateSnapshots: "none",
  expect: {
    toHaveScreenshot: {
      animations: "disabled",
      caret: "hide",
      scale: "css",
      // The per-pixel colour threshold absorbs anti-aliasing noise; beyond
      // it at most 10 pixels may differ. Measured on 2026-10-04: changing
      // only --jl-radius-md from 8px to 0 changes 18 to 370 pixels per
      // screenshot, so even that small change fails every page.
      threshold: 0.2,
      maxDiffPixels: 10,
    },
  },
  use: {
    baseURL: `http://127.0.0.1:${String(port)}`,
    locale: "en-US",
    timezoneId: "UTC",
    reducedMotion: "reduce",
    deviceScaleFactor: 1,
    // Never record video: no ffmpeg is installed (tools/manifest.json).
    video: "off",
    trace: "off",
    screenshot: "only-on-failure",
  },
  projects: [
    { name: "e2e", testMatch: /flows\.spec\.ts/, use: { ...devices["Desktop Chrome"], ...desktop, deviceScaleFactor: 1 } },
    { name: "desktop", testMatch: /visual\.spec\.ts/, use: { ...devices["Desktop Chrome"], ...desktop, deviceScaleFactor: 1 } },
    { name: "mobile", testMatch: /visual\.spec\.ts/, use: { ...devices["Desktop Chrome"], ...mobile, deviceScaleFactor: 1 } },
  ],
  webServer: {
    command: `${vite} build --logLevel warn --outDir ${outDir} --emptyOutDir && ${vite} preview --outDir ${outDir} --port ${String(port)} --strictPort --host 127.0.0.1`,
    url: `http://127.0.0.1:${String(port)}/login`,
    // vite.config.ts proxies /api to a local Jelee; point it at the discard
    // port so a request the fake API misses can never reach a real server.
    env: { JELEE_DEV_API: "http://127.0.0.1:9" },
    reuseExistingServer: false,
    timeout: 180_000,
  },
});
