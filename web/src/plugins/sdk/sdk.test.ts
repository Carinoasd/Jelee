import { describe, expect, it } from "vitest";
import accentManifest from "../official/accent-tokens/manifest.json";
import factsManifest from "../official/item-facts/manifest.json";
import { isPluginRoutePath } from "./hooks";
import { validateManifest, type ManifestResult } from "./manifest";
import { compareVersions, parseRange, parseVersion, satisfies } from "./semver";
import { SDK_VERSION } from "./version";

const environment = { jeleeVersion: "0.1.0" };

function manifest(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: "example.sample",
    name: { "zh-CN": "示例", "zh-TW": "範例", "ja-JP": "サンプル", "en-US": "Sample" },
    description: { "zh-CN": "示例", "zh-TW": "範例", "ja-JP": "サンプル", "en-US": "Sample" },
    version: "1.2.3",
    sdkVersion: "^1.0.0",
    minJeleeVersion: "0.1.0",
    permissions: [],
    hooks: ["media.detail.tabs"],
    dependencies: {},
    entry: "./index.ts",
    ...overrides,
  };
}

function codes(result: ManifestResult): string[] {
  return result.ok ? [] : result.issues.map((issue) => issue.code);
}

describe("semver", () => {
  it("parses strict versions only", () => {
    expect(parseVersion("1.2.3-beta.1+build")).toEqual({ major: 1, minor: 2, patch: 3, prerelease: ["beta", "1"] });
    for (const bad of ["1.2", "01.2.3", "1.2.3.4", "v1.2.3", "", "1.2.x"]) {
      expect(parseVersion(bad)).toBeNull();
    }
  });

  it("orders prereleases before releases", () => {
    const v = (text: string) => parseVersion(text)!;
    expect(compareVersions(v("1.0.0-alpha"), v("1.0.0"))).toBe(-1);
    expect(compareVersions(v("1.0.0-alpha.2"), v("1.0.0-alpha.10"))).toBe(-1);
    expect(compareVersions(v("1.0.0-alpha"), v("1.0.0-alpha.1"))).toBe(-1);
    expect(compareVersions(v("2.0.0"), v("1.9.9"))).toBe(1);
    expect(compareVersions(v("1.0.0"), v("1.0.0+meta"))).toBe(0);
  });

  it.each([
    ["1.4.0", "^1.0.0", true],
    ["2.0.0", "^1.0.0", false],
    ["2.0.0-rc.1", "^1.0.0", false],
    ["0.2.5", "^0.2.0", true],
    ["0.3.0", "^0.2.0", false],
    ["0.0.3", "^0.0.3", true],
    ["0.0.4", "^0.0.3", false],
    ["1.2.9", "~1.2.0", true],
    ["1.3.0", "~1.2.0", false],
    ["1.9.0", "1.x", true],
    ["1.0.0", ">=1.0.0 <2.0.0", true],
    ["2.0.0", ">=1.0.0 <2.0.0", false],
    ["3.1.0", "^1.0.0 || ^3.0.0", true],
    ["1.0.0", "*", true],
    ["1.1.0", ">1.0", true],
    ["1.0.5", ">1.0", false],
    ["1.0.0-beta.2", ">=1.0.0-beta.1", true],
    ["1.0.1-beta.2", ">=1.0.0-beta.1", false],
  ])("%s satisfies %s: %s", (version, range, expected) => {
    expect(satisfies(version, range)).toBe(expected);
  });

  it("rejects malformed ranges", () => {
    for (const bad of ["", "^", "1.x.3", ">=abc", "~>1.0", "1.0.0 ||"]) {
      expect(parseRange(bad)).toBeNull();
    }
  });
});

