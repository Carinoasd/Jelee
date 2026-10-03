import { flushPromises, mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";
import { createApp } from "vue";
import { createMemoryHistory } from "vue-router";
import App from "./App.vue";
import { installAppPlugins } from "./plugins";
import { createFakeServer } from "./test/fakeServer";

async function boot(path: string) {
  const server = createFakeServer();
  server.libraries = [{ id: "a", name: "Movies <b>bold</b>", roots: 2 }];
  const host = createApp(App);
  const plugins = installAppPlugins(host, { languages: ["en-US"], fetch: server.fetch, history: createMemoryHistory() });
  await plugins.router.push(path);
  await plugins.router.isReady();
  const wrapper = mount(App, {
    global: {
      plugins: [plugins.router, plugins.i18n, plugins.pinia],
      provide: host._context.provides,
    },
    attachTo: document.body,
  });
  return { server, wrapper, router: plugins.router };
}

describe("web skeleton", () => {
  it("redirects to login, signs in, lists libraries and has no playback", async () => {
    const { wrapper, router } = await boot("/libraries");
    expect(router.currentRoute.value.name).toBe("login");
    expect(router.currentRoute.value.query.redirect).toBe("/libraries");

    const inputs = wrapper.findAll("input");
    await inputs[0]!.setValue("admin");
    await inputs[1]!.setValue("correct horse battery");
    await wrapper.find("form").trigger("submit");
    await vi.waitFor(() => {
      expect(router.currentRoute.value.name).toBe("libraries");
    });
    await vi.waitFor(() => {
      expect(wrapper.find(".jl-libraries__name").exists()).toBe(true);
    });
    // The user's saved locale (ja-JP) overrides the browser language.
    expect(document.documentElement.lang).toBe("ja-JP");
    expect(wrapper.find("h1").text()).toBe("ライブラリ");
    const item = wrapper.find(".jl-libraries__name");
    // User content is escaped, never interpreted as markup.
    expect(item.text()).toBe("Movies <b>bold</b>");
    expect(item.find("b").exists()).toBe(false);

    const html = wrapper.html();
    expect(html).not.toMatch(/<video|<audio/i);
    expect(router.getRoutes().some((route) => /play|stream|cast/i.test(route.path))).toBe(false);
    expect(localStorage.length + sessionStorage.length).toBe(0);
    wrapper.unmount();
  });

  it("shows a localized error for wrong credentials", async () => {
    const { wrapper } = await boot("/login");
    const inputs = wrapper.findAll("input");
    await inputs[0]!.setValue("admin");
    await inputs[1]!.setValue("wrong");
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(wrapper.find("[role=alert]").text()).toBe("The user name or password is incorrect.");
    expect((inputs[1]!.element as HTMLInputElement).value).toBe("");
    wrapper.unmount();
  });
});
