import type { PluginContext } from "@jelee/plugin-sdk";
import { flushPromises } from "@vue/test-utils";
import { createPinia } from "pinia";
import { createApp } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { apiKey } from "@/api";
import { createApiClient } from "@/api/client";
import type { components } from "@/api/schema";
import { createAppI18n } from "@/i18n";
import { readServerVersion } from "@/plugins/host/start";
import { pluginHostKey, serverVersionKey, usePluginStore } from "@/plugins/host/store";
import { routes } from "@/router/routes";
import { useAppearanceStore } from "@/stores/appearance";
import { useAuthStore } from "@/stores/auth";
import { builtInPresets, toUserLayout, useLayoutStore } from "@/stores/layout";
import { useToastStore } from "@/stores/toasts";
import { captureDownloads } from "@/test/downloads";
import { createFakeServer, createFakeSite, type FakeServer, type FakeSite } from "@/test/fakeServer";
import { mountView, unmountAll } from "@/test/mountView";
import { fakeBundle, sameMessages, testManifest, type FakeBundle } from "@/test/plugins";

type User = components["schemas"]["User"];

let server: FakeServer;
let site: FakeSite;
let sheets: CSSStyleSheet[];

beforeEach(() => {
  server = createFakeServer();
  server.cookie = true;
  site = createFakeSite();
  server.site = site;
  sheets = [];
  Object.defineProperty(document, "adoptedStyleSheets", {
    configurable: true,
    get: () => sheets,
    set: (value: CSSStyleSheet[]) => {
      sheets = value;
    },
  });
});

afterEach(() => {
  unmountAll();
  localStorage.clear();
  Reflect.deleteProperty(document, "adoptedStyleSheets");
});

const appliedCss = () => sheets.map((sheet) => [...sheet.cssRules].map((rule) => rule.cssText).join("\n")).join("\n");
const paths = (method: string) => server.requests.filter((request) => request.method === method).map((request) => new URL(request.url).pathname);
const bodies = async (method: string, path: string) =>
  Promise.all(server.requests.filter((request) => request.method === method && new URL(request.url).pathname === path).map((request) => request.clone().json() as Promise<unknown>));

function regular(): User {
  return { ...server.user, id: "00000000-0000-4000-8000-0000000000aa", name: "viewer", admin: false };
}

/** Runs stores in a bare app context with a signed-in user. */
function host(user: User | null, bundles: readonly FakeBundle[] = [], version?: string) {
  const app = createApp({ render: () => null });
  const api = createApiClient({ fetch: server.fetch, baseUrl: "http://localhost" });
  api.auth.resume({ csrf: server.csrf });
  app.provide(apiKey, api);
  app.use(createPinia());
  app.use(createAppI18n("en-US"));
  app.use(createRouter({ history: createMemoryHistory(), routes }));
  app.provide(pluginHostKey, { bundles });
  if (version !== undefined) {
    app.provide(serverVersionKey, version);
  }
  app.runWithContext(() => {
    useAuthStore().user = user;
  });
  return { app, run: <T>(fn: () => T): T => app.runWithContext(fn) };
}

function settingsPlugin(id: string, contexts: Record<string, PluginContext>, enabledByDefault = true): FakeBundle {
  return fakeBundle(
    testManifest(id, { permissions: ["settings.storage"] }),
    {
      messages: sameMessages({}),
      setup: (context) => {
        contexts[id] = context;
      },
    },
    enabledByDefault,
  );
}

