import { flushPromises } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { usePluginStore } from "@/plugins/host/store";
import { useAppearanceStore } from "@/stores/appearance";
import { createFakeServer, type FakeServer } from "@/test/fakeServer";
import { mountView, unmountAll } from "@/test/mountView";
import { fakeBundle, providePlugins, sameMessages, testManifest } from "@/test/plugins";

let server: FakeServer;
let sheets: CSSStyleSheet[];

beforeEach(() => {
  server = createFakeServer();
  server.cookie = true;
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

describe("plugin administration (G32.4)", () => {
  it("shows information, permissions and rejection reasons, and toggles plugins at once", async () => {
    const good = fakeBundle(testManifest("vendor.good", { permissions: ["catalog.read", "settings.storage"], author: "Vendor" }), {
      messages: sameMessages({ label: "x" }),
      setup: (context) => {
        context.register("item.action", { id: "a", label: "label", run: () => undefined });
      },
    });
    const future = fakeBundle(testManifest("vendor.future", { sdkVersion: "^2.0.0", minJeleeVersion: "9.0.0" }), { messages: sameMessages({}), setup: () => undefined });
    const { wrapper, pinia } = await mountView("/admin/plugins", { fetch: server.fetch, user: server.user, plugins: [providePlugins([good, future])] });
    const store = usePluginStore(pinia);
    await store.settled();
    await flushPromises();

    const goodCard = wrapper.find('[data-plugin="vendor.good"]');
    expect(goodCard.text()).toContain("Active");
    expect(goodCard.text()).toContain("catalog.read — Read item details, file summaries and the library list the current user may see (read only).");
    expect(goodCard.text()).toContain("item.action — 1 contributions");
    expect(goodCard.text()).toContain("Vendor");

    const futureCard = wrapper.find('[data-plugin="vendor.future"]');
    expect(futureCard.text()).toContain("Rejected");
    expect(futureCard.text()).toContain("Needs SDK ^2.0.0; this is 1.0.0.");
    expect(futureCard.text()).toContain("Needs Jelee 9.0.0 or later; this is 0.1.0.");
    expect(futureCard.find("button").exists()).toBe(false);

    const toggle = goodCard.findAll("button").find((button) => button.text() === "Enable vendor.good")!;
    expect(toggle.attributes("aria-pressed")).toBe("true");
    await toggle.trigger("click");
    expect(store.contributions("item.action")).toEqual([]);
    expect(wrapper.find('[data-plugin="vendor.good"]').text()).toContain("Disabled");
    await wrapper.find('[data-plugin="vendor.good"] button[aria-pressed]').trigger("click");
    await store.settled();
    expect(store.contributions("item.action")).toHaveLength(1);
  });

  it("orders plugins with the keyboard", async () => {
    const make = (id: string) => fakeBundle(testManifest(id), { messages: sameMessages({}), setup: () => undefined });
    const { wrapper, pinia } = await mountView("/admin/plugins", { fetch: server.fetch, user: server.user, plugins: [providePlugins([make("vendor.one"), make("vendor.two")])] });
    await wrapper.find('[data-row="vendor.two"] [data-move="up"]').trigger("keydown", { key: "ArrowUp", altKey: true });
    await flushPromises();
    expect(usePluginStore(pinia).order).toEqual(["vendor.two", "vendor.one"]);
    expect(wrapper.findAll("article h2").map((heading) => heading.text())).toEqual(["vendor.two", "vendor.one"]);
  });
});

describe("custom CSS administration (G33.4)", () => {
  it("shows what is dropped, applies only the sanitized CSS and never adds <style>", async () => {
    const { wrapper } = await mountView("/admin/appearance", { fetch: server.fetch, user: server.user });
    const textarea = wrapper.find("textarea");
    await textarea.setValue("body { color: red; background: url(https://evil.example/x.png) } @import url(https://evil.example/x.css);");
    await wrapper.findAll("button").find((button) => button.text() === "Check")!.trigger("click");
    expect(wrapper.text()).toContain("url() may only point to same-site paths.");
    expect(wrapper.text()).toContain("CSS import rules are not allowed.");
    expect(appliedCss()).toBe("");

    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(appliedCss()).toContain("color: red");
    expect(appliedCss()).not.toContain("evil");
    expect(document.querySelectorAll("style")).toHaveLength(0);

    await textarea.setValue("</style><script>alert(1)</script>");
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(wrapper.text()).toContain("The whole CSS was refused; no rule is applied.");
    expect(appliedCss()).toBe("");
  });

  it("sanitizes stored CSS again when it is loaded", () => {
    // Storage is untrusted: something other than this page may have written it.
    localStorage.setItem("jelee.ui.v1.admin-css.custom", JSON.stringify({ css: "a { color: blue; background: url(javascript:alert(1)) }", allowExternalFonts: true, fontHosts: ["fonts.example.com", "not a host"] }));
    return mountView("/admin/appearance", { fetch: server.fetch, user: server.user }).then(({ pinia }) => {
      const store = useAppearanceStore(pinia);
      expect(store.settings.policy.fontHosts).toEqual(["fonts.example.com"]);
      expect(appliedCss()).toContain("color: blue");
      expect(appliedCss()).not.toContain("javascript");
      expect(store.result.issues.map((issue) => issue.code)).toEqual(["script_url"]);
    });
  });

  it("validates the font host allowlist before saving", async () => {
    const { wrapper } = await mountView("/admin/appearance", { fetch: server.fetch, user: server.user });
    await wrapper.find('input[type="checkbox"]').setValue(true);
    const hosts = wrapper.findAll("textarea")[1]!;
    await hosts.setValue("fonts.example.com\nhttps://evil.example");
    expect(wrapper.text()).toContain("Not valid host names: https://evil.example");
    expect(hosts.attributes("aria-invalid")).toBe("true");
    const save = vi.spyOn(useAppearanceStore(), "save");
    await wrapper.find("form").trigger("submit");
    expect(save).not.toHaveBeenCalled();
    await hosts.setValue("fonts.example.com");
    await wrapper.find("textarea").setValue("@font-face { font-family: B; src: url(https://fonts.example.com/b.woff2) }");
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    // jsdom's CSSOM drops the src descriptor, so check the sanitized text.
    expect(useAppearanceStore().result.css).toBe("@font-face{font-family:B;src:url(https://fonts.example.com/b.woff2)}");
    expect(appliedCss()).toContain("@font-face");
  });
});
