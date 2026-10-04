/* eslint-disable vue/one-component-per-file -- small inline test components */
import { PluginApiError, PluginPermissionError, type PluginContext } from "@jelee/plugin-sdk";
import { flushPromises } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import { createFakeServer, type FakeServer } from "@/test/fakeServer";
import { mountView, unmountAll } from "@/test/mountView";
import { fakeBundle, pluginHost, providePlugins, sameMessages, testManifest } from "@/test/plugins";
import { useToastStore } from "@/stores/toasts";
import { officialPlugins } from "./catalog";

const movieId = "20000000-0000-4000-8000-000000000001";
const libraryId = "10000000-0000-4000-8000-00000000000a";

function seed(server: FakeServer) {
  server.cookie = true;
  server.libraries = [{ id: libraryId, name: "Movies", roots: 1 }];
  server.items = [{ id: movieId, libraryId, kind: "Movie", title: "Arrival" }];
  server.details[movieId] = { overview: "Linguist meets heptapods.", genres: ["Drama"], externalIds: [{ type: "imdb", value: "tt2543164", default: true }] };
  server.sources[movieId] = [];
}

const noop = () => undefined;
const action = (id: string) => ({ id, label: "label", run: noop });

afterEach(() => {
  unmountAll();
  localStorage.clear();
});

