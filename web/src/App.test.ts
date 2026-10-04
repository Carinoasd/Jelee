import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createApp } from "vue";
import { createMemoryHistory } from "vue-router";
import App from "./App.vue";
import { installAppPlugins } from "./plugins";
import { createFakeServer, currentSessionId, userId, type FakeServer } from "./test/fakeServer";

const libraryA = "10000000-0000-4000-8000-00000000000a";
const libraryB = "10000000-0000-4000-8000-00000000000b";
const movieId = "20000000-0000-4000-8000-000000000001";

function seed(server: FakeServer) {
  server.libraries = [
    { id: libraryA, name: "Movies <b>bold</b>", roots: 2 },
    { id: libraryB, name: "Shows", roots: 1 },
  ];
  server.items = [
    { id: movieId, libraryId: libraryA, kind: "Movie", title: "Arrival <script>x</script>" },
    { id: "20000000-0000-4000-8000-000000000002", libraryId: libraryB, kind: "Series", title: "Other Show" },
    { id: "20000000-0000-4000-8000-000000000003", libraryId: libraryA, kind: "HomeVideo", title: "Holiday" },
  ];
  server.details[movieId] = {
    originalTitle: "Story of Your Life",
    overview: "Linguist <i>meets</i> heptapods.",
    premiereDate: "2016-11-11",
    productionYear: 2016,
    genres: ["Drama", "Science Fiction"],
    externalIds: [
      { type: "tmdb", value: "329865", default: true },
      { type: "imdb", value: "tt2543164", default: false },
    ],
    nfo: { status: "valid", readAt: "2026-10-01T00:00:00Z", fields: ["title", "originalTitle", "overview"] },
  };
  server.sources[movieId] = [
    {
      id: "40000000-0000-4000-8000-000000000001",
      container: "mkv",
      contentType: "video/x-matroska",
      probed: true,
      sizeBytes: 9_000_000_000,
      durationMicros: 6_960_000_000,
      bitRate: 10_344_827,
      version: { displayName: "2160p · HEVC · TrueHD 7.1", qualityScore: 9 },
      videoTracks: [{ index: 0, codec: "hevc", width: 3840, height: 2160, default: true, primary: true }],
      audioTracks: [{ index: 1, codec: "truehd", language: "eng", channelLayout: "7.1", default: true, forced: false, atmos: true }],
      subtitleTracks: [{ index: 2, codec: "hdmv_pgs_subtitle", format: "pgs", language: "eng", default: false, forced: true }],
      externalTracks: [
        { id: "50000000-0000-4000-8000-000000000001", kind: "subtitle", format: "srt", language: "zh", forced: false, sdh: true, default: false, commentary: false, sizeBytes: 10 },
        { id: "50000000-0000-4000-8000-000000000002", kind: "audio", format: "ac3", language: "ja", forced: false, sdh: false, default: false, commentary: true, sizeBytes: 20 },
      ],
    },
  ];
  server.sessions = [
    session(currentSessionId, "Jelee Web", "web", "2026-10-03T10:00:00Z"),
    session("30000000-0000-4000-8000-000000000001", "Living room TV", "native", "2026-10-04T08:00:00Z"),
  ];
}

function session(id: string, deviceName: string, clientKind: "web" | "native", lastSeenAt: string) {
  return {
    id,
    userId,
    clientKind,
    deviceName,
    createdAt: "2026-10-01T00:00:00Z",
    expiresAt: "2026-11-01T00:00:00Z",
    lastSeenAt,
  };
}

const mounted: { unmount(): void }[] = [];
afterEach(() => {
  for (const wrapper of mounted.splice(0)) {
    wrapper.unmount();
  }
});

async function boot(path: string, prepare: (server: FakeServer) => void = () => undefined) {
  const server = createFakeServer();
  seed(server);
  prepare(server);
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
  mounted.push(wrapper);
  await flushPromises();
  return { server, wrapper, router: plugins.router, i18n: plugins.i18n };
}

