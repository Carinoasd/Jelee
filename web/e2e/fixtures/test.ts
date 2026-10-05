// Shared Playwright fixtures: a fake API per page, a fixed clock, a fixed
// font and no motion, plus the checks every test ends with: no media element
// in the DOM, no request to a playback route and no unanswered API call.
import { test as base, expect, type Page } from "@playwright/test";
import { installFakeApi, type FakeApi, type FakeApiOptions } from "./api";
import { now } from "./data";

/**
 * Path segments of playback, streaming, subtitle/audio track delivery and
 * casting routes; mirrors scripts/check-no-playback.mjs for live requests.
 */
const playbackSegment = /^(?:play|player|playback|playing|now-playing|stream|streams|subtitles|audio|videos|cast|pip|picture-in-picture|theater|master\.m3u8|main\.m3u8)$/i;

export function isPlaybackRequest(path: string): boolean {
  return path.split("/").some((segment) => playbackSegment.test(segment)) || /\.(?:m3u8|mpd|ts|m4s)$/i.test(path);
}

/**
 * One installed font for every machine: system-ui resolves differently per
 * distribution, which would make the screenshots depend on the host. DejaVu
 * Sans ships with fontconfig on every Debian and Ubuntu image (CI included).
 */
export const stableStyle = `
html:root { --jl-font-family: "DejaVu Sans", sans-serif; }
*, *::before, *::after { caret-color: transparent !important; transition: none !important; animation: none !important; }
`;

export interface Fixtures {
  /** Options for the fake API; override with test.use({ apiOptions }). */
  apiOptions: FakeApiOptions;
  api: FakeApi;
}

export const test = base.extend<Fixtures>({
  apiOptions: [{}, { option: true }],
  // Automatic: no test may reach a real server, even one that never names api.
  api: [
    async ({ page, apiOptions }, use) => {
    await page.clock.setFixedTime(new Date(now));
    await page.addInitScript((css) => {
      const style = document.createElement("style");
      style.dataset.e2e = "stable";
      style.textContent = css;
      document.addEventListener("DOMContentLoaded", () => {
        document.head.append(style);
      });
    }, stableStyle);
    const api = await installFakeApi(page, apiOptions);
    const playback: string[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (isPlaybackRequest(url.pathname) || ["media"].includes(request.resourceType())) {
        playback.push(request.method() + " " + url.pathname);
      }
    });
    await use(api);
    // G27.4: no page may contain or request media playback.
    if (!page.isClosed()) {
      await expectNoMediaElements(page);
    }
    expect(playback, "requests to playback routes or media resources").toEqual([]);
    expect(api.unhandled, "API requests the fake server does not answer").toEqual([]);
    },
    { auto: true },
  ],
});

/** No <video> or <audio> element, also inside open shadow roots. */
export async function expectNoMediaElements(page: Page): Promise<void> {
  const count = await page.evaluate(() => {
    const walk = (root: Document | ShadowRoot): number =>
      root.querySelectorAll("video, audio").length +
      Array.from(root.querySelectorAll("*")).reduce((sum, element) => sum + (element.shadowRoot ? walk(element.shadowRoot) : 0), 0);
    return walk(document);
  });
  expect(count, "media elements in the DOM").toBe(0);
}

/** Waits until the view settled: no skeletons, fonts and images loaded. */
export async function settle(page: Page): Promise<void> {
  await page.waitForLoadState("networkidle");
  await expect(page.locator(".jl-skeleton, [aria-busy='true']")).toHaveCount(0);
  await page.evaluate(async () => {
    await document.fonts.ready;
    // Lazy images outside the viewport never load and are not in the shot.
    const visible = Array.from(document.images).filter((image) => {
      const box = image.getBoundingClientRect();
      return !image.complete && box.bottom > 0 && box.top < window.innerHeight;
    });
    await Promise.all(
      visible.map(
        (image) =>
          new Promise((resolve) => {
            image.addEventListener("load", resolve, { once: true });
            image.addEventListener("error", resolve, { once: true });
          }),
      ),
    );
  });
}

export { expect };