describe("plugin host: validation and lifecycle (G32.2, G32.4)", () => {
  it("never loads a plugin whose manifest is rejected and keeps the reasons", async () => {
    const incompatible = fakeBundle(testManifest("vendor.future", { sdkVersion: "^2.0.0" }), { messages: sameMessages({}), setup: noop });
    const malformed = fakeBundle({ id: "Bad Id", hooks: "nope" }, { messages: sameMessages({}), setup: noop });
    const { store } = pluginHost([incompatible, malformed]);
    await store.settled();
    expect(incompatible.load).not.toHaveBeenCalled();
    expect(malformed.load).not.toHaveBeenCalled();
    const [future, bad] = store.plugins;
    expect(future?.status).toBe("rejected");
    expect(future?.issues).toContainEqual({ code: "sdk_incompatible", field: "sdkVersion", params: { required: "^2.0.0", current: "1.0.0" } });
    expect(bad?.status).toBe("rejected");
    expect(bad?.issues.map((issue) => issue.code)).toEqual(expect.arrayContaining(["invalid_id", "invalid_hooks", "invalid_text"]));
  });

  it("refuses a second plugin with an existing ID", async () => {
    const first = fakeBundle(testManifest("vendor.same"), { messages: sameMessages({}), setup: noop });
    const second = fakeBundle(testManifest("vendor.same"), { messages: sameMessages({}), setup: noop });
    const { store } = pluginHost([first, second]);
    await store.settled();
    expect(store.plugins.map((plugin) => plugin.status)).toEqual(["active", "rejected"]);
    expect(store.plugins[1]?.problems[0]?.code).toBe("duplicate_id");
    expect(second.load).not.toHaveBeenCalled();
  });

  it("enables, disables and reorders at once", async () => {
    const one = fakeBundle(testManifest("vendor.one"), { messages: sameMessages({}), setup: (context) => {
        context.register("item.action", action("a"));
      } });
    const two = fakeBundle(testManifest("vendor.two"), { messages: sameMessages({}), setup: (context) => {
        context.register("item.action", action("b"));
      } });
    const off = fakeBundle(testManifest("vendor.off"), { messages: sameMessages({}), setup: (context) => {
        context.register("item.action", action("c"));
      } }, false);
    const { store } = pluginHost([one, two, off]);
    await store.settled();
    const keys = () => store.contributions("item.action").map((contribution) => contribution.key);
    expect(keys()).toEqual(["vendor.one/item.action/a", "vendor.two/item.action/b"]);
    expect(off.load).not.toHaveBeenCalled();

    store.setOrder(["vendor.two", "vendor.one"]);
    expect(keys()).toEqual(["vendor.two/item.action/b", "vendor.one/item.action/a"]);

    // Disabling removes the contributions synchronously.
    store.setEnabled("vendor.two", false);
    expect(keys()).toEqual(["vendor.one/item.action/a"]);
    expect(store.plugins.find((plugin) => plugin.key === "vendor.two")?.status).toBe("disabled");

    store.setEnabled("vendor.off", true);
    await store.settled();
    expect(keys()).toEqual(["vendor.one/item.action/a", "vendor.off/item.action/c"]);
    // The choices persist in this browser.
    expect(JSON.parse(localStorage.getItem("jelee.ui.v1.plugin-host.state")!)).toEqual({
      enabled: { "vendor.two": false, "vendor.off": true },
      order: ["vendor.two", "vendor.one", "vendor.off"],
    });
  });

  it("blocks a plugin until its dependency is present, compatible and enabled", async () => {
    const base = fakeBundle(testManifest("vendor.base", { version: "1.4.0" }), { messages: sameMessages({}), setup: noop }, false);
    const needsBase = fakeBundle(testManifest("vendor.addon", { dependencies: { "vendor.base": "^1.2.0" } }), { messages: sameMessages({}), setup: noop });
    const needsNewer = fakeBundle(testManifest("vendor.newer", { dependencies: { "vendor.base": "^2.0.0" } }), { messages: sameMessages({}), setup: noop });
    const needsMissing = fakeBundle(testManifest("vendor.orphan", { dependencies: { "vendor.absent": "*" } }), { messages: sameMessages({}), setup: noop });
    const { store } = pluginHost([base, needsBase, needsNewer, needsMissing]);
    await store.settled();
    const view = (key: string) => store.plugins.find((plugin) => plugin.key === key)!;
    expect(view("vendor.addon").status).toBe("blocked");
    expect(view("vendor.addon").problems[0]?.code).toBe("dependency_inactive");
    expect(view("vendor.newer").problems[0]).toEqual({ code: "dependency_version", params: { dependency: "vendor.base", range: "^2.0.0", version: "1.4.0" } });
    expect(view("vendor.orphan").problems[0]?.code).toBe("dependency_missing");
    expect(needsBase.load).not.toHaveBeenCalled();

    store.setEnabled("vendor.base", true);
    await store.settled();
    expect(view("vendor.addon").status).toBe("active");
    expect(view("vendor.newer").status).toBe("blocked");
  });

  it("isolates load failures, invalid definitions, throwing setups and undeclared hooks", async () => {
    const good = fakeBundle(testManifest("vendor.good"), { messages: sameMessages({}), setup: (context) => {
        context.register("item.action", action("ok"));
      } });
    const missing = fakeBundle(testManifest("vendor.missing"), new Error("chunk failed"));
    const invalid = fakeBundle(testManifest("vendor.invalid"), { messages: { "en-US": {} }, setup: noop } as never);
    const uneven = fakeBundle(testManifest("vendor.uneven"), { messages: { ...sameMessages({ a: "a" }), "ja-JP": {} }, setup: noop });
    const throwing = fakeBundle(testManifest("vendor.throws"), {
      messages: sameMessages({}),
      setup: (context) => {
        context.register("item.action", action("first"));
        throw new Error("setup exploded");
      },
    });
    const undeclared = fakeBundle(testManifest("vendor.sneaky"), {
      messages: sameMessages({}),
      setup: (context) => {
        context.register("settings.section", { id: "x", title: "t", component: () => Promise.resolve({ render: () => null }) });
      },
    });
    const badRoute = fakeBundle(testManifest("vendor.route", { hooks: ["route.register"], permissions: ["ui.routes"] }), {
      messages: sameMessages({}),
      setup: (context) => {
        context.register("route.register", { path: "now-playing", title: "t", component: () => Promise.resolve({ render: () => null }) });
      },
    });
    const { store } = pluginHost([good, missing, invalid, uneven, throwing, undeclared, badRoute]);
    await store.settled();
    const problem = (key: string) => store.plugins.find((plugin) => plugin.key === key)?.problems[0];
    expect(store.plugins.map((plugin) => [plugin.key, plugin.status])).toEqual([
      ["vendor.good", "active"],
      ["vendor.missing", "failed"],
      ["vendor.invalid", "failed"],
      ["vendor.uneven", "failed"],
      ["vendor.throws", "failed"],
      ["vendor.sneaky", "failed"],
      ["vendor.route", "failed"],
    ]);
    expect(problem("vendor.missing")).toEqual({ code: "load_failed", params: { error: "Error: chunk failed" } });
    expect(problem("vendor.invalid")?.code).toBe("invalid_definition");
    expect(problem("vendor.uneven")?.code).toBe("invalid_definition");
    expect(problem("vendor.throws")).toEqual({ code: "setup_failed", params: { error: "Error: setup exploded" } });
    expect(problem("vendor.sneaky")?.params.error).toContain("hook not declared in manifest: settings.section");
    expect(problem("vendor.route")?.params.error).toContain("route.register: invalid path");
    // A failed setup contributes nothing, not even what it registered first.
    expect(store.contributions("item.action").map((contribution) => contribution.pluginId)).toEqual(["vendor.good"]);
  });

  it("closes register() once setup has returned", async () => {
    let saved: PluginContext | null = null;
    const late = fakeBundle(testManifest("vendor.late"), {
      messages: sameMessages({}),
      setup: (context) => {
        saved = context;
      },
    });
    const { store } = pluginHost([late]);
    await store.settled();
    expect(() => {
      saved!.register("item.action", action("later"));
    }).toThrow("only available during setup");
    expect(store.contributions("item.action")).toEqual([]);
  });
});

