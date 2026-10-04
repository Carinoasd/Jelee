import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { apiKey } from "@/api";
import { createApiClient } from "@/api/client";
import { createAppI18n } from "@/i18n";
import { createFakeServer, type FakeServer } from "@/test/fakeServer";
import { readDevMode } from "./api";
import DevModeBanner from "./DevModeBanner.vue";

const mounted: { unmount(): void }[] = [];
afterEach(() => {
  for (const wrapper of mounted.splice(0)) {
    wrapper.unmount();
  }
  vi.useRealTimers();
});

async function mountBanner(server: FakeServer, locale: "en-US" | "zh-TW" = "en-US", refreshMs = 60_000) {
  const api = createApiClient({ fetch: server.fetch });
  const wrapper = mount(DevModeBanner, {
    props: { refreshMs },
    global: { plugins: [createAppI18n(locale)], provide: { [apiKey as symbol]: api } },
  });
  mounted.push(wrapper);
  await flushPromises();
  return wrapper;
}

describe("developer mode banner (G45.3)", () => {
  it("stays hidden in production", async () => {
    const server = createFakeServer();
    const wrapper = await mountBanner(server);
    expect(wrapper.find("[data-testid=devmode-banner]").exists()).toBe(false);
    expect(server.requests.map((request) => new URL(request.url).pathname)).toEqual(["/api/v1/system"]);
  });

  it("shows a persistent warning with the deadline and disappears when the session ends", async () => {
    vi.useFakeTimers();
    const server = createFakeServer();
    server.devMode = { active: true, expiresAt: "2026-10-04T13:00:00Z" };
    const wrapper = await mountBanner(server, "zh-TW", 1000);
    const banner = wrapper.find("[data-testid=devmode-banner]");
    expect(banner.attributes("role")).toBe("alert");
    expect(banner.text()).toContain("開發者模式已開啟");
    expect(banner.text()).toContain("切勿在生產環境使用開發者模式");
    expect(banner.text()).toContain("2026");
    server.devMode = { active: false, expiresAt: "" };
    await vi.advanceTimersByTimeAsync(1000);
    await flushPromises();
    expect(wrapper.find("[data-testid=devmode-banner]").exists()).toBe(false);
  });

  it("falls back to the response header and never fails the page", async () => {
    const header = createFakeServer();
    const fetchWithHeader: typeof globalThis.fetch = () =>
      Promise.resolve(new Response("{}", { status: 503, headers: { "Content-Type": "application/json", "X-Jelee-Dev-Mode": "true" } }));
    expect(await readDevMode(createApiClient({ fetch: fetchWithHeader }).client)).toEqual({ active: true, expiresAt: "" });
    const broken: typeof globalThis.fetch = () => Promise.reject(new Error("offline"));
    expect(await readDevMode(createApiClient({ fetch: broken }).client)).toEqual({ active: false, expiresAt: "" });
    header.devMode = { active: true, expiresAt: "not a date" };
    const wrapper = await mountBanner(header);
    expect(wrapper.find("[data-testid=devmode-banner]").text()).not.toContain("not a date");
  });
});
