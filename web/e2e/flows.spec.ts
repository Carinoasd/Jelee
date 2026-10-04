// End-to-end flows (G27.4, G34.5): keyboard-only browsing, the two-step
// confirmation of a dangerous action, the no-playback assertions on every key
// page and a dependency-free accessibility check. Every test also ends with
// the checks in fixtures/test.ts (no media element, no playback request, no
// unanswered API call).
import type { Locator, Page } from "@playwright/test";
import { detailItem, ids } from "./fixtures/data";
import { expect, expectNoMediaElements, settle, test } from "./fixtures/test";
import { visualPages } from "./fixtures/pages";

/** Presses Tab (or Shift+Tab) until target has focus; fails after max presses. */
async function tabTo(page: Page, target: Locator, { backwards = false, max = 60 } = {}): Promise<void> {
  for (let presses = 0; presses < max; presses++) {
    if (await target.evaluate((element) => element === document.activeElement)) {
      return;
    }
    await page.keyboard.press(backwards ? "Shift+Tab" : "Tab");
  }
  throw new Error("not reachable by keyboard within " + String(max) + " presses: " + target.toString());
}

/** The visible focus indicator: an outline or box shadow on the focused element. */
async function expectVisibleFocus(target: Locator): Promise<void> {
  const indicator = await target.evaluate((element) => {
    const style = getComputedStyle(element);
    return (style.outlineStyle !== "none" && Number.parseFloat(style.outlineWidth) > 0) || style.boxShadow !== "none";
  });
  expect(indicator, "focus indicator on " + target.toString()).toBe(true);
}

test.describe("keyboard", () => {
  test.use({ apiOptions: { signedIn: false } });

  test("signs in, opens a library and an item and goes back without a mouse", async ({ page, api }) => {
    await page.goto("/login");
    const name = page.getByLabel("User name");
    await tabTo(page, name);
    await expectVisibleFocus(name);
    await page.keyboard.type("admin");
    await page.keyboard.press("Tab");
    await expect(page.getByLabel("Password")).toBeFocused();
    await page.keyboard.type("not-a-real-password");
    await page.keyboard.press("Enter");

    await expect(page).toHaveURL(/\/libraries$/);
    // Navigation moves focus to the new page's heading.
    await expect(page.locator("#main h1")).toBeFocused();
    const movies = page.getByRole("link", { name: /Movies/ });
    await tabTo(page, movies);
    await expectVisibleFocus(movies);
    await page.keyboard.press("Enter");

    await expect(page).toHaveURL(new RegExp("/libraries/" + ids.movies));
    await expect(page.locator("#main h1")).toHaveText("Movies");
    await expect(page.locator("#main h1")).toBeFocused();
    const item = page.getByRole("link", { name: detailItem.title });
    await tabTo(page, item);
    await page.keyboard.press("Enter");

    await expect(page).toHaveURL(new RegExp("/items/" + detailItem.id));
    await expect(page.locator("#main h1")).toHaveText(detailItem.title);
    // Back through the breadcrumb. The detail heading renders only with its
    // data, so focus is not on it here (see the known-gap test below).
    const crumb = page.getByRole("link", { name: "Movies", exact: true });
    await tabTo(page, crumb);
    await expectVisibleFocus(crumb);
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(new RegExp("/libraries/" + ids.movies));
    await expect(page.locator("#main h1")).toBeFocused();
    // And with the browser's history.
    await page.goBack();
    await expect(page).toHaveURL(new RegExp("/items/" + detailItem.id));

    expect(api.writes.map((write) => write.method + " " + write.path)).toEqual(["POST /api/v1/auth/login"]);
  });

  test("skip link moves focus to the main content", async ({ page }) => {
    await page.goto("/login");
    await expect(page.locator("#main h1")).toBeVisible();
    await page.keyboard.press("Tab");
    const skip = page.locator(".jl-skip-link");
    await expect(skip).toBeFocused();
    await expect(skip).toBeVisible();
    await page.keyboard.press("Enter");
    await expect(page.locator("#main")).toBeFocused();
  });
});