describe("plugin context boundary (G32.3)", () => {
  function capture(permissions: string[]) {
    const captured: { context: PluginContext | null } = { context: null };
    const bundle = fakeBundle(testManifest("vendor.probe", { permissions }), {
      messages: sameMessages({ hello: "Hello {name}" }),
      setup: (context) => {
        captured.context = context;
      },
    });
    return { bundle, captured };
  }

  /** Every object and function reachable from a value through own properties and prototypes. */
  function reachable(root: unknown): { values: Set<unknown>; strings: Set<string> } {
    const values = new Set<unknown>();
    const strings = new Set<string>();
    const stop = new Set<unknown>([Object.prototype, Function.prototype, Array.prototype, Error.prototype, Promise.prototype]);
    const visit = (value: unknown) => {
      if (typeof value === "string") {
        strings.add(value);
        return;
      }
      if ((typeof value !== "object" && typeof value !== "function") || value === null || values.has(value) || stop.has(value)) {
        return;
      }
      values.add(value);
      for (const key of Reflect.ownKeys(value)) {
        const descriptor = Object.getOwnPropertyDescriptor(value, key);
        if (descriptor && "value" in descriptor) {
          visit(descriptor.value);
        }
      }
      visit(Object.getPrototypeOf(value));
    };
    visit(root);
    return { values, strings };
  }

  it("hands out no client, credential strategy or CSRF value", async () => {
    const server = createFakeServer();
    seed(server);
    const { bundle, captured } = capture(["catalog.read", "user.read", "settings.storage"]);
    const { store, api } = pluginHost([bundle], server.fetch);
    api.auth.resume({ csrf: server.csrf });
    await store.settled();
    const context = captured.context!;
    const { values, strings } = reachable(context);
    expect(values.has(api.client)).toBe(false);
    expect(values.has(api.auth)).toBe(false);
    expect(strings.has(server.csrf)).toBe(false);
    expect(strings.has(server.token)).toBe(false);
    expect(Object.isFrozen(context)).toBe(true);
    expect(Object.isFrozen(context.api)).toBe(true);
    // Only fixed read operations exist: no generic request, no writes.
    expect(Object.keys(context.api).sort()).toEqual(["item", "itemSources", "libraries", "me"]);
  });

  it("calls only fixed GET endpoints, without CSRF or Authorization headers, and returns copies", async () => {
    const server = createFakeServer();
    seed(server);
    const { bundle, captured } = capture(["catalog.read", "user.read"]);
    const { store, api } = pluginHost([bundle], server.fetch);
    api.auth.resume({ csrf: server.csrf });
    await store.settled();
    const context = captured.context!;
    const item = await context.api.item(movieId);
    expect(item).toMatchObject({ id: movieId, title: "Arrival", hasOverview: true, genres: ["Drama"] });
    expect(Object.isFrozen(item)).toBe(true);
    expect(await context.api.libraries()).toEqual([{ id: libraryId, name: "Movies" }]);
    const me = await context.api.me();
    expect(me).toEqual({ name: "admin", displayName: "Admin", locale: "ja-JP", admin: true });
    expect(Object.keys(me)).not.toContain("id");
    expect(server.requests.map((request) => request.method + " " + new URL(request.url).pathname)).toEqual([
      "GET /api/v1/items/" + movieId + "/details",
      "GET /api/v1/libraries",
      "GET /api/v1/users/me",
    ]);
    for (const request of server.requests) {
      expect(request.headers.get("X-Jelee-CSRF")).toBeNull();
      expect(request.headers.get("Authorization")).toBeNull();
    }
    // Malformed IDs never become a request path.
    await expect(context.api.item("../users")).rejects.toBeInstanceOf(PluginApiError);
    expect(server.requests).toHaveLength(3);
  });

  it("refuses capabilities the manifest did not request, without any request", async () => {
    const server = createFakeServer();
    seed(server);
    const { bundle, captured } = capture([]);
    const { store } = pluginHost([bundle], server.fetch);
    await store.settled();
    const context = captured.context!;
    await expect(context.api.item(movieId)).rejects.toBeInstanceOf(PluginPermissionError);
    await expect(context.api.me()).rejects.toMatchObject({ permission: "user.read" });
    expect(() => {
      context.settings.set("x", 1);
    }).toThrow(PluginPermissionError);
    expect(context.settings.get("x", 7)).toBe(7);
    expect(server.requests).toHaveLength(0);
  });

  it("maps server errors to the error code only", async () => {
    const server = createFakeServer();
    seed(server);
    server.cookie = false;
    const { bundle, captured } = capture(["catalog.read"]);
    const { store } = pluginHost([bundle], server.fetch);
    await store.settled();
    const failure = await captured.context!.api.libraries().catch((error: unknown) => error);
    expect(failure).toBeInstanceOf(PluginApiError);
    expect(failure).toMatchObject({ code: "authentication_required", status: 401 });
    expect(Object.keys(failure as object)).not.toContain("traceId");
  });

  it("keeps each plugin's settings in its own namespace", async () => {
    const contexts: Record<string, PluginContext> = {};
    const make = (id: string) =>
      fakeBundle(testManifest(id, { permissions: ["settings.storage"] }), {
        messages: sameMessages({}),
        setup: (context) => {
          contexts[id] = context;
        },
      });
    const { store } = pluginHost([make("vendor.a"), make("vendor.b")]);
    await store.settled();
    contexts["vendor.a"]!.settings.set("color", "teal");
    expect(contexts["vendor.a"]!.settings.get("color", "none")).toBe("teal");
    expect(contexts["vendor.b"]!.settings.get("color", "none")).toBe("none");
    expect(JSON.parse(localStorage.getItem("jelee.ui.v1.plugin-settings.vendor.a")!)).toEqual({ color: "teal" });
    expect(localStorage.getItem("jelee.ui.v1.plugin-settings.vendor.b")).toBeNull();
    // Values of another type than the fallback are not handed out.
    expect(contexts["vendor.a"]!.settings.get("color", 0)).toBe(0);
    expect(() => {
      contexts["vendor.a"]!.settings.set("bad key!", 1);
    }).toThrow(TypeError);
    expect(() => {
      contexts["vendor.a"]!.settings.set("big", "x".repeat(20_000));
    }).toThrow(RangeError);
    store.clearSettings("vendor.a");
    await store.settled();
    expect(localStorage.getItem("jelee.ui.v1.plugin-settings.vendor.a")).toBeNull();
    expect(contexts["vendor.a"]!.settings.get("color", "none")).toBe("none");
  });

  it("translates with the plugin's own catalog and interpolates parameters", async () => {
    const { bundle, captured } = capture([]);
    const { store } = pluginHost([bundle]);
    await store.settled();
    expect(captured.context!.t("hello", { name: "Ada" })).toBe("Hello Ada");
    expect(captured.context!.t("missing")).toBe("missing");
  });
});