describe("manifest validation (G32.2)", () => {
  it("accepts both official manifests against this SDK", () => {
    for (const raw of [factsManifest, accentManifest]) {
      const result = validateManifest(raw, environment);
      expect(result.ok, JSON.stringify(result)).toBe(true);
    }
  });

  it("returns a frozen manifest", () => {
    const result = validateManifest(manifest(), environment);
    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(Object.isFrozen(result.manifest)).toBe(true);
      expect(Object.isFrozen(result.manifest.name)).toBe(true);
    }
  });

  it("refuses an SDK range that excludes this host and says which versions", () => {
    const result = validateManifest(manifest({ sdkVersion: "^2.0.0" }), environment);
    expect(result.ok).toBe(false);
    expect(result.ok ? [] : result.issues).toContainEqual({ code: "sdk_incompatible", field: "sdkVersion", params: { required: "^2.0.0", current: SDK_VERSION } });
  });

  it("refuses a plugin that needs a newer Jelee", () => {
    const result = validateManifest(manifest({ minJeleeVersion: "0.2.0" }), environment);
    expect(result.ok ? [] : result.issues).toContainEqual({ code: "jelee_too_old", field: "minJeleeVersion", params: { required: "0.2.0", current: "0.1.0" } });
  });

  it.each<[string, Record<string, unknown>, string]>([
    ["unknown permission", { permissions: ["network.any"] }, "unknown_permission"],
    ["duplicate permission", { permissions: ["catalog.read", "catalog.read"] }, "invalid_permissions"],
    ["unknown hook", { hooks: ["player.controls"] }, "unknown_hook"],
    ["no hooks", { hooks: [] }, "invalid_hooks"],
    ["hook without its permission", { hooks: ["route.register"] }, "hook_needs_permission"],
    ["theme hook without permission", { hooks: ["theme.token"] }, "hook_needs_permission"],
    ["bad id", { id: "Sample" }, "invalid_id"],
    ["single-segment id", { id: "sample" }, "invalid_id"],
    ["missing locale", { name: { "zh-CN": "x", "zh-TW": "x", "en-US": "x" } }, "invalid_text"],
    ["empty text", { description: { "zh-CN": " ", "zh-TW": "x", "ja-JP": "x", "en-US": "x" } }, "invalid_text"],
    ["bad version", { version: "1.0" }, "invalid_version"],
    ["bad sdk range", { sdkVersion: "latest" }, "invalid_sdk_range"],
    ["bad min version", { minJeleeVersion: ">=0.1.0" }, "invalid_min_jelee"],
    ["entry outside the plugin", { entry: "../host/store.ts" }, "invalid_entry"],
    ["remote entry", { entry: "https://cdn.example/x.js" }, "invalid_entry"],
    ["self dependency", { dependencies: { "example.sample": "^1.0.0" } }, "invalid_dependencies"],
    ["bad dependency range", { dependencies: { "example.other": "whatever" } }, "invalid_dependencies"],
    ["unknown field", { enabled: true }, "unknown_field"],
    ["empty author", { author: "" }, "invalid_author"],
  ])("rejects %s", (_name, overrides, code) => {
    expect(codes(validateManifest(manifest(overrides), environment))).toContain(code);
  });

  it("rejects non-objects", () => {
    expect(codes(validateManifest(null, environment))).toEqual(["not_object"]);
    expect(codes(validateManifest([], environment))).toEqual(["not_object"]);
    expect(codes(validateManifest("{}", environment))).toEqual(["not_object"]);
  });

  it("accepts a hook once its permission is declared", () => {
    expect(validateManifest(manifest({ hooks: ["route.register"], permissions: ["ui.routes"] }), environment).ok).toBe(true);
  });

  it("warns, but loads, when a declared hook is deprecated (G32.6)", () => {
    const deprecations = { "media.detail.tabs": { since: "1.4.0", removedIn: "2.0.0", replacement: "metadata.panel" } };
    const result = validateManifest(manifest(), { ...environment, deprecations });
    expect(result.ok).toBe(true);
    expect(result.ok ? result.warnings : []).toEqual([{ code: "hook_deprecated", hook: "media.detail.tabs", deprecation: deprecations["media.detail.tabs"] }]);
  });
});

describe("plugin route paths", () => {
  it("accepts lowercase segments", () => {
    expect(isPluginRoutePath("preview")).toBe(true);
    expect(isPluginRoutePath("tools/token-preview")).toBe(true);
  });

  it.each(["", "Preview", "a//b", "../admin", "a/b/c/d/e", "play", "now-playing", "x/stream", "watch", "pip", "a?b"])("rejects %s", (path) => {
    expect(isPluginRoutePath(path)).toBe(false);
  });
});