describe("site plugin configuration (G32.4)", () => {
  it("applies the server's order, enablement and settings, and stores an administrator's changes for everyone", async () => {
    site.plugins = {
      plugins: [
        { id: "vendor.b", enabled: true },
        { id: "vendor.a", enabled: false },
      ],
      settings: { "vendor.b": { color: "violet" } },
      revision: 4,
      updatedAt: "2026-10-01T00:00:00Z",
    };
    localStorage.setItem("jelee.ui.v1.plugin-host.state", JSON.stringify({ enabled: { "vendor.a": true }, order: ["vendor.a"] }));
    const contexts: Record<string, PluginContext> = {};
    const { run } = host(server.user, [settingsPlugin("vendor.a", contexts), settingsPlugin("vendor.b", contexts)]);
    const store = run(() => usePluginStore());
    await store.settled();
    await flushPromises();
    await store.settled();
    // The server wins over this browser's copy.
    expect(store.source).toBe("server");
    expect(store.order).toEqual(["vendor.b", "vendor.a"]);
    expect(store.plugins.find((plugin) => plugin.key === "vendor.a")?.status).toBe("disabled");
    expect(contexts["vendor.b"]!.settings.get("color", "teal")).toBe("violet");
    expect(paths("GET")).toContain("/api/v1/site/plugins/config");

    contexts["vendor.b"]!.settings.set("color", "amber");
    expect(contexts["vendor.b"]!.settings.get("color", "teal")).toBe("amber");
    store.setEnabled("vendor.a", true);
    await store.settled();
    expect(site.plugins.revision).toBeGreaterThanOrEqual(5);
    expect(site.plugins.settings).toEqual({ "vendor.b": { color: "amber" } });
    expect(site.plugins.plugins).toEqual([
      { id: "vendor.b", enabled: true },
      { id: "vendor.a", enabled: true },
    ]);
    // Every write carried the revision it was based on.
    const writes = (await bodies("PUT", "/api/v1/site/plugins")) as { revision: number }[];
    expect(writes.map((body) => body.revision)).toEqual(writes.map((_body, index) => 4 + index));
    expect(localStorage.getItem("jelee.ui.v1.plugin-settings.vendor.b")).toBeNull();
    expect(JSON.parse(localStorage.getItem("jelee.ui.v1.plugin-host.state")!)).toEqual({ enabled: { "vendor.a": true }, order: ["vendor.a"] });
  });

  it("lets other users read the site values and keeps their own changes in their browser", async () => {
    site.plugins = { plugins: [{ id: "vendor.a", enabled: true }], settings: { "vendor.a": { color: "violet", size: 2 } }, revision: 1, updatedAt: "2026-10-01T00:00:00Z" };
    const contexts: Record<string, PluginContext> = {};
    const { run } = host(regular(), [settingsPlugin("vendor.a", contexts)]);
    const store = run(() => usePluginStore());
    await store.settled();
    await flushPromises();
    expect(paths("GET")).toContain("/api/v1/site/plugins");
    expect(paths("GET")).not.toContain("/api/v1/site/plugins/config");
    expect(contexts["vendor.a"]!.settings.get("color", "teal")).toBe("violet");
    contexts["vendor.a"]!.settings.set("color", "amber");
    expect(contexts["vendor.a"]!.settings.get("color", "teal")).toBe("amber");
    expect(contexts["vendor.a"]!.settings.get("size", 0)).toBe(2);
    expect(contexts["vendor.a"]!.settings.keys()).toEqual(["color", "size"]);
    expect(paths("PUT")).toEqual([]);
    expect(JSON.parse(localStorage.getItem("jelee.ui.v1.plugin-settings.vendor.a")!)).toEqual({ color: "amber" });
  });

  it("reloads the server's state when a write is refused", async () => {
    site.plugins.revision = 2;
    const contexts: Record<string, PluginContext> = {};
    const { run } = host(server.user, [settingsPlugin("vendor.a", contexts)]);
    const store = run(() => usePluginStore());
    await store.settled();
    await flushPromises();
    // Another administrator saved in between.
    site.plugins = { plugins: [{ id: "vendor.a", enabled: true }], settings: { "vendor.a": { color: "violet" } }, revision: 3, updatedAt: "2026-10-01T00:00:00Z" };
    contexts["vendor.a"]!.settings.set("color", "amber");
    await store.settled();
    await flushPromises();
    await store.settled();
    expect(run(() => useToastStore()).toasts).toContainEqual(expect.objectContaining({ key: "plugins.saveFailed", tone: "danger" }));
    expect(contexts["vendor.a"]!.settings.get("color", "teal")).toBe("violet");
    expect(site.plugins.settings).toEqual({ "vendor.a": { color: "violet" } });
  });

  it("falls back to this browser when the server has no site settings", async () => {
    server.site = null;
    localStorage.setItem("jelee.ui.v1.plugin-host.state", JSON.stringify({ enabled: { "vendor.a": false }, order: [] }));
    const contexts: Record<string, PluginContext> = {};
    const { run } = host(server.user, [settingsPlugin("vendor.a", contexts)]);
    const store = run(() => usePluginStore());
    await store.settled();
    await flushPromises();
    expect(store.source).toBe("browser");
    expect(store.plugins[0]?.status).toBe("disabled");
    store.setEnabled("vendor.a", true);
    expect((JSON.parse(localStorage.getItem("jelee.ui.v1.plugin-host.state")!) as { enabled: unknown }).enabled).toEqual({ "vendor.a": true });
  });

  it("checks minJeleeVersion against the server's version", async () => {
    server.version = "2.3.0";
    const { app } = host(null);
    const client = app.runWithContext(() => createApiClient({ fetch: server.fetch, baseUrl: "http://localhost" }).client);
    expect(await readServerVersion(client)).toBe("2.3.0");
    server.version = "not a version";
    expect(await readServerVersion(client)).toBe("0.1.0");

    const modern = fakeBundle(testManifest("vendor.modern", { minJeleeVersion: "2.0.0" }), { messages: sameMessages({}), setup: () => undefined });
    const store = host(null, [modern], "2.3.0").run(() => usePluginStore());
    await store.settled();
    expect(store.version).toBe("2.3.0");
    expect(store.plugins[0]?.status).toBe("active");
    const old = host(null, [modern], "1.9.0").run(() => usePluginStore());
    expect(old.plugins[0]?.status).toBe("rejected");
  });
});