describe("plugin rendering isolation (G32.3)", () => {
  let server: FakeServer;
  beforeEach(() => {
    server = createFakeServer();
    seed(server);
  });

  const crashing = defineComponent({
    setup() {
      throw new Error("tab exploded");
    },
  });
  const renderCrash = defineComponent({
    // eslint-disable-next-line vue/require-render-return -- the render fails on purpose
    render() {
      throw new Error("render exploded");
    },
  });
  const working = defineComponent({
    props: { item: { type: Object, required: true } },
    setup(props) {
      return () => h("p", { class: "working-tab" }, "Working " + (props.item as { title: string }).title);
    },
  });

  it("degrades only the failing plugin's area; the page and other plugins keep working", async () => {
    const bad = fakeBundle(testManifest("vendor.bad", { hooks: ["media.detail.tabs", "metadata.panel"] }), {
      messages: sameMessages({ tab: "Broken tab", panel: "Broken panel" }),
      setup(context) {
        context.register("media.detail.tabs", { id: "broken", title: "tab", component: () => Promise.resolve(crashing) });
        context.register("metadata.panel", { id: "broken", title: "panel", component: () => Promise.resolve(renderCrash) });
      },
    });
    const good = fakeBundle(testManifest("vendor.good", { hooks: ["media.detail.tabs"] }), {
      messages: sameMessages({ tab: "Good tab" }),
      setup(context) {
        context.register("media.detail.tabs", { id: "good", title: "tab", component: () => Promise.resolve(working) });
      },
    });
    const { wrapper, pinia } = await mountView("/items/" + movieId, { fetch: server.fetch, user: server.user, plugins: [providePlugins([bad, good])] });
    await vi.waitFor(() => {
      expect(wrapper.findAll('[data-testid="plugin-fallback"]').length).toBeGreaterThanOrEqual(2);
    });
    // The host page is intact.
    expect(wrapper.find("#item-title").text()).toBe("Arrival");
    expect(wrapper.text()).toContain("Linguist meets heptapods.");
    expect(wrapper.text()).toContain("Broken panel");
    expect(wrapper.text()).toContain("The plugin \"vendor.bad\" failed");
    // The other plugin's tab still works.
    const tabs = wrapper.findAll('[role="tab"]');
    expect(tabs.map((tab) => tab.text())).toEqual(["Broken tab", "Good tab"]);
    await tabs[1]!.trigger("click");
    await vi.waitFor(() => {
      expect(wrapper.find(".working-tab").text()).toBe("Working Arrival");
    });
    const { usePluginStore } = await import("./store");
    const store = usePluginStore(pinia);
    expect(store.plugins.find((plugin) => plugin.key === "vendor.bad")?.failures).toBeGreaterThanOrEqual(2);
    expect(store.plugins.find((plugin) => plugin.key === "vendor.good")?.failures).toBe(0);
  });

  it("supports keyboard navigation between plugin tabs", async () => {
    const tabs = fakeBundle(testManifest("vendor.tabs", { hooks: ["media.detail.tabs"] }), {
      messages: sameMessages({ one: "One", two: "Two" }),
      setup(context) {
        context.register("media.detail.tabs", { id: "one", title: "one", component: () => Promise.resolve(working) });
        context.register("media.detail.tabs", { id: "two", title: "two", component: () => Promise.resolve(working) });
      },
    });
    const { wrapper } = await mountView("/items/" + movieId, { fetch: server.fetch, user: server.user, plugins: [providePlugins([tabs])] });
    await vi.waitFor(() => {
      expect(wrapper.findAll('[role="tab"]')).toHaveLength(2);
    });
    const [one, two] = wrapper.findAll('[role="tab"]');
    expect(one!.attributes("aria-selected")).toBe("true");
    expect(two!.attributes("tabindex")).toBe("-1");
    await one!.trigger("keydown", { key: "ArrowRight" });
    await flushPromises();
    expect(two!.attributes("aria-selected")).toBe("true");
    expect(document.activeElement).toBe(two!.element);
    await two!.trigger("keydown", { key: "Home" });
    expect(one!.attributes("aria-selected")).toBe("true");
  });

  it("reports a failing item action as a toast and keeps the page", async () => {
    const failing = fakeBundle(testManifest("vendor.act"), {
      messages: sameMessages({ label: "Do it" }),
      setup(context) {
        context.register("item.action", {
          id: "do",
          label: "label",
          run: () => Promise.reject(new Error("nope")),
        });
      },
    });
    const { wrapper, pinia } = await mountView("/items/" + movieId, { fetch: server.fetch, user: server.user, plugins: [providePlugins([failing])] });
    await vi.waitFor(() => {
      expect(wrapper.find('[role="group"] button').exists()).toBe(true);
    });
    await wrapper.find('[role="group"] button').trigger("click");
    await flushPromises();
    expect(useToastStore(pinia).toasts.map((toast) => [toast.key, toast.tone, toast.params])).toEqual([["plugins.actionFailed", "danger", { name: "Do it" }]]);
    expect(wrapper.find("#item-title").exists()).toBe(true);
  });
});