test.describe("known gaps", () => {
  // G34.5 finding: App.vue focuses "#main h1" one tick after navigation, but
  // the item detail heading only renders once the item has loaded, so focus
  // stays on the document. Expected to fail until the focus handling waits
  // for the heading; Playwright then reports an unexpected pass.
  test("focus moves to a heading that renders with its data", async ({ page }) => {
    test.fail();
    await page.goto("/libraries/" + ids.movies);
    await expect(page.locator("#main h1")).toHaveText("Movies");
    await page.getByRole("link", { name: detailItem.title }).press("Enter");
    await expect(page.locator("#main h1")).toHaveText(detailItem.title);
    await expect(page.locator("#main h1")).toBeFocused({ timeout: 2000 });
  });
});

test.describe("two-step confirmation", () => {
  test("deleting a webhook needs a second, explicit press", async ({ page, api }) => {
    await page.goto("/admin/webhooks");
    await settle(page);
    const deletes = () => api.writes.filter((write) => write.method === "DELETE");

    await page.getByRole("button", { name: "Delete", exact: true }).click();
    const confirm = page.getByRole("button", { name: "Delete webhook" });
    // The first press only opens the prompt and moves focus to the confirm button.
    await expect(confirm).toBeFocused();
    await expect(page.getByRole("alert").filter({ hasText: "cannot be undone" })).toBeVisible();
    expect(deletes()).toEqual([]);

    // Escape backs out and returns focus to the trigger.
    await page.keyboard.press("Escape");
    await expect(confirm).toBeHidden();
    await expect(page.getByRole("button", { name: "Delete", exact: true })).toBeFocused();
    expect(deletes()).toEqual([]);

    // Cancel backs out as well.
    await page.keyboard.press("Enter");
    await page.getByRole("button", { name: "Cancel" }).click();
    expect(deletes()).toEqual([]);

    // Only the second press sends the request.
    await page.getByRole("button", { name: "Delete", exact: true }).press("Enter");
    await page.getByRole("button", { name: "Delete webhook" }).press("Enter");
    await expect.poll(() => deletes().map((write) => write.path)).toEqual(["/api/v1/webhooks/" + ids.webhook]);
  });
});

/** Problems a dependency-free scan finds; see docs/frontend-adr.md. */
async function accessibilityProblems(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const problems: string[] = [];
    const visible = (element: Element) => {
      const style = getComputedStyle(element);
      const box = element.getBoundingClientRect();
      return style.visibility !== "hidden" && style.display !== "none" && box.width > 0 && box.height > 0;
    };
    const describe = (element: Element) =>
      element.tagName.toLowerCase() + (element.id ? "#" + element.id : "") + " " + JSON.stringify((element.textContent || "").trim().slice(0, 40));
    const named = (element: Element) => {
      const labelledby = element.getAttribute("aria-labelledby");
      return (
        (element.getAttribute("aria-label") ?? "").trim() !== "" ||
        (labelledby !== null && labelledby.split(/\s+/).some((id) => (document.getElementById(id)?.textContent ?? "").trim() !== "")) ||
        ("labels" in element && ((element as HTMLInputElement).labels?.length ?? 0) > 0) ||
        (element.getAttribute("title") ?? "").trim() !== ""
      );
    };
    if ((document.documentElement.getAttribute("lang") ?? "") === "") {
      problems.push("html without lang");
    }
    if (document.querySelectorAll("#main h1").length !== 1) {
      problems.push("main content needs exactly one h1");
    }
    for (const element of document.querySelectorAll("input:not([type=hidden]), select, textarea")) {
      if (visible(element) && !named(element)) {
        problems.push("form control without a label: " + describe(element));
      }
    }
    for (const element of document.querySelectorAll("button, a[href], [role=button], [role=link], [role=tab]")) {
      const text = (element.textContent || "").trim() !== "" || Array.from(element.querySelectorAll("img[alt]")).some((image) => image.getAttribute("alt") !== "");
      if (visible(element) && !text && !named(element)) {
        problems.push("control without an accessible name: " + describe(element));
      }
    }
    for (const image of document.querySelectorAll("img:not([alt])")) {
      problems.push("image without alt: " + describe(image));
    }
    const seen = new Set<string>();
    for (const element of document.querySelectorAll("[id]")) {
      if (seen.has(element.id)) {
        problems.push("duplicate id " + element.id);
      }
      seen.add(element.id);
    }
    return problems;
  });
}