describe("site appearance (G33.2–G33.5)", () => {
  it("applies the server's sanitized CSS and token overrides to other users instead of this browser's copy", async () => {
    site.appearance = {
      ...site.appearance,
      customCss: "a { color: red } b { background: url(https://evil.example/x) }",
      tokens: { light: { "color-primary": "#123456" }, dark: { "color-primary": "#abcdef" } },
    };
    localStorage.setItem("jelee.ui.v1.admin-css.custom", JSON.stringify({ css: "i { color: green }" }));
    const { run } = host(regular());
    const store = run(() => useAppearanceStore());
    await flushPromises();
    expect(store.source).toBe("server");
    expect(store.config).toBeNull();
    expect(paths("GET")).not.toContain("/api/v1/site/appearance/config");
    expect(appliedCss()).toContain("color: red");
    expect(appliedCss()).toContain("--jl-color-primary: #123456");
    expect(appliedCss()).toContain("--jl-color-primary: #abcdef");
    expect(appliedCss()).not.toContain("green");
    expect(appliedCss()).not.toContain("evil");
  });

  it("keeps this browser's copy while signed out or when the server cannot be read", async () => {
    server.site = null;
    localStorage.setItem("jelee.ui.v1.admin-css.custom", JSON.stringify({ css: "i { color: green }" }));
    const { run } = host(regular());
    const store = run(() => useAppearanceStore());
    await flushPromises();
    expect(store.source).toBe("browser");
    expect(appliedCss()).toContain("color: green");
  });

  it("saves custom CSS for everyone, with the revision, and reports the server's refusals", async () => {
    const { wrapper } = await mountView("/admin/appearance", { fetch: server.fetch, user: server.user, csrf: server.csrf });
    await flushPromises();
    expect(wrapper.text()).toContain("Saved on the server for every signed-in user and device.");
    await wrapper.find("textarea").setValue("body { color: red; background: url(https://evil.example/x.png) }");
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(site.writes).toEqual(["appearance:update"]);
    expect(site.appearance.customCss).toContain("evil.example");
    expect(site.appearance.cssIssues.map((issue) => issue.code)).toEqual(["external_url"]);
    expect(appliedCss()).toContain("color: red");
    expect(appliedCss()).not.toContain("evil");
    expect(((await bodies("PUT", "/api/v1/site/appearance"))[0] as { revision: number }).revision).toBe(0);

    // The server refuses structural problems as a whole.
    await wrapper.find("textarea").setValue("</style><script>alert(1)</script>");
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(site.writes).toEqual(["appearance:update"]);
    expect(useToastStore().toasts).toContainEqual(expect.objectContaining({ key: "appearance.serverRejected" }));
    expect(appliedCss()).toContain("color: red");

    // Another administrator saved in between.
    site.appearance = { ...site.appearance, revision: 7 };
    await wrapper.find("textarea").setValue("body { color: blue }");
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(useToastStore().toasts).toContainEqual(expect.objectContaining({ key: "errors.conflict" }));
    expect(site.writes).toEqual(["appearance:update"]);
  });

  it("edits the site defaults: theme, token overrides and default layout", async () => {
    const { wrapper } = await mountView("/admin/appearance", { fetch: server.fetch, user: server.user, csrf: server.csrf });
    await flushPromises();
    const defaults = wrapper.find('[aria-labelledby="appearance-defaults"]');
    await defaults.find("select").setValue("dark");
    const [light, dark] = defaults.findAll("textarea");
    await light!.setValue("color-primary: #0f766e\nz-index: 3\ncolor-bg: url(/x.png)");
    expect(defaults.text()).toContain("Unknown names, unsafe or repeated values: z-index: 3, color-bg: url(/x.png)");
    await defaults.find("form").trigger("submit");
    await flushPromises();
    expect(site.writes).toEqual([]);

    await light!.setValue("--jl-color-primary: #0f766e");
    await dark!.setValue('font-family: "Noto Sans", sans-serif');
    await defaults.findAll("select")[1]!.setValue("focused");
    await defaults.find("form").trigger("submit");
    await flushPromises();
    expect(site.writes).toEqual(["appearance:update"]);
    expect(site.appearance.defaultTheme).toBe("dark");
    expect(site.appearance.tokens).toEqual({ light: { "color-primary": "#0f766e" }, dark: { "font-family": '"Noto Sans", sans-serif' } });
    expect(site.appearance.defaultLayout).toEqual(toUserLayout(builtInPresets.focused, []).current);
    expect(appliedCss()).toContain("--jl-color-primary: #0f766e");
  });

  it("exports, imports and resets the site settings", async () => {
    site.appearance = { ...site.appearance, customCss: "a { color: red }" };
    site.plugins = { plugins: [{ id: "vendor.a", enabled: false }], settings: {}, revision: 2, updatedAt: "2026-10-01T00:00:00Z" };
    const capture = captureDownloads();
    try {
      const { wrapper } = await mountView("/admin/appearance", { fetch: server.fetch, user: server.user, csrf: server.csrf });
      await flushPromises();
      const file = wrapper.find('[aria-labelledby="appearance-file"]');
      await file.findAll("button").find((button) => button.text() === "Export JSON")!.trigger("click");
      await flushPromises();
      expect(capture.downloads.map((download) => download.fileName)).toEqual(["jelee-site-settings.json"]);
      const exported = JSON.parse(await capture.downloads[0]!.blob.text()) as { format: string; appearance: { customCss: string }; plugins: { plugins: unknown[] } };
      expect(exported.format).toBe("jelee.site-settings");
      expect(exported.appearance.customCss).toBe("a { color: red }");
      expect(exported).not.toHaveProperty("appearance.revision");

      // Import a modified file.
      exported.appearance.customCss = "a { color: purple }";
      exported.plugins.plugins = [];
      const input = file.find('input[type="file"]');
      Object.defineProperty(input.element, "files", { configurable: true, value: [new File([JSON.stringify(exported)], "site.json", { type: "application/json" })] });
      await input.trigger("change");
      await flushPromises();
      expect(site.writes).toEqual(["appearance:import", "plugins:import"]);
      expect(appliedCss()).toContain("purple");
      expect(useToastStore().toasts).toContainEqual(expect.objectContaining({ key: "appearance.file.imported" }));

      Object.defineProperty(input.element, "files", { configurable: true, value: [new File(["{not json"], "bad.json")] });
      await input.trigger("change");
      await flushPromises();
      expect(useToastStore().toasts).toContainEqual(expect.objectContaining({ key: "appearance.file.invalid" }));
      expect(site.writes).toHaveLength(2);

      const reset = file.findAll("button").find((button) => button.text() === "Restore default appearance")!;
      await reset.trigger("click");
      await file.findAll("button").find((button) => button.text() === "Restore defaults")!.trigger("click");
      await flushPromises();
      expect(site.writes.at(-1)).toBe("appearance:reset");
      expect(appliedCss()).not.toContain("purple");
    } finally {
      capture.restore();
    }
  });
});