describe("official plugins (G32.5)", () => {
  it("both pass validation; item facts is on by default and the accent example is opt-in", async () => {
    const { store } = pluginHost(officialPlugins());
    await store.settled();
    expect(store.plugins.map((plugin) => [plugin.key, plugin.status, plugin.official])).toEqual([
      ["jelee.accent-tokens", "disabled", true],
      ["jelee.item-facts", "active", true],
    ]);
    expect(store.plugins[1]?.contributionCounts).toEqual({ "media.detail.tabs": 1, "item.action": 1, "settings.section": 1 });
  });

  it("ship the same message keys in all four languages", async () => {
    for (const path of ["../official/item-facts/messages", "../official/accent-tokens/messages"]) {
      const { messages } = (await import(/* @vite-ignore */ path)) as { messages: Record<string, Record<string, string>> };
      const keys = Object.values(messages).map((catalog) => Object.keys(catalog).sort().join(","));
      expect(Object.keys(messages).sort()).toEqual(["en-US", "ja-JP", "zh-CN", "zh-TW"]);
      expect(new Set(keys).size).toBe(1);
      for (const catalog of Object.values(messages)) {
        for (const [key, text] of Object.entries(catalog)) {
          const slots = (value: string) => [...value.matchAll(/\{(\w+)\}/g)].map((match) => match[1]).sort().join(",");
          expect(slots(text), key).toBe(slots(messages["en-US"]![key]!));
          expect(text.trim(), key).not.toBe("");
        }
      }
    }
  });
});
