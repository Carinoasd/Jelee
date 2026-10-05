import { flushPromises } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createApp } from "vue";
import { createMemoryHistory } from "vue-router";
import App from "@/App.vue";
import { installAppPlugins } from "@/plugins";
import { officialPlugins } from "@/plugins/host/catalog";
import { usePluginStore } from "@/plugins/host/store";
import { useToastStore } from "@/stores/toasts";
import { createFakeServer, type FakeServer } from "@/test/fakeServer";
import { mountView, unmountAll } from "@/test/mountView";
import { fakeBundle, providePlugins, sameMessages, testManifest } from "@/test/plugins";
import { mount } from "@vue/test-utils";

const movieId = "20000000-0000-4000-8000-000000000001";
const libraryId = "10000000-0000-4000-8000-00000000000a";

function seed(server: FakeServer) {
  server.cookie = true;
  server.libraries = [{ id: libraryId, name: "Movies", roots: 1 }];
  server.items = [{ id: movieId, libraryId, kind: "Movie", title: "Arrival" }];
  server.details[movieId] = {
    originalTitle: "Story of Your Life",
    productionYear: 2016,
    overview: "Linguist meets heptapods.",
    genres: ["Drama"],
    externalIds: [
      { type: "tmdb", value: "329865", default: true },
      { type: "imdb", value: "tt2543164", default: false },
      { type: "imdb", value: "javascript:alert(1)", default: false },
    ],
  };
  server.sources[movieId] = [
    {
      id: "40000000-0000-4000-8000-000000000001",
      primary: false,
      container: "mkv",
      contentType: "video/x-matroska",
      probed: true,
      sizeBytes: 2 * 1024 ** 3,
      version: { displayName: "", qualityScore: 1 },
      videoTracks: [{ index: 0, codec: "hevc", width: 3840, height: 2160, default: true, primary: true }],
      audioTracks: [],
      subtitleTracks: [],
      externalTracks: [],
    },
  ];
}