describe("account layout (G33.5)", () => {
  it("follows the account's stored layout and saves changes with the preferences", async () => {
    server.preferences = {
      theme: "dark",
      density: "compact",
      layout: { current: { home: [{ id: "latest", visible: true }, { id: "welcome", visible: false }], detail: [] }, presets: [{ id: "custom-1", name: "Mine", layout: { home: [], detail: [] } }] },
    };
    const { run } = host(server.user);
    const layout = run(() => useLayoutStore());
    await flushPromises();
    expect(layout.source).toBe("server");
    expect(layout.layout.home.map((entry) => entry.id)).toEqual(["latest", "welcome", "libraries"]);
    expect(layout.visibleIds("home")).toEqual(["latest", "libraries"]);
    expect(layout.presets.map((preset) => preset.name)).toEqual(["Mine"]);

    layout.setVisible("home", "welcome", true);
    await flushPromises();
    const sent = (await bodies("PUT", "/api/v1/users/me/preferences")) as { theme: string; density: string; layout: { current: { home: { id: string; visible: boolean }[] } } }[];
    expect(sent).toHaveLength(1);
    expect(sent[0]!.theme).toBe("dark");
    expect(sent[0]!.density).toBe("compact");
    expect(sent[0]!.layout.current.home).toContainEqual({ id: "welcome", visible: true });
    expect(server.preferences.layout?.presets).toHaveLength(1);
  });

  it("uses the site default layout until the user customizes it", async () => {
    site.appearance = { ...site.appearance, defaultLayout: toUserLayout(builtInPresets.metadata, []).current };
    const { run } = host(server.user);
    const layout = run(() => useLayoutStore());
    await flushPromises();
    expect(layout.source).toBe("site");
    expect(layout.layout.home.map((entry) => entry.id)).toEqual(builtInPresets.metadata.home.map((entry) => entry.id));
    layout.reset();
    await flushPromises();
    expect(layout.layout.detail.map((entry) => entry.id)).toEqual(builtInPresets.metadata.detail.map((entry) => entry.id));
  });

  it("keeps the layout in this browser while signed out", async () => {
    const { run } = host(null);
    const layout = run(() => useLayoutStore());
    await flushPromises();
    layout.setVisible("home", "welcome", false);
    await flushPromises();
    expect(paths("PUT")).toEqual([]);
    expect((JSON.parse(localStorage.getItem("jelee.ui.v1.page-layout.guest")!) as { layout: { home: unknown[] } }).layout.home).toContainEqual({ id: "welcome", visible: false });
  });
});
