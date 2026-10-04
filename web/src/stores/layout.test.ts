import { flushPromises } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createFakeServer, type FakeServer } from "@/test/fakeServer";
import { mountView, unmountAll } from "@/test/mountView";
import { builtInPresets, moveEntry, normalizeLayout, useLayoutStore } from "./layout";
import { persistMaxLength, readPersisted, writePersisted } from "./persist";

const movieId = "20000000-0000-4000-8000-000000000001";
const libraryId = "10000000-0000-4000-8000-00000000000a";

function server(): FakeServer {
  const fake = createFakeServer();
  fake.cookie = true;
  fake.libraries = [{ id: libraryId, name: "Movies", roots: 1 }];
  fake.items = [{ id: movieId, libraryId, kind: "Movie", title: "Arrival" }];
  fake.details[movieId] = { overview: "Linguist meets heptapods.", genres: ["Drama"], externalIds: [{ type: "imdb", value: "tt2543164", default: true }] };
  return fake;
}

const layoutKey = (userId: string) => "jelee.ui.v1.page-layout." + userId;

afterEach(() => {
  unmountAll();
  localStorage.clear();
});

describe("layout model (G33.5)", () => {
  it("moves entries and ignores out-of-range moves", () => {
    expect(moveEntry(["a", "b", "c"], 0, 2)).toEqual(["b", "c", "a"]);
    expect(moveEntry(["a", "b", "c"], 2, 0)).toEqual(["c", "a", "b"]);
    expect(moveEntry(["a", "b", "c"], 0, -1)).toEqual(["a", "b", "c"]);
    expect(moveEntry(["a", "b", "c"], 1, 3)).toEqual(["a", "b", "c"]);
  });

  it("repairs untrusted stored layouts", () => {
    const repaired = normalizeLayout({
      home: [{ id: "latest", visible: false }, { id: "evil", visible: true }, { id: "latest", visible: true }, "x", { id: "welcome", visible: "yes" }],
      detail: null,
    });
    expect(repaired.home).toEqual([
      { id: "latest", visible: false },
      { id: "welcome", visible: true },
      { id: "libraries", visible: true },
    ]);
    expect(repaired.detail).toEqual(builtInPresets.standard.detail);
  });
});

describe("browser storage adapter", () => {
  it("refuses keys that name credentials and oversized values", () => {
    for (const key of ["session", "csrf-token", "auth.state", "bearer", "user-password"]) {
      expect(() => writePersisted(key, 1)).toThrow(TypeError);
    }
    expect(() => writePersisted("page-layout", 1, "../x")).toThrow(TypeError);
    expect(writePersisted("page-layout", "x".repeat(persistMaxLength))).toBe(false);
    expect(localStorage.length).toBe(0);
  });

  it("reads back only JSON", () => {
    localStorage.setItem("jelee.ui.v1.page-layout.guest", "{not json");
    expect(readPersisted("page-layout", "guest")).toBeNull();
  });
});