let server: FakeServer;
let sheets: CSSStyleSheet[];
beforeEach(() => {
  server = createFakeServer();
  seed(server);
  // jsdom lacks adoptedStyleSheets; give the document a working one.
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

function enable(...ids: string[]) {
  localStorage.setItem("jelee.ui.v1.plugin-host.state", JSON.stringify({ enabled: Object.fromEntries(ids.map((id) => [id, true])), order: [] }));
}

const tokenCss = () => sheets.map((sheet) => [...sheet.cssRules].map((rule) => rule.cssText).join("\n")).join("\n");

describe("item facts example", () => {
  // The item page loads its version panels lazily; they ask for the
  // versions (the fake server answers 404) at a moment that depends on the
  // load. Waiting for that request keeps it out of the assertions below.
  async function versionPanelsSettled() {
    await vi.waitFor(() => {
      expect(server.requests.some((request) => new URL(request.url).pathname.endsWith("/versions"))).toBe(true);
    });
    await flushPromises();
  }

  it("adds a facts tab that reads the file summary through the restricted API on request", async () => {
    const { wrapper } = await mountView("/items/" + movieId, { fetch: server.fetch, user: server.user, plugins: [providePlugins(officialPlugins())] });
    await vi.waitFor(() => {
      expect(wrapper.find('[role="tab"]').text()).toBe("Facts");
    });
    await vi.waitFor(() => {
      expect(wrapper.text()).toContain("5 of 6 present");
    });
    await versionPanelsSettled();
    // External links are opt-in; nothing leaves the site by default.
    expect(wrapper.findAll('[role="tabpanel"] a')).toHaveLength(0);
    const before = server.requests.length;
    const load = wrapper.findAll('[role="tabpanel"] button').find((button) => button.text() === "Load file summary")!;
    await load.trigger("click");
    await flushPromises();
    expect(server.requests.slice(before).map((request) => request.method + " " + new URL(request.url).pathname)).toEqual([
      "GET /api/v1/items/" + movieId + "/sources",
    ]);
    expect(wrapper.text()).toContain("1 versions, 2 GB in total");
    expect(wrapper.text()).toContain("Highest resolution 3840×2160");
  });

  it("links only well-formed IDs once the setting is on", async () => {
    localStorage.setItem("jelee.ui.v1.plugin-settings.jelee.item-facts", JSON.stringify({ externalLinks: true }));
    const { wrapper } = await mountView("/items/" + movieId, { fetch: server.fetch, user: server.user, plugins: [providePlugins(officialPlugins())] });
    await vi.waitFor(() => {
      expect(wrapper.findAll('[role="tabpanel"] a')).toHaveLength(2);
    });
    const links = wrapper.findAll('[role="tabpanel"] a');
    expect(links.map((link) => link.attributes("href"))).toEqual(["https://www.themoviedb.org/movie/329865", "https://www.imdb.com/title/tt2543164/"]);
    for (const link of links) {
      expect(link.attributes("rel")).toBe("noopener noreferrer");
    }
  });

  it("copies the item ID, and a missing clipboard only produces an error toast", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    const { wrapper, pinia } = await mountView("/items/" + movieId, { fetch: server.fetch, user: server.user, plugins: [providePlugins(officialPlugins())] });
    await vi.waitFor(() => {
      expect(wrapper.find('[role="group"] button').text()).toBe("Copy item ID");
    });
    await versionPanelsSettled();
    await wrapper.find('[role="group"] button').trigger("click");
    await flushPromises();
    expect(writeText).toHaveBeenCalledWith(movieId);
    const toasts = useToastStore(pinia);
    // The lazily loaded version panels may add their own toasts (the fake
    // server has no version routes) at any moment; look at the plugin's only.
    const pluginToasts = () => toasts.toasts.filter((toast) => toast.key.startsWith("plugins."));
    expect(pluginToasts().at(-1)).toMatchObject({ key: "plugins.notice", tone: "success", params: { message: "Item ID copied." } });

    Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
    await wrapper.find('[role="group"] button').trigger("click");
    await flushPromises();
    expect(pluginToasts().at(-1)).toMatchObject({ key: "plugins.actionFailed", tone: "danger" });
    Reflect.deleteProperty(navigator, "clipboard");
  });

  it("stores its setting from the settings page section", async () => {
    const { wrapper } = await mountView("/settings", { fetch: server.fetch, user: server.user, plugins: [providePlugins(officialPlugins())] });
    await vi.waitFor(() => {
      expect(wrapper.find('[data-plugin="jelee.item-facts"] input[type="checkbox"]').exists()).toBe(true);
    });
    expect(wrapper.find('[data-plugin="jelee.item-facts"] h2').text()).toBe("Item facts");
    await wrapper.find('[data-plugin="jelee.item-facts"] input[type="checkbox"]').setValue(true);
    expect(JSON.parse(localStorage.getItem("jelee.ui.v1.plugin-settings.jelee.item-facts")!)).toEqual({ externalLinks: true });
  });
});

describe("accent token example", () => {
  it("applies validated token overrides as a constructed sheet and follows its setting", async () => {
    enable("jelee.accent-tokens");
    const { wrapper, pinia } = await mountView("/settings", { fetch: server.fetch, user: server.user, plugins: [providePlugins(officialPlugins())] });
    const store = usePluginStore(pinia);
    await store.settled();
    await flushPromises();
    expect(tokenCss()).toContain("--jl-color-primary: #0f766e");
    expect(tokenCss()).toContain("--jl-color-primary: #5eead4");
    expect(document.head.querySelector("style")).toBeNull();

    await vi.waitFor(() => {
      expect(wrapper.find('[data-plugin="jelee.accent-tokens"] input[value="violet"]').exists()).toBe(true);
    });
    await wrapper.find('[data-plugin="jelee.accent-tokens"] input[value="violet"]').setValue(true);
    await flushPromises();
    expect(tokenCss()).toContain("--jl-color-primary: #6d28d9");

    store.setEnabled("jelee.accent-tokens", false);
    await flushPromises();
    expect(tokenCss()).toBe("");
  });

  it("drops token values and names that fail validation", async () => {
    const hostile = fakeBundle(testManifest("vendor.tokens", { hooks: ["theme.token"], permissions: ["ui.theme"] }), {
      messages: sameMessages({}),
      setup(context) {
        context.register("theme.token", {
          id: "evil",
          tokens: () =>
            ({
              "color-primary": "red;}body{display:none",
              "color-focus": "url(https://evil.example/x)",
              "color-text": "expression(alert(1))",
              "z-index": "9999",
              "color-border": "#123456",
            }) as never,
        });
      },
    });
    const { pinia } = await mountView("/settings", { fetch: server.fetch, user: server.user, plugins: [providePlugins([hostile])] });
    await usePluginStore(pinia).settled();
    await flushPromises();
    const css = tokenCss();
    expect(css).toContain("--jl-color-border: #123456");
    expect(css).not.toMatch(/display|evil|expression|z-index|--jl-color-primary|--jl-color-focus|--jl-color-text:/);
  });

  it("registers its page while enabled; a deep link resolves after plugins load", async () => {
    enable("jelee.accent-tokens");
    const host = createApp(App);
    const plugins = installAppPlugins(host, { languages: ["en-US"], fetch: server.fetch, history: createMemoryHistory(), plugins: { bundles: officialPlugins() } });
    await plugins.router.push("/x/jelee.accent-tokens/preview");
    await plugins.router.isReady();
    const wrapper = mount(App, { global: { plugins: [plugins.router, plugins.i18n, plugins.pinia], provide: host._context.provides }, attachTo: document.body });
    // The signed-in user's locale (ja-JP) applies to plugin messages too.
    await vi.waitFor(() => {
      expect(wrapper.find("h1").text()).toBe("デザイントークンのプレビュー");
    });
    expect(plugins.router.currentRoute.value.path).toBe("/x/jelee.accent-tokens/preview");
    await vi.waitFor(() => {
      expect(wrapper.text()).toContain("サンプルボタン");
    });

    const store = await plugins.extensions;
    store.setEnabled("jelee.accent-tokens", false);
    await flushPromises();
    expect(plugins.router.getRoutes().some((route) => route.path.startsWith("/x/"))).toBe(false);
    await plugins.router.push("/x/jelee.accent-tokens/preview");
    await flushPromises();
    expect(plugins.router.currentRoute.value.name).toBe("not-found");
    wrapper.unmount();
  });
});