/** Text whose contrast against its background is below WCAG 2.1 AA (1.4.3). */
async function contrastProblems(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const parse = (value: string): [number, number, number, number] | null => {
      const match = /rgba?\(([\d.]+),?\s*([\d.]+),?\s*([\d.]+)(?:,?\s*\/?\s*([\d.]+%?))?\)/.exec(value);
      if (match === null) {
        return null;
      }
      const alpha = match[4] === undefined ? 1 : match[4].endsWith("%") ? Number.parseFloat(match[4]) / 100 : Number.parseFloat(match[4]);
      return [Number(match[1]), Number(match[2]), Number(match[3]), alpha];
    };
    const luminance = ([r, g, b]: number[]) => {
      const channel = (value: number) => {
        const c = value / 255;
        return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
      };
      return 0.2126 * channel(r ?? 0) + 0.7152 * channel(g ?? 0) + 0.0722 * channel(b ?? 0);
    };
    const blend = (top: number[], bottom: number[]) => {
      const alpha = top[3] ?? 1;
      return [0, 1, 2].map((i) => (top[i] ?? 0) * alpha + (bottom[i] ?? 0) * (1 - alpha));
    };
    /** The opaque background behind element, or null over images or gradients. */
    const background = (element: Element | null): number[] | null => {
      const layers: number[][] = [];
      for (let current = element; current !== null; current = current.parentElement) {
        const style = getComputedStyle(current);
        if (style.backgroundImage !== "none") {
          return null;
        }
        const color = parse(style.backgroundColor);
        if (color !== null && color[3] > 0) {
          layers.push(color);
          if (color[3] >= 1) {
            break;
          }
        }
      }
      return layers.reduceRight<number[]>((under, layer) => blend(layer, under), [255, 255, 255]);
    };
    const problems: string[] = [];
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    const checked = new Set<Element>();
    for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
      const element = node.parentElement;
      if (element === null || checked.has(element) || (node.textContent ?? "").trim() === "") {
        continue;
      }
      checked.add(element);
      const style = getComputedStyle(element);
      const box = element.getBoundingClientRect();
      if (style.visibility === "hidden" || box.width === 0 || box.height === 0 || element.closest("[disabled], [aria-hidden=true], .jl-visually-hidden") !== null) {
        continue;
      }
      const color = parse(style.color);
      const behind = background(element);
      if (color === null || behind === null) {
        continue;
      }
      const front = blend([...color.slice(0, 3), color[3] * Number.parseFloat(style.opacity)], behind);
      const [light, dark] = [luminance(front), luminance(behind)].sort((a, b) => b - a) as [number, number];
      const ratio = (light + 0.05) / (dark + 0.05);
      const size = Number.parseFloat(style.fontSize);
      const large = size >= 24 || (size >= 18.66 && Number(style.fontWeight) >= 700);
      if (ratio < (large ? 3 : 4.5)) {
        problems.push(ratio.toFixed(2) + ":1 " + JSON.stringify((node.textContent ?? "").trim().slice(0, 40)) + " (" + style.color + " on rgb(" + behind.map(Math.round).join(", ") + "))");
      }
    }
    return problems;
  });
}

for (const scheme of ["light", "dark"] as const) {
  test.describe(`key pages ${scheme}`, () => {
    test.use({ colorScheme: scheme });
    for (const entry of visualPages) {
      test.describe(entry.name, () => {
        test.use({ apiOptions: entry.api ?? {} });
        test(`${entry.name} ${scheme}: no playback, labelled, AA contrast`, async ({ page }) => {
          await page.goto(entry.path);
          await expect(page.locator("#main h1").first()).toBeVisible();
          await settle(page);
          await expectNoMediaElements(page);
          expect(await page.locator("a[href*='play' i], a[href*='stream' i], [data-action*='play' i]").count(), "playback entry points").toBe(0);
          expect(await accessibilityProblems(page)).toEqual([]);
          expect(await contrastProblems(page)).toEqual([]);
        });
      });
    }
  });
}