async function signIn(wrapper: Awaited<ReturnType<typeof boot>>["wrapper"], password = "correct horse battery") {
  const inputs = wrapper.findAll("input");
  await inputs[0]!.setValue("admin");
  await inputs[1]!.setValue(password);
  await wrapper.find("form").trigger("submit");
  await flushPromises();
}

function assertNoPlayback(html: string) {
  expect(html).not.toMatch(/<video|<audio|<source|<track|<object|<embed|<iframe/i);
  expect(html).not.toMatch(/\bplay(er|back|ing)?\b|stream|再生|播放/i);
}

describe("sign-in flow", () => {
  it("redirects to login, signs in via cookie, returns to the page and lists libraries", async () => {
    const { wrapper, router, server } = await boot("/libraries?x=1");
    expect(router.currentRoute.value.name).toBe("login");
    expect(router.currentRoute.value.query.redirect).toBe("/libraries?x=1");

    await signIn(wrapper);
    await vi.waitFor(() => {
      expect(router.currentRoute.value.fullPath).toBe("/libraries?x=1");
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
    // The bearer token in the login response is never sent back.
    expect(server.requests.every((request) => request.headers.get("Authorization") === null)).toBe(true);

    assertNoPlayback(wrapper.html());
    expect(router.getRoutes().some((route) => /play|stream|cast|subtitles|audio/i.test(route.path))).toBe(false);
    expect(localStorage.length + sessionStorage.length).toBe(0);
  });

  it("shows localized errors for wrong credentials, rate limits and missing fields", async () => {
    const { wrapper } = await boot("/login");
    await wrapper.find("form").trigger("submit");
    expect(wrapper.findAll("[aria-invalid=true]")).toHaveLength(2);
    expect(wrapper.text()).toContain("Enter your user name.");

    await signIn(wrapper, "wrong");
    expect(wrapper.find("[role=alert]").text()).toBe("The user name or password is incorrect.");
    expect((wrapper.findAll("input")[1]!.element as HTMLInputElement).value).toBe("");
  });

  it("maps server error codes to catalog messages", async () => {
    const { wrapper } = await boot("/login", (server) => {
      const original = server.fetch;
      server.fetch = (input, init) => {
        const request = input instanceof Request ? input : new Request(input, init);
        if (request.url.endsWith("/api/v1/auth/login")) {
          return Promise.resolve(
            new Response(JSON.stringify({ error: { code: "auth_rate_limited", message: "raw server text", details: {}, traceId: "t" } }), {
              status: 429,
              headers: { "Content-Type": "application/json" },
            }),
          );
        }
        return original(request);
      };
    });
    await signIn(wrapper);
    expect(wrapper.find("[role=alert]").text()).toBe("Too many attempts. Please try again later.");
    expect(wrapper.text()).not.toContain("raw server text");
  });

  it("resumes a cookie session after a reload and keeps the deep link", async () => {
    const { router, wrapper, server } = await boot("/items/" + movieId, (s) => {
      s.cookie = true;
    });
    expect(router.currentRoute.value.name).toBe("item");
    await vi.waitFor(() => {
      expect(wrapper.find("#item-title").exists()).toBe(true);
    });
    expect(server.requests.map((request) => new URL(request.url).pathname)).toContain("/api/v1/auth/csrf");
  });

  it("signs out with the CSRF header and returns to login", async () => {
    const { wrapper, router, server } = await boot("/libraries", (s) => {
      s.cookie = true;
    });
    await wrapper.find(".jl-header button").trigger("click");
    await flushPromises();
    const logout = server.requests.find((request) => request.url.endsWith("/api/v1/auth/logout"))!;
    expect(logout.headers.get("X-Jelee-CSRF")).toBe(server.csrf);
    expect(server.cookie).toBe(false);
    expect(router.currentRoute.value.name).toBe("login");
    expect(wrapper.find(".jl-toast").text()).toContain("サインアウトしました。");
  });

  it("sends the user to login when the session expires mid-use", async () => {
    const { router, server, wrapper } = await boot("/libraries", (s) => {
      s.cookie = true;
    });
    server.cookie = false;
    await router.push("/account");
    await flushPromises();
    expect(router.currentRoute.value.name).toBe("login");
    expect(router.currentRoute.value.query).toMatchObject({ reason: "expired", redirect: "/account" });
    expect(wrapper.find("[role=alert]").text()).toContain("ログイン");
  });
});

describe("library items", () => {
  it("shows only the library's items as a poster wall and a list", async () => {
    const { wrapper, router, i18n } = await boot("/libraries/" + libraryA, (s) => {
      s.cookie = true;
    });
    i18n.global.locale.value = "en-US";
    await flushPromises();
    expect(wrapper.find("h1").text()).toBe("Movies <b>bold</b>");
    const titles = wrapper.findAll(".jl-card__title").map((node) => node.text());
    expect(titles).toEqual(["Arrival <script>x</script>", "Holiday"]);
    expect(wrapper.find("script").exists()).toBe(false);
    const images = wrapper.findAll("img");
    expect(images[0]!.attributes("src")).toBe(`/images/Primary/${movieId}?width=300`);
    expect(images[0]!.attributes("loading")).toBe("lazy");

    const toggle = wrapper.findAll("[role=group] button");
    expect(toggle.map((button) => button.attributes("aria-pressed"))).toEqual(["true", "false"]);
    await toggle[1]!.trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query.view).toBe("list");
    expect(wrapper.find("table").exists()).toBe(true);
    expect(wrapper.findAll("tbody tr")).toHaveLength(2);
    expect(wrapper.findAll("[role=group] button")[1]!.attributes("aria-pressed")).toBe("true");
    assertNoPlayback(wrapper.html());
  });

  it("sorts on the server through the URL and counts the whole library", async () => {
    const { wrapper, router, server } = await boot("/libraries/" + libraryA, (s) => {
      s.cookie = true;
      s.user = { ...s.user, locale: "en-US" };
      s.items = s.items.map((item) => (item.title === "Holiday" ? { ...item, productionYear: 2020 } : item));
    });
    expect(wrapper.find(".jl-count").text()).toBe("Showing 2 of 2");
    const select = wrapper.find("select.jl-sort__select");
    await select.setValue("year");
    await flushPromises();
    expect(router.currentRoute.value.query.sort).toBe("year");
    expect(wrapper.findAll(".jl-card__title").map((node) => node.text())).toEqual(["Holiday", "Arrival <script>x</script>"]);
    const last = new URL(server.requests.at(-1)!.url).searchParams;
    expect(last.get("parentId")).toBe(libraryA);
    expect(last.get("sort")).toBe("productionYear");
    expect(last.get("order")).toBe("desc");
  });

  it("shows the empty state and the error state with retry", async () => {
    const emptyLibrary = "10000000-0000-4000-8000-00000000000c";
    const { wrapper, server, router } = await boot("/libraries/" + emptyLibrary, (s) => {
      s.cookie = true;
    });
    expect(wrapper.text()).toContain("このライブラリにはまだアイテムがありません。");

    server.failItems = true;
    await router.push("/libraries/" + libraryA);
    await flushPromises();
    const alert = wrapper.find("[role=alert]");
    expect(alert.text()).toContain("サーバーは起動中です");
    expect(alert.text()).toContain("trace-not_ready");
    server.failItems = false;
    await alert.find("button").trigger("click");
    await flushPromises();
    expect(wrapper.findAll(".jl-card")).toHaveLength(2);
  });
});

describe("item detail", () => {
  it("shows metadata, external IDs and NFO markers and has no playback UI", async () => {
    const { wrapper, i18n } = await boot("/items/" + movieId, (s) => {
      s.cookie = true;
    });
    i18n.global.locale.value = "en-US";
    await flushPromises();
    expect(wrapper.find("#item-title").text()).toBe("Arrival <script>x</script>");
    const text = wrapper.text();
    expect(text).toContain("Story of Your Life");
    expect(text).toContain("2016");
    expect(text).toContain("Linguist <i>meets</i> heptapods.");
    expect(wrapper.find("i").exists()).toBe(false);
    expect(text).toContain("tt2543164");
    expect(text).toContain("Science Fiction");
    expect(wrapper.findAll(".jl-badge--accent").length).toBeGreaterThan(0);
    expect(text).toContain("open it in a native Jelee client app");

    const html = wrapper.html();
    expect(html).not.toMatch(/<video|<audio|<source|<track|<object|<embed|<iframe/i);
    // Every interactive element is a link back to browsing or a UI control.
    const actions = wrapper.findAll("main button, main a").map((node) => node.text());
    expect(actions.some((label) => /play|watch|stream|cast/i.test(label))).toBe(false);
    for (const link of wrapper.findAll("main a")) {
      expect(link.attributes("href")).toMatch(/^\/(libraries|items)(\/|$)/);
    }
  });

  it("shows details and file information to regular users without any delivery route", async () => {
    const { wrapper, server } = await boot("/items/" + movieId, (s) => {
      s.cookie = true;
      s.user = { ...s.user, admin: false, locale: "zh-TW" };
    });
    const text = wrapper.text();
    expect(text).toContain("Linguist <i>meets</i> heptapods.");
    expect(text).toContain("中繼資料已從有效的 NFO 檔案讀取。");
    expect(text).toContain("2160p · HEVC · TrueHD 7.1");
    expect(text).toContain("3840×2160");
    expect(text).toContain("1:56:00");
    expect(text).toContain("truehd");
    expect(text).toContain("pgs");
    expect(text).toContain("外掛字幕與音軌檔案");
    expect(text).toContain("聽障");
    expect(text).toContain("評論");
    expect(text).toContain("請使用原生用戶端觀看");
    expect(wrapper.html()).not.toMatch(/\/api\/v1\/sources|\/subtitles\/|\/audio\//);
    // The page never touches the administrator metadata endpoint.
    expect(server.requests.some((request) => new URL(request.url).pathname.endsWith("/metadata"))).toBe(false);
    assertNoPlayback(wrapper.html());
  });

  it("keeps the details when file information fails", async () => {
    const { wrapper } = await boot("/items/" + movieId, (s) => {
      s.cookie = true;
      s.failSources = true;
      s.user = { ...s.user, locale: "en-US" };
    });
    expect(wrapper.find("#item-title").text()).toBe("Arrival <script>x</script>");
    expect(wrapper.text()).toContain("File information could not be loaded right now.");
  });

  it("shows a localized not-found error for unknown items", async () => {
    const { wrapper } = await boot("/items/20000000-0000-4000-8000-0000000000ff", (s) => {
      s.cookie = true;
      s.user = { ...s.user, locale: "zh-CN" };
    });
    expect(wrapper.find("[role=alert]").text()).toContain("内容不存在");
  });
});

describe("account", () => {
  it("lists sessions and revokes another device", async () => {
    const { wrapper, server, router } = await boot("/login", (s) => {
      s.user = { ...s.user, locale: "en-US" };
    });
    await signIn(wrapper);
    await router.push("/account");
    await flushPromises();
    const rows = wrapper.findAll(".jl-session");
    expect(rows).toHaveLength(2);
    expect(rows[0]!.text()).toContain("This device");
    expect(rows[1]!.text()).toContain("Living room TV");

    await rows[1]!.find("button").trigger("click");
    await flushPromises();
    expect(wrapper.findAll(".jl-session")).toHaveLength(1);
    expect(server.sessions).toHaveLength(1);
    expect(wrapper.text()).toContain("Session on “Living room TV” revoked.");
  });

  it("restores a session when revoking fails and signs out on revoking this device", async () => {
    const { wrapper, server, router } = await boot("/login", (s) => {
      s.user = { ...s.user, locale: "en-US" };
    });
    await signIn(wrapper);
    await router.push("/account");
    await flushPromises();

    server.failRevoke = true;
    await wrapper.findAll(".jl-session")[1]!.find("button").trigger("click");
    await flushPromises();
    expect(wrapper.findAll(".jl-session")).toHaveLength(2);
    expect(wrapper.find(".jl-toast--danger").text()).toContain("The server is busy");

    server.failRevoke = false;
    await wrapper.findAll(".jl-session")[0]!.find("button").trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.name).toBe("login");
    expect(server.cookie).toBe(false);
  });
});
