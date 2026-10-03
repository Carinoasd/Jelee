import { describe, expect, it } from "vitest";
import { buildMessages } from "./index";
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