describe("home page and layout editing", () => {
  it("renders visible blocks in the stored order and skips hidden ones", async () => {
    const fake = server();
    localStorage.setItem(
      layoutKey(fake.user.id),
      JSON.stringify({ layout: { home: [{ id: "latest", visible: true }, { id: "libraries", visible: false }, { id: "welcome", visible: true }] } }),
    );
    const { wrapper } = await mountView("/", { fetch: fake.fetch, user: fake.user });
    await flushPromises();
    expect(wrapper.findAll(".jl-home__block h2").map((heading) => heading.text())).toEqual(["Latest premieres", "Welcome back, Admin"]);
    // Hidden blocks load nothing (the layout itself comes with the preferences and site appearance).
    expect(fake.requests.map((request) => new URL(request.url).pathname).filter((path) => !path.startsWith("/api/v1/users/me/preferences") && !path.startsWith("/api/v1/site/"))).toEqual(["/api/v1/items"]);
    expect(wrapper.find(".jl-home__poster").text()).toContain("Arrival");
  });

  it("reorders with the keyboard only, keeps focus on the moved row and announces it", async () => {
    const fake = server();
    const { wrapper, pinia } = await mountView("/", { fetch: fake.fetch, user: fake.user });
    const customize = wrapper.findAll("button").find((button) => button.text() === "Customize home")!;
    await customize.trigger("click");
    expect(customize.attributes("aria-pressed")).toBe("true");
    const rows = () => wrapper.findAll(".jl-reorder__row").map((row) => row.attributes("data-row"));
    expect(rows()).toEqual(["welcome", "libraries", "latest"]);

    // "Move down" on the first row: Enter/Space on a native button is a click.
    const down = wrapper.find('[data-row="welcome"] [data-move="down"]');
    expect(down.attributes("aria-label")).toBe("Move Welcome and shortcuts down");
    (down.element as HTMLButtonElement).focus();
    await down.trigger("click");
    await flushPromises();
    expect(rows()).toEqual(["libraries", "welcome", "latest"]);
    expect(document.activeElement).toBe(wrapper.find('[data-row="welcome"] [data-move="down"]').element);
    expect(wrapper.find('.jl-reorder [role="status"]').text()).toBe("Welcome and shortcuts moved to position 2 of 3.");

    // Alt+ArrowDown from anywhere in the row; at the end the focus moves to
    // the still-enabled "move up" button.
    await wrapper.find('[data-row="welcome"] [data-move="down"]').trigger("keydown", { key: "ArrowDown", altKey: true });
    await flushPromises();
    expect(rows()).toEqual(["libraries", "latest", "welcome"]);
    expect(wrapper.find('[data-row="welcome"] [data-move="down"]').attributes("disabled")).toBeDefined();
    expect(document.activeElement).toBe(wrapper.find('[data-row="welcome"] [data-move="up"]').element);

    // Alt+ArrowUp moves back; plain arrows do nothing.
    await wrapper.find('[data-row="latest"] [data-move="up"]').trigger("keydown", { key: "ArrowUp" });
    expect(rows()).toEqual(["libraries", "latest", "welcome"]);
    await wrapper.find('[data-row="latest"] [data-move="up"]').trigger("keydown", { key: "ArrowUp", altKey: true });
    await flushPromises();
    expect(rows()).toEqual(["latest", "libraries", "welcome"]);

    // Visibility toggles; the page follows and the choice persists per account.
    await wrapper.find('[data-row="libraries"] input[type="checkbox"]').setValue(false);
    await flushPromises();
    expect(wrapper.findAll(".jl-home__block h2").map((heading) => heading.text())).toEqual(["Latest premieres", "Welcome back, Admin"]);
    const stored = JSON.parse(localStorage.getItem(layoutKey(fake.user.id))!) as { layout: { home: unknown } };
    expect(stored.layout.home).toEqual([
      { id: "latest", visible: true },
      { id: "libraries", visible: false },
      { id: "welcome", visible: true },
    ]);
    expect(useLayoutStore(pinia).activePreset()).toBeNull();
  });

  it("reorders by dragging as well", async () => {
    const fake = server();
    const { wrapper } = await mountView("/", { fetch: fake.fetch, user: fake.user });
    await wrapper.findAll("button").find((button) => button.text() === "Customize home")!.trigger("click");
    await wrapper.find('[data-row="latest"]').trigger("dragstart", { dataTransfer: { setData: vi.fn(), effectAllowed: "" } });
    await wrapper.find('[data-row="welcome"]').trigger("drop");
    await flushPromises();
    expect(wrapper.findAll(".jl-reorder__row").map((row) => row.attributes("data-row"))).toEqual(["latest", "welcome", "libraries"]);
  });

  it("orders and hides item page panels", async () => {
    const fake = server();
    localStorage.setItem(
      layoutKey(fake.user.id),
      JSON.stringify({ layout: { detail: [{ id: "externalIds", visible: true }, { id: "overview", visible: false }, { id: "genres", visible: true }] } }),
    );
    const { wrapper } = await mountView("/items/" + movieId, { fetch: fake.fetch, user: fake.user });
    await flushPromises();
    const headings = wrapper.findAll(".jl-detail__section h2").map((heading) => heading.text());
    expect(headings.slice(0, 2)).toEqual(["External IDs", "Genres"]);
    expect(headings).not.toContain("Overview");
    expect(wrapper.text()).not.toContain("Linguist meets heptapods.");
  });
});

describe("layout presets", () => {
  it("switches built-in presets, saves, reapplies and deletes custom ones", async () => {
    const fake = server();
    const { wrapper, pinia } = await mountView("/settings", { fetch: fake.fetch, user: fake.user });
    const store = useLayoutStore(pinia);
    const select = wrapper.findAll("select").find((element) => element.findAll("option").some((option) => option.text() === "Focused"))!;
    await select.setValue("focused");
    await flushPromises();
    expect(store.layout).toEqual(builtInPresets.focused);
    expect(store.activePreset()).toBe("focused");

    store.move("home", 0, 1);
    expect(store.activePreset()).toBeNull();
    const name = wrapper.findAll("input").find((input) => input.element.labels?.[0]?.textContent === "New preset name")!;
    await name.setValue("  Mine  ");
    await wrapper.find(".jl-layout__save").trigger("submit");
    await flushPromises();
    expect(store.presets.map((preset) => [preset.id, preset.name])).toEqual([["custom-1", "Mine"]]);
    expect(store.activePreset()).toBe("custom-1");

    await select.setValue("standard");
    expect(store.layout).toEqual(builtInPresets.standard);
    await select.setValue("custom-1");
    expect(store.activePreset()).toBe("custom-1");
    const remove = wrapper.findAll("button").find((button) => button.text() === "Delete preset Mine")!;
    await remove.trigger("click");
    expect(store.presets).toEqual([]);

    // Empty names are refused with a message.
    await wrapper.find(".jl-layout__save").trigger("submit");
    await flushPromises();
    expect(wrapper.text()).toContain("Enter a preset name.");
  });
});
