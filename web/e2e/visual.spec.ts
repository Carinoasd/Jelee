// Visual regression (G34.6): key pages in the light and dark theme, at the
// desktop and mobile widths of the "desktop" and "mobile" projects. Baselines
// live in e2e/__screenshots__ and change only through make web-visual-update,
// after a person has looked at the new images.
import { visualPages } from "./fixtures/pages";
import { expect, settle, test } from "./fixtures/test";

for (const scheme of ["light", "dark"] as const) {
  test.describe(scheme, () => {
    test.use({ colorScheme: scheme });
    for (const entry of visualPages) {
      test.describe(entry.name, () => {
        test.use({ apiOptions: entry.api ?? {} });
        test(`${entry.name} ${scheme}`, async ({ page, api }) => {
          await page.goto(entry.path);
          await expect(page.locator("#main h1").first()).toBeVisible();
          if (api.options.devMode) {
            await expect(page.getByTestId("devmode-banner")).toBeVisible();
          }
          await settle(page);
          await expect(page).toHaveScreenshot(`${entry.name}-${scheme}.png`);
        });
      });
    }
  });
}
