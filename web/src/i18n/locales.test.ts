import { describe, expect, it } from "vitest";
import { buildMessages, createAppI18n, loadNamespace } from "./index";
import { negotiateLocale } from "./locales";

describe("negotiateLocale", () => {
  it.each([
    [[], "zh-CN"],
    [["fr-FR"], "en-US"],
    [["fr-FR", "ja"], "ja-JP"],
    [["zh-Hant-HK"], "zh-TW"],
    [["zh-HK"], "zh-TW"],
    [["zh"], "zh-CN"],
    [["zh-SG"], "zh-CN"],
    [["en-GB", "zh-CN"], "en-US"],
  ])("%j -> %s", (languages, expected) => {
    expect(negotiateLocale(languages)).toBe(expected);
  });
});

describe("buildMessages", () => {
  it("merges namespaces per locale and wraps a flat reserved core.json", () => {
    const messages = buildMessages({
      "./en-US/auth.json": { auth: { title: "Sign in" } },
      "./en-US/core.json": { Favorites: "Favorites" },
      "./xx-XX/auth.json": { auth: { title: "ignored" } },
    });
    expect(messages["en-US"]).toEqual({ auth: { title: "Sign in" }, core: { Favorites: "Favorites" } });
    expect(Object.keys(messages)).toEqual(["zh-CN", "zh-TW", "ja-JP", "en-US"]);
  });
});

describe("lazy namespaces", () => {
  it("merges a namespace into running instances in every locale and starts new ones with it", async () => {
    const running = createAppI18n("en-US");
    expect(running.global.te("networkRules.title")).toBe(false);
    // The eager catalogs never carry a lazy namespace.
    expect(running.global.te("common.appName")).toBe(true);
    await loadNamespace("networkRules");
    expect(running.global.t("networkRules.title")).toBe("Network rules");
    running.global.locale.value = "zh-TW";
    expect(running.global.t("networkRules.title")).toBe("網路規則");
    expect(createAppI18n("ja-JP").global.t("networkRules.title")).toBe("ネットワークルール");
  });
});
